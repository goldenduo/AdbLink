package agent

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/goldenduo/AdbLink/pkg/tunnel"
)

const (
	defaultSOCKS5Port1080 = "127.0.0.1:1080"
	defaultSOCKS5Port7890 = "127.0.0.1:7890"
	socks5ProbeTimeout    = 3 * time.Second

	socks5Version       = 5
	socks5NoAuth        = 0
	socks5NoAcceptable  = 0xff
	socks5Connect       = 1
	socks5AddressIPv4   = 1
	socks5AddressDomain = 3
	socks5AddressIPv6   = 4
)
var defaultSOCKS5CandidateAddrs = []string{
	defaultSOCKS5Port1080,
	defaultSOCKS5Port7890,
}

func (a *Agent) resolveProxyCandidates() []string {
	if a.cfg.ProxyAddr != "" {
		return []string{normalizeProxyAddr(a.cfg.ProxyAddr)}
	}
	for _, envKey := range []string{"ALL_PROXY", "all_proxy", "SOCKS5_PROXY", "socks5_proxy"} {
		if val := strings.TrimSpace(os.Getenv(envKey)); val != "" {
			return []string{normalizeProxyAddr(val)}
		}
	}
	return defaultSOCKS5CandidateAddrs
}

func normalizeProxyAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "socks5://")
	addr = strings.TrimPrefix(addr, "socks5h://")
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	return addr
}


// dialServer prefers the local SOCKS5 listener when one responds to a valid
// SOCKS5 greeting. A non-SOCKS service or a closed port falls back to direct
// dialing; once a SOCKS5 proxy is identified, failures are returned instead
// of silently bypassing it.
func (a *Agent) logPrintf(format string, v ...interface{}) {
	if a != nil && a.logger != nil {
		a.logger.Printf(format, v...)
	}
}

func (a *Agent) dialServer(ctx context.Context) (net.Conn, error) {
	conn, err := a.dialTransport(ctx)
	if err != nil {
		return nil, err
	}

	tunnel.ConfigureTCPConn(conn)

	tlsWanted := !a.cfg.DisableTLS
	if a.cfg.TLSEnabled {
		tlsWanted = true
	}
	if a.cfg.DisableTLS {
		tlsWanted = false
	}

	if tlsWanted {
		serverName := a.cfg.TLSServerName
		if serverName == "" {
			serverName = a.cfg.ServerAddr
		}
		insecure := !a.cfg.TLSStrictVerify
		if a.cfg.TLSInsecure {
			insecure = true
		}
		tlsConfig, err := tunnel.ClientTLSConfig(insecure, a.cfg.TLSCAFile, serverName)
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("create TLS client config failed: %w", err)
		}
		tlsConn := tls.Client(conn, tlsConfig)
		handshakeTimeout := a.cfg.DialTimeout
		if handshakeTimeout <= 0 {
			handshakeTimeout = 10 * time.Second
		}
		handshakeCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
		defer cancel()
		if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("TLS client handshake failed: %w", err)
		}
		a.logPrintf("TLS encryption active (server: %s, SNI: %s, cipher: %s)",
			a.cfg.ServerAddr, tlsConfig.ServerName, tls.CipherSuiteName(tlsConn.ConnectionState().CipherSuite))
		return tlsConn, nil
	}

	return conn, nil
}

// dialTransport establishes the underlying TCP or proxy connection.
func (a *Agent) dialTransport(ctx context.Context) (net.Conn, error) {
	if a.cfg.ProxyAddr != "" {
		proxyAddr := normalizeProxyAddr(a.cfg.ProxyAddr)
		// User explicitly configured a proxy. Try SOCKS5 first, then HTTP CONNECT.
		sConn, sDetected, sErr := dialSOCKS5(ctx, proxyAddr, a.cfg.ServerAddr, a.cfg.DialTimeout)
		if sDetected && sErr == nil {
			a.setProxyInfo("SOCKS5", proxyAddr)
			a.logPrintf("Connecting to %s through explicitly configured SOCKS5 proxy %s", a.cfg.ServerAddr, proxyAddr)
			return sConn, nil
		}
		hConn, hDetected, hErr := dialHTTPProxy(ctx, proxyAddr, a.cfg.ServerAddr, a.cfg.DialTimeout)
		if hDetected && hErr == nil {
			a.setProxyInfo("HTTP", proxyAddr)
			a.logPrintf("Connecting to %s through explicitly configured HTTP proxy %s", a.cfg.ServerAddr, proxyAddr)
			return hConn, nil
		}
		if sErr != nil {
			return nil, fmt.Errorf("configured proxy %s failed: %w", proxyAddr, sErr)
		}
		if hErr != nil {
			return nil, fmt.Errorf("configured HTTP proxy %s failed: %w", proxyAddr, hErr)
		}
		return nil, fmt.Errorf("unable to connect through configured proxy %s", proxyAddr)
	}

	candidates := a.resolveProxyCandidates()
	for _, proxyAddr := range candidates {
		proxyConn, proxyDetected, proxyErr := dialSOCKS5(ctx, proxyAddr, a.cfg.ServerAddr, a.cfg.DialTimeout)
		if proxyDetected {
			if proxyErr != nil {
				a.logPrintf("SOCKS5 proxy detected at %s, but proxy connection failed: %v", proxyAddr, proxyErr)
				return nil, proxyErr
			}
			a.setProxyInfo("SOCKS5", proxyAddr)
			a.logPrintf("SOCKS5 proxy detected at %s; connecting to %s through proxy", proxyAddr, a.cfg.ServerAddr)
			return proxyConn, nil
		}

		// Try HTTP CONNECT proxy if SOCKS5 was not detected on that port
		httpConn, httpDetected, httpErr := dialHTTPProxy(ctx, proxyAddr, a.cfg.ServerAddr, a.cfg.DialTimeout)
		if httpDetected {
			if httpErr != nil {
				a.logPrintf("HTTP proxy detected at %s, but proxy connection failed: %v", proxyAddr, httpErr)
				return nil, httpErr
			}
			a.setProxyInfo("HTTP", proxyAddr)
			a.logPrintf("HTTP proxy detected at %s; connecting to %s through proxy", proxyAddr, a.cfg.ServerAddr)
			return httpConn, nil
		}

		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.setProxyInfo("DIRECT", "")
	a.logPrintf("No proxy detected at %s; connecting directly to %s", strings.Join(candidates, " or "), a.cfg.ServerAddr)
	dialer := &net.Dialer{Timeout: a.cfg.DialTimeout}
	return dialer.DialContext(ctx, "tcp", a.cfg.ServerAddr)
}

// dialHTTPProxy opens an HTTP CONNECT tunnel to targetAddr.
func dialHTTPProxy(ctx context.Context, proxyAddr, targetAddr string, timeout time.Duration) (conn net.Conn, detected bool, err error) {
	probeTimeout := timeout
	if probeTimeout <= 0 || probeTimeout > socks5ProbeTimeout {
		probeTimeout = socks5ProbeTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	dialer := &net.Dialer{Timeout: probeTimeout}
	c, err := dialer.DialContext(probeCtx, "tcp", proxyAddr)
	if err != nil {
		return nil, false, err
	}

	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: AdbLink\r\nProxy-Connection: Keep-Alive\r\n\r\n", targetAddr, targetAddr)
	_ = c.SetDeadline(time.Now().Add(probeTimeout))
	if _, err := io.WriteString(c, req); err != nil {
		_ = c.Close()
		return nil, false, err
	}

	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	_ = c.SetDeadline(time.Time{})
	if err != nil {
		_ = c.Close()
		return nil, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		if br.Buffered() > 0 {
			return tunnel.NewPeekConn(c, br), true, nil
		}
		return c, true, nil
	}

	_ = c.Close()
	if resp.StatusCode == http.StatusProxyAuthRequired || (resp.StatusCode >= 400 && resp.StatusCode < 600) {
		return nil, true, fmt.Errorf("HTTP proxy returned %s", resp.Status)
	}
	return nil, false, errors.New("not an HTTP proxy")
}

// dialSOCKS5 checks the listener with a SOCKS5 greeting and, if it supports
// the no-auth method, opens a CONNECT tunnel to targetAddr. detected is true
// as soon as the peer identifies itself as SOCKS5, including when it requires
// an unsupported authentication method.
func dialSOCKS5(ctx context.Context, proxyAddr, targetAddr string, timeout time.Duration) (conn net.Conn, detected bool, err error) {
	probeTimeout := timeout
	if probeTimeout <= 0 || probeTimeout > socks5ProbeTimeout {
		probeTimeout = socks5ProbeTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	dialer := &net.Dialer{Timeout: probeTimeout}
	conn, err = dialer.DialContext(probeCtx, "tcp", proxyAddr)
	if err != nil {
		return nil, false, err
	}
	keepConn := false
	defer func() {
		if !keepConn {
			_ = conn.Close()
		}
	}()

	if err := setSOCKS5Deadline(ctx, conn, probeTimeout); err != nil {
		return nil, false, err
	}
	stopCancel := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
	})
	defer stopCancel()

	if err := writeAll(conn, []byte{socks5Version, 1, socks5NoAuth}); err != nil {
		return nil, false, err
	}
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return nil, false, err
	}
	if greeting[0] != socks5Version {
		return nil, false, fmt.Errorf("unexpected proxy protocol version %d", greeting[0])
	}
	detected = true
	if greeting[1] != socks5NoAuth {
		if greeting[1] == socks5NoAcceptable {
			return nil, true, fmt.Errorf("SOCKS5 proxy does not accept unauthenticated clients")
		}
		return nil, true, fmt.Errorf("SOCKS5 proxy requires unsupported authentication method %d", greeting[1])
	}
	if err := setSOCKS5Deadline(ctx, conn, timeout); err != nil {
		return nil, true, err
	}

	request, err := socks5ConnectRequest(targetAddr)
	if err != nil {
		return nil, true, err
	}
	if err := writeAll(conn, request); err != nil {
		return nil, true, fmt.Errorf("send SOCKS5 CONNECT request: %w", err)
	}

	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return nil, true, fmt.Errorf("read SOCKS5 CONNECT response: %w", err)
	}
	if reply[0] != socks5Version || reply[2] != 0 {
		return nil, true, fmt.Errorf("invalid SOCKS5 CONNECT response header")
	}
	if reply[1] != 0 {
		return nil, true, fmt.Errorf("SOCKS5 CONNECT request rejected with reply code %d", reply[1])
	}
	if err := discardSOCKS5BoundAddress(conn, reply[3]); err != nil {
		return nil, true, fmt.Errorf("read SOCKS5 CONNECT bound address: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, true, err
	}
	keepConn = true
	return conn, true, nil
}

func setSOCKS5Deadline(ctx context.Context, conn net.Conn, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = socks5ProbeTimeout
	}
	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	return conn.SetDeadline(deadline)
}

func socks5ConnectRequest(targetAddr string) ([]byte, error) {
	host, portText, err := net.SplitHostPort(targetAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid SOCKS5 destination %q: %w", targetAddr, err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("invalid SOCKS5 destination port %q: %w", portText, err)
	}

	request := []byte{socks5Version, socks5Connect, 0}
	if ip := net.ParseIP(host); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			request = append(request, socks5AddressIPv4)
			request = append(request, ipv4...)
		} else {
			request = append(request, socks5AddressIPv6)
			request = append(request, ip.To16()...)
		}
	} else {
		domain := []byte(host)
		if len(domain) == 0 || len(domain) > 255 {
			return nil, fmt.Errorf("invalid SOCKS5 destination hostname %q", host)
		}
		request = append(request, socks5AddressDomain, byte(len(domain)))
		request = append(request, domain...)
	}
	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], uint16(port))
	request = append(request, portBytes[:]...)
	return request, nil
}

func discardSOCKS5BoundAddress(conn net.Conn, addressType byte) error {
	var addressLength int64
	switch addressType {
	case socks5AddressIPv4:
		addressLength = 4
	case socks5AddressIPv6:
		addressLength = 16
	case socks5AddressDomain:
		var size [1]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			return err
		}
		addressLength = int64(size[0])
	default:
		return fmt.Errorf("unknown SOCKS5 address type %d", addressType)
	}
	_, err := io.CopyN(io.Discard, conn, addressLength+2)
	return err
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

package agent

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

const (
	localSOCKS5ProxyAddr = "127.0.0.1:1080"
	socks5ProbeTimeout   = time.Second

	socks5Version       = 5
	socks5NoAuth        = 0
	socks5NoAcceptable  = 0xff
	socks5Connect       = 1
	socks5AddressIPv4   = 1
	socks5AddressDomain = 3
	socks5AddressIPv6   = 4
)

// dialServer prefers the local SOCKS5 listener when one responds to a valid
// SOCKS5 greeting. A non-SOCKS service or a closed port falls back to direct
// dialing; once a SOCKS5 proxy is identified, failures are returned instead
// of silently bypassing it.
func (a *Agent) dialServer(ctx context.Context) (net.Conn, error) {
	proxyConn, proxyDetected, proxyErr := dialSOCKS5(ctx, localSOCKS5ProxyAddr, a.cfg.ServerAddr, a.cfg.DialTimeout)
	if proxyDetected {
		if proxyErr != nil {
			a.logger.Printf("SOCKS5 proxy detected at %s, but proxy connection failed: %v", localSOCKS5ProxyAddr, proxyErr)
			return nil, proxyErr
		}
		a.logger.Printf("SOCKS5 proxy detected at %s; connecting to %s through proxy", localSOCKS5ProxyAddr, a.cfg.ServerAddr)
		return proxyConn, nil
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.logger.Printf("No SOCKS5 proxy detected at %s; connecting directly to %s", localSOCKS5ProxyAddr, a.cfg.ServerAddr)
	dialer := &net.Dialer{Timeout: a.cfg.DialTimeout}
	return dialer.DialContext(ctx, "tcp", a.cfg.ServerAddr)
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

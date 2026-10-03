package tunnel

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// TLSRecordTypeHandshake is the first byte of a TLS record containing a handshake message.
	TLSRecordTypeHandshake = 0x16
)

// GenerateSelfSignedCert generates an in-memory self-signed ECDSA certificate
// valid for localhost and common hostnames/IPs.
func GenerateSelfSignedCert(hosts ...string) (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to generate private key: %w", err)
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to generate serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"AdbLink"},
			CommonName:   "AdbLink Gateway",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour), // 10 years
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	// Default SANs
	template.DNSNames = append(template.DNSNames, "localhost", "adblink")
	template.IPAddresses = append(template.IPAddresses, net.ParseIP("127.0.0.1"), net.ParseIP("::1"))

	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		// Strip brackets or port if present
		if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
			h = h[1 : len(h)-1]
		}
		if hostOnly, _, err := net.SplitHostPort(h); err == nil {
			h = hostOnly
		}
		if ip := net.ParseIP(h); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, h)
		}
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to create certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	b, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to marshal private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: b})

	return tls.X509KeyPair(certPEM, keyPEM)
}

// ServerTLSConfig creates a *tls.Config for the server. If certFile and keyFile
// are provided, it loads them. Otherwise, it auto-generates a self-signed certificate.
func ServerTLSConfig(certFile, keyFile string, hosts ...string) (*tls.Config, error) {
	var cert tls.Certificate
	var err error

	if certFile != "" && keyFile != "" {
		cert, err = tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS certificate/key pair: %w", err)
		}
	} else {
		cert, err = GenerateSelfSignedCert(hosts...)
		if err != nil {
			return nil, fmt.Errorf("failed to generate self-signed TLS certificate: %w", err)
		}
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"adblink", "http/1.1"},
	}, nil
}

// ClientTLSConfig creates a *tls.Config for the agent client.
func ClientTLSConfig(insecure bool, caFile, serverName string) (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: insecure,
		NextProtos:         []string{"adblink", "http/1.1"},
	}

	if serverName != "" {
		// Strip port if user passed host:port
		if hostOnly, _, err := net.SplitHostPort(serverName); err == nil {
			serverName = hostOnly
		}
		if strings.HasPrefix(serverName, "[") && strings.HasSuffix(serverName, "]") {
			serverName = serverName[1 : len(serverName)-1]
		}
		cfg.ServerName = serverName
	}

	if caFile != "" {
		caData, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caData) {
			return nil, errors.New("failed to parse CA certificate from PEM data")
		}
		cfg.RootCAs = pool
	}

	return cfg, nil
}

// PeekConn wraps a net.Conn with a prepended reader.
type PeekConn struct {
	net.Conn
	r io.Reader
}

func NewPeekConn(c net.Conn, r io.Reader) *PeekConn {
	return &PeekConn{Conn: c, r: r}
}

func (p *PeekConn) Read(b []byte) (int, error) {
	if p.r != nil {
		return p.r.Read(b)
	}
	return p.Conn.Read(b)
}
// DetectAndHandleTLS peeks the first byte of conn with a deadline.
// If the first byte is 0x16 (TLS ClientHello), it wraps conn in a tls.Server.
// Otherwise, it returns a buffered PeekConn with the byte preserved.
// If tlsConfig is nil, it always returns the plain connection.
func DetectAndHandleTLS(ctx context.Context, conn net.Conn, tlsConfig *tls.Config, timeout time.Duration) (net.Conn, bool, error) {
	if tlsConfig == nil {
		return conn, false, nil
	}

	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	var firstByte [1]byte
	n, err := io.ReadFull(conn, firstByte[:])
	_ = conn.SetReadDeadline(time.Time{})

	if err != nil {
		return nil, false, fmt.Errorf("detect TLS first byte error: %w", err)
	}

	peeked := &PeekConn{
		Conn: conn,
		r:    io.MultiReader(bytes.NewReader(firstByte[:n]), conn),
	}

	if firstByte[0] == TLSRecordTypeHandshake {
		tlsConn := tls.Server(peeked, tlsConfig)
		handshakeCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
			_ = tlsConn.Close()
			return nil, true, fmt.Errorf("TLS server handshake failed: %w", err)
		}
		return tlsConn, true, nil
	}

	return peeked, false, nil
}

// SafeControlStream represents a resilient control stream with safe write timeouts.
type SafeControlStream struct {
	conn net.Conn
	mu   sync.Mutex
}

// NewSafeControlStream wraps a control stream net.Conn.
func NewSafeControlStream(conn net.Conn) *SafeControlStream {
	return &SafeControlStream{conn: conn}
}

// WriteCommand writes a command with an explicit timeout.
func (s *SafeControlStream) WriteCommand(cmd string, timeout time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if timeout > 0 {
		_ = s.conn.SetWriteDeadline(time.Now().Add(timeout))
		defer func() { _ = s.conn.SetWriteDeadline(time.Time{}) }()
	}

	_, err := io.WriteString(s.conn, cmd)
	return err
}

// Conn returns the underlying net.Conn.
func (s *SafeControlStream) Conn() net.Conn {
	return s.conn
}

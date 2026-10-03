package tunnel_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"testing"
	"time"

	"github.com/goldenduo/AdbLink/pkg/tunnel"
)

func TestTLSGenerationAndConfigs(t *testing.T) {
	cert, err := tunnel.GenerateSelfSignedCert("localhost", "127.0.0.1", "168.107.0.103")
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert failed: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatalf("expected non-empty certificate")
	}

	serverCfg, err := tunnel.ServerTLSConfig("", "", "127.0.0.1")
	if err != nil {
		t.Fatalf("ServerTLSConfig failed: %v", err)
	}
	if serverCfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("expected MinVersion TLS1.2, got %x", serverCfg.MinVersion)
	}

	clientCfg, err := tunnel.ClientTLSConfig(true, "", "127.0.0.1:8888")
	if err != nil {
		t.Fatalf("ClientTLSConfig failed: %v", err)
	}
	if !clientCfg.InsecureSkipVerify {
		t.Errorf("expected InsecureSkipVerify=true")
	}
	if clientCfg.ServerName != "127.0.0.1" {
		t.Errorf("expected ServerName=127.0.0.1, got %q", clientCfg.ServerName)
	}
}

func TestDetectAndHandleTLS(t *testing.T) {
	serverTLS, err := tunnel.ServerTLSConfig("", "", "127.0.0.1")
	if err != nil {
		t.Fatalf("failed to init server TLS: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Test TLS client connection
	doneTLS := make(chan error, 1)
	go func() {
		rawConn, acceptErr := listener.Accept()
		if acceptErr != nil {
			doneTLS <- acceptErr
			return
		}
		defer rawConn.Close()

		conn, isTLS, detectErr := tunnel.DetectAndHandleTLS(ctx, rawConn, serverTLS, 3*time.Second)
		if detectErr != nil {
			doneTLS <- detectErr
			return
		}
		if !isTLS {
			t.Errorf("expected isTLS=true for TLS client")
		}

		buf := make([]byte, 5)
		if _, err := io.ReadFull(conn, buf); err != nil {
			doneTLS <- err
			return
		}
		if string(buf) != "HELLO" {
			t.Errorf("expected HELLO, got %s", string(buf))
		}
		_, _ = conn.Write([]byte("WORLD"))
		doneTLS <- nil
	}()

	clientTLS, _ := tunnel.ClientTLSConfig(true, "", "127.0.0.1")
	dialConn, err := tls.Dial("tcp", listener.Addr().String(), clientTLS)
	if err != nil {
		t.Fatalf("client TLS dial failed: %v", err)
	}
	_, _ = dialConn.Write([]byte("HELLO"))
	resp := make([]byte, 5)
	_, _ = io.ReadFull(dialConn, resp)
	if string(resp) != "WORLD" {
		t.Errorf("expected WORLD, got %s", string(resp))
	}
	dialConn.Close()
	if err := <-doneTLS; err != nil {
		t.Fatalf("server TLS handling error: %v", err)
	}

	// 2. Test Plaintext client connection (starting with 0x00)
	donePlain := make(chan error, 1)
	go func() {
		rawConn, acceptErr := listener.Accept()
		if acceptErr != nil {
			donePlain <- acceptErr
			return
		}
		defer rawConn.Close()

		conn, isTLS, detectErr := tunnel.DetectAndHandleTLS(ctx, rawConn, serverTLS, 3*time.Second)
		if detectErr != nil {
			donePlain <- detectErr
			return
		}
		if isTLS {
			t.Errorf("expected isTLS=false for plaintext client")
		}

		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			donePlain <- err
			return
		}
		if !bytes.Equal(buf, []byte{0x00, 0x00, 0x01, 0x00}) {
			t.Errorf("unexpected bytes: %v", buf)
		}
		_, _ = conn.Write([]byte("OKAY"))
		donePlain <- nil
	}()

	plainDial, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("plain dial failed: %v", err)
	}
	_, _ = plainDial.Write([]byte{0x00, 0x00, 0x01, 0x00})
	plainResp := make([]byte, 4)
	_, _ = io.ReadFull(plainDial, plainResp)
	if string(plainResp) != "OKAY" {
		t.Errorf("expected OKAY, got %s", string(plainResp))
	}
	plainDial.Close()
	if err := <-donePlain; err != nil {
		t.Fatalf("server plain handling error: %v", err)
	}
}

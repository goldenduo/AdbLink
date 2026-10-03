package agent

import (
	"context"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

func TestResolveProxyCandidates(t *testing.T) {
	// 1. Default candidates
	ag := &Agent{}
	candidates := ag.resolveProxyCandidates()
	if len(candidates) != 2 || candidates[0] != "127.0.0.1:1080" || candidates[1] != "127.0.0.1:7890" {
		t.Fatalf("unexpected default candidates: %v", candidates)
	}

	// 2. Explicit cfg.ProxyAddr override
	agWithCfg := &Agent{cfg: Config{ProxyAddr: "socks5://10.0.0.1:1080"}}
	candidates = agWithCfg.resolveProxyCandidates()
	if len(candidates) != 1 || candidates[0] != "10.0.0.1:1080" {
		t.Fatalf("unexpected candidates with ProxyAddr: %v", candidates)
	}

	// 3. Env var override
	os.Setenv("ALL_PROXY", "socks5h://127.0.0.1:9050")
	defer os.Unsetenv("ALL_PROXY")
	candidates = ag.resolveProxyCandidates()
	if len(candidates) != 1 || candidates[0] != "127.0.0.1:9050" {
		t.Fatalf("unexpected candidates with ALL_PROXY: %v", candidates)
	}
}

func TestDialServerWithMockSOCKS5(t *testing.T) {
	// Start a mock SOCKS5 listener
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start mock listener: %v", err)
	}
	defer listener.Close()

	proxyAddr := listener.Addr().String()

	// Handle mock SOCKS5 handshake
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 256)
				// Read greeting
				n, err := c.Read(buf)
				if err != nil || n < 3 || buf[0] != 5 {
					return
				}
				// Reply no auth
				_, _ = c.Write([]byte{5, 0})

				// Read connect request
				n, err = c.Read(buf)
				if err != nil || n < 4 || buf[1] != 1 {
					return
				}
				// Reply connect success
				_, _ = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80})

				// Echo any payload back
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	ag := &Agent{
		cfg: Config{
			ProxyAddr:   proxyAddr,
			ServerAddr:  "example.com:80",
			DisableTLS:  true,
			DialTimeout: 2 * time.Second,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, err := ag.dialServer(ctx)
	if err != nil {
		t.Fatalf("dialServer through mock SOCKS5 failed: %v", err)
	}
	defer conn.Close()

	// Verify connection is usable
	msg := []byte("hello proxy")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	recvBuf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, recvBuf); err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(recvBuf) != string(msg) {
		t.Fatalf("received %q, expected %q", string(recvBuf), string(msg))
	}
}

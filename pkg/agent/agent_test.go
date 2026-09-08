package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"testing"
	"time"

	"github.com/goldenduo/AdbLink/pkg/server"
)

func TestAgentServerTunnel(t *testing.T) {
	// 1. Start mock local adbd
	mockAdbdListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start mock adbd: %v", err)
	}
	defer mockAdbdListener.Close()
	mockAdbdAddr := mockAdbdListener.Addr().String()

	// Mock adbd handles incoming streams by echoing with a prefix
	go func() {
		for {
			conn, err := mockAdbdListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				n, err := c.Read(buf)
				if err != nil {
					return
				}
				resp := append([]byte("MOCK_ADBD_REPLY:"), buf[:n]...)
				_, _ = c.Write(resp)
			}(conn)
		}
	}()

	// 2. Start AdbLink Server
	serverCfg := server.Config{
		ListenAddr:    "127.0.0.1:0",
		AdvertiseHost: "127.0.0.1",
		PortMin:       45000,
		PortMax:       45010,
		Logger:        log.New(io.Discard, "", 0),
	}
	srv, err := server.NewServer(serverCfg)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer srv.Stop()

	// 3. Connect Agent
	agentCfg := Config{
		ServerAddr:     srv.GetListenAddr(),
		DeviceID:       "test-device-001",
		Model:          "TestPhone",
		AndroidVersion: "15",
		LocalAdbAddr:   mockAdbdAddr,
		Logger:         log.New(io.Discard, "", 0),
	}
	ag := NewAgent(agentCfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = ag.Run(ctx)
	}()

	// Wait for device to appear in server registry
	var devInfo server.DeviceInfo
	var found bool
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		devInfo, found = srv.GetDevice("test-device-001")
		if found && devInfo.Status == "ONLINE" {
			break
		}
	}
	if !found {
		t.Fatalf("device did not register in time")
	}

	// 4. Connect simulated ADB client to server's exposed port
	adbClientConn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", devInfo.AssignedPort), 2*time.Second)
	if err != nil {
		t.Fatalf("failed to connect to server exposed port %d: %v", devInfo.AssignedPort, err)
	}
	defer adbClientConn.Close()

	// Send ADB command
	testMsg := []byte("CNXN 01000000 00100000 00000000 host::test")
	if _, err := adbClientConn.Write(testMsg); err != nil {
		t.Fatalf("write to adb port failed: %v", err)
	}

	replyBuf := make([]byte, 1024)
	_ = adbClientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := adbClientConn.Read(replyBuf)
	if err != nil {
		t.Fatalf("read reply from adb port failed: %v", err)
	}

	expectedPrefix := []byte("MOCK_ADBD_REPLY:")
	if !bytes.HasPrefix(replyBuf[:n], expectedPrefix) {
		t.Fatalf("unexpected reply: got %s, want prefix %s", string(replyBuf[:n]), string(expectedPrefix))
	}
}

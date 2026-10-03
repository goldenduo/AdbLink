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
		ListenAddr:        "127.0.0.1:0",
		AdvertiseHost:     "127.0.0.1",
		PortMin:           45000,
		PortMax:           45010,
		HeartbeatInterval: 20 * time.Millisecond,
		Logger:            log.New(io.Discard, "", 0),
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
	initialLastSeen := devInfo.LastSeenAt
	time.Sleep(80 * time.Millisecond)
	updatedInfo, ok := srv.GetDevice("test-device-001")
	if !ok || !updatedInfo.LastSeenAt.After(initialLastSeen) {
		t.Fatalf("control heartbeat did not update LastSeenAt: initial=%v updated=%v", initialLastSeen, updatedInfo.LastSeenAt)
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

func TestRemoteDisconnectStopsAgent(t *testing.T) {
	// 1. Start mock local adbd
	mockAdbdListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start mock adbd: %v", err)
	}
	defer mockAdbdListener.Close()
	mockAdbdAddr := mockAdbdListener.Addr().String()

	// 2. Start AdbLink Server
	serverCfg := server.Config{
		ListenAddr:    "127.0.0.1:0",
		AdvertiseHost: "127.0.0.1",
		PortMin:       46000,
		PortMax:       46010,
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
		ServerAddr:        srv.GetListenAddr(),
		DeviceID:          "test-disconnect-device",
		Model:             "DisconnectPhone",
		AndroidVersion:    "15",
		LocalAdbAddr:      mockAdbdAddr,
		RetryInterval:     10 * time.Millisecond,
		MaxRetryInterval:  30 * time.Millisecond,
		HeartbeatInterval: 20 * time.Millisecond,
		Logger:            log.New(io.Discard, "", 0),
	}
	ag := NewAgent(agentCfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agentDone := make(chan struct{})
	go func() {
		_ = ag.Run(ctx)
		close(agentDone)
	}()

	// Wait for device to appear in server registry
	var found bool
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		devInfo, ok := srv.GetDevice("test-disconnect-device")
		if ok && devInfo.Status == "ONLINE" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("device did not register online in time")
	}

	// 4. Trigger disconnect from server side
	if err := srv.DisconnectDevice("test-disconnect-device"); err != nil {
		t.Fatalf("DisconnectDevice failed: %v", err)
	}

	// 5. Verify agent cleanly terminates its Run() loop and does NOT reconnect
	select {
	case <-agentDone:
		// Successfully terminated!
	case <-time.After(3 * time.Second):
		t.Fatalf("agent did not exit within timeout after server disconnect")
	}

	// Wait 1 second to ensure agent does not re-register
	time.Sleep(1 * time.Second)
	_, stillExists := srv.GetDevice("test-disconnect-device")
	if stillExists {
		t.Fatalf("device still exists or reconnected after disconnect")
	}

	// A late reconnect attempt from the same process (or a duplicate agent
	// started before the stop tombstone expires) must be told to exit rather
	// than being accepted as a new online session.
	lateAgent := NewAgent(agentCfg)
	lateCtx, lateCancel := context.WithCancel(context.Background())
	defer lateCancel()
	lateDone := make(chan struct{})
	go func() {
		_ = lateAgent.Run(lateCtx)
		close(lateDone)
	}()
	select {
	case <-lateDone:
	case <-time.After(2 * time.Second):
		t.Fatal("late agent did not honor the server stop request")
	}
}

func TestAgentReconnectsAfterTransportEviction(t *testing.T) {
	srv, err := server.NewServer(server.Config{
		ListenAddr:        "127.0.0.1:0",
		AdvertiseHost:     "127.0.0.1",
		PortMin:           47000,
		PortMax:           47010,
		GracePeriod:       2 * time.Second,
		HeartbeatInterval: 20 * time.Millisecond,
		Logger:            log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer srv.Stop()

	ag := NewAgent(Config{
		ServerAddr:        srv.GetListenAddr(),
		DeviceID:          "test-reconnect-device",
		Model:             "ReconnectPhone",
		LocalAdbAddr:      "127.0.0.1:1",
		RetryInterval:     10 * time.Millisecond,
		MaxRetryInterval:  30 * time.Millisecond,
		HeartbeatInterval: 20 * time.Millisecond,
		Logger:            log.New(io.Discard, "", 0),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ag.Run(ctx) }()

	var first server.DeviceInfo
	if !waitForDeviceStatus(t, srv, "test-reconnect-device", "ONLINE", 3*time.Second, &first) {
		t.Fatal("agent did not register before transport eviction")
	}

	// EvictDevice models a real TCP/session failure. Unlike the web-console
	// DisconnectDevice operation, the agent must reconnect automatically.
	srv.EvictDevice("test-reconnect-device")

	deadline := time.Now().Add(3 * time.Second)
	var second server.DeviceInfo
	for time.Now().Before(deadline) {
		if info, ok := srv.GetDevice("test-reconnect-device"); ok && info.Status == "ONLINE" && info.ConnectedAt.After(first.ConnectedAt) {
			second = info
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if second.Status != "ONLINE" {
		t.Fatal("agent did not reconnect after transport eviction")
	}
	if second.AssignedPort != first.AssignedPort {
		t.Fatalf("reconnect changed assigned port: first=%d second=%d", first.AssignedPort, second.AssignedPort)
	}
}

func waitForDeviceStatus(t *testing.T, srv *server.Server, deviceID, status string, timeout time.Duration, result *server.DeviceInfo) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if info, ok := srv.GetDevice(deviceID); ok && info.Status == status {
			if result != nil {
				*result = info
			}
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
func TestAgentServerTLSTunnel(t *testing.T) {
	mockAdbdListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start mock adbd: %v", err)
	}
	defer mockAdbdListener.Close()
	mockAdbdAddr := mockAdbdListener.Addr().String()

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
				resp := append([]byte("SECURE_TLS_REPLY:"), buf[:n]...)
				_, _ = c.Write(resp)
			}(conn)
		}
	}()

	serverCfg := server.Config{
		ListenAddr:        "127.0.0.1:0",
		AdvertiseHost:     "127.0.0.1",
		PortMin:           48000,
		PortMax:           48010,
		TLSEnabled:        true,
		HeartbeatInterval: 20 * time.Millisecond,
		Logger:            log.New(io.Discard, "", 0),
	}
	srv, err := server.NewServer(serverCfg)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer srv.Stop()

	agentCfg := Config{
		ServerAddr:     srv.GetListenAddr(),
		DeviceID:       "tls-test-device-001",
		Model:          "TLSTestPhone",
		AndroidVersion: "15",
		LocalAdbAddr:   mockAdbdAddr,
		TLSEnabled:     true,
		TLSInsecure:    true,
		Logger:         log.New(io.Discard, "", 0),
	}
	ag := NewAgent(agentCfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = ag.Run(ctx)
	}()

	var devInfo server.DeviceInfo
	if !waitForDeviceStatus(t, srv, "tls-test-device-001", "ONLINE", 3*time.Second, &devInfo) {
		t.Fatalf("TLS device did not register in time")
	}

	adbClientConn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", devInfo.AssignedPort), 2*time.Second)
	if err != nil {
		t.Fatalf("failed to connect to TLS exposed ADB port: %v", err)
	}
	defer adbClientConn.Close()

	if _, err := adbClientConn.Write([]byte("PING_OVER_TLS")); err != nil {
		t.Fatalf("failed to send data through TLS ADB port: %v", err)
	}

	replyBuf := make([]byte, 1024)
	_ = adbClientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := adbClientConn.Read(replyBuf)
	if err != nil {
		t.Fatalf("read reply from TLS adb port failed: %v", err)
	}

	expected := "SECURE_TLS_REPLY:PING_OVER_TLS"
	if string(replyBuf[:n]) != expected {
		t.Fatalf("unexpected reply: got %s, want %s", string(replyBuf[:n]), expected)
	}
}
func TestAgentHeartbeatTimeoutTriggersReconnect(t *testing.T) {
	serverCfg := server.Config{
		ListenAddr:        "127.0.0.1:0",
		AdvertiseHost:     "127.0.0.1",
		PortMin:           49000,
		PortMax:           49010,
		HeartbeatInterval: 20 * time.Millisecond,
		Logger:            log.New(io.Discard, "", 0),
	}
	srv, err := server.NewServer(serverCfg)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}

	agentCfg := Config{
		ServerAddr:        srv.GetListenAddr(),
		DeviceID:          "heartbeat-test-device",
		Model:             "HeartbeatPhone",
		HeartbeatInterval: 20 * time.Millisecond,
		RetryInterval:     20 * time.Millisecond,
		MaxRetryInterval:  50 * time.Millisecond,
		Logger:            log.New(io.Discard, "", 0),
	}
	ag := NewAgent(agentCfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = ag.Run(ctx)
	}()

	var first server.DeviceInfo
	if !waitForDeviceStatus(t, srv, "heartbeat-test-device", "ONLINE", 3*time.Second, &first) {
		t.Fatalf("device did not register in time")
	}

	// Evict the device from server without STOP to test active heartbeat detection and reconnect
	srv.EvictDevice("heartbeat-test-device")

	var second server.DeviceInfo
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if info, ok := srv.GetDevice("heartbeat-test-device"); ok && info.Status == "ONLINE" && info.ConnectedAt.After(first.ConnectedAt) {
			second = info
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if second.Status != "ONLINE" {
		t.Fatal("agent failed to auto-reconnect after heartbeat failure/eviction")
	}
	_ = srv.Stop()
}

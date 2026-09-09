package agent

import (
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"sync"
	"time"

	"github.com/goldenduo/AdbLink/pkg/protocol"
	"github.com/goldenduo/AdbLink/pkg/tunnel"
	"github.com/hashicorp/yamux"
)

// Config configures the AdbLink Agent (Android native proxy).
type Config struct {
	ServerAddr         string        // Remote server address, e.g. "1.2.3.4:9000"
	DeviceID           string        // Unique device serial or ID (auto-detected if empty)
	Model              string        // Device model (auto-detected if empty)
	Manufacturer       string        // Device manufacturer
	AndroidVersion     string        // Android OS version
	LocalAdbAddr       string        // Local adbd address (default "127.0.0.1:5555")
	RequestedPort      int           // Desired port on server (0 = auto)
	Token              string        // Authentication token
	AutoAdbd           bool          // Automatically enable adbd TCP port
	RetryInterval      time.Duration // Initial reconnect backoff
	MaxRetryInterval   time.Duration // Maximum reconnect backoff
	DialTimeout        time.Duration // Server dial timeout
	Logger             *log.Logger
}

// Agent is the native proxy running on Android.
type Agent struct {
	cfg        Config
	logger     *log.Logger
	mu         sync.Mutex
	session    *yamux.Session
	closed     bool
	cancelFunc context.CancelFunc
}

// NewAgent creates a new Agent instance.
func NewAgent(cfg Config) *Agent {
	if cfg.LocalAdbAddr == "" {
		cfg.LocalAdbAddr = "127.0.0.1:5555"
	}
	if cfg.RetryInterval == 0 {
		cfg.RetryInterval = 2 * time.Second
	}
	if cfg.MaxRetryInterval == 0 {
		cfg.MaxRetryInterval = 30 * time.Second
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(os.Stdout, "[AdbLink-Agent] ", log.LstdFlags|log.Lmsgprefix)
	}

	// Auto-detect properties if missing
	detected := DetectProperties()
	if cfg.DeviceID == "" {
		cfg.DeviceID = detected.DeviceID
	}
	if cfg.Model == "" {
		cfg.Model = detected.Model
	}
	if cfg.Manufacturer == "" {
		cfg.Manufacturer = detected.Manufacturer
	}
	if cfg.AndroidVersion == "" {
		cfg.AndroidVersion = detected.AndroidVersion
	}

	return &Agent{
		cfg:    cfg,
		logger: cfg.Logger,
	}
}

// Run starts the agent connection loop and blocks until context is cancelled or Stop is called.
func (a *Agent) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancelFunc = cancel
	a.mu.Unlock()

	a.logger.Printf("Starting AdbLink Agent (ID: %s, Model: %s, Android: %s)",
		a.cfg.DeviceID, a.cfg.Model, a.cfg.AndroidVersion)
	a.logger.Printf("Target local adbd: %s -> Remote server: %s",
		a.cfg.LocalAdbAddr, a.cfg.ServerAddr)

	// Ensure local adbd is up
	if err := EnsureAdbd(a.cfg.LocalAdbAddr, a.cfg.AutoAdbd, a.logger); err != nil {
		a.logger.Printf("Warning: adbd check notice: %v", err)
	}

	backoff := a.cfg.RetryInterval

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := a.connectAndServe(ctx)
		if err != nil {
			a.logger.Printf("Connection error: %v", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Exponential backoff with jitter
		jitter := time.Duration(rand.Int63n(int64(backoff / 2)))
		sleepDuration := backoff + jitter
		a.logger.Printf("Reconnecting to %s in %v...", a.cfg.ServerAddr, sleepDuration)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleepDuration):
		}

		backoff *= 2
		if backoff > a.cfg.MaxRetryInterval {
			backoff = a.cfg.MaxRetryInterval
		}
	}
}

// connectAndServe handles a single persistent connection session to the server.
func (a *Agent) connectAndServe(ctx context.Context) error {
	a.logger.Printf("Connecting to server at %s...", a.cfg.ServerAddr)

	dialer := &net.Dialer{Timeout: a.cfg.DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", a.cfg.ServerAddr)
	if err != nil {
		return fmt.Errorf("dial server failed: %w", err)
	}
	defer conn.Close()

	// Perform registration handshake
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	req := protocol.RegisterRequest{
		Magic:          protocol.MagicHeader,
		Version:        protocol.CurrentProtocolVersion,
		DeviceID:       a.cfg.DeviceID,
		Model:          a.cfg.Model,
		Manufacturer:   a.cfg.Manufacturer,
		AndroidVersion: a.cfg.AndroidVersion,
		Token:          a.cfg.Token,
		RequestedPort:  a.cfg.RequestedPort,
		ClientVersion:  "1.1.0",
	}

	if err := protocol.WriteMsg(conn, req); err != nil {
		return fmt.Errorf("send register request failed: %w", err)
	}

	var resp protocol.RegisterResponse
	if err := protocol.ReadMsg(conn, &resp); err != nil {
		return fmt.Errorf("read register response failed: %w", err)
	}

	if resp.Status != protocol.StatusOK {
		return fmt.Errorf("server rejected registration: [%s] %s", resp.Status, resp.Message)
	}

	a.logger.Printf("==> Successfully registered! Server exposed ADB port: %d (Host: %s)",
		resp.AssignedPort, resp.AdvertiseHost)
	a.logger.Printf("==> Connect from anywhere: adb connect %s:%d",
		resp.AdvertiseHost, resp.AssignedPort)

	_ = conn.SetDeadline(time.Time{})

	// Upgrade to Yamux client session
	session, err := yamux.Client(conn, tunnel.DefaultYamuxConfig())
	if err != nil {
		return fmt.Errorf("start yamux client session failed: %w", err)
	}
	defer session.Close()

	a.mu.Lock()
	a.session = session
	a.mu.Unlock()

	// Wait for context cancellation or stream loop completion
	streamErrChan := make(chan error, 1)
	go func() {
		streamErrChan <- a.acceptStreams(ctx, session)
	}()

	select {
	case <-ctx.Done():
		_ = session.Close()
		return ctx.Err()
	case err := <-streamErrChan:
		return err
	}
}

// acceptStreams listens for incoming reverse streams from the server and forwards to local adbd.
func (a *Agent) acceptStreams(ctx context.Context, session *yamux.Session) error {
	for {
		stream, err := session.AcceptStream()
		if err != nil {
			if err == io.EOF || session.IsClosed() {
				return fmt.Errorf("server closed session")
			}
			return fmt.Errorf("accept stream error: %w", err)
		}

		go a.handleStream(stream)
	}
}

// handleStream forwards a single stream to local adbd.
func (a *Agent) handleStream(stream *yamux.Stream) {
	defer stream.Close()

	localConn, err := net.DialTimeout("tcp", a.cfg.LocalAdbAddr, 2*time.Second)
	if err != nil {
		a.logger.Printf("Failed to connect to local adbd at %s: %v", a.cfg.LocalAdbAddr, err)
		return
	}
	defer localConn.Close()

	n1, n2, err := tunnel.Forward(localConn, stream)
	if err != nil && err != io.EOF {
		a.logger.Printf("Stream forwarding ended with error: %v (local->server: %d, server->local: %d)",
			err, n1, n2)
	}
}

// Stop shuts down the agent.
func (a *Agent) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return
	}
	a.closed = true

	if a.cancelFunc != nil {
		a.cancelFunc()
	}
	if a.session != nil {
		_ = a.session.Close()
	}
}

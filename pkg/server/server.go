package server

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goldenduo/AdbLink/pkg/protocol"
	"github.com/goldenduo/AdbLink/pkg/tunnel"
	"github.com/hashicorp/yamux"
)

var (
	ErrServerClosed = errors.New("server closed")
	ErrNotFound     = errors.New("device not found")
)

// Config configures the AdbLink Server.
type Config struct {
	ListenAddr    string        // e.g. ":9000"
	AdvertiseHost string        // e.g. "127.0.0.1" or public IP
	PortMin       int           // e.g. 55550
	PortMax       int           // e.g. 55599
	Token         string        // Shared secret auth token (optional)
	GracePeriod   time.Duration // Reconnection grace period (e.g. 30s)
	Logger        *log.Logger
}

// DeviceInfo provides snapshot metadata and statistics for a device.
type DeviceInfo struct {
	DeviceID         string    `json:"device_id"`
	Model            string    `json:"model"`
	Manufacturer     string    `json:"manufacturer"`
	AndroidVersion   string    `json:"android_version"`
	RemoteAddr       string    `json:"remote_addr"`
	AssignedPort     int       `json:"assigned_port"`
	AdbConnectTarget string    `json:"adb_connect_target"`
	ConnectedAt      time.Time `json:"connected_at"`
	LastSeenAt       time.Time `json:"last_seen_at"`
	ActiveStreams    int64     `json:"active_streams"`
	TotalConnections int64     `json:"total_connections"`
	BytesSent        int64     `json:"bytes_sent"`
	BytesReceived    int64     `json:"bytes_received"`
	Status           string    `json:"status"` // "ONLINE", "DISCONNECTED"
}

// DeviceSession represents an active or grace-period agent connection.
type DeviceSession struct {
	mu               sync.RWMutex
	info             DeviceInfo
	yamuxSession     *yamux.Session
	tcpListener      net.Listener
	controlStream    net.Conn
	closed           bool
	closeChan        chan struct{}
	disconnectTimer  *time.Timer
	activeStreams    int64
	totalConnections int64
	bytesSent        int64
	bytesReceived    int64
}

// Server is the central reverse-tunnel ADB server.
type Server struct {
	cfg          Config
	logger       *log.Logger
	portPool     *PortPool
	listener     net.Listener
	mu           sync.RWMutex
	devices      map[string]*DeviceSession
	closed       bool
	shutdownChan chan struct{}
	wg           sync.WaitGroup
}

// NewServer creates a new AdbLink Server.
func NewServer(cfg Config) (*Server, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8888"
	}
	if cfg.AdvertiseHost == "" {
		cfg.AdvertiseHost = "127.0.0.1"
	}
	if cfg.PortMin == 0 {
		cfg.PortMin = 55550
	}
	if cfg.PortMax == 0 {
		cfg.PortMax = 55599
	}
	if cfg.GracePeriod == 0 {
		cfg.GracePeriod = 30 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(os.Stdout, "[AdbLink-Server] ", log.LstdFlags|log.Lmsgprefix)
	}

	pool, err := NewPortPool(cfg.PortMin, cfg.PortMax)
	if err != nil {
		return nil, fmt.Errorf("failed to create port pool: %w", err)
	}

	return &Server{
		cfg:          cfg,
		logger:       cfg.Logger,
		portPool:     pool,
		devices:      make(map[string]*DeviceSession),
		shutdownChan: make(chan struct{}),
	}, nil
}

// Start begins listening for Agent connections.
func (s *Server) Start() error {
	l, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s failed: %w", s.cfg.ListenAddr, err)
	}
	s.listener = l

	s.logger.Printf("Server listening on %s (ADB port range: %d-%d, advertise host: %s)",
		l.Addr().String(), s.cfg.PortMin, s.cfg.PortMax, s.cfg.AdvertiseHost)

	s.wg.Add(1)
	go s.acceptLoop()

	return nil
}

// GetListenAddr returns the actual listening address of the server.
func (s *Server) GetListenAddr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.cfg.ListenAddr
}

// acceptLoop accepts incoming TCP connections from agents.
func (s *Server) acceptLoop() {
	defer s.wg.Done()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.shutdownChan:
				return
			default:
				s.logger.Printf("Accept error: %v", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
		}

		go s.handleAgentConn(conn)
	}
}

// handleAgentConn performs handshake, port assignment, and starts reverse tunneling.
func (s *Server) handleAgentConn(conn net.Conn) {
	remoteAddr := conn.RemoteAddr().String()
	s.logger.Printf("Incoming connection from %s", remoteAddr)

	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	var req protocol.RegisterRequest
	if err := protocol.ReadMsg(conn, &req); err != nil {
		s.logger.Printf("Failed to read register request from %s: %v", remoteAddr, err)
		_ = conn.Close()
		return
	}

	// Validate magic & version
	if req.Magic != protocol.MagicHeader {
		s.logger.Printf("Invalid magic header from %s: %x", remoteAddr, req.Magic)
		_ = protocol.WriteMsg(conn, protocol.RegisterResponse{
			Magic:   protocol.MagicHeader,
			Version: protocol.CurrentProtocolVersion,
			Status:  protocol.StatusError,
			Message: "invalid magic header",
		})
		_ = conn.Close()
		return
	}

	// Validate token if configured
	if s.cfg.Token != "" && req.Token != s.cfg.Token {
		s.logger.Printf("Unauthorized token from %s (device: %s)", remoteAddr, req.DeviceID)
		_ = protocol.WriteMsg(conn, protocol.RegisterResponse{
			Magic:   protocol.MagicHeader,
			Version: protocol.CurrentProtocolVersion,
			Status:  protocol.StatusUnauthorized,
			Message: "invalid authentication token",
		})
		_ = conn.Close()
		return
	}

	if req.DeviceID == "" {
		req.DeviceID = fmt.Sprintf("device-%d", time.Now().UnixNano())
	}
	// Evict any existing session for this device to prevent port bind conflicts
	s.EvictDevice(req.DeviceID)

	// Acquire port
	assignedPort, err := s.portPool.Acquire(req.RequestedPort, req.DeviceID)
	if err != nil {
		s.logger.Printf("Port acquisition failed for device %s: %v", req.DeviceID, err)
		_ = protocol.WriteMsg(conn, protocol.RegisterResponse{
			Magic:   protocol.MagicHeader,
			Version: protocol.CurrentProtocolVersion,
			Status:  protocol.StatusPortUnavailable,
			Message: fmt.Sprintf("cannot allocate port: %v", err),
		})
		_ = conn.Close()
		return
	}

	// Reset deadline for persistent connection
	_ = conn.SetDeadline(time.Time{})

	// Send success response
	resp := protocol.RegisterResponse{
		Magic:         protocol.MagicHeader,
		Version:       protocol.CurrentProtocolVersion,
		Status:        protocol.StatusOK,
		Message:       "Registration successful",
		ServerVersion: "1.3.0",
		AdvertiseHost: s.cfg.AdvertiseHost,
	}
	if err := protocol.WriteMsg(conn, resp); err != nil {
		s.logger.Printf("Failed to send register response to %s: %v", remoteAddr, err)
		s.portPool.Release(assignedPort, req.DeviceID)
		_ = conn.Close()
		return
	}

	// Upgrade connection to Yamux server
	session, err := yamux.Server(conn, tunnel.DefaultYamuxConfig())
	if err != nil {
		s.logger.Printf("Failed to create yamux server session for %s: %v", remoteAddr, err)
		s.portPool.Release(assignedPort, req.DeviceID)
		_ = conn.Close()
		return
	}

	// Start exposed ADB listener
	adbListenAddr := fmt.Sprintf("0.0.0.0:%d", assignedPort)
	adbListener, err := net.Listen("tcp", adbListenAddr)
	if err != nil {
		s.logger.Printf("Failed to bind ADB port %s: %v", adbListenAddr, err)
		s.portPool.Release(assignedPort, req.DeviceID)
		_ = session.Close()
		return
	}

	devSession := s.registerDeviceSession(req, remoteAddr, assignedPort, session, adbListener)
	s.logger.Printf("Device registered: %s (model: %s) -> Port %d. Connect via: adb connect %s:%d",
		req.DeviceID, req.Model, assignedPort, s.cfg.AdvertiseHost, assignedPort)

	// Accept dedicated control stream from agent
	go func() {
		ctrlStream, err := session.AcceptStream()
		if err == nil {
			devSession.mu.Lock()
			devSession.controlStream = ctrlStream
			devSession.mu.Unlock()
		}
	}()

	// Run ADB forwarding loop
	go s.runAdbForwardingLoop(devSession)

	// Monitor yamux session for disconnect
	go s.monitorSession(devSession)
}

// registerDeviceSession stores or updates the device session in registry.
func (s *Server) registerDeviceSession(
	req protocol.RegisterRequest,
	remoteAddr string,
	assignedPort int,
	session *yamux.Session,
	adbListener net.Listener,
) *DeviceSession {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if this device had a previous session
	oldSession, exists := s.devices[req.DeviceID]
	if exists {
		oldSession.mu.Lock()
		if oldSession.disconnectTimer != nil {
			oldSession.disconnectTimer.Stop()
			oldSession.disconnectTimer = nil
		}
		if oldSession.tcpListener != nil {
			_ = oldSession.tcpListener.Close()
		}
		if oldSession.yamuxSession != nil {
			_ = oldSession.yamuxSession.Close()
		}
		oldSession.mu.Unlock()
	}

	devSession := &DeviceSession{
		info: DeviceInfo{
			DeviceID:         req.DeviceID,
			Model:            req.Model,
			Manufacturer:     req.Manufacturer,
			AndroidVersion:   req.AndroidVersion,
			RemoteAddr:       remoteAddr,
			AssignedPort:     assignedPort,
			AdbConnectTarget: fmt.Sprintf("%s:%d", s.cfg.AdvertiseHost, assignedPort),
			ConnectedAt:      time.Now(),
			LastSeenAt:       time.Now(),
			Status:           "ONLINE",
		},
		yamuxSession: session,
		tcpListener:  adbListener,
		closeChan:    make(chan struct{}),
	}

	s.devices[req.DeviceID] = devSession
	return devSession
}

// runAdbForwardingLoop accepts ADB connections from clients and tunnels them to the agent.
func (s *Server) runAdbForwardingLoop(dev *DeviceSession) {
	for {
		clientConn, err := dev.tcpListener.Accept()
		if err != nil {
			dev.mu.RLock()
			closed := dev.closed
			dev.mu.RUnlock()
			if closed {
				return
			}
			return
		}

		go s.forwardAdbConnection(dev, clientConn)
	}
}

// forwardAdbConnection pipes data between an incoming ADB client connection and the Agent.
func (s *Server) forwardAdbConnection(dev *DeviceSession, clientConn net.Conn) {
	atomic.AddInt64(&dev.totalConnections, 1)
	atomic.AddInt64(&dev.activeStreams, 1)
	defer atomic.AddInt64(&dev.activeStreams, -1)

	dev.mu.RLock()
	session := dev.yamuxSession
	dev.mu.RUnlock()

	if session == nil || session.IsClosed() {
		_ = clientConn.Close()
		return
	}

	// Open a multiplexed stream to the Android agent
	stream, err := session.OpenStream()
	if err != nil {
		s.logger.Printf("Failed to open yamux stream to device %s: %v", dev.info.DeviceID, err)
		_ = clientConn.Close()
		return
	}

	_, _, _ = tunnel.ForwardWithCallbacks(
		clientConn, stream,
		func(n int64) {
			atomic.AddInt64(&dev.bytesReceived, n)
		},
		func(n int64) {
			atomic.AddInt64(&dev.bytesSent, n)
		},
	)

	dev.mu.Lock()
	dev.info.LastSeenAt = time.Now()
	dev.mu.Unlock()
}

// monitorSession detects disconnection and manages the grace period.
func (s *Server) monitorSession(dev *DeviceSession) {
	// yamux.Session doesn't have an error channel, but calling OpenStream or Ping or waiting on closed state
	// We can periodically ping or wait for session close
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.shutdownChan:
			return
		case <-dev.closeChan:
			return
		case <-ticker.C:
			dev.mu.RLock()
			session := dev.yamuxSession
			closed := dev.closed
			dev.mu.RUnlock()

			if closed || session == nil || session.IsClosed() {
				s.handleDeviceDisconnected(dev)
				return
			}

			// Ping peer to verify liveness
			_, err := session.Ping()
			if err != nil {
				s.logger.Printf("Ping failed for device %s: %v, marking disconnected", dev.info.DeviceID, err)
				s.handleDeviceDisconnected(dev)
				return
			}

			dev.mu.Lock()
			dev.info.LastSeenAt = time.Now()
			dev.mu.Unlock()
		}
	}
}
func (s *Server) handleDeviceDisconnected(dev *DeviceSession) {
	dev.mu.Lock()
	if dev.closed {
		dev.mu.Unlock()
		return
	}
	dev.closed = true
	dev.info.Status = "DISCONNECTED"
	if dev.tcpListener != nil {
		_ = dev.tcpListener.Close()
		dev.tcpListener = nil
	}
	if dev.yamuxSession != nil {
		_ = dev.yamuxSession.Close()
		dev.yamuxSession = nil
	}
	dev.mu.Unlock()


	// Verify this session is still the active one before releasing its port
	s.mu.RLock()
	currentDev, exists := s.devices[dev.info.DeviceID]
	s.mu.RUnlock()
	if exists && currentDev != dev {
		s.logger.Printf("Skipping port release for %s: a new session has already taken over", dev.info.DeviceID)
		return
	}
	s.logger.Printf("Device %s disconnected. Keeping port %d reserved for %v grace period",
		dev.info.DeviceID, dev.info.AssignedPort, s.cfg.GracePeriod)

	s.portPool.ReleaseWithGracePeriod(dev.info.AssignedPort, dev.info.DeviceID, s.cfg.GracePeriod)

	// Set timer to purge device after grace period
	dev.mu.Lock()
	dev.disconnectTimer = time.AfterFunc(s.cfg.GracePeriod, func() {
		s.purgeDevice(dev.info.DeviceID)
	})
	dev.mu.Unlock()
}

// purgeDevice removes the device completely after grace period expires.
func (s *Server) purgeDevice(deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dev, exists := s.devices[deviceID]
	if !exists {
		return
	}

	dev.mu.Lock()
	if dev.info.Status == "ONLINE" {
		dev.mu.Unlock()
		return
	}
	dev.closed = true
	dev.mu.Unlock()

	delete(s.devices, deviceID)
	s.logger.Printf("Device %s grace period expired, purged from registry", deviceID)
}

// GetDevices returns snapshots of all registered devices.
func (s *Server) GetDevices() []DeviceInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]DeviceInfo, 0, len(s.devices))
	for _, dev := range s.devices {
		dev.mu.RLock()
		info := dev.info
		info.ActiveStreams = atomic.LoadInt64(&dev.activeStreams)
		info.TotalConnections = atomic.LoadInt64(&dev.totalConnections)
		info.BytesSent = atomic.LoadInt64(&dev.bytesSent)
		info.BytesReceived = atomic.LoadInt64(&dev.bytesReceived)
		dev.mu.RUnlock()
		list = append(list, info)
	}
	return list
}

// GetDevice returns snapshot info for a specific device.
func (s *Server) GetDevice(id string) (DeviceInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dev, ok := s.devices[id]
	if !ok {
		return DeviceInfo{}, false
	}

	dev.mu.RLock()
	defer dev.mu.RUnlock()

	info := dev.info
	info.ActiveStreams = atomic.LoadInt64(&dev.activeStreams)
	info.TotalConnections = atomic.LoadInt64(&dev.totalConnections)
	info.BytesSent = atomic.LoadInt64(&dev.bytesSent)
	info.BytesReceived = atomic.LoadInt64(&dev.bytesReceived)
	return info, true
}

// DisconnectDevice forces a device disconnection and commands the agent to exit cleanly.
func (s *Server) DisconnectDevice(id string) error {
	s.mu.RLock()
	dev, ok := s.devices[id]
	s.mu.RUnlock()

	if !ok {
		return ErrNotFound
	}

	dev.mu.Lock()
	if dev.controlStream != nil {
		s.logger.Printf("Sending STOP command to agent on device %s...", id)
		_, _ = dev.controlStream.Write([]byte("STOP\n"))
		_ = dev.controlStream.Close()
		dev.controlStream = nil
	}
	dev.mu.Unlock()

	// Wait briefly for agent to receive command and exit gracefully
	time.Sleep(250 * time.Millisecond)

	s.EvictDevice(id)
	s.portPool.Release(dev.info.AssignedPort, id)
	return nil
}

func (s *Server) EvictDevice(deviceID string) {
	s.mu.Lock()
	dev, exists := s.devices[deviceID]
	if exists {
		delete(s.devices, deviceID)
	}
	s.mu.Unlock()

	if !exists {
		return
	}

	dev.mu.Lock()
	if dev.closed {
		dev.mu.Unlock()
		return
	}
	dev.closed = true
	dev.info.Status = "DISCONNECTED"
	
	// Close resources to free OS ports
	if dev.tcpListener != nil {
		_ = dev.tcpListener.Close()
		dev.tcpListener = nil
	}
	if dev.yamuxSession != nil {
		_ = dev.yamuxSession.Close()
		dev.yamuxSession = nil
	}
	if dev.controlStream != nil {
		_ = dev.controlStream.Close()
		dev.controlStream = nil
	}
	if dev.disconnectTimer != nil {
		dev.disconnectTimer.Stop()
		dev.disconnectTimer = nil
	}

	// Manually trigger port release to ensure PortPool state is updated before the new connection acquires it.
	// The new connection will immediately reclaim this reservation in PortPool.Acquire.
	s.portPool.ReleaseWithGracePeriod(dev.info.AssignedPort, deviceID, s.cfg.GracePeriod)

	s.logger.Printf("Evicted previous session for device %s to allow rapid reconnect", deviceID)
}

// Stop shuts down the server and all device listeners.
func (s *Server) Stop() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.shutdownChan)
	if s.listener != nil {
		_ = s.listener.Close()
	}

	for _, dev := range s.devices {
		dev.mu.Lock()
		dev.closed = true
		if dev.tcpListener != nil {
			_ = dev.tcpListener.Close()
		}
		if dev.yamuxSession != nil {
			_ = dev.yamuxSession.Close()
		}
		if dev.disconnectTimer != nil {
			dev.disconnectTimer.Stop()
		}
		dev.mu.Unlock()
	}
	s.mu.Unlock()

	s.wg.Wait()
	return nil
}

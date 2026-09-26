package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
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

const (
	controlStreamWait = 2 * time.Second
	stopRequestTTL    = 30 * time.Second
)

// Config configures the AdbLink Server.
type Config struct {
	ListenAddr        string        // e.g. ":9000"
	AdvertiseHost     string        // e.g. "127.0.0.1" or public IP
	PortMin           int           // e.g. 55550
	PortMax           int           // e.g. 55599
	Token             string        // Shared secret auth token (optional)
	GracePeriod       time.Duration // Reconnection grace period (e.g. 30s)
	HeartbeatInterval time.Duration // Yamux heartbeat interval
	Logger            *log.Logger
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
	closeOnce        sync.Once
	controlReady     chan struct{}
	controlReadyOnce sync.Once
	controlWriteMu   sync.Mutex
	stopAck          chan struct{}
	stopAckOnce      sync.Once
	disconnectTimer  *time.Timer
	activeStreams    int64
	totalConnections int64
	bytesSent        int64
	bytesReceived    int64
}

// Server is the central reverse-tunnel ADB server.
type Server struct {
	cfg            Config
	logger         *log.Logger
	portPool       *PortPool
	listener       net.Listener
	mu             sync.RWMutex
	registrationMu sync.Mutex
	devices        map[string]*DeviceSession
	stopRequests   map[string]time.Time
	closed         bool
	shutdownChan   chan struct{}
	wg             sync.WaitGroup
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
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = tunnel.DefaultHeartbeatInterval
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
		stopRequests: make(map[string]time.Time),
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
	tunnel.ConfigureTCPConn(conn)

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
	// Serialize eviction, port allocation, listener binding, and registry
	// insertion for the same server. Without this, two fast reconnects could
	// both pass the old-session check and leak a port or replace each other.
	s.registrationMu.Lock()
	defer s.registrationMu.Unlock()
	if s.isStopRequested(req.DeviceID) {
		s.logger.Printf("Rejecting reconnect for manually disconnected device %s", req.DeviceID)
		_ = protocol.WriteMsg(conn, protocol.RegisterResponse{
			Magic:   protocol.MagicHeader,
			Version: protocol.CurrentProtocolVersion,
			Status:  protocol.StatusStopped,
			Message: "agent was manually disconnected from the web console",
		})
		_ = conn.Close()
		return
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
		ServerVersion: "1.5.2",
		AdvertiseHost: s.cfg.AdvertiseHost,
	}
	if err := protocol.WriteMsg(conn, resp); err != nil {
		s.logger.Printf("Failed to send register response to %s: %v", remoteAddr, err)
		s.portPool.Release(assignedPort, req.DeviceID)
		_ = conn.Close()
		return
	}

	// Upgrade connection to Yamux server
	session, err := yamux.Server(conn, tunnel.YamuxConfig(s.cfg.HeartbeatInterval))
	if err != nil {
		s.logger.Printf("Failed to create yamux server session for %s: %v", remoteAddr, err)
		s.portPool.Release(assignedPort, req.DeviceID)
		_ = conn.Close()
		return
	}

	// Start exposed ADB listener
	adbListenAddr := fmt.Sprintf(":%d", assignedPort)
	adbListener, err := net.Listen("tcp", adbListenAddr)
	if err != nil {
		s.logger.Printf("Failed to bind ADB port %s: %v", adbListenAddr, err)
		s.portPool.Release(assignedPort, req.DeviceID)
		_ = session.Close()
		return
	}

	devSession := s.registerDeviceSession(req, remoteAddr, assignedPort, session, adbListener)
	s.logger.Printf("Device registered: %s (model: %s) -> Port %d. Connect via: adb connect %s",
		req.DeviceID, req.Model, assignedPort, net.JoinHostPort(s.cfg.AdvertiseHost, strconv.Itoa(assignedPort)))

	// Accept and monitor the dedicated control stream from the agent. Keeping
	// this reader alive is also what lets an explicit web-console disconnect
	// wait for STOP_ACK instead of racing the transport close.
	go s.acceptControlStream(devSession)

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
	oldSession := s.devices[req.DeviceID]

	devSession := &DeviceSession{
		info: DeviceInfo{
			DeviceID:         req.DeviceID,
			Model:            req.Model,
			Manufacturer:     req.Manufacturer,
			AndroidVersion:   req.AndroidVersion,
			RemoteAddr:       remoteAddr,
			AssignedPort:     assignedPort,
			AdbConnectTarget: net.JoinHostPort(s.cfg.AdvertiseHost, strconv.Itoa(assignedPort)),
			ConnectedAt:      time.Now(),
			LastSeenAt:       time.Now(),
			Status:           "ONLINE",
		},
		yamuxSession: session,
		tcpListener:  adbListener,
		closeChan:    make(chan struct{}),
		controlReady: make(chan struct{}),
		stopAck:      make(chan struct{}),
	}

	s.devices[req.DeviceID] = devSession
	s.mu.Unlock()

	if oldSession != nil {
		// This is a defensive path for a concurrent lifecycle race. The normal
		// registration flow evicts the previous session before reaching here.
		oldDeviceID, oldPort, _, _, _, _, _ := s.closeDeviceResources(oldSession, "DISCONNECTED")
		if oldPort != assignedPort {
			s.portPool.ReleaseWithGracePeriod(oldPort, oldDeviceID, s.cfg.GracePeriod)
		}
	}
	return devSession
}

// acceptControlStream accepts the first agent-created stream and keeps it
// reserved for management commands. All ADB streams are opened by the server
// later, so accepting this stream separately prevents STOP/control races.
func (s *Server) acceptControlStream(dev *DeviceSession) {
	dev.mu.RLock()
	session := dev.yamuxSession
	dev.mu.RUnlock()
	if session == nil {
		dev.controlReadyOnce.Do(func() { close(dev.controlReady) })
		return
	}

	stream, err := session.AcceptStream()
	if err != nil {
		dev.controlReadyOnce.Do(func() { close(dev.controlReady) })
		return
	}

	dev.mu.Lock()
	if dev.closed {
		dev.mu.Unlock()
		_ = stream.Close()
		dev.controlReadyOnce.Do(func() { close(dev.controlReady) })
		return
	}
	dev.controlStream = stream
	dev.mu.Unlock()
	dev.controlReadyOnce.Do(func() { close(dev.controlReady) })
	go s.runControlHeartbeat(dev)

	reader := bufio.NewReader(stream)
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			dev.mu.RLock()
			closed := dev.closed
			dev.mu.RUnlock()
			if !closed && !session.IsClosed() {
				// A control stream that disappears independently of Yamux is not
				// a usable agent session. Closing Yamux wakes monitorSession and
				// lets the agent's reconnect loop take over.
				_ = session.Close()
			}
			return
		}

		switch strings.TrimSpace(line) {
		case "STOP_ACK":
			dev.stopAckOnce.Do(func() { close(dev.stopAck) })
		case "PONG":
			dev.mu.Lock()
			dev.info.LastSeenAt = time.Now()
			dev.mu.Unlock()
		}
	}
}

// runControlHeartbeat sends a lightweight application heartbeat while the
// tunnel is idle. Yamux also sends transport-level PING/PONG frames; this
// control heartbeat gives the server an accurate LastSeenAt value without
// adding more frames while a large ADB stream is already moving data.
func (s *Server) runControlHeartbeat(dev *DeviceSession) {
	ticker := time.NewTicker(s.cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.shutdownChan:
			return
		case <-dev.closeChan:
			return
		case <-ticker.C:
			if atomic.LoadInt64(&dev.activeStreams) > 0 {
				continue
			}
			if err := s.sendControlCommand(dev, "PING\n"); err != nil {
				return
			}
		}
	}
}

func (s *Server) isStopRequested(deviceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	expiresAt, ok := s.stopRequests[deviceID]
	if !ok {
		return false
	}
	if time.Now().After(expiresAt) {
		delete(s.stopRequests, deviceID)
		return false
	}
	return true
}

func (s *Server) requestStop(deviceID string) {
	ttl := stopRequestTTL
	if s.cfg.GracePeriod > ttl {
		ttl = s.cfg.GracePeriod
	}
	s.mu.Lock()
	s.stopRequests[deviceID] = time.Now().Add(ttl)
	s.mu.Unlock()
}

// runAdbForwardingLoop accepts ADB connections from clients and tunnels them to the agent.
func (s *Server) runAdbForwardingLoop(dev *DeviceSession) {
	for {
		dev.mu.RLock()
		listener := dev.tcpListener
		dev.mu.RUnlock()
		if listener == nil {
			return
		}

		clientConn, err := listener.Accept()
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
	deviceID := dev.info.DeviceID
	dev.mu.RUnlock()

	if session == nil || session.IsClosed() {
		_ = clientConn.Close()
		return
	}

	// Open a multiplexed stream to the Android agent
	stream, err := session.OpenStream()
	if err != nil {
		s.logger.Printf("Failed to open yamux stream to device %s: %v", deviceID, err)
		_ = clientConn.Close()
		return
	}

	n1, n2, forwardErr := tunnel.ForwardWithCallbacks(
		clientConn, stream,
		func(n int64) {
			atomic.AddInt64(&dev.bytesReceived, n)
		},
		func(n int64) {
			atomic.AddInt64(&dev.bytesSent, n)
		},
	)
	if forwardErr != nil && !errors.Is(forwardErr, io.EOF) && !errors.Is(forwardErr, net.ErrClosed) {
		s.logger.Printf("ADB stream for device %s ended with error: %v (host->phone: %d, phone->host: %d)",
			deviceID, forwardErr, n1, n2)
	}

	dev.mu.Lock()
	dev.info.LastSeenAt = time.Now()
	dev.mu.Unlock()
}

// monitorSession detects disconnection and manages the grace period.
func (s *Server) monitorSession(dev *DeviceSession) {
	dev.mu.RLock()
	session := dev.yamuxSession
	closed := dev.closed
	dev.mu.RUnlock()

	if closed || session == nil || session.IsClosed() {
		s.handleDeviceDisconnected(dev)
		return
	}

	// YamuxConfig owns the bidirectional PING/PONG heartbeat. This monitor only
	// waits for Yamux's authoritative close signal; sending another Ping here
	// would make a transient write delay compete with an active ADB transfer.
	select {
	case <-s.shutdownChan:
	case <-dev.closeChan:
	case <-session.CloseChan():
		dev.mu.RLock()
		deviceID := dev.info.DeviceID
		dev.mu.RUnlock()
		s.logger.Printf("Yamux session closed for device %s", deviceID)
		s.handleDeviceDisconnected(dev)
	}
}

// closeDeviceResources marks a session closed and detaches its resources.
// Network operations are performed after releasing dev.mu so a Yamux shutdown
// cannot deadlock a goroutine that is trying to observe the device state.
func (s *Server) closeDeviceResources(dev *DeviceSession, status string) (deviceID string, port int, session *yamux.Session, listener net.Listener, control net.Conn, timer *time.Timer, alreadyClosed bool) {
	dev.mu.Lock()
	deviceID = dev.info.DeviceID
	port = dev.info.AssignedPort
	alreadyClosed = dev.closed
	if !dev.closed {
		dev.closed = true
		if status != "" {
			dev.info.Status = status
		}
	}
	dev.closeOnce.Do(func() { close(dev.closeChan) })
	dev.controlReadyOnce.Do(func() { close(dev.controlReady) })

	session = dev.yamuxSession
	dev.yamuxSession = nil
	listener = dev.tcpListener
	dev.tcpListener = nil
	control = dev.controlStream
	dev.controlStream = nil
	timer = dev.disconnectTimer
	dev.disconnectTimer = nil
	dev.mu.Unlock()

	if listener != nil {
		_ = listener.Close()
	}
	if session != nil {
		_ = session.Close()
	}
	if control != nil {
		_ = control.Close()
	}
	if timer != nil {
		timer.Stop()
	}
	return
}

func (s *Server) handleDeviceDisconnected(dev *DeviceSession) {
	deviceID, port, _, _, _, _, alreadyClosed := s.closeDeviceResources(dev, "DISCONNECTED")
	if alreadyClosed {
		return
	}

	// Verify this session is still the active one before releasing its port
	s.mu.RLock()
	currentDev, exists := s.devices[deviceID]
	s.mu.RUnlock()
	if exists && currentDev != dev {
		s.logger.Printf("Skipping port release for %s: a new session has already taken over", deviceID)
		return
	}
	if !exists {
		return
	}
	s.logger.Printf("Device %s disconnected. Keeping port %d reserved for %v grace period",
		deviceID, port, s.cfg.GracePeriod)

	s.portPool.ReleaseWithGracePeriod(port, deviceID, s.cfg.GracePeriod)

	// Set timer to purge device after grace period
	dev.mu.Lock()
	if dev.disconnectTimer == nil {
		dev.disconnectTimer = time.AfterFunc(s.cfg.GracePeriod, func() {
			s.purgeDevice(deviceID, dev)
		})
	}
	dev.mu.Unlock()
}

// purgeDevice removes the device completely after grace period expires.
func (s *Server) purgeDevice(deviceID string, expectedSession ...*DeviceSession) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dev, exists := s.devices[deviceID]
	var expected *DeviceSession
	if len(expectedSession) > 0 {
		expected = expectedSession[0]
	}
	if !exists || (expected != nil && dev != expected) {
		return
	}

	dev.mu.Lock()
	if dev.info.Status == "ONLINE" {
		dev.mu.Unlock()
		return
	}
	dev.closed = true
	dev.closeOnce.Do(func() { close(dev.closeChan) })
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
	// Serialize the stop request with a concurrent registration. This prevents a
	// reconnect from winning the port allocation race while the web request is
	// still waiting for STOP_ACK.
	s.registrationMu.Lock()
	defer s.registrationMu.Unlock()

	s.mu.RLock()
	dev, ok := s.devices[id]
	s.mu.RUnlock()

	if !ok {
		return ErrNotFound
	}

	s.requestStop(id)

	dev.mu.RLock()
	controlReady := dev.controlReady
	dev.mu.RUnlock()
	select {
	case <-controlReady:
	case <-time.After(controlStreamWait):
		s.logger.Printf("Timed out waiting for control stream from device %s; closing transport", id)
	case <-s.shutdownChan:
	}

	if err := s.sendControlCommand(dev, "STOP\n"); err != nil && !errors.Is(err, net.ErrClosed) {
		s.logger.Printf("Unable to send STOP to device %s: %v", id, err)
	} else if err == nil {
		select {
		case <-dev.stopAck:
		case <-time.After(controlStreamWait):
			s.logger.Printf("Timed out waiting for STOP_ACK from device %s; closing transport", id)
		case <-s.shutdownChan:
		}
	}

	// Explicit disconnects release the port immediately. The stop tombstone
	// above blocks a reconnect race and makes the agent exit on the next
	// registration attempt if STOP could not reach it.
	s.evictDeviceSession(id, dev, false)
	return nil
}

func (s *Server) sendControlCommand(dev *DeviceSession, command string) error {
	dev.mu.RLock()
	control := dev.controlStream
	closed := dev.closed
	dev.mu.RUnlock()
	if control == nil || closed {
		return net.ErrClosed
	}

	dev.controlWriteMu.Lock()
	defer dev.controlWriteMu.Unlock()
	_ = control.SetWriteDeadline(time.Now().Add(controlStreamWait))
	_, err := io.WriteString(control, command)
	_ = control.SetWriteDeadline(time.Time{})
	return err
}

func (s *Server) EvictDevice(deviceID string) {
	s.evictDeviceSession(deviceID, nil, true)
}

// evictDeviceSession removes exactly the expected session when expected is
// non-nil. This identity check is important for manual disconnects: an old
// request must never release the port belonging to a freshly reconnected
// session.
func (s *Server) evictDeviceSession(deviceID string, expected *DeviceSession, keepGrace bool) bool {
	s.mu.Lock()
	dev, exists := s.devices[deviceID]
	if exists && expected != nil && dev != expected {
		s.mu.Unlock()
		return false
	}
	if exists {
		delete(s.devices, deviceID)
	}
	s.mu.Unlock()

	if !exists {
		return false
	}

	_, port, _, _, _, _, _ := s.closeDeviceResources(dev, "DISCONNECTED")
	if keepGrace {
		// A transport eviction is normally followed by an automatic reconnect;
		// reserve the same port for that device during the grace period.
		s.portPool.ReleaseWithGracePeriod(port, deviceID, s.cfg.GracePeriod)
	} else {
		s.portPool.Release(port, deviceID)
	}

	s.logger.Printf("Evicted previous session for device %s", deviceID)
	return true
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
	devices := make([]*DeviceSession, 0, len(s.devices))
	for id, dev := range s.devices {
		devices = append(devices, dev)
		delete(s.devices, id)
	}
	s.mu.Unlock()

	for _, dev := range devices {
		deviceID, port, _, _, _, _, _ := s.closeDeviceResources(dev, "DISCONNECTED")
		s.portPool.Release(port, deviceID)
	}

	s.wg.Wait()
	return nil
}

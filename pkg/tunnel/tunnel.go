package tunnel

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

const (
	// DefaultHeartbeatInterval is deliberately shorter than the idle timeout
	// used by many mobile carriers and NAT devices. Yamux PING frames keep an
	// otherwise idle agent connection from being silently discarded.
	DefaultHeartbeatInterval = 15 * time.Second

	// Yamux writes share the underlying TCP connection with all ADB streams.
	// A congested mobile link can take longer than the yamux default to accept a
	// frame, so the write safety valve must not be treated as an ADB operation
	// timeout.
	yamuxConnectionWriteTimeout = 2 * time.Minute
	// Keep a long, but bounded, half-close grace period for commands such as
	// adb install, which may finish the upload before returning the result.
	yamuxStreamCloseTimeout = 5 * time.Minute
)

var bufPool = sync.Pool{
	New: func() interface{} {
		// 64KB buffer for high throughput adb push/pull
		buf := make([]byte, 64*1024)
		return &buf
	},
}

// DefaultYamuxConfig returns a tuned Yamux configuration for high-throughput ADB tunneling.
func DefaultYamuxConfig() *yamux.Config {
	return YamuxConfig(DefaultHeartbeatInterval)
}

// YamuxConfig returns a tuned Yamux configuration with an explicit heartbeat
// interval. A non-positive interval uses DefaultHeartbeatInterval.
//
// Yamux's keepalive is enabled on purpose: the PING/PONG frames keep idle
// mobile/NAT connections alive and make a genuinely dead TCP session close so
// the agent can reconnect. ConnectionWriteTimeout is intentionally much longer
// than the heartbeat interval because a PING shares the ordered TCP connection
// with ADB payload data.
func YamuxConfig(heartbeatInterval time.Duration) *yamux.Config {
	if heartbeatInterval <= 0 {
		heartbeatInterval = DefaultHeartbeatInterval
	}

	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = heartbeatInterval
	cfg.ConnectionWriteTimeout = yamuxConnectionWriteTimeout
	// Allow 1MB window size for fast file transfer (adb push/pull)
	cfg.MaxStreamWindowSize = 1024 * 1024
	cfg.StreamOpenTimeout = 30 * time.Second
	cfg.StreamCloseTimeout = yamuxStreamCloseTimeout
	cfg.LogOutput = io.Discard
	return cfg
}

// ConfigureTCPConn enables transport-level liveness without imposing an
// application-level deadline on ADB data. It is safe to call for any net.Conn.
func ConfigureTCPConn(conn net.Conn) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tcpConn.SetKeepAlive(true)
	_ = tcpConn.SetKeepAlivePeriod(30 * time.Second)
}

type closeWriter interface {
	CloseWrite() error
}

func copyLoop(dst, src net.Conn, onBytes func(int64)) (int64, error) {
	bufPtr := bufPool.Get().(*[]byte)
	defer bufPool.Put(bufPtr)
	buf := *bufPtr

	var total int64
	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw > 0 {
				total += int64(nw)
				if onBytes != nil {
					onBytes(int64(nw))
				}
			}
			if ew != nil {
				return total, ew
			}
			if nr != nw {
				return total, io.ErrShortWrite
			}
		}
		if er != nil {
			if er != io.EOF {
				return total, er
			}
			break
		}
	}
	return total, nil
}

// Forward copies data bidirectionally between conn1 and conn2.
func Forward(conn1, conn2 net.Conn) (n1to2, n2to1 int64, err error) {
	return ForwardWithCallbacks(conn1, conn2, nil, nil)
}

// ForwardWithCallbacks copies data bidirectionally between conn1 and conn2, invoking onBytes callbacks in real-time.
func ForwardWithCallbacks(
	conn1, conn2 net.Conn,
	onBytes1to2 func(int64),
	onBytes2to1 func(int64),
) (n1to2, n2to1 int64, err error) {
	var wg sync.WaitGroup
	wg.Add(2)

	var err1, err2 error

	// conn1 -> conn2
	go func() {
		defer wg.Done()
		n1to2, err1 = copyLoop(conn2, conn1, onBytes1to2)
		if cw, ok := conn2.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			_ = conn2.Close()
		}
	}()

	// conn2 -> conn1
	go func() {
		defer wg.Done()
		n2to1, err2 = copyLoop(conn1, conn2, onBytes2to1)
		if cw, ok := conn1.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			_ = conn1.Close()
		}
	}()

	wg.Wait()
	_ = conn1.Close()
	_ = conn2.Close()

	if err1 != nil && err1 != io.EOF {
		return n1to2, n2to1, err1
	}
	if err2 != nil && err2 != io.EOF {
		return n1to2, n2to1, err2
	}

	return n1to2, n2to1, nil
}

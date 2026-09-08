package tunnel

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
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
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 15 * time.Second
	cfg.ConnectionWriteTimeout = 15 * time.Second
	// Allow 1MB window size for fast file transfer (adb push/pull)
	cfg.MaxStreamWindowSize = 1024 * 1024
	cfg.StreamOpenTimeout = 30 * time.Second
	cfg.StreamCloseTimeout = 30 * time.Second
	cfg.LogOutput = io.Discard
	return cfg
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

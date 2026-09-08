package tunnel

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

func TestForwardBidirectional(t *testing.T) {
	// Create pipe
	c1, c2 := net.Pipe()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		buf := make([]byte, 5)
		if _, err := io.ReadFull(c2, buf); err != nil {
			t.Errorf("read from c2 failed: %v", err)
			return
		}
		if string(buf) != "PING!" {
			t.Errorf("expected PING!, got %s", string(buf))
			return
		}
		if _, err := c2.Write([]byte("PONG!")); err != nil {
			t.Errorf("write to c2 failed: %v", err)
			return
		}
		_ = c2.Close()
	}()

	clientConn1, clientConn2 := net.Pipe()

	go func() {
		_, _, _ = Forward(c1, clientConn1)
	}()

	// Write from clientConn2
	if _, err := clientConn2.Write([]byte("PING!")); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	reply := make([]byte, 5)
	if _, err := io.ReadFull(clientConn2, reply); err != nil {
		t.Fatalf("read reply failed: %v", err)
	}
	if string(reply) != "PONG!" {
		t.Fatalf("expected PONG!, got %s", string(reply))
	}

	_ = clientConn2.Close()
	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server done")
	}
}

func TestYamuxLoopback(t *testing.T) {
	clientConn, serverConn := net.Pipe()

	serverSession, err := yamux.Server(serverConn, DefaultYamuxConfig())
	if err != nil {
		t.Fatalf("yamux.Server failed: %v", err)
	}
	defer serverSession.Close()

	clientSession, err := yamux.Client(clientConn, DefaultYamuxConfig())
	if err != nil {
		t.Fatalf("yamux.Client failed: %v", err)
	}
	defer clientSession.Close()

	accepted := make(chan *yamux.Stream, 1)
	go func() {
		stream, err := serverSession.AcceptStream()
		if err != nil {
			return
		}
		accepted <- stream
	}()

	clientStream, err := clientSession.OpenStream()
	if err != nil {
		t.Fatalf("OpenStream failed: %v", err)
	}
	defer clientStream.Close()

	var serverStream *yamux.Stream
	select {
	case serverStream = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for server AcceptStream")
	}
	defer serverStream.Close()

	testData := []byte("hello yamux stream test data")
	go func() {
		_, _ = clientStream.Write(testData)
	}()

	buf := make([]byte, len(testData))
	if _, err := io.ReadFull(serverStream, buf); err != nil {
		t.Fatalf("ReadFull failed: %v", err)
	}

	if !bytes.Equal(buf, testData) {
		t.Fatalf("data mismatch: got %s, want %s", string(buf), string(testData))
	}
}

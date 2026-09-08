package web

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/goldenduo/AdbLink/pkg/server"
)

func TestWebEndpoints(t *testing.T) {
	srv, err := server.NewServer(server.Config{
		ListenAddr: "127.0.0.1:0",
		Logger:     log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	defer srv.Stop()

	ws := NewWebServer(srv, "127.0.0.1:0", log.New(io.Discard, "", 0))

	// Test GET /
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	ws.handleIndex(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Test GET /api/v1/health
	req = httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	w = httptest.NewRecorder()
	ws.handleHealth(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var healthResp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&healthResp); err != nil {
		t.Fatalf("decode health resp error: %v", err)
	}
	if healthResp["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", healthResp["status"])
	}

	// Test GET /api/v1/devices
	req = httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	w = httptest.NewRecorder()
	ws.handleDevices(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var devices []server.DeviceInfo
	if err := json.NewDecoder(w.Body).Decode(&devices); err != nil {
		t.Fatalf("decode devices error: %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("expected 0 devices, got %d", len(devices))
	}
}

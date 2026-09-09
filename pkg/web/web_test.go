package web

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/goldenduo/AdbLink/pkg/server"
)

func TestWebEndpoints(t *testing.T) {
	tempDataDir, err := os.MkdirTemp("", "adblink-web-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDataDir)

	srv, err := server.NewServer(server.Config{
		ListenAddr: "127.0.0.1:0",
		Logger:     log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	defer srv.Stop()

	ws := NewWebServer(srv, "127.0.0.1:0", log.New(io.Discard, "", 0), tempDataDir, "")

	// 1. Test GET /
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	ws.handleIndex(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for index, got %d", w.Code)
	}

	// 2. Test GET /api/v1/health
	req = httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	w = httptest.NewRecorder()
	ws.handleHealth(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for health, got %d", w.Code)
	}

	// 3. Test Auth Status (initially false)
	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/status", nil)
	w = httptest.NewRecorder()
	ws.handleAuthStatus(w, req)
	var statusResp map[string]bool
	_ = json.NewDecoder(w.Body).Decode(&statusResp)
	if statusResp["initialized"] || statusResp["logged_in"] {
		t.Fatalf("expected uninitialized and logged_out, got %+v", statusResp)
	}

	// 4. Test Initial Setup
	setupBody := []byte(`{"password":"admin_password_123","confirm_password":"admin_password_123"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", bytes.NewReader(setupBody))
	w = httptest.NewRecorder()
	ws.handleAuthSetup(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on setup, got %d: %s", w.Code, w.Body.String())
	}

	// Extract session cookie from setup
	cookies := w.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatalf("expected session cookie after setup")
	}

	// 5. Test Access Protected /api/v1/devices with Cookie
	req = httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	req.AddCookie(sessionCookie)
	w = httptest.NewRecorder()
	ws.handleDevices(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with session cookie, got %d", w.Code)
	}

	// 6. Test Access from non-localhost without Cookie -> 401
	req = httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	req.RemoteAddr = "198.51.100.10:4567" // External IP
	w = httptest.NewRecorder()
	ws.handleDevices(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthorized external request, got %d", w.Code)
	}

	// 7. Test Login with wrong password
	loginWrong := []byte(`{"password":"wrong","remember_me":true}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginWrong))
	w = httptest.NewRecorder()
	ws.handleAuthLogin(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on wrong login password, got %d", w.Code)
	}

	// 8. Test Login with correct password and remember_me
	loginRight := []byte(`{"password":"admin_password_123","remember_me":true}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginRight))
	w = httptest.NewRecorder()
	ws.handleAuthLogin(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on correct login, got %d", w.Code)
	}
	loginCookies := w.Result().Cookies()
	var rememberCookie *http.Cookie
	for _, c := range loginCookies {
		if c.Name == sessionCookieName {
			rememberCookie = c
			break
		}
	}
	if rememberCookie == nil || rememberCookie.MaxAge <= 0 {
		t.Fatalf("expected remember me cookie with positive MaxAge")
	}

	// 9. Test Change Password
	changeBody := []byte(`{"old_password":"admin_password_123","new_password":"new_secure_pass","confirm_password":"new_secure_pass"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", bytes.NewReader(changeBody))
	req.AddCookie(rememberCookie)
	w = httptest.NewRecorder()
	ws.handleAuthPassword(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on change password, got %d: %s", w.Code, w.Body.String())
	}

	// 10. Test Logout
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(rememberCookie)
	w = httptest.NewRecorder()
	ws.handleAuthLogout(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on logout, got %d", w.Code)
	}

	// Token should now be invalid
	if ws.auth.ValidateSession(rememberCookie.Value) {
		t.Fatalf("session should be invalid after logout")
	}
}

package web

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuthManagerSetupAndLogin(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "adblink-auth-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	am, err := NewAuthManager(tempDir, "test-secret-token")
	if err != nil {
		t.Fatalf("failed to init auth manager: %v", err)
	}

	// 1. Check initial state
	if am.IsInitialized() {
		t.Fatalf("expected uninitialized auth manager")
	}

	// 2. Setup password
	if err := am.SetupPassword("short"); err == nil {
		t.Fatalf("expected error on password shorter than 6 chars")
	}
	if err := am.SetupPassword("admin123456"); err != nil {
		t.Fatalf("setup password failed: %v", err)
	}
	if !am.IsInitialized() {
		t.Fatalf("expected initialized auth manager after setup")
	}

	// Duplicate setup should fail
	if err := am.SetupPassword("anotherPassword"); err == nil {
		t.Fatalf("expected error on duplicate setup")
	}

	// 3. Verify password
	if !am.VerifyPassword("admin123456") {
		t.Fatalf("verify password failed for correct password")
	}
	if am.VerifyPassword("wrongPassword") {
		t.Fatalf("verify password passed for wrong password")
	}

	// 4. Test persistence
	amReloaded, err := NewAuthManager(tempDir, "test-secret-token")
	if err != nil {
		t.Fatalf("reload auth manager failed: %v", err)
	}
	if !amReloaded.IsInitialized() || !amReloaded.VerifyPassword("admin123456") {
		t.Fatalf("persistence failed after reload")
	}

	// 5. Change password
	if err := am.ChangePassword("wrongPass", "newpass123"); err == nil {
		t.Fatalf("expected error changing password with wrong old password")
	}
	if err := am.ChangePassword("admin123456", "newpass123"); err != nil {
		t.Fatalf("change password failed: %v", err)
	}
	if !am.VerifyPassword("newpass123") {
		t.Fatalf("new password not active")
	}
	if am.VerifyPassword("admin123456") {
		t.Fatalf("old password still active")
	}

	// 6. Session management
	token, ttl, err := am.CreateSession(false)
	if err != nil {
		t.Fatalf("create session failed: %v", err)
	}
	if ttl != defaultSessionTTL {
		t.Fatalf("unexpected ttl for standard session: %v", ttl)
	}
	if !am.ValidateSession(token) {
		t.Fatalf("validate session failed for valid token")
	}

	// Remember me session
	rememberToken, rememberTTL, err := am.CreateSession(true)
	if err != nil {
		t.Fatalf("create remember session failed: %v", err)
	}
	if rememberTTL != rememberMeTTL {
		t.Fatalf("unexpected ttl for remember session: %v", rememberTTL)
	}
	if !am.ValidateSession(rememberToken) {
		t.Fatalf("validate session failed for remember token")
	}

	// Revoke session
	am.RevokeSession(token)
	if am.ValidateSession(token) {
		t.Fatalf("session still valid after revocation")
	}

	// 7. API Token check
	if !am.CheckAPIToken("test-secret-token") {
		t.Fatalf("api token check failed for correct token")
	}
	if am.CheckAPIToken("wrong-token") {
		t.Fatalf("api token check passed for wrong token")
	}
}

func TestSessionExpiration(t *testing.T) {
	tempDir := filepath.Join(os.TempDir(), "adblink-auth-expire")
	_ = os.RemoveAll(tempDir)
	defer os.RemoveAll(tempDir)

	am, _ := NewAuthManager(tempDir, "")
	token, _, _ := am.CreateSession(false)

	am.mu.Lock()
	s := am.sessions[token]
	s.ExpiresAt = time.Now().Add(-1 * time.Second)
	am.sessions[token] = s
	am.mu.Unlock()

	if am.ValidateSession(token) {
		t.Fatalf("expected expired session to be invalid")
	}
}

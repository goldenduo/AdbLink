package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	sessionCookieName = "adblink_session"
	defaultSessionTTL = 12 * time.Hour
	rememberMeTTL     = 30 * 24 * time.Hour
)

// AuthConfig stores persisted credentials.
type AuthConfig struct {
	Initialized  bool   `json:"initialized"`
	Salt         string `json:"salt"`
	PasswordHash string `json:"password_hash"`
	UpdatedAt    string `json:"updated_at"`
}

// SessionInfo holds active session data.
type SessionInfo struct {
	Token      string    `json:"token"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	RememberMe bool      `json:"remember_me"`
}

// AuthManager manages authentication state, persistent password, and sessions.
type AuthManager struct {
	mu          sync.RWMutex
	filePath    string
	cfg         AuthConfig
	sessions    map[string]SessionInfo
	serverToken string
}

// NewAuthManager initializes the authentication manager and loads existing credentials.
func NewAuthManager(dataDir, serverToken string) (*AuthManager, error) {
	if dataDir == "" {
		dataDir = "data"
	}
	_ = os.MkdirAll(dataDir, 0750)
	authFile := filepath.Join(dataDir, "auth.json")

	am := &AuthManager{
		filePath:    authFile,
		sessions:    make(map[string]SessionInfo),
		serverToken: serverToken,
	}

	if err := am.load(); err != nil {
		return nil, err
	}
	return am, nil
}

func (a *AuthManager) load() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	data, err := os.ReadFile(a.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			a.cfg = AuthConfig{Initialized: false}
			return nil
		}
		return fmt.Errorf("failed to read auth file: %w", err)
	}

	var cfg AuthConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse auth file: %w", err)
	}
	a.cfg = cfg
	return nil
}

func (a *AuthManager) saveLocked() error {
	data, err := json.MarshalIndent(a.cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.filePath, data, 0600)
}

// IsInitialized returns whether an admin password has been set.
func (a *AuthManager) IsInitialized() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg.Initialized
}

// SetupPassword sets the initial password. Fails if already initialized.
func (a *AuthManager) SetupPassword(password string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cfg.Initialized {
		return errors.New("admin password already initialized")
	}
	if len(password) < 6 {
		return errors.New("password must be at least 6 characters long")
	}

	salt, hash, err := hashPassword(password)
	if err != nil {
		return err
	}

	a.cfg = AuthConfig{
		Initialized:  true,
		Salt:         salt,
		PasswordHash: hash,
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	return a.saveLocked()
}

// VerifyPassword checks if the supplied password is correct.
func (a *AuthManager) VerifyPassword(password string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if !a.cfg.Initialized {
		return false
	}
	return verifyPassword(password, a.cfg.Salt, a.cfg.PasswordHash)
}

// ChangePassword verifies old password and sets new password.
func (a *AuthManager) ChangePassword(oldPass, newPass string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.cfg.Initialized {
		return errors.New("admin password not initialized")
	}
	if !verifyPassword(oldPass, a.cfg.Salt, a.cfg.PasswordHash) {
		return errors.New("incorrect current password")
	}
	if len(newPass) < 6 {
		return errors.New("new password must be at least 6 characters long")
	}

	salt, hash, err := hashPassword(newPass)
	if err != nil {
		return err
	}

	a.cfg.Salt = salt
	a.cfg.PasswordHash = hash
	a.cfg.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return a.saveLocked()
}

// CreateSession generates a new session token.
func (a *AuthManager) CreateSession(rememberMe bool) (string, time.Duration, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", 0, err
	}
	token := hex.EncodeToString(tokenBytes)

	ttl := defaultSessionTTL
	if rememberMe {
		ttl = rememberMeTTL
	}

	a.sessions[token] = SessionInfo{
		Token:      token,
		CreatedAt:  time.Now(),
		ExpiresAt:  time.Now().Add(ttl),
		RememberMe: rememberMe,
	}

	// Clean expired sessions periodically
	for k, s := range a.sessions {
		if time.Now().After(s.ExpiresAt) {
			delete(a.sessions, k)
		}
	}

	return token, ttl, nil
}

// ValidateSession checks if session token is valid and active.
func (a *AuthManager) ValidateSession(token string) bool {
	if token == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	s, ok := a.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(s.ExpiresAt) {
		delete(a.sessions, token)
		return false
	}
	return true
}

// RevokeSession deletes an active session token.
func (a *AuthManager) RevokeSession(token string) {
	if token == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, token)
}

// CheckAPIToken checks if request matches optional server auth token.
func (a *AuthManager) CheckAPIToken(token string) bool {
	if a.serverToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(a.serverToken)) == 1
}

func hashPassword(password string) (string, string, error) {
	saltBytes := make([]byte, 16)
	if _, err := rand.Read(saltBytes); err != nil {
		return "", "", err
	}
	salt := hex.EncodeToString(saltBytes)

	h := sha256.New()
	h.Write([]byte(password + ":" + salt))
	hash := hex.EncodeToString(h.Sum(nil))
	return salt, hash, nil
}

func verifyPassword(password, salt, expectedHash string) bool {
	h := sha256.New()
	h.Write([]byte(password + ":" + salt))
	computed := hex.EncodeToString(h.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(computed), []byte(expectedHash)) == 1
}

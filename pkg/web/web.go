package web

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
	"github.com/goldenduo/AdbLink/pkg/server"
)

// Server provides the Web Dashboard and REST API for AdbLink.
type WebServer struct {
	srv        *server.Server
	listenAddr string
	logger     *log.Logger
	startTime  time.Time
	httpServer *http.Server
	auth       *AuthManager
}

// NewWebServer creates a new WebServer instance.
// Optional arguments: options[0] = dataDir, options[1] = serverToken.
func NewWebServer(srv *server.Server, listenAddr string, logger *log.Logger, options ...string) *WebServer {
	dataDir := "data"
	serverToken := ""
	if len(options) > 0 && options[0] != "" {
		dataDir = options[0]
	}
	if len(options) > 1 {
		serverToken = options[1]
	}

	auth, err := NewAuthManager(dataDir, serverToken)
	if err != nil {
		logger.Printf("Warning: failed to initialize auth manager in %s: %v", dataDir, err)
	}

	return &WebServer{
		srv:        srv,
		listenAddr: listenAddr,
		logger:     logger,
		startTime:  time.Now(),
		auth:       auth,
	}
}

// Start begins serving the Web UI and API.
func (w *WebServer) Start() error {
	mux := http.NewServeMux()

	// Static & App
	mux.HandleFunc("/", w.handleIndex)

	// Auth API
	mux.HandleFunc("/api/v1/auth/status", w.handleAuthStatus)
	mux.HandleFunc("/api/v1/auth/setup", w.handleAuthSetup)
	mux.HandleFunc("/api/v1/auth/login", w.handleAuthLogin)
	mux.HandleFunc("/api/v1/auth/logout", w.handleAuthLogout)
	mux.HandleFunc("/api/v1/auth/password", w.handleAuthPassword)

	// Device & System API
	mux.HandleFunc("/api/v1/devices", w.handleDevices)
	mux.HandleFunc("/api/v1/devices/", w.handleDeviceAction)
	mux.HandleFunc("/api/v1/health", w.handleHealth)

	w.httpServer = &http.Server{
		Addr:         w.listenAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	w.logger.Printf("Web Dashboard & REST API available at http://%s", w.listenAddr)

	go func() {
		if err := w.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			w.logger.Printf("Web server error: %v", err)
		}
	}()

	return nil
}

// Stop shuts down the web server.
func (w *WebServer) Stop() error {
	if w.httpServer != nil {
		return w.httpServer.Close()
	}
	return nil
}

func (w *WebServer) isAuthorized(r *http.Request) bool {
	if w.auth == nil {
		return true
	}

	// 1. Check API token in headers (for adblink-ctl or automated scripts)
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if w.auth.CheckAPIToken(token) {
			return true
		}
	}
	customToken := r.Header.Get("X-AdbLink-Token")
	if customToken != "" && w.auth.CheckAPIToken(customToken) {
		return true
	}

	// 2. Check session cookie
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil && w.auth.ValidateSession(cookie.Value) {
		return true
	}

	// 3. Localhost loopback bypass if no server-token is enforced
	if w.auth.serverToken == "" {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err == nil && (host == "127.0.0.1" || host == "::1" || host == "localhost") {
			return true
		}
	}

	return false
}

func (w *WebServer) handleAuthStatus(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(rw, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	initialized := false
	loggedIn := false

	if w.auth != nil {
		initialized = w.auth.IsInitialized()
		cookie, err := r.Cookie(sessionCookieName)
		if err == nil && w.auth.ValidateSession(cookie.Value) {
			loggedIn = true
		}
	}

	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(map[string]bool{
		"initialized": initialized,
		"logged_in":   loggedIn,
	})
}

func (w *WebServer) handleAuthSetup(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if w.auth.IsInitialized() {
		http.Error(rw, `{"error":"Admin password already initialized"}`, http.StatusBadRequest)
		return
	}

	var req struct {
		Password        string `json:"password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(rw, `{"error":"Invalid request payload"}`, http.StatusBadRequest)
		return
	}

	if req.Password == "" || req.Password != req.ConfirmPassword {
		http.Error(rw, `{"error":"Passwords do not match"}`, http.StatusBadRequest)
		return
	}
	if len(req.Password) < 6 {
		http.Error(rw, `{"error":"Password must be at least 6 characters long"}`, http.StatusBadRequest)
		return
	}

	if err := w.auth.SetupPassword(req.Password); err != nil {
		http.Error(rw, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	// Auto-login upon setup
	token, ttl, _ := w.auth.CreateSession(true)
	http.SetCookie(rw, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(map[string]string{"status": "ok"})
}

func (w *WebServer) handleAuthLogin(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Password   string `json:"password"`
		RememberMe bool   `json:"remember_me"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(rw, `{"error":"Invalid request payload"}`, http.StatusBadRequest)
		return
	}

	if !w.auth.VerifyPassword(req.Password) {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(rw).Encode(map[string]string{"error": "Incorrect password"})
		return
	}

	token, ttl, err := w.auth.CreateSession(req.RememberMe)
	if err != nil {
		http.Error(rw, `{"error":"Failed to create session"}`, http.StatusInternalServerError)
		return
	}

	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if req.RememberMe {
		cookie.MaxAge = int(ttl.Seconds())
	}
	http.SetCookie(rw, cookie)

	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(map[string]string{"status": "ok"})
}

func (w *WebServer) handleAuthLogout(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if cookie, err := r.Cookie(sessionCookieName); err == nil && w.auth != nil {
		w.auth.RevokeSession(cookie.Value)
	}

	http.SetCookie(rw, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(map[string]string{"status": "ok"})
}

func (w *WebServer) handleAuthPassword(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Must be authorized to change password
	if !w.isAuthorized(r) {
		http.Error(rw, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}

	var req struct {
		OldPassword     string `json:"old_password"`
		NewPassword     string `json:"new_password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(rw, `{"error":"Invalid request payload"}`, http.StatusBadRequest)
		return
	}

	if req.NewPassword == "" || req.NewPassword != req.ConfirmPassword {
		http.Error(rw, `{"error":"New passwords do not match"}`, http.StatusBadRequest)
		return
	}
	if len(req.NewPassword) < 6 {
		http.Error(rw, `{"error":"Password must be at least 6 characters long"}`, http.StatusBadRequest)
		return
	}

	if err := w.auth.ChangePassword(req.OldPassword, req.NewPassword); err != nil {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(rw).Encode(map[string]string{"error": err.Error()})
		return
	}

	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(map[string]string{"status": "ok"})
}

func (w *WebServer) handleHealth(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(map[string]interface{}{
		"status":         "ok",
		"uptime_seconds": int(time.Since(w.startTime).Seconds()),
	})
}

func (w *WebServer) handleDevices(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(rw, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !w.isAuthorized(r) {
		http.Error(rw, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}

	devices := w.srv.GetDevices()
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(devices)
}

func (w *WebServer) handleDeviceAction(rw http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		http.Error(rw, "Invalid URL", http.StatusBadRequest)
		return
	}

	if !w.isAuthorized(r) {
		http.Error(rw, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}

	deviceID := parts[3]

	if len(parts) == 4 && r.Method == http.MethodGet {
		dev, ok := w.srv.GetDevice(deviceID)
		if !ok {
			http.Error(rw, "Device not found", http.StatusNotFound)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(dev)
		return
	}

	if len(parts) == 5 && parts[4] == "disconnect" && r.Method == http.MethodPost {
		err := w.srv.DisconnectDevice(deviceID)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusNotFound)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]string{"status": "disconnected"})
		return
	}

	http.Error(rw, "Not found", http.StatusNotFound)
}

func (w *WebServer) handleIndex(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = rw.Write([]byte(indexHTML))
}

const indexHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>AdbLink - ADB Reverse Tunnel Management</title>
<style>
  :root {
    --bg: #0d1117;
    --surface: #161b22;
    --surface-hover: #1f242c;
    --border: #30363d;
    --text: #c9d1d9;
    --text-bright: #f0f6fc;
    --text-muted: #8b949e;
    --primary: #58a6ff;
    --primary-hover: #79b8ff;
    --success: #3fb950;
    --warn: #d29922;
    --danger: #f85149;
    --font: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    --mono: "SFMono-Regular", Consolas, "Liberation Mono", Menlo, Courier, monospace;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { background: var(--bg); color: var(--text); font-family: var(--font); line-height: 1.5; min-height: 100vh; }
  
  .app-container { max-width: 1200px; margin: 0 auto; padding: 24px; }
  header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 24px; border-bottom: 1px solid var(--border); padding-bottom: 16px; }
  .logo-group { display: flex; align-items: center; gap: 12px; }
  .logo-icon { width: 38px; height: 38px; background: linear-gradient(135deg, #1f6feb, #388bfd); border-radius: 8px; display: flex; align-items: center; justify-content: center; font-size: 20px; box-shadow: 0 0 16px rgba(56, 139, 253, 0.3); }
  h1 { font-size: 22px; color: var(--text-bright); font-weight: 600; letter-spacing: -0.5px; }
  .subtitle { font-size: 13px; color: var(--text-muted); }
  .header-actions { display: flex; align-items: center; gap: 10px; }

  .stats-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 16px; margin-bottom: 24px; }
  .stat-card { background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 16px 20px; }
  .stat-label { font-size: 13px; color: var(--text-muted); margin-bottom: 4px; }
  .stat-value { font-size: 26px; font-weight: bold; color: var(--text-bright); }
  
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: 8px; overflow: hidden; margin-bottom: 24px; }
  .card-header { padding: 14px 20px; border-bottom: 1px solid var(--border); font-weight: 600; color: var(--text-bright); display: flex; justify-content: space-between; align-items: center; }
  table { width: 100%; border-collapse: collapse; text-align: left; }
  th { background: #0e1319; padding: 12px 18px; font-size: 13px; font-weight: 600; color: var(--text-muted); border-bottom: 1px solid var(--border); }
  td { padding: 14px 18px; font-size: 14px; border-bottom: 1px solid var(--border); }
  tr:last-child td { border-bottom: none; }
  tr:hover td { background: rgba(255,255,255,0.02); }
  
  .badge { display: inline-flex; align-items: center; gap: 6px; padding: 2px 10px; border-radius: 12px; font-size: 12px; font-weight: 600; }
  .badge-online { background: rgba(63, 185, 80, 0.15); color: var(--success); }
  .badge-offline { background: rgba(210, 153, 34, 0.15); color: var(--warn); }
  .badge-dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
  .code-box { font-family: var(--mono); font-size: 13px; background: #0e1319; border: 1px solid var(--border); border-radius: 6px; padding: 4px 8px; display: inline-flex; align-items: center; gap: 8px; }
  
  .btn { cursor: pointer; border: none; outline: none; padding: 7px 14px; border-radius: 6px; font-size: 13px; font-weight: 500; transition: all 0.15s; display: inline-flex; align-items: center; gap: 6px; }
  .btn-primary { background: #238636; color: #fff; }
  .btn-primary:hover { background: #2ea043; }
  .btn-secondary { background: #21262d; color: var(--text); border: 1px solid var(--border); }
  .btn-secondary:hover { background: #30363d; color: var(--text-bright); }
  .btn-copy { background: #21262d; color: var(--text); border: 1px solid var(--border); padding: 3px 8px; font-size: 12px; }
  .btn-copy:hover { background: #30363d; color: var(--text-bright); }
  .btn-danger { background: rgba(248, 81, 73, 0.15); color: var(--danger); border: 1px solid rgba(248, 81, 73, 0.3); }
  .btn-danger:hover { background: rgba(248, 81, 73, 0.3); }
  
  .empty-state { padding: 48px; text-align: center; color: var(--text-muted); }
  .copy-toast { position: fixed; bottom: 24px; right: 24px; background: var(--success); color: #fff; padding: 8px 16px; border-radius: 6px; font-size: 14px; opacity: 0; transition: opacity 0.2s; pointer-events: none; z-index: 1000; }

  /* Auth Screens */
  .auth-overlay { display: flex; align-items: center; justify-content: center; min-height: 80vh; padding: 20px; }
  .auth-card { width: 100%; max-width: 400px; background: var(--surface); border: 1px solid var(--border); border-radius: 12px; padding: 32px; box-shadow: 0 8px 24px rgba(0,0,0,0.5); }
  .auth-title { font-size: 20px; font-weight: 600; color: var(--text-bright); margin-bottom: 8px; text-align: center; }
  .auth-desc { font-size: 13px; color: var(--text-muted); margin-bottom: 24px; text-align: center; }
  .form-group { margin-bottom: 16px; }
  .form-label { display: block; font-size: 13px; font-weight: 500; margin-bottom: 6px; color: var(--text); }
  .form-input { width: 100%; padding: 10px 12px; background: #0e1319; border: 1px solid var(--border); border-radius: 6px; color: var(--text-bright); font-size: 14px; outline: none; transition: border-color 0.15s; }
  .form-input:focus { border-color: var(--primary); }
  .checkbox-group { display: flex; align-items: center; gap: 8px; margin-bottom: 20px; font-size: 13px; color: var(--text); cursor: pointer; }
  .checkbox-group input { cursor: pointer; width: 16px; height: 16px; accent-color: var(--primary); }
  .auth-error { background: rgba(248, 81, 73, 0.1); border: 1px solid rgba(248, 81, 73, 0.3); color: var(--danger); font-size: 13px; padding: 8px 12px; border-radius: 6px; margin-bottom: 16px; display: none; }

  /* Modal */
  .modal-backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.7); display: none; align-items: center; justify-content: center; z-index: 999; }
  .modal { width: 100%; max-width: 420px; background: var(--surface); border: 1px solid var(--border); border-radius: 12px; padding: 24px; box-shadow: 0 12px 32px rgba(0,0,0,0.6); }
  .modal-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; border-bottom: 1px solid var(--border); padding-bottom: 12px; }
  .modal-title { font-size: 16px; font-weight: 600; color: var(--text-bright); }
  .modal-close { background: none; border: none; color: var(--text-muted); cursor: pointer; font-size: 18px; }
  .modal-actions { display: flex; justify-content: flex-end; gap: 10px; margin-top: 20px; }
</style>
</head>
<body>

<!-- 1. Authentication View (Setup & Login) -->
<div id="auth-view" style="display: none;">
  <div class="auth-overlay">
    <!-- Setup Card -->
    <div id="setup-card" class="auth-card" style="display: none;">
      <div style="text-align: center; margin-bottom: 16px;">
        <div class="logo-icon" style="margin: 0 auto;">⚡</div>
      </div>
      <div class="auth-title">设置管理员密码</div>
      <div class="auth-desc">欢迎使用 AdbLink，首次运行请设置 Web 控制台管理员密码以保护安全。</div>
      <div id="setup-error" class="auth-error"></div>
      <form onsubmit="handleSetup(event)">
        <div class="form-group">
          <label class="form-label">管理员新密码 (至少6位)</label>
          <input type="password" id="setup-pass" class="form-input" required minlength="6" placeholder="输入密码">
        </div>
        <div class="form-group">
          <label class="form-label">确认管理员密码</label>
          <input type="password" id="setup-confirm" class="form-input" required minlength="6" placeholder="再次确认密码">
        </div>
        <button type="submit" class="btn btn-primary" style="width: 100%; padding: 10px; justify-content: center; margin-top: 8px;">完成初始化并登录</button>
      </form>
    </div>

    <!-- Login Card -->
    <div id="login-card" class="auth-card" style="display: none;">
      <div style="text-align: center; margin-bottom: 16px;">
        <div class="logo-icon" style="margin: 0 auto;">⚡</div>
      </div>
      <div class="auth-title">AdbLink 登录</div>
      <div class="auth-desc">请输入管理员密码访问设备管理控制台</div>
      <div id="login-error" class="auth-error"></div>
      <form onsubmit="handleLogin(event)">
        <div class="form-group">
          <label class="form-label">管理员密码</label>
          <input type="password" id="login-pass" class="form-input" required placeholder="输入管理员密码">
        </div>
        <label class="checkbox-group">
          <input type="checkbox" id="login-remember" checked>
          <span>记住我的登录状态 (30天免密)</span>
        </label>
        <button type="submit" class="btn btn-primary" style="width: 100%; padding: 10px; justify-content: center;">登 录</button>
      </form>
    </div>
  </div>
</div>

<!-- 2. Main Dashboard View -->
<div id="app-view" class="app-container" style="display: none;">
  <header>
    <div class="logo-group">
      <div class="logo-icon">⚡</div>
      <div>
        <h1>AdbLink Gateway</h1>
        <div class="subtitle">High-Performance Android Reverse Tunneling</div>
      </div>
    </div>
    <div class="header-actions">
      <button class="btn btn-secondary" onclick="openPasswordModal()">🔑 修改密码</button>
      <button class="btn btn-secondary" onclick="handleLogout()">🚪 退出登录</button>
    </div>
  </header>

  <div class="stats-grid">
    <div class="stat-card">
      <div class="stat-label">Online Devices</div>
      <div class="stat-value" id="stat-online">0</div>
    </div>
    <div class="stat-card">
      <div class="stat-label">Active Yamux Streams</div>
      <div class="stat-value" id="stat-streams">0</div>
    </div>
    <div class="stat-card">
      <div class="stat-label">Total Connections</div>
      <div class="stat-value" id="stat-conns">0</div>
    </div>
    <div class="stat-card">
      <div class="stat-label">Network Throughput (Rx / Tx)</div>
      <div class="stat-value" id="stat-traffic">0 B</div>
    </div>
  </div>

  <div class="card">
    <div class="card-header">
      <span>Connected Android Devices</span>
      <button class="btn btn-secondary" style="padding: 4px 10px; font-size: 12px;" onclick="loadDevices()">Refresh</button>
    </div>
    <table>
      <thead>
        <tr>
          <th>STATUS</th>
          <th>DEVICE ID</th>
          <th>MODEL</th>
          <th>ANDROID</th>
          <th>PORT</th>
          <th>ADB CONNECT COMMAND</th>
          <th>STREAMS</th>
          <th>TRAFFIC (RX/TX)</th>
          <th>ACTIONS</th>
        </tr>
      </thead>
      <tbody id="device-tbody">
        <tr><td colspan="9" class="empty-state">Loading device data...</td></tr>
      </tbody>
    </table>
  </div>
</div>

<!-- 3. Change Password Modal -->
<div id="password-modal" class="modal-backdrop">
  <div class="modal">
    <div class="modal-header">
      <div class="modal-title">修改管理员密码</div>
      <button class="modal-close" onclick="closePasswordModal()">&times;</button>
    </div>
    <div id="modal-error" class="auth-error"></div>
    <form onsubmit="handleChangePassword(event)">
      <div class="form-group">
        <label class="form-label">当前原密码</label>
        <input type="password" id="old-pass" class="form-input" required placeholder="输入当前使用的密码">
      </div>
      <div class="form-group">
        <label class="form-label">新密码 (至少6位)</label>
        <input type="password" id="new-pass" class="form-input" required minlength="6" placeholder="输入新密码">
      </div>
      <div class="form-group">
        <label class="form-label">确认新密码</label>
        <input type="password" id="confirm-new-pass" class="form-input" required minlength="6" placeholder="再次输入新密码">
      </div>
      <div class="modal-actions">
        <button type="button" class="btn btn-secondary" onclick="closePasswordModal()">取消</button>
        <button type="submit" class="btn btn-primary">确认修改</button>
      </div>
    </form>
  </div>
</div>

<div id="toast" class="copy-toast">Command copied to clipboard!</div>

<script>
let pollInterval = null;

function checkAuthAndInit() {
  fetch('/api/v1/auth/status')
    .then(r => r.json())
    .then(data => {
      document.getElementById('auth-view').style.display = 'none';
      document.getElementById('setup-card').style.display = 'none';
      document.getElementById('login-card').style.display = 'none';
      document.getElementById('app-view').style.display = 'none';

      if (!data.initialized) {
        // Step 1: Need initial setup
        document.getElementById('auth-view').style.display = 'block';
        document.getElementById('setup-card').style.display = 'block';
      } else if (!data.logged_in) {
        // Step 2: Need login
        document.getElementById('auth-view').style.display = 'block';
        document.getElementById('login-card').style.display = 'block';
      } else {
        // Step 3: Logged in -> show dashboard
        document.getElementById('app-view').style.display = 'block';
        loadDevices();
        if (!pollInterval) {
          pollInterval = setInterval(loadDevices, 2000);
        }
      }
    })
    .catch(err => {
      console.error('Failed to check auth status:', err);
    });
}

function handleSetup(e) {
  e.preventDefault();
  const pass = document.getElementById('setup-pass').value;
  const confirm = document.getElementById('setup-confirm').value;
  const errBox = document.getElementById('setup-error');
  errBox.style.display = 'none';

  if (pass !== confirm) {
    errBox.innerText = '两次输入的密码不一致';
    errBox.style.display = 'block';
    return;
  }

  fetch('/api/v1/auth/setup', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password: pass, confirm_password: confirm })
  })
  .then(r => r.json().then(d => ({ status: r.status, body: d })))
  .then(res => {
    if (res.status !== 200) {
      errBox.innerText = res.body.error || '初始化密码失败';
      errBox.style.display = 'block';
    } else {
      checkAuthAndInit();
    }
  });
}

function handleLogin(e) {
  e.preventDefault();
  const pass = document.getElementById('login-pass').value;
  const remember = document.getElementById('login-remember').checked;
  const errBox = document.getElementById('login-error');
  errBox.style.display = 'none';

  fetch('/api/v1/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password: pass, remember_me: remember })
  })
  .then(r => r.json().then(d => ({ status: r.status, body: d })))
  .then(res => {
    if (res.status !== 200) {
      errBox.innerText = res.body.error || '密码错误';
      errBox.style.display = 'block';
    } else {
      checkAuthAndInit();
    }
  });
}

function handleLogout() {
  if (!confirm('确定要退出登录吗？')) return;
  fetch('/api/v1/auth/logout', { method: 'POST' })
    .then(() => {
      if (pollInterval) {
        clearInterval(pollInterval);
        pollInterval = null;
      }
      checkAuthAndInit();
    });
}

function openPasswordModal() {
  document.getElementById('modal-error').style.display = 'none';
  document.getElementById('old-pass').value = '';
  document.getElementById('new-pass').value = '';
  document.getElementById('confirm-new-pass').value = '';
  document.getElementById('password-modal').style.display = 'flex';
}

function closePasswordModal() {
  document.getElementById('password-modal').style.display = 'none';
}

function handleChangePassword(e) {
  e.preventDefault();
  const oldPass = document.getElementById('old-pass').value;
  const newPass = document.getElementById('new-pass').value;
  const confirm = document.getElementById('confirm-new-pass').value;
  const errBox = document.getElementById('modal-error');
  errBox.style.display = 'none';

  if (newPass !== confirm) {
    errBox.innerText = '新密码两次输入不一致';
    errBox.style.display = 'block';
    return;
  }

  fetch('/api/v1/auth/password', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ old_password: oldPass, new_password: newPass, confirm_password: confirm })
  })
  .then(r => r.json().then(d => ({ status: r.status, body: d })))
  .then(res => {
    if (res.status !== 200) {
      errBox.innerText = res.body.error || '修改密码失败';
      errBox.style.display = 'block';
    } else {
      alert('密码修改成功！');
      closePasswordModal();
    }
  });
}

function formatBytes(bytes) {
  if (bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
}

function copyCmd(text) {
  navigator.clipboard.writeText(text).then(() => {
    const t = document.getElementById('toast');
    t.style.opacity = '1';
    setTimeout(() => { t.style.opacity = '0'; }, 2000);
  });
}

function disconnectDevice(id) {
  if (!confirm('确定要断开并停止设备 ' + id + ' 上的代理吗？\n手机上的 adblink-agent 将会自动彻底退出。')) return;
  fetch('/api/v1/devices/' + encodeURIComponent(id) + '/disconnect', { method: 'POST' })
    .then(r => r.json())
    .then(() => {
      setTimeout(loadDevices, 400);
    });
}

function loadDevices() {
  fetch('/api/v1/devices')
    .then(res => {
      if (res.status === 401) {
        checkAuthAndInit();
        return null;
      }
      return res.json();
    })
    .then(data => {
      if (!data) return;
      const tbody = document.getElementById('device-tbody');
      if (!data || data.length === 0) {
        tbody.innerHTML = '<tr><td colspan="9" class="empty-state">No Android devices connected. Run adblink-agent on your phone to connect.</td></tr>';
        document.getElementById('stat-online').innerText = '0';
        document.getElementById('stat-conns').innerText = '0';
        document.getElementById('stat-streams').innerText = '0';
        document.getElementById('stat-traffic').innerText = '0 B';
        return;
      }

      let onlineCount = 0;
      let totalConns = 0;
      let activeStreams = 0;
      let totalBytes = 0;

      let html = '';
      data.forEach(dev => {
        const isOnline = dev.status === 'ONLINE';
        if (isOnline) onlineCount++;
        totalConns += (dev.total_connections || 0);
        activeStreams += (dev.active_streams || 0);
        totalBytes += ((dev.bytes_sent || 0) + (dev.bytes_received || 0));

        const connectCmd = 'adb connect ' + dev.adb_connect_target;
        html += '<tr>';
        html += '<td><span class="badge ' + (isOnline ? 'badge-online' : 'badge-offline') + '"><span class="badge-dot"></span>' + dev.status + '</span></td>';
        html += '<td style="font-family: var(--mono); font-weight: 500;">' + dev.device_id + '</td>';
        html += '<td>' + (dev.model || '-') + '</td>';
        html += '<td>' + (dev.android_version || '-') + '</td>';
        html += '<td style="font-family: var(--mono); font-weight: 600; color: var(--primary);">' + dev.assigned_port + '</td>';
        html += '<td><div class="code-box"><span>' + connectCmd + '</span><button class="btn btn-copy" onclick="copyCmd(\'' + connectCmd + '\')">Copy</button></div></td>';
        html += '<td>' + (dev.active_streams || 0) + '</td>';
        html += '<td>' + formatBytes(dev.bytes_received || 0) + ' / ' + formatBytes(dev.bytes_sent || 0) + '</td>';
        html += '<td><button class="btn btn-danger" title="Disconnect and stop agent process on phone" onclick="disconnectDevice(\'' + dev.device_id + '\')">Disconnect</button></td>';
        html += '</tr>';
      });

      tbody.innerHTML = html;
      document.getElementById('stat-online').innerText = onlineCount;
      document.getElementById('stat-conns').innerText = totalConns;
      document.getElementById('stat-streams').innerText = activeStreams;
      document.getElementById('stat-traffic').innerText = formatBytes(totalBytes);
    })
    .catch(() => {});
}

// Start
checkAuthAndInit();
</script>
</body>
</html>
`

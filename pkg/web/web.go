package web

import (
	"encoding/json"
	"log"
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
}

// NewWebServer creates a new WebServer instance.
func NewWebServer(srv *server.Server, listenAddr string, logger *log.Logger) *WebServer {
	return &WebServer{
		srv:        srv,
		listenAddr: listenAddr,
		logger:     logger,
		startTime:  time.Now(),
	}
}

// Start begins serving the Web UI and API.
func (w *WebServer) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("/", w.handleIndex)
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

	devices := w.srv.GetDevices()
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(devices)
}

func (w *WebServer) handleDeviceAction(rw http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// /api/v1/devices/{id} or /api/v1/devices/{id}/disconnect
	if len(parts) < 4 {
		http.Error(rw, "Invalid URL", http.StatusBadRequest)
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
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>AdbLink - ADB Reverse Tunnel Management</title>
<style>
  :root {
    --bg: #0d1117;
    --surface: #161b22;
    --border: #30363d;
    --text: #c9d1d9;
    --text-bright: #f0f6fc;
    --primary: #58a6ff;
    --primary-hover: #79b8ff;
    --success: #3fb950;
    --warn: #d29922;
    --danger: #f85149;
    --font: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif, "Apple Color Emoji", "Segoe UI Emoji";
    --mono: "SFMono-Regular", Consolas, "Liberation Mono", Menlo, Courier, monospace;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { background: var(--bg); color: var(--text); font-family: var(--font); line-height: 1.5; padding: 24px; }
  .container { max-width: 1200px; margin: 0 auto; }
  header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 24px; border-bottom: 1px solid var(--border); padding-bottom: 16px; }
  .logo-group { display: flex; align-items: center; gap: 12px; }
  .logo-icon { width: 36px; height: 36px; background: linear-gradient(135deg, #1f6feb, #388bfd); border-radius: 8px; display: flex; align-items: center; justify-content: center; font-size: 20px; }
  h1 { font-size: 24px; color: var(--text-bright); font-weight: 600; }
  .subtitle { font-size: 14px; color: #8b949e; }
  .stats-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 16px; margin-bottom: 24px; }
  .stat-card { background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 16px; }
  .stat-label { font-size: 13px; color: #8b949e; margin-bottom: 4px; }
  .stat-value { font-size: 24px; font-weight: bold; color: var(--text-bright); }
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: 8px; overflow: hidden; margin-bottom: 24px; }
  .card-header { padding: 14px 18px; border-bottom: 1px solid var(--border); font-weight: 600; color: var(--text-bright); display: flex; justify-content: space-between; align-items: center; }
  table { width: 100%; border-collapse: collapse; text-align: left; }
  th { background: #0e1319; padding: 12px 16px; font-size: 13px; font-weight: 600; color: #8b949e; border-bottom: 1px solid var(--border); }
  td { padding: 12px 16px; font-size: 14px; border-bottom: 1px solid var(--border); }
  tr:last-child td { border-bottom: none; }
  tr:hover td { background: rgba(255,255,255,0.02); }
  .badge { display: inline-flex; align-items: center; gap: 6px; padding: 2px 8px; border-radius: 12px; font-size: 12px; font-weight: 600; }
  .badge-online { background: rgba(63, 185, 80, 0.15); color: var(--success); }
  .badge-offline { background: rgba(210, 153, 34, 0.15); color: var(--warn); }
  .badge-dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
  .code-box { font-family: var(--mono); font-size: 13px; background: #0e1319; border: 1px solid var(--border); border-radius: 4px; padding: 4px 8px; display: inline-flex; align-items: center; gap: 8px; }
  .btn { cursor: pointer; border: none; outline: none; padding: 6px 12px; border-radius: 6px; font-size: 13px; font-weight: 500; transition: background 0.15s; }
  .btn-primary { background: #238636; color: #fff; }
  .btn-primary:hover { background: #2ea043; }
  .btn-copy { background: #21262d; color: var(--text); border: 1px solid var(--border); padding: 3px 8px; font-size: 12px; }
  .btn-copy:hover { background: #30363d; color: var(--text-bright); }
  .btn-danger { background: rgba(248, 81, 73, 0.15); color: var(--danger); border: 1px solid rgba(248, 81, 73, 0.3); }
  .btn-danger:hover { background: rgba(248, 81, 73, 0.3); }
  .empty-state { padding: 48px; text-align: center; color: #8b949e; }
  .copy-toast { position: fixed; bottom: 24px; right: 24px; background: var(--success); color: #fff; padding: 8px 16px; border-radius: 6px; font-size: 14px; opacity: 0; transition: opacity 0.2s; pointer-events: none; }
  .copy-toast.show { opacity: 1; }
</style>
</head>
<body>
<div class="container">
  <header>
    <div class="logo-group">
      <div class="logo-icon">⚡</div>
      <div>
        <h1>AdbLink</h1>
        <div class="subtitle">ADB Reverse Tunneling & Device Management</div>
      </div>
    </div>
    <div>
      <button class="btn btn-copy" onclick="loadDevices()">⟳ Refresh</button>
    </div>
  </header>

  <div class="stats-grid">
    <div class="stat-card">
      <div class="stat-label">Online Devices</div>
      <div class="stat-value" id="stat-online">0</div>
    </div>
    <div class="stat-card">
      <div class="stat-label">Total Handled Connections</div>
      <div class="stat-value" id="stat-conns">0</div>
    </div>
    <div class="stat-card">
      <div class="stat-label">Active Streams</div>
      <div class="stat-value" id="stat-streams">0</div>
    </div>
    <div class="stat-card">
      <div class="stat-label">Total Transferred</div>
      <div class="stat-value" id="stat-traffic">0 B</div>
    </div>
  </div>

  <div class="card">
    <div class="card-header">
      <span>Connected Android Devices</span>
      <span style="font-size: 12px; color: #8b949e;">Auto-refreshes every 3s</span>
    </div>
    <div id="table-container">
      <table>
        <thead>
          <tr>
            <th>Status</th>
            <th>Device ID</th>
            <th>Model</th>
            <th>Android</th>
            <th>Exposed Port</th>
            <th>ADB Connect Command</th>
            <th>Streams</th>
            <th>Traffic (Rx / Tx)</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody id="device-tbody">
          <tr><td colspan="9" class="empty-state">Loading devices...</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</div>

<div class="copy-toast" id="toast">Copied to clipboard!</div>

<script>
function formatBytes(bytes) {
  if (bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}

function showToast(text) {
  const toast = document.getElementById('toast');
  toast.innerText = text || 'Copied to clipboard!';
  toast.classList.add('show');
  setTimeout(() => toast.classList.remove('show'), 2000);
}

function copyCmd(cmd) {
  navigator.clipboard.writeText(cmd).then(() => showToast('Copied: ' + cmd));
}

function disconnectDevice(id) {
  if (!confirm('Disconnect device ' + id + '?')) return;
  fetch('/api/v1/devices/' + encodeURIComponent(id) + '/disconnect', { method: 'POST' })
    .then(r => r.json())
    .then(() => loadDevices());
}

function loadDevices() {
  fetch('/api/v1/devices')
    .then(res => res.json())
    .then(data => {
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
        html += '<td><button class="btn btn-danger" onclick="disconnectDevice(\'' + dev.device_id + '\')">Disconnect</button></td>';
        html += '</tr>';
      });

      tbody.innerHTML = html;
      document.getElementById('stat-online').innerText = onlineCount;
      document.getElementById('stat-conns').innerText = totalConns;
      document.getElementById('stat-streams').innerText = activeStreams;
      document.getElementById('stat-traffic').innerText = formatBytes(totalBytes);
    })
    .catch(err => {
      console.error('Failed to load devices:', err);
    });
}

loadDevices();
setInterval(loadDevices, 3000);
</script>
</body>
</html>
`

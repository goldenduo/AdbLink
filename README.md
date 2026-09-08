# AdbLink - High-Performance Android ADB Reverse Tunnel Gateway

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://golang.org)
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20Android%20%7C%20macOS%20%7C%20Windows-green)](https://github.com/goldenduo/AdbLink)
[![Release](https://img.shields.io/github/v/release/goldenduo/AdbLink?display_name=tag&include_prereleases)](https://github.com/goldenduo/AdbLink/releases)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[中文文档 (Chinese)](README_CN.md)

**AdbLink** is a lightweight, high-performance, production-grade **ADB Reverse Tunnel Gateway** designed for Android devices.

When an Android phone is behind NAT, firewalls, or mobile data networks (4G/5G) where inbound connections are impossible, the device runs a native static proxy binary (`adblink-agent`) via `adb shell` to establish an outbound persistent connection to a remote server (`adblink-server`). The server dynamically allocates and exposes a dedicated ADB port. Any developer or CI system can simply run `adb connect <server-ip>:<port>` to control the phone as if it were plugged in via direct USB.

---

## Architecture

```
┌─────────────────────────────────┐
│         Android Phone           │
│                                 │
│  [adbd daemon (:5555)]          │
│          ▲                      │
│          │ localhost            │
│          ▼                      │
│  [adblink-agent (Native Proxy)] │
└────────────────┬────────────────┘
                 │
                 │ TCP / Yamux Multiplexed Long Connection (Outbound)
                 ▼
┌────────────────────────────────────────────────────────┐
│                    Remote Server                       │
│                                                        │
│  [adblink-server] (:9000 control port, :9001 Web/API)  │
│          │                                             │
│          ├──────────────┬──────────────┬──────────────┐│
│          ▼              ▼              ▼              ▼│
│       Port 55550     Port 55551     Port 55552     ... │
└──────────▲─────────────────────────────────────────────┘
           │
           │ adb connect <server-ip>:55550
           ▼
     [Developer PC / CI Server]
     (adb shell, adb push, adb pull, scrcpy, etc.)
```

### Key Features

- **NAT/Firewall Traversal**: The phone only needs outbound Internet connectivity (cellular or WiFi). No public IP or router port forwarding needed on the phone side.
- **Pure Static Native Binary**: `adblink-agent` is compiled statically (`CGO_ENABLED=0`) with zero libc or bionic dependencies, running reliably on any Android version (Android 5.0 through 15+).
- **Stream Multiplexing**: Powered by Yamux to multiplex multiple concurrent ADB sessions over a single TCP tunnel, with 170+ MB/s file transfer speeds (`adb push` / `adb pull`).
- **Real-Time Traffic Monitoring**: Real-time packet-level throughput metrics for sent/received bytes, active streams, and total connections.
- **Resilient Auto-Reconnect with Grace Period**: Automatic exponential backoff reconnect on network drops. The server holds port reservations for a configurable grace period (default 30s) so the port remains unchanged after reconnection.
- **Multi-Architecture**: Cross-compiled binaries for ARM64, ARMv7, x86_64, and x86.
- **Modern Web Dashboard**: Embedded dark-mode web management console with zero external CDN dependencies.
- **CLI Management**: `adblink-ctl` for listing devices, auto-connecting, and deploying agents.

---

## Quick Start

### 1. Build Binaries

```bash
# Build local binaries
make build

# Or cross-compile for all supported platforms (Android arm64/x64, Linux, macOS, Windows)
make build-all
```

Output directory layout in `bin/`:

```text
bin/
├── android/                             # Android Native Static Agent
│   ├── adblink-agent-arm64              # Android ARM64 (aarch64)
│   ├── adblink-agent-x86_64             # Android x86_64 (x64)
│   ├── adblink-agent-armv7              # Android 32-bit ARM
│   └── adblink-agent-x86                # Android 32-bit x86
├── linux/                               # Linux PC / Server
│   ├── adblink-server-linux-amd64       # Server (x64)
│   ├── adblink-server-linux-arm64       # Server (ARM64)
│   ├── adblink-ctl-linux-amd64          # CLI Tool (x64)
│   └── adblink-ctl-linux-arm64          # CLI Tool (ARM64)
├── darwin/                              # macOS (Mac PC)
│   ├── adblink-server-darwin-arm64      # Server (Apple Silicon M1/M2/M3/M4)
│   ├── adblink-server-darwin-amd64      # Server (Intel Mac)
│   ├── adblink-ctl-darwin-arm64         # CLI Tool (Apple Silicon)
│   └── adblink-ctl-darwin-amd64         # CLI Tool (Intel Mac)
└── windows/                             # Windows PC
    ├── adblink-server-windows-amd64.exe # Server (Windows x64)
    ├── adblink-server-windows-arm64.exe # Server (Windows ARM64)
    ├── adblink-ctl-windows-amd64.exe    # CLI Tool (Windows x64)
    └── adblink-ctl-windows-arm64.exe    # CLI Tool (Windows ARM64)
```

---

### 2. Start Server (`adblink-server`)

On your host or cloud server:

```bash
./bin/adblink-server \
  -listen :9000 \
  -web :9001 \
  -host 127.0.0.1 \
  -port-min 55550 \
  -port-max 55599
```

Flags:
- `-listen`: Agent control listen port (default `:9000`).
- `-web`: Web dashboard & REST API listen port (default `:9001`).
- `-host`: Hostname/IP advertised for `adb connect` (use public IP if deploying remotely).
- `-port-min` / `-port-max`: Port range allocated to connected devices.
- `-token`: Optional authentication token.

---

### 3. Deploy Agent onto Android Phone

#### Option A: One-click deploy via `adblink-ctl push`

```bash
./bin/adblink-ctl push -s <device_serial> -server <server_ip>:9000
```

#### Option B: Manual deploy via ADB

```bash
# Push binary to phone (select arm64 or x86_64 depending on device)
adb push bin/android/adblink-agent-arm64 /data/local/tmp/adblink-agent
adb shell chmod +x /data/local/tmp/adblink-agent

# Run directly in adb shell (no nohup needed):
adb shell /data/local/tmp/adblink-agent -server <server_ip>:9000

# Or run silently in the background with -d (built-in daemon, no nohup needed):
adb shell /data/local/tmp/adblink-agent -server <server_ip>:9000 -d
```

---

### 4. Connect via ADB

```bash
# List devices on the server
./bin/adblink-ctl list

# Connect to the first available device
./bin/adblink-ctl connect

# Or connect directly with adb
adb connect 127.0.0.1:55550

# Run commands
adb -s 127.0.0.1:55550 shell
adb -s 127.0.0.1:55550 push app.apk /data/local/tmp/
```

---

## Testing & Verification

```bash
# Run unit tests with Go race detector
make test

# Run full end-to-end integration test with live Android instance
make test-e2e
```

---

## Docker Deployment

```bash
docker-compose up -d
```

---

## License

MIT License. See [LICENSE](LICENSE) for details.

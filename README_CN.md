# AdbLink - 高性能 Android ADB 反向穿透网关

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://golang.org)
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20Android-green)](https://github.com/goldenduo/AdbLink)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**AdbLink** 是一个专为 Android 设备设计的轻量级、高性能、生产级的 **ADB 反向穿透网关（Reverse Tunnel）**。

当 Android 手机位于内网、移动网络（4G/5G）、NAT 或防火墙之后，远端服务器无法直接通过 IP 访问手机时，手机通过运行一个原生的静态代理程序（`adblink-agent`）向远端服务器（`adblink-server`）发起长连接。服务器为该手机动态分配并暴露一个 ADB 端口，服务器端或局域网内的开发者只需执行 `adb connect <server-ip>:<port>`，即可像直连 USB 一样通过标准 ADB 命令调试和管理手机。

---

## 架构设计

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
                 │ TCP / Yamux 复用长连接 (Outbound)
                 ▼
┌────────────────────────────────────────────────────────┐
│                    Remote Server                       │
│                                                        │
│  [adblink-server] (:9000 控制监听, :9001 Web/API)        │
│          │                                             │
│          ├──────────────┬──────────────┬──────────────┐│
│          ▼              ▼              ▼              ▼│
│       Port 55550     Port 55551     Port 55552     ... │
└──────────▲─────────────────────────────────────────────┘
           │
           │ adb connect <server-ip>:55550
           ▼
     [Developer PC / CI Server]
     (adb shell, adb push, adb pull, scrcpy, 等)
```

### 核心特性

- **跨网络穿透**：手机仅需具备出网能力（4G/5G 或任意 WiFi），即可主动连回公网/内网服务器，无需公网 IP 或路由器端口映射。
- **纯静态原生二进制**：`adblink-agent` 采用纯静态编译（CGO_ENABLED=0），无任何动态链接库（libc/bionic）依赖，全版本 Android（Android 5.0 ~ 15+）开箱即用。
- **高并发与流复用**：基于 Yamux 协议，在单个 TCP 长连接上多路复用并发 ADB 会话，支持高带宽文件传输（实测 `adb push`/`adb pull` 达 170+ MB/s）。
- **实时流量与状态监控**：内置流式流量统计，准确监控双向传输流量（Rx / Tx）、活动连接数和会话生命周期。
- **断线重连与端口保留（Grace Period）**：支持网络抖动自动指数退避重连；断开时服务器自动保留端口租赁（默认 30 秒），重连后端口保持一致，无需重新 `adb connect`。
- **多架构支持**：支持 ARM64、ARMv7、x86_64、x86。
- **精美 Web 管理看板**：开箱即用、无外部 CDN 依赖的现代化暗色仪表盘，一键复制 `adb connect` 命令。
- **CLI 命令行工具**：提供 `adblink-ctl`，支持快速查询设备、自动执行 `adb connect`，以及一键部署 agent 到手机。

---

## 快速上手

### 1. 编译构建

本项目提供一键构建 Makefile，支持编译当前平台或者全平台全架构（Android arm64/x64，PC Linux/macOS/Windows）：

```bash
# 方式一：编译本地常用组件
make build

# 方式二：一键交叉编译全平台全架构（Android、Linux、macOS、Windows）
make build-all
```

编译输出目录及文件清单（`bin/` 目录）：

```text
bin/
├── android/                             # 手机端原生静态代理 (Native Agent)
│   ├── adblink-agent-arm64              # Android ARM64 (aarch64) 真实手机主力
│   ├── adblink-agent-x86_64             # Android x86_64 (x64) 模拟器/容器
│   ├── adblink-agent-armv7              # Android 32位 ARM
│   └── adblink-agent-x86                # Android 32位 x86
├── linux/                               # Linux PC / 服务器
│   ├── adblink-server-linux-amd64       # 服务端 (x64)
│   ├── adblink-server-linux-arm64       # 服务端 (ARM64)
│   ├── adblink-ctl-linux-amd64          # 控制工具 (x64)
│   └── adblink-ctl-linux-arm64          # 控制工具 (ARM64)
├── darwin/                              # macOS (Mac PC)
│   ├── adblink-server-darwin-arm64      # 服务端 (Apple Silicon M1/M2/M3/M4)
│   ├── adblink-server-darwin-amd64      # 服务端 (Intel Mac)
│   ├── adblink-ctl-darwin-arm64         # 控制工具 (Apple Silicon)
│   └── adblink-ctl-darwin-amd64         # 控制工具 (Intel Mac)
└── windows/                             # Windows PC
    ├── adblink-server-windows-amd64.exe # 服务端 (Windows x64)
    ├── adblink-server-windows-arm64.exe # 服务端 (Windows ARM64)
    ├── adblink-ctl-windows-amd64.exe    # 控制工具 (Windows x64)
    └── adblink-ctl-windows-arm64.exe    # 控制工具 (Windows ARM64)
```

---

### 2. 启动服务端 (`adblink-server`)

在拥有固定 IP 或局域网可达的主机/云服务器上启动服务端：

```bash
./bin/adblink-server \
  -listen :9000 \
  -web :9001 \
  -host 127.0.0.1 \
  -port-min 55550 \
  -port-max 55599
```

参数说明：
- `-listen`：控制信道监听端口，手机 Agent 连接该端口（默认 `:9000`）。
- `-web`：Web 管理控制台和 REST API 端口（默认 `:9001`）。
- `-host`：服务端对外宣告的主机名或 IP（用于生成 `adb connect <host>:<port>`，若在外网请填服务器外网 IP）。
- `-port-min` / `-port-max`：分配给连接手机的 ADB 端口范围。
- `-token`：可选安全验证密钥。

---

### 3. 在 Android 手机上运行代理 (`adblink-agent`)

#### 方式 A：使用 `adblink-ctl` 一键部署（推荐）

如果手机当前已通过 USB 或临时 ADB 连接在本机：

```bash
# 自动检测架构、推送文件、后台启动
./bin/adblink-ctl push -s <手机serial> -server <服务器IP>:9000
```

#### 方式 B：手动推送与运行

```bash
# 1. 将编译好的静态代理程序推送到手机
adb push bin/adblink-agent-android-arm64 /data/local/tmp/adblink-agent

# 2. 赋予执行权限
adb shell chmod +x /data/local/tmp/adblink-agent

# 3. 后台启动代理连接远端服务器
adb shell "nohup /data/local/tmp/adblink-agent -server <服务器IP>:9000 > /data/local/tmp/adblink.log 2>&1 &"

# 4. 查看连接日志
adb shell cat /data/local/tmp/adblink.log
```

成功连接后，日志将输出分配的端口号：
```text
[AdbLink-Agent] Starting AdbLink Agent (ID: 0123456789ABCDEF, Model: Pixel 8, Android: 15)
[AdbLink-Agent] Connecting to server at 192.168.1.100:9000...
[AdbLink-Agent] ==> Successfully registered! Server exposed ADB port: 55550 (Host: 192.168.1.100)
[AdbLink-Agent] ==> Connect from anywhere: adb connect 192.168.1.100:55550
```

---

### 4. 通过 ADB 连接手机

在服务器或能访问服务器的任意开发者电脑上：

```bash
# 查看所有已连接的设备及其端口
./bin/adblink-ctl list

# 自动连接
./bin/adblink-ctl connect

# 或者直接使用标准 adb 命令
adb connect 192.168.1.100:55550

# 执行任意 ADB 操作！
adb -s 192.168.1.100:55550 shell
adb -s 192.168.1.100:55550 push myapp.apk /data/local/tmp/
adb -s 192.168.1.100:55550 logcat
```

---

## Web 控制台与 REST API

打开浏览器访问 `http://<服务器IP>:9001` 即可查看管理仪表盘：

- **实时设备列表**：显示设备 ID、机型、系统版本、在线状态、暴露端口。
- **实时监控指标**：当前活动连接数、总连接数、传输字节数（Rx/Tx）、在线时长。
- **一键复制**：点击直接复制 `adb connect` 命令。
- **设备管理**：支持踢下线和手动刷新。

### REST API 端点

| 方法 | 路径 | 说明 |
| :--- | :--- | :--- |
| `GET` | `/api/v1/health` | 服务健康检查与运行时间 |
| `GET` | `/api/v1/devices` | 获取所有已注册设备的 JSON 列表 |
| `GET` | `/api/v1/devices/{id}` | 获取单个设备详情 |
| `POST` | `/api/v1/devices/{id}/disconnect` | 断开指定设备的连接 |

---

## 自动化测试与验证

项目包含完善的单元测试与真实设备端到端（E2E）测试用例：

```bash
# 运行单元测试（包含竞态检测 -race）
make test

# 运行真实 Android 实例端到端测试
make test-e2e
```

E2E 测试自动涵盖：
1. 服务端与 Agent 协议握手。
2. 动态端口分配与监听。
3. `adb connect` 逆向建连。
4. 远程 shell 指令执行验证。
5. 随机大文件双向推送/拉取及 SHA-256 数据一致性校验。

---

## 生产部署

### Docker / Docker Compose

使用提供的 `docker-compose.yml`：

```bash
docker-compose up -d
```

### Systemd 系统服务

```bash
# 1. 拷贝二进制
sudo cp bin/adblink-server /usr/local/bin/

# 2. 安装并启动 systemd 服务
sudo cp systemd/adblink-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now adblink-server

# 查看状态
sudo systemctl status adblink-server
```

---

## 常见问题与排查 (FAQ)

**Q: 手机上的 `adblink-agent` 是否需要 root 权限？**  
A: **不需要**。Android 的 `adb shell` 默认运行在 `shell (uid=2000)` 用户下，属于 `inet` 用户组，具备访问外网 TCP 和连接本机的权限。

**Q: 如果手机上的 `adbd` 没有监听 TCP 5555 端口怎么办？**  
A: `adblink-agent` 默认开启 `-auto-adbd=true`，会自动检测 `127.0.0.1:5555`。若手机未开启 TCP 调试，只要有 root 或在 shell 权限下，agent 会尝试自动配置 `setprop service.adb.tcp.port 5555` 并重启 adbd。你也可以在手机插上 USB 时执行一次 `adb tcpip 5555` 开启。

**Q: 手机网络切换（如 WiFi 切 5G）断开连接怎么办？**  
A: `adblink-agent` 具备智能指数退避重连机制；同时 `adblink-server` 会为断开的设备保留默认 30 秒的端口租赁（Grace Period）。设备重连后无缝复用原端口，无需重新执行 `adb connect`。

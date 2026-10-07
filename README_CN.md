# AdbLink - 高性能 Android ADB 反向穿透网关

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://golang.org)
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20Android%20%7C%20macOS%20%7C%20Windows-green)](https://github.com/goldenduo/AdbLink)
[![Release](https://img.shields.io/github/v/release/goldenduo/AdbLink?display_name=tag&include_prereleases)](https://github.com/goldenduo/AdbLink/releases)
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
│  [adblink-server] (:8888 控制监听, :9999 Web/API)        │
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
- **心跳保活与自动重连**：Yamux PING/PONG 心跳配合 TCP keepalive，降低移动网络/NAT 对空闲长连接的清理概率；真正断线后 Agent 自动指数退避重连，服务端在 Grace Period 内保留原端口。
- **实时流量与状态监控**：内置流式流量统计，准确监控双向传输流量（Rx / Tx）、活动连接数和会话生命周期。
- **可靠的网页断开**：网页点击“断开”会发送 STOP 并等待确认，同时阻断断线重连竞态；手机端 Agent 会退出，不会过一会儿又自动连回来。
- **Android 保活措施**：Agent 运行期间尽力关闭 Doze、禁止 Wi‑Fi 熄屏休眠，退出时恢复原设置；如果设备有 `termux-wake-lock`，会自动使用真正的 partial wakelock。纯原生进程在没有 Android App 权限时无法直接申请完整的 CPU WakeLock。
- **多架构支持**：支持 ARM64、ARMv7、x86_64、x86。
- **精美 Web 管理看板**：开箱即用、无外部 CDN 依赖的现代化暗色仪表盘，实时记录并展示手机客户端 IP 与连接模式（SOCKS5 代理 / 直连），支持一键复制 `adb connect` 命令。
- **智能交互式 CLI 工具**：提供 `adblink-ctl`，启动时交互式展示并选择当前手机，引导输入服务器地址与端口并自动持久化最近历史记录，下次直接快捷复用。
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
  -listen :8888 \
  -web :9999 \
  -host 127.0.0.1 \
  -port-min 55550 \
  -port-max 55599
```

参数说明：
- `-listen`：控制信道监听端口，手机 Agent 连接该端口（默认 `:8888`）。
- `-web`：Web 管理控制台和 REST API 端口（默认 `:9999`）。
- `-host`：服务端对外宣告的主机名或 IP（用于生成 `adb connect <host>:<port>`，若在外网请填服务器外网 IP）。
- `-port-min` / `-port-max`：分配给连接手机的 ADB 端口范围。
- `-token`：可选安全验证密钥。
- `-heartbeat`：Yamux 心跳间隔，默认 `15s`，应小于运营商/NAT 的空闲超时时间。
- `-tls`：开启控制信道 TLS 加密（默认开启并自动检测，未指定证书时自动生成自签名证书）。
- `-tls-cert` / `-tls-key`：可选自定义 TLS 证书及私钥文件。
- `-tls-strict`：强制仅允许 TLS 连接，拒绝明文 Agent。
#### IPv6 部署

服务端监听 IPv6 通配地址时使用方括号；`-host` 只填设备和开发机都能访问的 IPv6 主机地址，不带方括号和端口。Agent 的 `-server` 需要把 IPv6 地址放在方括号中：

```bash
# 将 2001:db8::10 替换为服务端实际可达的 IPv6 地址
./bin/adblink-server -listen '[::]:8888' -web '[::]:9999' \
  -host '2001:db8::10' -port-min 55550 -port-max 55599

adb shell /data/local/tmp/adblink-agent \
  -server '[2001:db8::10]:8888' -retry 2s -max-retry 30s
```

注册后用 `adb connect '[2001:db8::10]:55550'` 连接分配的端口。Agent 在 TCP/Yamux 会话断开后会自动重连；`-retry` 和 `-max-retry` 分别设置初始与最大重试间隔。路由器或主机防火墙需允许控制端口、Web 端口和 ADB 端口范围通过。
#### 安全传输与公网长连接保活部署 (默认 TLS 1.3 与代理支持)

在复杂跨公网、多运营商或长途网络环境下，明文长连接可能遭遇中间网络节点的数据包检查、协议误判或空闲连接重置。AdbLink 默认提供端到端 TLS 1.3 传输层高强度加密，将控制协议、Yamux 复用流与 ADB 数据全量包裹：

1. **服务端（远端服务器）启动（默认已开启 TLS）**：
   ```bash
   # 服务端默认开启 TLS 且自动在内存生成自签名证书，亦可配置正规证书或监听 443 端口
   ./bin/adblink-server -listen :8888 -web :9999 -host <服务器公网IP>
   ```

2. **手机端 Agent 启动（默认已启用 TLS）**：
   ```bash
   # Agent 默认开启 TLS 1.3 且自动信任服务端自签名证书，无需额外参数
   adb shell /data/local/tmp/adblink-agent -server <服务器公网IP>:8888 -d
   ```

3. **配合本地代理环境（可选）**：
   Agent 内置智能代理探测，会自动探测本地 `127.0.0.1:1080` 或 `127.0.0.1:7890` 的 SOCKS5 / HTTP CONNECT 代理；亦可显式指定：
   ```bash
   adb shell /data/local/tmp/adblink-agent -server <服务器公网IP>:8888 -proxy 127.0.0.1:1080 -d
   ```
---

### 3. 在 Android 手机上运行代理 (`adblink-agent`)

#### 方式 A：使用 `adblink-ctl` 一键部署（推荐）

如果手机当前已通过 USB 或临时 ADB 连接在本机：

```bash
# 自动检测架构、推送文件、后台启动
./bin/adblink-ctl push -s <手机serial> -server <服务器IP>:8888
```

#### 方式 B：手动推送与直接运行

```bash
# 1. 将编译好的静态代理程序推送到手机（根据手机架构选 arm64 或 x86_64）
adb push bin/android/adblink-agent-arm64 /data/local/tmp/adblink-agent

# 2. 赋予执行权限
adb shell chmod +x /data/local/tmp/adblink-agent

# 3. 在 adb shell 中直接运行（无需 nohup 等任何复杂参数）：
adb shell /data/local/tmp/adblink-agent -server <服务器IP>:8888

# 或者若需在后台静默运行，添加 -d 即可（程序自身守护，无需 nohup）：
adb shell /data/local/tmp/adblink-agent -server <服务器IP>:8888 -d

# 可选调节（默认已开启心跳和 Android 保活措施）：
# adb shell /data/local/tmp/adblink-agent -server <服务器IP>:8888 -heartbeat 15s
# 如不希望修改 Android 电源/网络策略，可追加 -no-keep-awake
```

Agent 会自动检测断线并重连；Android 端会在 Agent 进程运行期间尽力保持网络可用，进程退出后恢复临时修改的系统设置。

成功连接后，日志将输出分配的端口号：
```text
[AdbLink-Agent] Starting AdbLink Agent (ID: 0123456789ABCDEF, Model: Pixel 8, Android: 15)
[AdbLink-Agent] Connecting to server at 192.168.1.100:8888...
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

打开浏览器访问 `http://<服务器IP>:9999`（或配置域名 `http://adb.126111.xyz`）即可查看管理仪表盘：

- **实时设备列表**：显示设备 ID、机型、客户端 IP 与连接模式（SOCKS5 代理 / 直连）、系统版本、在线状态、暴露端口。
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

### Nginx 反向代理配置（域名映射示例）

如果需要通过独立域名访问 Web 控制台（例如映射 `adb.126111.xyz` 到本地 `:9999` 端口）：

```nginx
# /etc/nginx/conf.d/adblink.conf
server {
    listen 80;
    listen [::]:80;

    server_name adb.126111.xyz;
    client_max_body_size 50m;

    location / {
        proxy_pass http://127.0.0.1:9999;

        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;

        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
```

配置完成后执行 `sudo nginx -t && sudo systemctl reload nginx` 即可生效。

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

**Q: 网页点击“断开”后，手机为什么不会自动重新连回？**
A: 这是有意设计：服务端会发送 STOP、等待 Agent 确认，并在短时间内拒绝同一设备 ID 的迟到重连。若要再次连接，重新启动手机上的 Agent 即可。

**Q: 能否用多线程把一次 `adb push` 再拆快一些？**
A: 不建议。ADB 文件传输是有序、有状态的单连接协议；当前 Yamux 已经让多个独立 ADB 连接并发传输。拆分单个流会引入乱序、校验和稳定性风险，因此稳定优先不做这种优化。

**Q: 跨公网长连接频繁或不定时断开怎么办？**  
A: 公网长连接不定时断开的核心原因通常是中间运营商网关超时清理、NAT 状态丢弃或中间网络抖动引发的心跳超时。优化方案：  
1. **默认 TLS 1.3 加密传输**：v1.6.0+ 起默认全程启用 TLS 1.3 加密，防止中间网络设备对明文特征报文产生误判或异常重置。  
2. **加固的心跳重试**：控制信道超时放宽至 10 秒并支持 3 次连续容错，避免偶发网络丢包导致长连接被提前拆除。  
3. **代理网络通道**：可配合本地代理节点（如 `-proxy 127.0.0.1:1080`），通过稳定的代理通道传输加密流量。

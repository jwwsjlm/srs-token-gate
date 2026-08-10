# SRS Token Gate

轻量级 SRS 推拉流 Token 鉴权与统一 Web 控制台。

SRS Token Gate 在 [SRS](https://github.com/ossrs/srs) 前增加一个 Go 网关，为 RTMP、HTTP-FLV、HLS 和 WebRTC 提供 Token 鉴权，同时保护 SRS 控制台、播放器页面和管理 API。

## 功能

- RTMP 推流和播放 Token 鉴权。
- HTTP-FLV、HLS 播放 Token 鉴权。
- WebRTC WHIP 推流和 WHEP 播放 Token 鉴权。
- Token 可设置推流、播放或双向权限。
- Token 可精确绑定指定流名称。
- 独立页内登录和统一 SRS 控制台。
- 按用户、IP、应用、流名和方向聚合在线调用用户。
- 自动合并 HLS 播放列表和 TS 分片请求。
- `config.yaml` 自动热加载。
- 支持域名 HTTPS 反代和无域名 IP 直连。
- Docker 日志自动轮转。
- 服务器只拉取公开镜像，不需要 Go 编译环境。

## 架构

项目只运行两个长期容器：

```text
浏览器 / 播放客户端 ──> Token Gate :9085 ──> SRS HTTP/API
RTMP 客户端          ──────────────────────> SRS :1935
WebRTC 媒体          ──────────────────────> SRS :8000
                                           │
SRS Hook ──────────────────────────────────> Token Gate
```

- `srs-token-auth`：自动初始化配置、登录、Token 管理、鉴权和 HTTP 反向代理。
- `srs`：处理 RTMP、HTTP-FLV、HLS 和 WebRTC 媒体。

SRS 的 `1985` 和 `8080` 只绑定宿主机回环地址，不能从公网绕过 Token Gate。

## 环境要求

- Linux x86-64 服务器。
- Docker Engine。
- Docker Compose V2。
- 使用 WebRTC 时需要公网 IP。

## 快速开始

### 1. 准备配置

```bash
cp .env.example .env
```

首次部署不需要手动创建 `config.yaml`。Token Gate 会自动从模板创建：

```text
srs-token-gate/config.yaml
```

并自动生成：

- 管理员强密码。
- SRS Hook Secret。
- Token 数据目录。
- HLS 数据目录。
- SRS 运行配置。

### 2. 启动

```bash
docker compose pull
docker compose up -d
```

查看状态：

```bash
docker compose ps
```

正常状态：

```text
srs-token-auth   healthy
srs              running
```

### 3. 查看管理员密码

```bash
docker compose run --rm token-auth show-admin-password
```

### 4. 打开控制台

域名模式：

```text
https://example.com/token/
```

IP 直连模式：

```text
http://203.0.113.10:9085/token/
```

登录后可以进入 SRS 概览、客户端列表和 Token 管理页面。

## 配置说明

用户只需要关注两个文件：

```text
.env
srs-token-gate/config.yaml
```

### `.env`

`.env` 只配置 Docker 启动前必须确定的宿主机监听、端口和镜像：

```env
# 域名反代使用 127.0.0.1；IP 直连使用 0.0.0.0。
GATEWAY_BIND=127.0.0.1

# Token Gate 对外 HTTP 端口。
GATEWAY_PORT=9085

# RTMP 对外端口。
RTMP_PORT=1935

# WebRTC UDP 和 TCP fallback 端口。
RTC_UDP_PORT=8000
RTC_TCP_PORT=8000

# Token Gate 公开镜像。
TOKEN_GATE_IMAGE=docker.io/guanren/srs-token-gate:latest
```

### `config.yaml`

```yaml
version: 1

public:
  # 控制台生成地址时使用的域名或公网 IP；留空时使用浏览器当前地址。
  address: example.com

admin:
  username: admin
  # password_hash 非空时优先使用 bcrypt。
  password: ""
  password_hash: ""

session:
  # HTTPS 域名模式为 true；HTTP/IP 模式为 false。
  cookie_secure: true
  max_age_seconds: 86400

srs:
  # WebRTC candidate 必须填写服务器公网 IP，不要填写域名。
  candidate: 203.0.113.10
  # 留空时首次启动自动生成。
  hook_secret: ""
  max_connections: 1000
  # 建议保持 warn，减少含查询参数的普通访问日志。
  log_level: warn

statistics:
  # HLS 最后一次请求后的在线保留时间。
  active_ttl_seconds: 30
  # 与 SRS 客户端列表对账的间隔。
  reconcile_interval_seconds: 15
```

`config.yaml` 包含管理员密码和 Hook Secret，文件权限应保持为 `0600`。

## 域名 HTTPS 模式

`.env`：

```env
GATEWAY_BIND=127.0.0.1
GATEWAY_PORT=9085
```

`config.yaml`：

```yaml
public:
  address: example.com

session:
  cookie_secure: true

srs:
  candidate: 203.0.113.10
```

Nginx/OpenResty 只需要反代一个入口：

```nginx
location / {
    proxy_pass http://127.0.0.1:9085;
    proxy_http_version 1.1;

    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $http_connection;
}
```

完整示例见 `srs-token-gate/docs/nginx.example.conf`。

## 无域名 IP 模式

`.env`：

```env
GATEWAY_BIND=0.0.0.0
GATEWAY_PORT=9085
```

`config.yaml`：

```yaml
public:
  address: 203.0.113.10

session:
  cookie_secure: false

srs:
  candidate: 203.0.113.10
```

控制台：

```text
http://203.0.113.10:9085/token/
```

请在防火墙中只放行实际使用的端口。

## 端口规划

| 端口 | 协议 | 用途 | 公网策略 |
|---|---|---|---|
| `1935` | TCP | RTMP 推流和播放 | 使用 RTMP 时开放 |
| `9085` | TCP | Token Gate、控制台、FLV/HLS、WHIP/WHEP | IP 模式开放；域名模式仅反代访问 |
| `8000` | UDP | WebRTC 媒体 | 使用 WebRTC 时开放 |
| `8000` | TCP | WebRTC TCP fallback | 使用 WebRTC 时开放 |
| `1985` | TCP | SRS API 和 RTC 信令源站 | 仅绑定 `127.0.0.1` |
| `8080` | TCP | SRS HTTP 源站 | 仅绑定 `127.0.0.1` |

不要将 `1985` 或 `8080` 直接开放到公网。

## Token 使用

登录控制台后打开“Token 鉴权”页面：

1. 填写调用用户名称。
2. 选择权限：推流、播放或推流和播放。
3. 可选填写限定流名。
4. 点击“生成密钥”。
5. 在地址生成器中复制所需地址。

限定流名采用精确匹配。例如 Token 绑定 `camera01` 后，不能访问 `camera02`。

### OBS 推流

OBS 必须分开填写：

```text
服务器：rtmp://example.com/live
串流密钥：camera01?token=<token>
```

不要同时在服务器地址和串流密钥中重复填写 Token。

### FFmpeg 或单地址客户端

```text
rtmp://example.com:1935/live/camera01?token=<token>
```

### HTTP-FLV

```text
https://example.com/live/camera01.flv?token=<token>
```

### HLS

```text
https://example.com/live/camera01.m3u8?token=<token>
```

### WebRTC WHIP/WHEP

```text
https://example.com/rtc/v1/whip/?app=live&stream=camera01&token=<token>
https://example.com/rtc/v1/whep/?app=live&stream=camera01&token=<token>
```

## 配置热加载

以下配置保存后会自动热加载：

```text
public.address
srs.candidate
srs.max_connections
srs.log_level
statistics.active_ttl_seconds
statistics.reconcile_interval_seconds
```

也可以在 Token 页面点击“重载统一配置”。

以下配置修改后需要重建容器：

```text
admin.*
session.*
srs.hook_secret
.env 中的监听地址和端口
```

```bash
docker compose up -d --force-recreate
```

如果新 YAML 无效或 SRS 重载失败，服务会继续使用旧的运行配置。

## 日常运维

查看状态：

```bash
docker compose ps
```

查看日志：

```bash
docker compose logs -f --tail=100
```

重启：

```bash
docker compose restart
```

升级：

```bash
docker compose pull
docker compose up -d --force-recreate
```

Compose 已为两个容器启用 Docker 日志轮转：

```text
单文件最大 10MB
最多保留 3 个文件
```

外部 Nginx/OpenResty 日志不受 Compose 管理，需要在宿主机单独配置轮转，并避免记录带 Token 的完整查询参数。

## 数据目录

需要保留的数据：

```text
srs-token-gate/config.yaml
srs-token-gate/data/tokens.json
srs-token-gate/data/hls/
```

更新镜像或重建容器不会重新生成已有 Token。

不要提交以下内容到 GitHub：

```text
.env
srs-token-gate/config.yaml
srs-token-gate/data/
*.key
*.pem
生产日志
Cookie
Token
```

## 本地开发与发布镜像

项目使用 Go，并固定依赖版本。主要依赖：

- `go-chi/chi`：HTTP 路由和中间件。
- `alexedwards/scs`：管理员 Session。
- `fsnotify`：配置文件监听。
- `golang.org/x/crypto/bcrypt`：密码哈希。
- `gopkg.in/yaml.v3`：严格 YAML 配置解析。

运行检查：

```bash
cd srs-token-gate/token-auth
go test ./...
go test -race ./...
go vet ./...
```

Windows 上发布 `linux/amd64` 镜像：

```powershell
.\srs-token-gate\scripts\publish-image.ps1
```

默认发布到：

```text
docker.io/guanren/srs-token-gate:latest
```

## 常见问题

### 初始化容器为什么不见了？

当前版本只有 `srs-token-auth` 和 `srs` 两个容器。初始化已经合并到 Token Gate 启动流程中。

### 修改 `config.yaml` 是否每次都需要重新编译？

不需要。支持热加载的配置保存后自动生效；需要重启的配置只需重建容器，不需要重新编译镜像。

### 为什么 OBS 推流失败？

检查是否将完整 RTMP 地址和串流密钥重复填写。OBS 应分别填写服务器和串流密钥。

### 为什么 HLS 显示多个底层连接？

HLS 会请求播放列表和多个 TS 分片。页面按用户聚合显示，关联连接数仅用于说明底层请求数量。

### 为什么不能访问公网 `1985` 或 `8080`？

这是安全设计。这两个端口只提供给宿主机和 Token Gate 使用，公网请求必须经过统一网关鉴权。

# SRS Token Gate

轻量级 SRS 推拉流 Token 鉴权与统一 Web 控制台。

SRS Token Gate 在 [SRS](https://github.com/ossrs/srs) 前增加一个 Go 网关，为 RTMP、HTTP-FLV、HLS 和 WebRTC 提供 Token 鉴权，同时保护 SRS 控制台、播放器页面和管理 API。

## 功能

- RTMP 推流和播放 Token 鉴权。
- HTTP-FLV、HLS 播放 Token 鉴权。
- WebRTC WHIP 推流和 WHEP 播放 Token 鉴权。
- Token 可设置推流、播放或双向权限。
- Token 可精确绑定指定流名称。
- 持久化房间管理，创建房间时自动生成专属双向 Token。
- 地址生成器可直接选择房间，房间名会显示在 RTMP、FLV、HLS 和 WebRTC 地址中。
- 当前调用用户可按房间筛选，推流和播放统计同步切换。
- 本次直播分享页：免登录观看，可开关，断线超过 30 秒后链接失效。
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

- `token-auth`（容器名 `srs-token-auth`）：自动初始化配置、登录、Token 管理、鉴权和 HTTP 反向代理。
- `srs`：处理 RTMP、HTTP-FLV、HLS 和 WebRTC 媒体。

SRS 的 `1985` 和 `8080` 只绑定宿主机回环地址，不能从公网绕过 Token Gate。

## 项目目录

| 路径 | 用途 |
|---|---|
| `.env.example` | Docker 监听地址、端口和镜像配置模板 |
| `docker-compose.yml` | 两容器部署编排 |
| `README.md` | 安装、配置、推拉流和排错教程 |
| `srs-token-gate/config.example.yaml` | 统一业务配置模板 |
| `srs-token-gate/conf/console/` | SRS 控制台覆盖页面和 Token 页面 |
| `srs-token-gate/conf/srs/` | SRS Hook、鉴权、HLS 和 WebRTC 配置模板 |
| `srs-token-gate/docs/` | Nginx/OpenResty 和 Token 相关文档 |
| `srs-token-gate/scripts/start-srs.sh` | 安装控制台文件并启动 SRS |
| `srs-token-gate/scripts/publish-image.ps1` | 本地手动构建并发布 Docker 镜像 |
| `srs-token-gate/token-auth/` | Go Token Gate 源码和 Dockerfile |
| `srs-token-gate/token-auth/vendor/` | 固定版本的 Go 依赖源码 |
| `srs-token-gate/data/` | Token 数据和 HLS 文件，生产环境不提交 |
| `srs-token-gate/config.yaml` | 实际运行配置，生产环境不提交 |
| `.github/workflows/publish-docker.yml` | 推送 `v*.*.*` Tag 时发布 GitHub Container Registry 镜像 |

GitHub Actions 发布镜像时只使用 `srs-token-gate/token-auth/` 作为 Docker 构建上下文。真实配置、Token、Cookie、私钥和生产日志不会进入仓库或镜像。

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
TOKEN_GATE_IMAGE=ghcr.io/jwwsjlm/srs-token-gate:latest
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

### 配置项速查

| 配置项 | 作用 | 热加载 | 备注 |
|---|---|---|---|
| `version` | 配置格式版本 | 否 | 变更后需要重启 |
| `public.address` | 生成推拉流地址使用的域名或公网 IP | 是 | 留空时使用当前访问地址 |
| `admin.username` | 管理员用户名 | 否 | 变更后需要重启 |
| `admin.password` | 明文管理员密码 | 否 | 仅在没有 `password_hash` 时使用 |
| `admin.password_hash` | bcrypt 管理员密码哈希 | 否 | 非空时优先使用 |
| `session.cookie_secure` | 是否只允许 HTTPS Cookie | 否 | HTTPS 为 `true`，IP HTTP 为 `false` |
| `session.max_age_seconds` | 管理员 Session 有效期 | 否 | 默认 86400 秒 |
| `srs.candidate` | WebRTC SDP 公网候选 IP | 是 | 只能填写公网 IP |
| `srs.hook_secret` | SRS Hook 内部共享密钥 | 否 | 不要提交或记录 |
| `srs.max_connections` | SRS 最大连接数 | 是 | 由 SRS RAW reload 应用 |
| `srs.log_level` | SRS 日志级别 | 是 | 建议使用 `warn` |
| `statistics.active_ttl_seconds` | HLS 最后活跃后的保留时间 | 是 | 默认 30 秒 |
| `statistics.reconcile_interval_seconds` | SRS 客户端对账间隔 | 是 | 默认 15 秒 |

`.env` 中的监听地址、端口和 `TOKEN_GATE_IMAGE` 属于 Compose 配置，修改后需要重新创建容器，不属于 `config.yaml` 热加载范围。

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

## 房间与 Token 使用

登录控制台后打开“Token 鉴权”页面：

推荐使用房间模式：

1. 输入房间名，例如 `huiyi-01`。
2. 点击“创建房间并生成 Token”。
3. 在地址生成器中选择房间。
4. 页面自动带入房间 Token，并生成全部推流和播放地址。

房间名直接作为流名称，因此地址中可以直接识别房间：

```text
rtmp://example.com:1935/live/huiyi-01?token=<token>
https://example.com/live/huiyi-01.flv?token=<token>
https://example.com/live/huiyi-01.m3u8?token=<token>
```

房间名仅允许小写字母、数字、`-`、`_`，必须以字母或数字开头，最多 64 个字符。房间自带 Token 同时具有推流和播放权限，并只能访问该房间。

“独立 Token 高级管理”继续支持原有模式：填写调用用户、权限和可选限定流名，然后手动选择 Token 和流名称。

限定流名采用精确匹配。例如房间 Token 绑定 `huiyi-01` 后，不能访问其他房间。

### OBS 推流

OBS 必须分开填写：

```text
服务器：rtmp://example.com/live
串流密钥：huiyi-01?token=<token>
```

不要同时在服务器地址和串流密钥中重复填写 Token。

### FFmpeg 或单地址客户端

```text
rtmp://example.com:1935/live/huiyi-01?token=<token>
```

### HTTP-FLV

```text
https://example.com/live/huiyi-01.flv?token=<token>
```

### HLS

```text
https://example.com/live/huiyi-01.m3u8?token=<token>
```

### WebRTC WHIP/WHEP

```text
https://example.com/rtc/v1/whip/?app=live&stream=huiyi-01&token=<token>
https://example.com/rtc/v1/whep/?app=live&stream=huiyi-01&token=<token>
```

### 分享本次直播（v1.2.0）

1. 先向房间推流，在“推拉流地址生成器”中选择该房间。
2. 在“免登录观看链接”一行点击“生成分享”。链接包含房间名，例如 `http://203.0.113.10:9085/watch/huiyi-01/<本次直播标识>`。
3. 点击“复制链接”发给观众；观众无需管理员账号或填写 Token，打开即可观看。
4. “关闭分享”只关闭观看入口，不中断推流；同场直播重新开启仍使用原链接。

分享默认不生成。重复点击生成返回同场链接；推流断开后等待 30 秒，期间重连继续使用原链接，超过后永久失效，下场直播需重新生成。停用房间或房间 Token、Token 过期、删除房间、重启 Token Gate，也会使分享失效。

分享凭据仅用于播放，不包含房间推流密钥。HLS 播放列表和分片均重新校验分享状态；SRS 状态检查失败时暂停分享播放。分享链接可以转发，任何拿到有效链接的人都能观看，暂不提供观看密码、到期时间或每位观众的独立权限。

观看页使用 HLS，支持现代 Chrome、Edge 和 Safari，并兼容 HTTP/IP 及 HTTPS 域名模式。页面尝试静音自动播放，必要时点击“点击播放”，再点击“开启声音”。HLS 通常有数秒至十几秒延迟；关闭分享后已缓冲的画面可能短暂继续播放。分享观众计入房间播放统计；同一 IP 的观众沿用现有聚合规则。

管理接口（要求管理员登录）：`GET/POST/PATCH /api/token/rooms/{id}/share`，PATCH 请求为 `{"enabled":true}` 或 `{"enabled":false}`。分享页与状态接口仅返回观看所需信息，不开放后台 API。

播放器使用固定版本 HLS.js **1.7.4**，脚本及 Apache-2.0 许可证位于 `srs-token-gate/token-auth/ui/`，随镜像提供，无外部 CDN 依赖。

升级到本版本时，同时更新仓库中的控制台覆盖文件；仅拉取网关镜像不会更新 SRS 容器内已复制的控制台页面。分享状态只存于内存，不改动 `tokens.json` 中现有房间和 Token 的结构。

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

房间、房间 Token、独立 Token 和会话数据都保存在 `tokens.json`。更新镜像或重建容器不会重新生成已有数据。

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
# 手动发布前先使用 GitHub Personal Access Token 登录 GHCR。
docker login ghcr.io
.\srs-token-gate\scripts\publish-image.ps1
```

默认发布到：

```text
ghcr.io/jwwsjlm/srs-token-gate:latest
```

## GitHub Tag 自动发布镜像

仓库内置 GitHub Actions：

```text
.github/workflows/publish-docker.yml
```

它只负责构建并推送 Docker 镜像，不运行项目测试或其他自动检查。Workflow 使用 GitHub Actions 自动提供的 `GITHUB_TOKEN`，不需要配置 Docker Hub 用户名或密码；仓库权限中已声明 `packages: write`。

首次发布后，请在 GitHub 仓库的 `Packages` 页面将该容器包设置为 `Public`，这样生产服务器无需登录即可拉取镜像。

推送语义化版本 Tag 即可发布：

```bash
git tag v1.0.0
git push origin v1.0.0
```

Action 会构建 `linux/amd64` 镜像，并发布以下标签：

```text
ghcr.io/jwwsjlm/srs-token-gate:1.0.0
ghcr.io/jwwsjlm/srs-token-gate:latest
```

生产环境推荐固定版本：

```env
TOKEN_GATE_IMAGE=ghcr.io/jwwsjlm/srs-token-gate:1.0.0
```

希望跟随最新版本时使用：

```env
TOKEN_GATE_IMAGE=ghcr.io/jwwsjlm/srs-token-gate:latest
```

升级：

```bash
docker compose pull token-auth
docker compose up -d --force-recreate
```

不要重复移动已经发布的版本 Tag；需要修复版本时发布新的 Tag。

## 常见问题

先执行下面三条命令查看最基本状态：

```bash
docker compose ps
docker compose logs --tail=100 token-auth
docker compose logs --tail=100 srs
```

检查 Compose 文件是否能被正确解析：

```bash
docker compose config
```

### `token-auth` 显示 unhealthy

确认 Token Gate 日志中没有配置解析错误：

```bash
docker compose logs --tail=100 token-auth
```

常见原因：

- `config.yaml` YAML 缩进错误。
- `admin.username` 为空。
- `admin.password` 和 `admin.password_hash` 同时为空且容器没有写权限。
- `GATEWAY_PORT` 已被其他服务占用。

### 首页跳转到 `/token/`

这是未登录保护行为。登录后访问：

```text
/console/ng_index.html#/summaries
```

如果登录后仍然循环跳转，检查 HTTPS 域名是否配置了：

```yaml
session:
  cookie_secure: true
```

IP HTTP 模式必须使用：

```yaml
session:
  cookie_secure: false
```

### 忘记管理员密码

```bash
docker compose run --rm token-auth show-admin-password
```

如果使用 `admin.password_hash`，系统不会显示原始密码，只能修改配置中的哈希并重新创建容器。

### 修改配置后没有生效

支持热加载的字段保存后会自动监听，也可以在 Token 页面点击“重载统一配置”。检查日志：

```bash
docker compose logs --tail=100 token-auth
```

管理员、Session、Hook Secret、监听地址或端口变更需要：

```bash
docker compose up -d --force-recreate
```

### OBS 推流失败

OBS 必须拆开填写：

```text
服务器：rtmp://域名或IP/live
串流密钥：stream?token=<token>
```

不能把完整 RTMP 地址填入服务器后，再在串流密钥中重复填写 Token。

如果日志中出现类似：

```text
?token=xxx?token=xxx
```

说明客户端重复拼接了 Token。

### HTTP-FLV 返回 403

按以下顺序排查：

1. URL 是否包含 `?token=`。
2. Token 是否启用。
3. Token 权限是否包含 `play` 或 `both`。
4. Token 是否绑定了其他流名。
5. 播放地址中的流名是否与生成器一致。

### HLS 返回 403 或 404

- `403`：通常是缺少 Token、Token 错误或流名限制不匹配。
- `404`：通常是当前没有推流，或 HLS 文件尚未生成。
- HLS 播放列表和 TS 分片需要继续携带 Token。

### WebRTC WHIP/WHEP 返回 403 或 502

- `403`：Token 缺失、权限不匹配或流名不匹配。
- `502`：鉴权已经通过，但 SRS WebRTC 信令或 SDP 请求失败；检查 `srs` 日志和 `srs.candidate`。
- `srs.candidate` 必须填写公网 IP，不能填写域名。
- 防火墙需要放行 UDP/TCP `8000`。

### SRS API 返回 401

公网访问 `/api/v1/*` 必须先登录 Token Gate 管理页面。SRS 原生 `1985` 端口不提供公网访问，属于安全设计。

### 公网无法访问 `1985` 或 `8080`

这是预期行为。Compose 将它们绑定到宿主机 `127.0.0.1`，防止绕过 Token Gate。公网入口应使用域名反代或 IP 直连的 `9085`。

### GitHub Container Registry 拉取失败

检查镜像配置：

```bash
docker compose config | grep TOKEN_GATE_IMAGE
docker pull ghcr.io/jwwsjlm/srs-token-gate:latest
```

公开 GHCR 镜像可以匿名拉取；如果仓库被设置为私有，需要先执行 `docker login ghcr.io`。生产环境推荐固定到已经验证过的版本标签，避免 `latest` 在升级时发生变化。

### 端口已被占用

查看占用端口：

```bash
ss -lntup | grep -E '1935|8000|9085'
```

修改 `.env` 中的 `GATEWAY_PORT`、`RTMP_PORT` 或 WebRTC 端口后重新创建容器。

### 日志占满磁盘

Compose 已为 `token-auth` 和 `srs` 配置 `10MB × 3` 的 Docker 日志轮转。检查配置：

```bash
docker inspect srs-token-auth --format '{{json .HostConfig.LogConfig}}'
docker inspect srs --format '{{json .HostConfig.LogConfig}}'
```

外部 Nginx/OpenResty 日志需要单独配置轮转。

### 当前调用用户数量和底层连接数量不同

页面按 Token 用户、IP、应用、流名和推流/播放方向聚合。HLS 的主播放列表、子列表和 TS 分片会合并为一个用户，但“关联连接数”仍会显示底层请求数量。

### 为什么只有两个容器？

当前版本只有 `srs-token-auth` 和 `srs` 两个长期容器。初始化已经合并到 Token Gate 启动流程中。

### 修改 `config.yaml` 是否每次都需要重新编译？

不需要。支持热加载的配置保存后自动生效；需要重启的配置只需重建容器，不需要重新编译镜像。

### 为什么 OBS 推流失败？

检查是否将完整 RTMP 地址和串流密钥重复填写。OBS 应分别填写服务器和串流密钥。

### 为什么 HLS 显示多个底层连接？

HLS 会请求播放列表和多个 TS 分片。页面按用户聚合显示，关联连接数仅用于说明底层请求数量。

### 为什么不能访问公网 `1985` 或 `8080`？

这是安全设计。这两个端口只提供给宿主机和 Token Gate 使用，公网请求必须经过统一网关鉴权。

# SRS Token Gate 部署说明

## 首次启动

```bash
cp .env.example .env
docker compose up -d --build
```

业务配置统一位于：

```text
srs-token-gate/config.yaml
```

若文件不存在，`token-auth` 启动时会从带注释的 `config.example.yaml` 自动创建，并生成管理员密码和 SRS Hook Secret。

查看管理员密码：

```bash
docker compose run --rm token-auth show-admin-password
```

## 数据文件

```text
srs-token-gate/config.yaml
srs-token-gate/data/tokens.json
srs-token-gate/data/hls/
```

`config.yaml` 包含敏感信息，权限固定为 `0600`，不要提交到 GitHub。

## 访问控制

- 未登录访问 `/`、`/console/*`、`/players/*` 会跳转 `/token/`。
- `/api/v1/*` 需要管理员 Session。
- `/live/*`、WHIP、WHEP 使用流 Token 鉴权。
- SRS `1985`、`8080` 仅绑定宿主机回环地址。
- 外部反代只需要转发到 `127.0.0.1:9085`。

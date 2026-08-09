# Changelog

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 的格式约定。

## [Unreleased]

### Removed

- 精简为仅保留核心运维功能：健康检查、登录/会话/登出、入站配置 CRUD、VLESS/Reality/用户编辑、入站分享链接与二维码、YAML 配置读取/保存、配置备份与恢复、校验/原子替换/失败回滚、保存后 mihomo 热重载。
- 删除运行状态总览（`GET /api/overview`）及 `/version`、`/traffic`、`/memory`、`/configs` 等聚合逻辑。
- 删除运行模式切换（`PATCH /api/mode`）与出站策略组/节点选择（`GET /api/proxies`、`PUT /api/proxies/{group}`）。
- 删除连接查看（`GET /api/connections`）。
- 删除日志 SSE（`GET /api/logs/stream`）。
- 删除 systemd 服务启停控制（`POST /api/service/{action}`）、`internal/service` 包及 `MIHOMO_SERVICE` 配置契约。
- `internal/mihomo/client.go` 精简为仅保留 `Reload` 及 `PUT /configs?force=true` 所需的 HTTP 请求代码。
- 对应删除上述功能的 API 路由、前端页面/板块、Go 测试与文档说明。

### Changed

- `internal/server/web/index.html` / `app.js` / `app.css`：登录后仅保留「入站」与「配置」两个页面。
- README / PLAN / CHANGELOG / `.env.example` / `install.sh` / `examples/mihomo-config.yaml` 同步精简。
- 已删除的 `/api` 路由统一由静态兜底返回 `{"error":"接口不存在"}` JSON 404。

### Security

- mihomo Secret 继续仅由后端持有，不发送到浏览器。

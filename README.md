# MPanel

MPanel 是面向个人 Linux VPS 的轻量 mihomo 运维面板。它由单个 Go 进程提供 API 和嵌入式中文界面，不需要 Node.js 或数据库。

功能包括：24 小时签名会话、运行状态总览、systemd 启停、运行模式（直连/规则/全局）切换、策略组与节点选择、完整 YAML 安全保存与回滚、最近 5 份备份、listener 可视化增删改，以及 mihomo 结构化日志 SSE 转发。

## 构建

需要 Go 1.23 或更新版本：

```bash
go test ./...
go vet ./...
go build -trimpath -o mpanel ./cmd/mpanel
```

前端通过 `embed` 编入二进制，不需要额外复制静态资源。

## 一键安装（Debian / Ubuntu）

项目自带 `install.sh`，支持 root 系统级安装和普通用户级安装，自动配置 systemd 和环境变量。

### Root 系统级安装（推荐）

```bash
git clone https://github.com/xiki45/Mpanel.git
cd Mpanel
sudo bash install.sh
```

脚本会自动：
- 从 GitHub Release 下载预编译二进制（无需 Go 环境，适合弱性能 VPS）
- 安装到 `/usr/local/bin/mpanel`
- 生成 `/etc/mpanel/mpanel.env`（随机密码 + 会话密钥）
- 创建 `/etc/systemd/system/mpanel.service`（含安全加固）
- 启动并启用 mpanel.service

### 普通用户级安装

```bash
git clone https://github.com/xiki45/Mpanel.git
cd Mpanel
bash install.sh
```

普通用户模式下：
- 二进制安装到 `~/.local/bin/mpanel`
- 配置生成到 `~/.config/mpanel/mpanel.env`
- systemd 用户级 service，自动启用 lingering 保持后台运行

### 同时安装 mihomo

如果服务器尚未安装 mihomo，可加 `--install-mihomo` 选项：

```bash
sudo bash install.sh --install-mihomo
```

### 卸载

```bash
# root 安装的卸载
sudo bash install.sh --uninstall

# 用户级安装的卸载
bash install.sh --uninstall
```

### 手动安装（替代方案）

如果不想使用一键脚本，可手动部署：

1. 确保已安装并由 `mihomo.service` 托管 mihomo，且配置中的 `external-controller` 只监听本机，例如 `127.0.0.1:9090`。
2. 将构建好的二进制安装为 `/usr/local/bin/mpanel`。
3. 创建 `/etc/mpanel/mpanel.env`，权限设为仅 root 可读，并参照 `.env.example` 配置。
4. 安装 `deploy/mpanel.service` 到 `/etc/systemd/system/mpanel.service`。
5. 重载 systemd 并启动服务。

```bash
sudo install -m 0755 mpanel /usr/local/bin/mpanel
sudo install -d -m 0700 /etc/mpanel
sudo install -m 0600 .env.example /etc/mpanel/mpanel.env
sudo install -m 0644 deploy/mpanel.service /etc/systemd/system/mpanel.service
sudo systemctl daemon-reload
sudo systemctl enable --now mpanel.service
sudo systemctl status mpanel.service
```

可用以下命令生成会话密钥：

```bash
openssl rand -base64 48
```

Root 系统级安装的 service 以 root 运行，因为需要执行 `systemctl` 并原子替换 `/etc/mihomo/config.yaml`。systemd unit 使用了 `ProtectSystem=strict` 等安全加固，仅开放 `/etc/mihomo` 写权限。普通用户级安装则运行在用户权限下，无系统级加固。

## TLS 反向代理

MPanel 默认监听 `0.0.0.0:8080`（对所有接口开放）。**不要直接暴露 HTTP 端口到公网**，务必通过反向代理加 TLS 访问。安装 Caddy 后，将 `deploy/Caddyfile` 中的域名替换为真实域名，并确保 DNS 已指向 VPS：

```bash
sudo install -m 0644 deploy/Caddyfile /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

Caddy 自动处理 TLS。MPanel 信任本机反代发送的 `X-Forwarded-Proto` 来设置 Secure Cookie 和执行同源检查。

> **安全提示**：安装脚本默认将 `MPANEL_LISTEN_ADDR` 设为 `0.0.0.0:8080` 以便外部访问。若仅在局域网或本机使用，建议改为 `127.0.0.1:8080` 以降低暴露面，再配合反代反向代理。

## 配置说明

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `MPANEL_LISTEN_ADDR` | `0.0.0.0:8080` | 面板监听地址 |
| `MPANEL_USERNAME` | `admin` | 登录用户名 |
| `MPANEL_PASSWORD` | 无，必填 | 登录密码 |
| `MPANEL_SESSION_SECRET` | 无，至少 32 字节 | Cookie HMAC 密钥 |
| `MIHOMO_API_URL` | `http://127.0.0.1:9090` | mihomo 控制 API |
| `MIHOMO_API_SECRET` | 空 | mihomo API Bearer secret |
| `MIHOMO_CONFIG_PATH` | `/etc/mihomo/config.yaml` | 固定配置路径 |
| `MIHOMO_BINARY` | `/usr/local/bin/mihomo` | 固定校验程序路径 |
| `MIHOMO_SERVICE` | `mihomo.service` | 固定 systemd unit |

敏感值只存在于服务环境中，不会发往浏览器。修改 API 只接受 JSON，并拒绝非同源浏览器请求；没有 `Origin` 的请求允许用于本机 CLI 运维。

模式切换与策略组选择通过 mihomo 控制 API 修改运行状态，不会改写 YAML。若需要在 mihomo 重启后保留策略组选择，请在 mihomo 配置中启用：

```yaml
profile:
  store-selected: true
```

## 配置事务

每次完整 YAML 保存、listener 变更或备份恢复都执行同一流程：

1. 结构化解析 YAML。
2. 在目标目录创建临时文件并执行 `mihomo -t -f <temp-file>`。
3. 校验通过后创建带时间戳的备份，以 `rename` 原子替换配置。
4. 调用 `PUT /configs?force=true` 热重载。
5. 热重载失败时恢复原字节，并尝试重新加载旧配置。

listener 编辑使用 `yaml.v3` 节点树，未知顶层字段和未知 listener 字段会保留。备份文件位于配置同目录，命名形式为 `config.yaml.bak.<UTC 时间戳>`，自动保留最近 5 份。

## 管理 API

面板自身提供一套需登录的 JSON API，前端通过它们管理 mihomo：

```text
POST   /api/auth/login
GET    /api/auth/session
POST   /api/auth/logout
GET    /api/overview
PATCH  /api/mode                 # 切换运行模式
GET    /api/proxies              # 读取策略组与可选节点
PUT    /api/proxies/{group}      # 选择策略组当前节点
POST   /api/service/{start|stop|restart}
GET    /api/config
PUT    /api/config
GET    /api/config/backups
POST   /api/config/backups/{id}/restore
GET    /api/listeners
POST   /api/listeners
PUT    /api/listeners/{name}
DELETE /api/listeners/{name}
GET    /api/logs/stream
GET    /healthz
```

- `PATCH /api/mode` 请求体为 `{"mode":"rule"}`（`rule`/`global`/`direct`），成功返回 `{"ok":true,"mode":"rule"}`。
- `GET /api/proxies` 返回 `{"proxies":{<组名>:{type,now,all:[...]}}}`，其中 `<组名>` 为代理/策略组名，`all` 为该策略组可选节点列表。
- `PUT /api/proxies/{group}` 请求体为 `{"name":"<节点名>"}`，成功返回 `{"ok":true}`；组名与节点名中的中文、空格、斜杠等字符均需 URL 编码。
- 错误统一返回 `{"error":"human-readable message"}`。

## 健康检查与排障

`GET /healthz` 无需登录，仅表示面板 HTTP 进程存活。mihomo 离线不会使面板健康检查失败，界面总览会明确显示离线。

```bash
curl http://127.0.0.1:8080/healthz
journalctl -u mpanel.service -f
```

若 service 操作失败，确认 MPanel 运行用户有权调用 `systemctl`。若保存失败，确认配置目录可写、`MIHOMO_BINARY` 可执行，以及 mihomo API URL 和 secret 正确。

## 安全边界

- 面板管理单个、由启动环境固定的 mihomo 实例，不接受任意文件路径、unit 或命令。
- 生产环境必须通过 TLS 使用，并限制 MPanel 与 mihomo controller 仅监听回环地址。
- 示例凭据都是占位符，部署前必须替换。
- 日志流由后端代理；浏览器不会获得 `MIHOMO_API_SECRET`。

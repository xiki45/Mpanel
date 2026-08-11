# MPanel

MPanel 是面向个人 Linux VPS 的轻量 mihomo 运维面板。它由单个 Go 进程提供 API 和嵌入式中文界面，不需要 Node.js 或数据库。

功能聚焦两大板块：**入站配置**（listener 可视化增删改，含 VLESS/Reality 结构化编辑，以及入站分享链接与二维码）和**配置编辑**（完整 YAML 安全保存与回滚、最近 5 份备份、`mihomo -t` 校验与热重载）。同时提供 24 小时签名会话。

策略控制（节点/策略组切换）与连接查看由 **Zashboard**（面向 mihomo 的 Web 仪表盘）承担，可通过 `--install-zashboard` 随本脚本一并安装。

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
- 从 GitHub Release 下载预编译二进制（默认最新正式版，可用 `--version` 固定版本；无需 Go 环境，适合弱性能 VPS）
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

```bash
systemctl status mihomo
```

```bash
sudo systemctl start mihomo
```

```bash
sudo systemctl stop mihomo
```

```bash
sudo systemctl restart mihomo
```

### 指定版本安装

默认从 GitHub Release 拉取最新正式版；如需固定到某个版本，加 `--version` 指定 release tag：

```bash
sudo bash install.sh --version v1.0.0   # 指定 tag
sudo bash install.sh --version 1.0.0    # 自动补 v 前缀，与上面等价
sudo bash install.sh --version latest   # 等价于默认（最新正式版）
```

`--version` 直接使用固定的 Release 下载地址，不依赖 GitHub API（不受匿名 API 限流影响）；tag 或对应架构资产不存在时脚本会直接报错退出，不会回退到源码编译。

### 同时安装 Zashboard（策略控制 / 连接查看）

Zashboard 是纯静态 Web 仪表盘，直接对接 mihomo 的 controller API，负责节点/策略组切换、延迟测速与实时连接查看。它架构无关，不依赖 Go：

```bash
sudo bash install.sh --install-zashboard
```

脚本会自动：
- 从 Zashboard Release 下载静态文件（`dist-no-fonts.zip`）
- 安装到 `/var/www/zashboard`（root）/ `~/.local/share/zashboard`（普通用户）
- 创建 `/etc/systemd/system/zashboard.service`（含 `Restart=on-failure` 崩溃自动重启）
- 启动并启用 zashboard.service，监听 `8081` 端口

安装完成后，在浏览器打开 `http://<服务器IP>:8081`，按界面提示填入 mihomo API 地址（默认 `http://127.0.0.1:9090`）与 secret 即可。若 mihomo 的 `external-controller` 未绑定 `0.0.0.0`，局域网内其他设备将无法访问该 API，请按需调整 mihomo 配置。

> **注意**：`--install-zashboard` 会覆盖已存在的 zashboard web 目录。该选项只安装面板前端，不下载 mihomo 核心。

### 卸载

```bash
# root 安装的卸载
sudo bash install.sh --uninstall

# 用户级安装的卸载
bash install.sh --uninstall
```

卸载时会停止并删除 mpanel 服务，并询问是否删除配置。若同时安装了 Zashboard，会一并停止 `zashboard.service` 并询问是否删除其 web 目录。

### 手动安装（替代方案）

如果不想使用一键脚本，可手动部署：

1. 确保已安装并由 `mihomo.service` 托管 mihomo，且配置中的 `external-controller` 只监听本机，例如 `127.0.0.1:9090`。
2. 将构建好的二进制安装为 `/usr/local/bin/mpanel`。
3. 创建 `/etc/mpanel/mpanel.env`，权限设为仅 root 可读，并参照 `.env.example` 配置。
4. 安装 `deploy/mpanel.service` 到 `/etc/systemd/system/mpanel.service`。
5. 重载 systemd 并启动服务。
6. （可选）手动部署 Zashboard：下载其 Release 静态文件解压到 `/var/www/zashboard`，用任意静态服务器托管 `8081` 端口即可；建议创建对应 systemd 服务以保活。

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

Root 系统级安装的 service 以 root 运行，因为需要原子替换 `/etc/mihomo/config.yaml`。systemd unit 使用了 `ProtectSystem=strict` 等安全加固，仅开放 `/etc/mihomo` 写权限。普通用户级安装则运行在用户权限下，无系统级加固。

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

敏感值只存在于服务环境中，不会发往浏览器。修改 API 只接受 JSON，并拒绝非同源浏览器请求；没有 `Origin` 的请求允许用于本机 CLI 运维。

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
GET    /api/config
PUT    /api/config
GET    /api/config/backups
POST   /api/config/backups/{id}/restore
GET    /api/listeners
POST   /api/listeners
PUT    /api/listeners/{name}
DELETE /api/listeners/{name}
GET    /api/listeners/{name}/shares
GET    /healthz
```

- `GET /api/listeners/{name}/shares` 需要 `host` 查询参数（公网域名或 IP），返回该入站的分享链接与二维码。
- 错误统一返回 `{"error":"human-readable message"}`。

## 健康检查与排障

`GET /healthz` 无需登录，仅表示面板 HTTP 进程存活。mihomo 离线不会使面板健康检查失败。

```bash
curl http://127.0.0.1:8080/healthz
journalctl -u mpanel.service -f
```

若保存失败，确认配置目录可写、`MIHOMO_BINARY` 可执行，以及 mihomo API URL 和 secret 正确。

## 安全边界

- 面板管理单个、由启动环境固定的 mihomo 实例，不接受任意文件路径或命令。
- 生产环境必须通过 TLS 使用，并限制 MPanel 与 mihomo controller 仅监听回环地址。
- 示例凭据都是占位符，部署前必须替换。
- mihomo API 访问由后端代理；浏览器不会获得 `MIHOMO_API_SECRET`。

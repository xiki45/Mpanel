# MPanel 轻量项目规划

## 1. 项目定位

MPanel 是供个人 Linux VPS 使用的 mihomo 管理面板。它借鉴 x-ui 的“状态总览 + 可视化管理 + 单文件部署”体验，但不实现多租户、计费、流量配额、证书签发、通知机器人等重型功能。

目标是用一个面板进程安全地管理一个已经由 systemd 托管的 mihomo 实例。

## 2. 技术方案

- 后端：Go 1.23，标准库 HTTP 服务，尽量减少第三方依赖。
- 前端：原生 HTML/CSS/JavaScript，使用 Go `embed` 打包，不需要 Node.js。
- 配置：环境变量；不使用数据库。
- 部署：单个可执行文件 + systemd service；可选反向代理负责 TLS。
- mihomo 集成：REST API 获取运行状态和执行热重载；本机命令执行配置校验和 systemd 启停。

建议目录：

```text
cmd/mpanel/main.go
internal/auth/
internal/config/
internal/mihomo/
internal/server/
web/
deploy/
```

允许实现者按职责微调目录，但必须保持单体、轻依赖。

## 3. 安全模型

- 面板默认监听 `127.0.0.1:8080`，公网访问应通过 Caddy/Nginx TLS 反代。
- mihomo 的 `external-controller` 默认只监听 `127.0.0.1:9090`。
- 浏览器永远不直接持有 mihomo secret；所有请求由后端代理。
- 面板密码只通过环境变量提供，后端启动时读取；禁止硬编码默认密码。
- 登录后使用带签名、过期时间 24 小时的 HttpOnly、SameSite=Strict Cookie；认证失败使用统一错误。
- 所有修改操作仅接受同源 JSON 请求，并校验 Origin（Origin 缺失时允许 CLI 调用）。
- 配置文件路径和 systemd unit 名由启动配置固定，API 不接受任意路径或命令。
- HTTP 服务配置合理的 header/read/write/idle 超时和请求体上限。

## 4. MVP 功能

### 4.1 登录

- 用户名和密码登录。
- 登录、会话检查、退出。
- 未认证 API 返回 401；页面访问回到登录界面。

### 4.2 总览

- mihomo 在线/离线状态、版本、运行模式。
- 当前上传/下载速度、累计上传/下载、内存占用。
- 活跃连接数。
- 数据不可用时显示明确的离线状态，不能让页面崩溃。

实时指标可以由后端 WebSocket 或 SSE 转发，也可由前端每 2 秒轮询后端聚合接口；个人 VPS 优先选择更简单可靠的方式。

### 4.3 运行控制

- 通过固定的 `systemctl` 参数启动、停止、重启 mihomo。
- 显示动作成功或后端返回的安全错误信息。
- “停止”和“重启”需要前端二次确认。

### 4.4 配置管理

- 读取和编辑完整 YAML 原文。
- 保存前调用固定的 mihomo 二进制执行配置校验，例如 `mihomo -t -f <temp-file>`。
- 校验失败时不得覆盖当前配置，并返回校验输出。
- 校验成功后：在同目录创建备份、以临时文件 + rename 原子替换、调用 mihomo `/configs?force=true` 热重载。
- 若热重载失败，恢复备份并尝试重新加载旧配置。
- 提供最近 5 份备份列表和恢复操作；恢复同样先校验，再原子替换并热重载。
- 编辑器仅为等宽文本区域，不引入大型代码编辑器依赖。

### 4.5 入站管理

- 读取配置中的 `listeners`，以表格展示名称、类型、监听地址和端口。
- MVP 支持新增、编辑、删除以下常见类型：`mixed`、`http`、`socks`、`shadowsocks`、`vmess`、`vless`、`trojan`、`hysteria2`。
- 表单采用“通用字段 + 协议专属 YAML 片段”模式，避免为所有 mihomo 字段手写表单。
- 保存时保留配置中未知顶层字段和未知 listener 字段。
- 所有变更复用配置管理的校验、备份、原子替换、热重载流程。
- 删除需要二次确认；名称不能为空且不得重复；端口必须为 `1..65535` 的整数。

### 4.6 日志

- 后端转发 mihomo `/logs?format=structured` 的流式响应到浏览器，使用 SSE。
- 页面保留最近 300 条，支持暂停、清空和按级别筛选。
- 客户端断开时及时取消上游请求，避免协程泄漏。

## 5. 后端 API

路径可做小幅调整，但语义和保护要求不变：

```text
POST   /api/auth/login
GET    /api/auth/session
POST   /api/auth/logout
GET    /api/overview
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

统一 JSON 错误：

```json
{"error":"human-readable message"}
```

不得把环境变量、mihomo secret、Cookie 签名密钥、完整内部命令或 Go 堆栈返回浏览器。

## 6. 启动配置

至少支持：

```text
MPANEL_LISTEN_ADDR=127.0.0.1:8080
MPANEL_USERNAME=admin
MPANEL_PASSWORD=<required>
MPANEL_SESSION_SECRET=<required, at least 32 bytes>
MIHOMO_API_URL=http://127.0.0.1:9090
MIHOMO_API_SECRET=<optional>
MIHOMO_CONFIG_PATH=/etc/mihomo/config.yaml
MIHOMO_BINARY=/usr/local/bin/mihomo
MIHOMO_SERVICE=mihomo.service
```

缺少必要配置时应快速失败并给出清晰日志。敏感配置不写入前端或示例明文值。

## 7. 界面设计

- 中文界面，桌面和手机均可用。
- 安静、紧凑的运维工具风格，不做营销首页和装饰性大卡片。
- 桌面端左侧导航，移动端顶部栏 + 可展开导航。
- 主色控制在中性灰、蓝绿状态色、红色危险操作，不使用单一深蓝或紫色主题。
- 页面包括：总览、入站、配置、日志。
- 交互必须有 loading、empty、offline、success、error 状态；按钮执行期间禁用，避免重复提交。
- 危险动作需要明确确认对话框。

## 8. 测试与质量要求

后端至少覆盖：

- 登录成功/失败、Cookie 篡改和过期、受保护路由。
- mihomo API Bearer 头、非 2xx 错误和超时处理。
- 配置校验失败不覆盖文件。
- 保存成功生成备份并替换文件。
- 热重载失败执行回滚。
- listener CRUD 保留未知 YAML 字段，拒绝重复名称和非法端口。
- service action 白名单，无法注入任意命令。

其他要求：

- `go test ./...` 通过。
- `go vet ./...` 通过。
- `go build ./cmd/mpanel` 通过。
- 提供 README、`.env.example`、示例 mihomo 配置、systemd unit 和最小 Caddy 反代示例。
- 不提交二进制、依赖缓存、真实密码或 secret。

## 9. 验收标准

1. 未登录无法访问任何管理 API，`/healthz` 除外。
2. 使用 mock mihomo API 启动后，可在浏览器完成登录并看到状态数据。
3. 在线与离线两种情况下页面均可正常使用和明确呈现状态。
4. 合法配置保存后原文件被替换、备份存在、热重载被调用。
5. 非法配置保存后原文件字节完全不变。
6. 模拟热重载失败后原文件恢复为保存前内容。
7. listener 增删改后 YAML 其他内容不丢失，并触发与完整配置相同的安全保存流程。
8. 日志流断开和重连正常，不泄露 mihomo secret。
9. 桌面宽度 1440px 和移动宽度 390px 下无横向溢出、文本遮挡或不可操作控件。
10. 所有自动测试、vet 和构建通过，文档可让 Debian/Ubuntu VPS 用户完成部署。

## 10. 暂不实现

- 多管理员、角色权限、多 VPS 集群管理。
- 用户计费、配额、到期时间、订阅分发。
- 自动申请 TLS 证书、DDNS、Telegram 通知。
- mihomo 自动下载或自更新。
- 出站节点、策略组、规则的完整可视化编辑器。
- Docker 内直接控制宿主机 systemd 的部署模式。

这些功能可在 MVP 稳定后按实际个人使用需求增量加入。

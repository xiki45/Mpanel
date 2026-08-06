# Changelog

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 的格式约定。

## [Unreleased]

### Added

- 运行模式切换：总览页提供 直连 / 规则 / 全局 三种模式的运行时切换（`PATCH /api/mode`）。
- 策略组列表：新增「出站」页面，展示 mihomo 策略组、当前出站节点及可选节点（`GET /api/proxies`）。
- 节点选择：可在策略组内选择出站节点（`PUT /api/proxies/{group}`）。
- 模式相关的策略组过滤：
  - 规则模式：隐藏隐式 `GLOBAL` 组，只显示用户配置的策略组。
  - 直连模式：不显示策略组，提示当前所有流量走直连。
  - 全局模式：显示全部策略组（含 `GLOBAL`）。
- 后端新增 `decodeStream`，正确处理 mihomo `/traffic`、`/memory` 流式接口（无流量时不推送数据）。

### Changed

- `internal/mihomo/client.go`：新增 `Proxies`、`SetMode`、`SelectProxy` 方法及流式解码支持。
- `internal/server/server.go`：扩展 `Mihomo` 接口，新增模式与策略组相关受保护路由。
- 前端 `index.html` / `app.js` / `app.css`：新增模式控件、出站页面及策略组样式。
- `examples/mihomo-config.yaml`：补充 `profile.store-selected: true`，使 mihomo 重启后保留策略组选择。
- README / PLAN 文档同步更新。

### Fixed

- 修复 `/api/overview` 因 mihomo `/traffic`、`/memory` 流式接口不关闭连接而阻塞数秒的问题，将总览轮询延迟从约 10 秒降至约 0.4 秒。
- 修复模式切换失败后下拉框未回滚的问题。
- 修复 mihomo 离线时模式控件仍可操作的问题。

### Security

- 模式切换与策略组选择均通过 mihomo 控制 API 修改运行时状态，不写回 YAML 配置。
- mihomo Secret 继续仅由后端持有，不发送到浏览器。

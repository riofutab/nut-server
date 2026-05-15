# Changelog

本项目所有重要变更都集中在这里。格式参考 [Keep a Changelog](https://keepachangelog.com/),版本号遵循 [Semantic Versioning](https://semver.org/)。

每个版本一段精简摘要,完整 release notes 见 `.github/release-notes/vX.Y.Z.md`,GitHub Release 也会贴一份。

## [Unreleased]

尚无未发布改动。

## [0.3.0] - 2026-05-15

聚焦可观测性、自动化与策略灵活度。

### Added
- **Prometheus `/metrics`** (M4): master admin 口暴露 `/metrics` (绑回环,不鉴权);slave 可选 `metrics_listen_addr`。Counter / GaugeVec 覆盖 UPS poll、注册、关机命令 / ack / 阶段、本地关机 phase。
- **e2e 集成测试套件** (M5): `internal/e2e/` + `e2e` build tag,loopback 上拉真 master + slave,覆盖闭环 / TLS / 重连 / metrics / install,CI 已接入。
- **`/install/slave` 一键装机** (M6): admin 接口返回 curl one-liner,目标机 `| sudo bash` 即装;`public_addr` 优先,fallback 到 Host;`node_id` 强校验 + shell-escape。
- **多策略关机条件** (M7): `shutdown_policies` 列表 (内 AND / 间 OR / target 并集),旧 `shutdown_policy` 自动合成 `default` 兼容;`when.on_battery` 改为对称三态。
- ldflags 把 git tag 注入 `internal/version.Version`,`/install/slave` 拿来选脚本版本。

### Fixed
- `shutdown_ack` 的 `status` label 现在走 allowlist,认证 slave 推任意状态字符串也不会爆 Prometheus cardinality (M4 本地 security review 修复)。

[完整发布说明](.github/release-notes/v0.3.0.md)

## [0.2.1] - 2026-05-15

Patch 版本,**无功能性改动**。CI 基础设施升级 (Node 24 一系列 actions) + 文档刷到 v0.2.0 实际形态。

[完整发布说明](.github/release-notes/v0.2.1.md)

## [0.2.0] - 2026-05-15

**Breaking changes** 较多,务必看 [Upgrade Notes](docs/upgrade.md)。

### Added
- 管理 API + Bearer admin_token (恒定时间比较)
- 节点目录 (first_seen / last_seen / tags / hostname / expected)
- 内嵌只读状态页 (`GET /`)
- 结构化日志 (slog JSON handler)
- 优雅停机 (SIGTERM/SIGINT → context cancellation)
- `.deb` / `.rpm` 发布产物 + `install-online.sh` 自动选包格式
- 状态文件原子写 + 0600 权限
- systemd unit 沙箱化 (`User=nut-server`, `ProtectSystem=strict` 等)

[完整发布说明](.github/release-notes/v0.2.0.md)

## [0.1.4] - 2026-04-13

### Added
- master 本机自关机 (`local_shutdown`): 远端节点完成后再关 master 主机;`max_wait` 兜底 + `emergency_runtime_minutes` 紧急阈值
- `/status` 暴露 `local_shutdown` 当前阶段
- shutdown 的 `executed` / `failed` 终态可以修正之前已标 `timeout` 的状态

[完整发布说明](.github/release-notes/v0.1.4.md)

## 早期版本

v0.1.2 及更早的版本未维护独立 release notes。如果你在跑这些版本, 强烈建议升级到 v0.2.x 以上 (含安全相关收紧)。

[0.3.0]: https://github.com/riofutab/nut-server/releases/tag/v0.3.0
[0.2.1]: https://github.com/riofutab/nut-server/releases/tag/v0.2.1
[0.2.0]: https://github.com/riofutab/nut-server/releases/tag/v0.2.0
[0.1.4]: https://github.com/riofutab/nut-server/releases/tag/v0.1.4

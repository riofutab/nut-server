# Changelog

本项目所有重要变更都集中在这里。格式参考 [Keep a Changelog](https://keepachangelog.com/),版本号遵循 [Semantic Versioning](https://semver.org/)。

每个版本一段精简摘要,完整 release notes 见 `.github/release-notes/vX.Y.Z.md`,GitHub Release 也会贴一份。

## [Unreleased]

尚无未发布改动。

## [0.5.0] - 2026-06-02

运维就绪 / 健壮性版本,一轮 6 维度架构师 review 的 13 条优化全部落地。**完全向后兼容**:新增 config 字段都有安全默认值,新增端点 / 指标为纯增量。

### Added
- **健康探针 `/healthz` + `/readyz`**: master admin 口与 slave metrics 口均新增免鉴权端点;`/healthz` 进程存活即 200,master `/readyz` 反映监听就绪(未就绪 503),供 k8s / systemd / LB 探针使用。
- **关机命令可配超时**: `local_shutdown.command_timeout`(master)/ `shutdown_command_timeout`(slave),默认 `2m`,经 `exec.CommandContext` 执行,挂死即杀并上报 `failed`,master 择机重试。
- **SLI 指标**: `nut_master_shutdown_ack_latency_seconds`、`nut_master_ups_poll_duration_seconds`(Histogram)与 `nut_master_ups_last_success_timestamp_seconds` / `nut_master_ups_last_error_timestamp_seconds`(gauge)。

### Changed
- **状态持久化移出热锁**: 落盘 fsync 移出 `commandMu`,锁内只取带 `persistSeq` 的快照,解锁后在 `persistMu` 下写盘并防旧快照覆盖新快照;master / slave 同源。
- **关机 fan-out 并发化**: `triggerShutdown` 改 per-target goroutine 并发,慢 / 半开 slave 不再拖累其余目标。
- **优雅关闭排空在途连接**: master `Run` 退出时关闭在途连接并等所有 handler 收尾,连接 handler 的状态写不再落在 `Run` 返回之后。

### Fixed
- **`WorkingDirectory` 缺目录致启动失败**: nfpm 包现在创建 `/usr/local/lib/nut-server`,修掉用推荐路径安装后 `systemctl start` 报 `status=200/CHDIR` 的真缺陷(v0.4.0 及更早受影响)。
- **`clone()` 漏拷 `ReplayDisabled`**: 状态快照 / `Status` 复制统一走 `clone()`,补回漏拷字段;关机状态分类收敛到单一 `status_class.go` 谓词表。
- **ack 计数定序**: `nut_master_shutdown_acks_total` 在新状态对 `Status()` 可见前自增,消除"已 executed 但计数未动"的中间态。

### Tests / CI
- CI 单测 + e2e 全量 `-race`,新增 `FuzzReadEnvelope` fuzz smoke 步骤。
- 新增 `verifyPeerIdentity` / `matchCertIdentity` 单测 + 真实 mTLS 握手测试、协议层 `ReadEnvelope` 单测与 fuzz、`normalizeLoadedLocalShutdown` 恢复路径测试。
- 抽出 `protocol.WriteEnvelope`、slave `ackMessage` 工厂、master `writeJSONError` / `buildTargetNodeSet`,SNMP OID 改命名常量。

### Docs
- 新增 `docs/tls.md`(openssl 签发 CA + 证书 + `bind_node_id_to_cert`)与 `docs/troubleshooting.md`(按症状的排障 runbook);示例配置补超时字段说明。

[完整发布说明](.github/release-notes/v0.5.0.md)

## [0.4.0] - 2026-06-01

健壮性 / 安全硬化版本,来自一轮架构师视角的逐行 review。**无破坏性配置改动**,但默认值收紧、占位 token 改为 fail-closed。

### Security
- **协议层无界读 DoS**: master / slave 共用 `protocol.ReadEnvelope`,单条 envelope 上限 1 MiB,认证前发超长行不再能 OOM 进程。
- **ack 身份绑定**: `shutdown_ack` 的 `node_id` 强制改写为连接已认证的 `NodeID`,认证 slave 无法替别的节点伪造关机状态。
- **占位 secret fail-closed**: `auth_tokens` / `admin_token` / slave `token` 留空、纯空白或仍是示例占位串时拒绝启动。
- **可选 mTLS 证书绑定** `tls.bind_node_id_to_cert` (默认 false): 开启后 `node_id` 必须匹配客户端证书 CN / SAN,挡住合法证书 + token 冒充任意节点。
- **`tls.disabled` 与证书字段互斥**、`atomicfile` 目录收到 `0700` 并 rename 后 fsync 父目录、随包附带钉死 argv 的 `/etc/sudoers.d/nut-server-master`。

### Fixed
- 市电恢复 (`!OnBattery`) 后清掉 auto-shutdown latch 与已完成的 activeCommand,latch 不再永久置位污染下次评估。
- 本机自关机丢弃竞态:live watcher 与 reload 两条路径统一守卫 (`!ok || ReplayDisabled`)。
- `all: true` 关机命令现在收编重连 / 新上线节点并 replay,不再漏关。
- replay 只在 `executed` 终态跳过,`failed` / `timeout` 继续重试。
- SNMP `NoSuchObject` / `NoSuchInstance` / `EndOfMibView` 当作错误,不再被读成 0 触发误关机。
- slave 半开连接检测 (`readIdleTimeout` = 3× ping),注册成功后才复位退避;紧急关机阈值仅在续航读数 `>= 0` 时比较。
- admin 手动关机部分下发失败返回 `207 Multi-Status`;`/install/slave` 脚本 URL 改用 `raw.githubusercontent.com` 修掉 404。

[完整发布说明](.github/release-notes/v0.4.0.md)

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

[0.5.0]: https://github.com/riofutab/nut-server/releases/tag/v0.5.0
[0.4.0]: https://github.com/riofutab/nut-server/releases/tag/v0.4.0
[0.3.0]: https://github.com/riofutab/nut-server/releases/tag/v0.3.0
[0.2.1]: https://github.com/riofutab/nut-server/releases/tag/v0.2.1
[0.2.0]: https://github.com/riofutab/nut-server/releases/tag/v0.2.0
[0.1.4]: https://github.com/riofutab/nut-server/releases/tag/v0.1.4

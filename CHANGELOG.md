# Changelog

本项目所有重要变更都集中在这里。格式参考 [Keep a Changelog](https://keepachangelog.com/),版本号遵循 [Semantic Versioning](https://semver.org/)。

每个版本一段精简摘要,完整 release notes 见 `.github/release-notes/vX.Y.Z.md`,GitHub Release 也会贴一份。

## [Unreleased]

计划发布为 v0.6.0,预发布版 v0.6.0-rc.1 供实机验证。所有破坏性配置变更集中在这一版,并包含下方 0.5.2 的全部修复(0.5.2 未单独发布)。**master 配置必须改写;先升 master 再升 slave。**逐字段对照见 [docs/upgrade.md](docs/upgrade.md)。

### Added
- **per-node token**:slave 首次用 `enrollment_tokens` 注册后,master 签发专属 token,只保存其 SHA-256;此后该 node_id 只能用专属 token 注册,共享 token 不能再冒用已注册节点。`enrollment: closed` 拒绝新节点;`DELETE /nodes/{node_id}/token` 吊销。`/status` 新增 `enrolled`。
- **策略防抖** `duration`:条件连续满足这么久才触发。轮询失败不清零也不触发。
- **UPS 失联兜底**:策略条件 `when.ups_unreachable_for`(断电期间 master 重启后一直读不到 UPS 时,从启动时刻计时);`local_shutdown.unreachable_after`(缺省等于 `max_wait`)在断电期间 UPS 失联时按紧急处理关 master,触发原因 `ups_unreachable`。
- **HTTPS admin** `admin_tls`(最低 TLS 1.2);admin 绑非回环地址又没配 TLS 时启动打 WARN。
- **SNMP profile**(`ups-mib` 默认 / `apc` / `cyberpower` / `eaton`)与单项覆盖,runtime 支持 minutes / seconds / timeticks;**支持 SNMPv3**(USM)。厂商 profile 尚未经真机验证。
- `install_ca_file`:装机脚本内嵌的 CA,设为签发 master 证书的 CA 后,master 换证书不用改 slave;未设置时沿用 `tls.cert_file`。
- **`-check-config`**:nut-master / nut-slave 只校验配置和关机权限后退出;安装与升级脚本在启动前调用,不通过则中止且保留旧版本。

### Changed
- **装机接口**改为 `POST /nodes/{node_id}/install-script`:为节点签发专属 token;token 与 CA 经 heredoc 写入临时文件传递,不出现在命令行;master 开 TLS 时内嵌 CA 与 server name;脚本固定到 release tag,开发版返回 503。
- `install-slave.sh` / `quick-install-slave.sh` 的 `--token` 改为 `--token-file`。
- 配置校验收紧:`snmp.runtime` 覆盖时必须写 `unit`;`snmp.timeout_seconds` 取 1..60(不写或 0 为 2);v1/v2c 的 `community` 必填;显式写 `charge_below: 0` / `runtime_below: 0` / `ups_unreachable_for: 0s` 被拒,`charge_below` 取 1..100;`poll_interval` / `command_timeout` / `offline_after` 为负数被拒;slave `shutdown_command` 显式写空列表被拒。
- SNMP 电量或续航读数为负数、或 runtime OID 返回 TimeTicks 而 `unit` 不是 `timeticks` 时,本次轮询失败。
- `-check-config` 同时加载 TLS 证书和私钥(master `tls`、`admin_tls`,slave `tls`),dry-run 下也加载。
- `install-slave.sh` 重跑时重新生成 `slave.yaml`(旧文件备份为 `slave.yaml.bak-<UTC 时间>`,只保留 `dry_run`);新二进制和配置先通过 `-check-config` 才替换;拒绝符号链接配置;服务在运行时重启。
- 装机接口对格式不对的 `master_addr` 返回 400,解析到回环地址时返回 503;支持 `[IPv6]:port`。
- `POST /commands/reset` 不再结束当前断电 episode:它停止重放当前命令和本次断电的命令,取消尚未开始执行的本机关机;已触发的策略在市电恢复前不会再触发,未触发的策略仍会触发,`local_shutdown` 的紧急条件仍会关 master。
- `install-master.sh` / `quick-install-master.sh` 的 `--token` 改为 `--token-file`,删除 `--admin-token`(`admin_token` 由脚本生成),token 不再出现在命令行和 sudo 日志里。
- `install-master.sh` 先用新二进制在临时位置跑 `-check-config`,通过后才替换已安装的二进制;服务在运行时重启到新版本。结束时给出的 slave 安装步骤改为在 master 上生成脚本、拷到 slave 执行。
- `upgrade-master.sh` / `upgrade-slave.sh` 在 `.deb` / `.rpm` 安装的机器上拒绝执行,避免 `/etc/systemd/system` 下的 unit 覆盖包里的 unit。
- CI 对所有 shell 脚本跑 `dash -n`、`bash -n` 和 `shellcheck -S warning`;packaging-smoke 增加从 v0.6.0 之前的 `.deb` 升级时打印手动启动命令的检查。
- token 比较先做 SHA-256 再逐个恒定时间比较,不再泄露长度;`admin_token` 同样处理。
- master `local_shutdown.command` 与 slave `shutdown_command` 的默认值改为 `["/usr/bin/sudo", "-n", "/sbin/shutdown", "-h", "now"]`,与随包 sudoers 一致;原默认值不经 sudo,以 `nut-server` 用户运行时必然失败。
- slave state 文件改为结构化格式(含签发的 token),旧文件的已处理命令记录丢弃一次。
- 指标 `nut_master_register_attempts_total` 新增 `result="enrolled"`。
- 取消预期节点改为 `DELETE /nodes/{node_id}/expected`(原 `DELETE /nodes/expect/{node_id}`),避免与 `DELETE /nodes/{node_id}/token` 路径冲突。
- 状态页新增「Master shutdown」区块:显示本机关机阶段、触发原因(含 `ups_unreachable`)、截止时间、失败原因与关机命令自检结果。
- 签发的 token 只在 slave 回复 `token_stored` 后生效,旧版 slave 不受影响,继续用 enrollment token 注册。

### Fixed
- 手动 dry-run 全部节点的演练完成后,残留的本机关机状态会挡住之后真实断电时的 master 关机;现在新的全部节点命令和紧急条件都会替换其他命令留下、尚未执行的本机关机状态,手动命令的 `dry_run` 不再带到紧急关机上。
- `POST /commands/reset` 清除任何尚未执行的本机关机,失败后反复重试的也一并清除。
- 断电期间 master 重启后、首次读到 UPS 之前重连的 slave,在读到 UPS 后由重试流程纳入本次命令并下发关机。
- 重启前已触发的 `ups_unreachable_for` 策略在 UPS 持续失联时继续确认恢复的 episode,不再一直等 UPS 读数。
- 市电恢复时取消本次断电内由紧急条件建立的本机关机(包括失败重试中的),即使之后 episode 已有命令。
- `emergency_runtime_minutes` 只在电池供电的读数上生效。
- master 在断电中真实关闭自身后再开机时,读到 UPS 之前不启用 UPS 失联兜底(策略与本机关机),避免 UPS 网卡启动慢时在市电下再次关机或向 slave 重放关机。
- 重试后又停在 `accepted` 的节点会再次超时并继续重试。
- 注册通过鉴权后、加入在线列表前若该节点 token 被轮换或吊销,连接被拒绝,不再以旧 token 留在线上。
- `.deb` / `.rpm` 升级时不再停止并 disable 服务;postinst 在配置通过 `-check-config` 后重启正在运行的服务,不通过则保留旧进程并告警。
- 从 v0.5.x 的 `.rpm` 升级后,旧包 `%preun` 停掉并 disable 的服务会被恢复:`%pre` 记录 enable / 运行状态,`%posttrans` 在 `-check-config` 通过后恢复。从 v0.5.x 的 `.deb` 升级时服务保持停止,postinst 打印要手动执行的 `-check-config` 与 `systemctl enable --now` 命令。只在真正重启后才打印 "restarted"。
- master / slave 的 state 文件无法解析时改名为 `<state_file>.corrupt-<UTC 时间>`、记错误日志并以空状态启动;master 会丢失节点 token 绑定。
- slave 无法写入签发的 token 时保持连接并继续用配置 token,只记错误日志。
- 关机自检只接受 sudo 的 `-n` / `-u` / `-g` / `--`;非 root 时不经 sudo 直接调用 `shutdown` / `poweroff` / `halt` / `reboot` / `systemctl` 判为未就绪。
- `install_ca_file` 只能包含 `CERTIFICATE` PEM 块,装机脚本只内嵌证书。
- `install-master.sh` 拒绝符号链接形式的 `master.yaml`,不再以 root 改写、chown、chmod 链接目标。
- `install-online.sh` 的错误信息改输出到 stderr;`/releases/latest` 请求失败(例如只有预发布版)时提示传 `--version`。
- 文档:quick start 固定 `--version v0.6.0-rc.1`(正式版前 `latest` 指向 v0.5.1);装机脚本只在 master 上用 `admin_token` 生成;`local_shutdown` 示例的 `emergency_runtime_minutes` 改为低于策略阈值并写明 sudo 关机命令;去掉已移除的 `RestrictAddressFamilies`;Go 徽章改为 1.26。

### Removed
- `auth_tokens`(改名 `enrollment_tokens`)、旧格式 `shutdown_policy`、`when.on_battery`、`snmp.output_source_oid` / `charge_oid` / `runtime_minutes_oid`、`GET /install/slave`。配置改为严格解析,未知字段启动报错。

## [0.5.2] - 未单独发布,随 v0.6.0 发布

修复一批会导致漏关、误关或关机失败的问题。**升级顺序:先 master 后 slave**;配置校验有收紧,详见 [docs/upgrade.md](docs/upgrade.md)。

### Fixed
- **按随包部署时真实关机必然失败**:systemd unit 显式或隐式开启 NoNewPrivileges,使 sudo 无法提权;sudoers 带参数条目末尾的 `""` 让规则永远匹配不上。两处都已修正,tar.gz 升级脚本现在也会替换 sudoers。
- **多策略不能逐级触发**:原先一次断电只执行第一条命中的策略。现引入断电周期(episode):后命中的策略并入同一条命令(同一个 command_id),目标只增不减。
- **旧配置 `require_on_battery: false` 语义反转**:原先被当成「必须市电供电」;两种策略都不写时,master 启动后会在市电下关掉所有 slave。现在等同不限制,且没有任何条件时启动报错。
- **任何命令都会触发 master 本机关机**:现在只有目标为全部节点的命令才会;命令带 `dry_run` 时本机关机只模拟,不再只看配置文件。
- **没有在线 slave 时 master 不关自己**:自动触发现在照常建命令,到 `max_wait` 或紧急续航时关 master;紧急续航在断电期间独立生效,不依赖任何命令。
- **失败节点不重试**:断电期间每 30s 对 `failed` / `timeout` 节点重发;重试成功的 `executed` 能覆盖之前的 `failed`。本机关机失败也每 30s 重试。
- **按 tag / node_id 下发的命令不收编重连节点**:匹配目标的节点重连后会被纳入并重放。
- **命令 ID 可能重复**:原先是持久化计数器,state 文件丢失后从 1 重来,slave 会把新命令当成历史命令只回 ack。现在 ID 带时间戳和随机后缀。
- **SNMP 异常值被当作 0**:检查 PDU 错误状态,只接受整数类型的值。
- **市电下反复关机**:策略只在 UPS 走电池时评估,来电后电池充电期间不再关掉重新开机的节点。
- master 重启后,恢复的断电周期要等第一次 UPS 读数后才重放或执行本机关机,避免来电开机后又被关掉。
- 市电恢复会取消尚未开始的本机关机。
- slave 拨号与 TLS 握手加 10s 超时;master 遇到临时的 Accept 错误退避重试,不再退出进程。

### Changed
- dry-run 下 slave 回报新状态 `simulated`,不再回 `executed`;命令摘要新增 `simulated` 计数。
- slave 注册时报告 `dry_run` 与关机权限自检结果 `shutdown_ready`,`/status` 与状态页显示。
- master 与 slave 启动时自检关机命令能否执行(sudo 命令用 `sudo -n -l`),结果写日志、`nut_slave_shutdown_ready` 指标与 `/status` 的 `local_shutdown.command_ready`;失败不退出进程。
- `/status` 新增 `episode`(断电开始时间、已触发策略);`POST /commands/reset` 同时结束当前断电周期。
- 断电期间策略触发会取代仍在进行的手动命令。
- 配置校验收紧:`when.on_battery: false`、`snmp.version: "3"`、master 端未开 `require_client_cert` 却设了 `ca_file` / `bind_node_id_to_cert`、没有任何策略条件,均启动报错。
- slave 已处理命令的记录上限 200 条。
- 安装与升级把 `/etc/nut-server` 设为 `root:nut-server` 0750、配置 0640,服务进程不能改写自己的 token。
- admin HTTP 加 `ReadHeaderTimeout` 与 64 KiB 请求体上限;`timeout_seconds` 上限 24h。
- Go 1.26;CI action 固定到 commit SHA,release 前跑完整测试,nfpm 下载校验 SHA256;新增在真实 systemd 上验证 unit + sudoers 的打包冒烟测试。

## [0.5.1] - 2026-07-08

小版本,内部优化 + 一处真实的无界增长修复。**无破坏性改动,无 config schema 变化**。

### Fixed
- **`s.commands` 历史命令无界增长**: 完成态命令超过 200 条后按完成时间裁剪最旧的,跳过当前 `activeCommand` 和 `local_shutdown` 正在等待的那条,长期运行的 master 状态文件与 `/status` 响应不再随历史命令数量无限增长。

### Changed
- **SNMP 轮询合并为单次批量 GET**: `output_source` / `charge` / `runtime` 三个 OID 从 3 次往返合并成 1 次,每个 `poll_interval` 周期少 2/3 网络开销。
- **状态文件落盘去掉多余的 pretty-print 缩进**: master / slave 都从 `MarshalIndent` 换成 `Marshal`,纯机器读写的崩溃恢复文件不需要人类可读格式。

### Removed
- **slave 端两个纯转发的空壳包装函数**(`readEnvelope` / `decodePayload`)清理,调用点直接走 `protocol.ReadEnvelope` / `protocol.DecodePayload`。

[完整发布说明](.github/release-notes/v0.5.1.md)

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

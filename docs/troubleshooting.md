# 故障排查 (Troubleshooting)

按现象组织。每条给出**快速定位命令**和**常见根因**。诊断时优先看三处:

- `journalctl -u nut-master -f` / `journalctl -u nut-slave -f` — 结构化 JSON 日志
- `curl -s -H "Authorization: Bearer $ADMIN_TOKEN" http://127.0.0.1:9001/status | jq` — 节点目录、活动命令、UPS 视图、本机关机阶段(配置了 `admin_tls` 时下面的 URL 都改成 `https://`)
- `curl -s http://127.0.0.1:9001/metrics` — Prometheus 指标(绑回环,不鉴权)
- `curl -s http://127.0.0.1:9001/healthz` / `/readyz` — 存活 / 就绪探针(免鉴权)

---

## 服务装完包却起不来

**现象**:`systemctl start nut-master` 失败,`systemctl status` 显示 `status=200/CHDIR` 或 `code=exited`。

```bash
systemctl status nut-master
journalctl -u nut-master -n 50 --no-pager
sudo -u nut-server /usr/local/bin/nut-master -check-config -config /etc/nut-server/master.yaml
```

`-check-config` 只校验配置和关机权限,通过输出 `config ok: <路径>`,失败输出 `config check failed: <路径>: <错误>` 并以 1 退出。要用 `nut-server` 用户跑,root 下 sudo 检查总能通过。slave 同理换成 `nut-slave` 和 `slave.yaml`。

常见根因:
- **配置仍是占位值**:`enrollment_tokens` / `admin_token` / slave `token` 留空或还是 `replace-with-strong-token` → master/slave **故意拒绝启动**(fail-closed)。日志会指出具体字段。改成强随机值:`openssl rand -hex 24`。
- **v0.6.0 删除的旧字段**:报 `line N: <字段> was removed in v0.6; ... (see docs/upgrade.md)`。涉及 `auth_tokens`、`shutdown_policy`、`when.on_battery`、`snmp.output_source_oid` / `charge_oid` / `runtime_minutes_oid`,按 [upgrade.md](upgrade.md) 的对照表改写。
- **未知字段**:配置按严格模式解析,拼错的字段报 `line N: field <名字> not found in type ...`。对照 `configs/*.example.yaml` 改正。
- **没有关机策略**:`shutdown_policies must define at least one policy`。v0.6.0 起至少要写一条。
- **策略组合不合法**:`when.ups_unreachable_for` 不能与 `charge_below` / `runtime_below` 或 `duration` 写在同一条策略里,要单独成一条。
- **`admin_tls` 只写了一项**:`admin_tls.cert_file and admin_tls.key_file must be set together`。
- **SNMPv3 配置不完整或多余**:v3 下不能写 `community`;当前 `security_level` 用不到的 `auth_*` / `priv_*` 也不能写;passphrase 至少 8 个字符。错误信息会指出字段。
- **安装 / 升级脚本中止**:脚本在启动服务前以 `nut-server` 用户跑 `-check-config`,不通过就停下。升级脚本此时保留旧二进制、不重启。按输出的错误改配置后重跑。
- **`WorkingDirectory` 缺失**(v0.4.0 及更早的包):`/usr/local/lib/nut-server` 不存在。v0.5.0 起包会创建它;旧包可手动 `sudo install -d -m0755 /usr/local/lib/nut-server`。
- **`state_file` 路径只读**:systemd `ProtectSystem=strict` 下只有 `/var/lib/nut-server` 可写。确认 `state_file: "/var/lib/nut-server/master-state.json"`。

---

## slave 连不上 master

```bash
journalctl -u nut-slave -f          # 看 dial / register 错误
curl -s .../status | jq '.nodes'    # master 视角:该 node 是否出现
curl -s .../metrics | grep nut_slave_connect_attempts_total
```

常见根因:
- **`register rejected: enrollment closed`**:master 设了 `enrollment: closed`,不接受新的 `node_id`。要加新节点,临时把 `enrollment` 改成 `open` 并重启 master,或者用 `POST /nodes/<node_id>/install-script` 生成装机脚本(脚本直接带专属 token,不受 `closed` 影响)。
- **`register rejected: invalid token`**,按情况区分:
  - slave 还没注册过:配置里的 `token` 不在 master 的 `enrollment_tokens` 里。
  - 该 `node_id` 已有专属 token,而这台 slave 用 enrollment token 注册:可能是另一台机器用了同一个 `node_id`,也可能是这台 slave 的 `state_file` 被删了、丢了存好的专属 token。确认是同一台机器后,在 master 上 `DELETE /nodes/<node_id>/token` 吊销旧 token,slave 会用 enrollment token 重新注册(需 `enrollment: open`)。
  - slave 存的专属 token 被吊销,或 master 的 state 文件丢失:slave 会打 `master rejected the issued token; falling back to the config token`,清掉存的 token,下次重连用配置里的 `token` 重新注册,`enrollment: open` 时自动恢复。
  - 用装机脚本安装的节点被吊销后:它配置里的 `token` 就是被吊销的专属 token,没有可回退的 enrollment token,会一直被拒。在 master 上重新生成装机脚本并在该节点执行。
  - 另外,用装机脚本重新生成 token 会让该节点旧的 token 失效并断开连接,直到新脚本执行完。
- **slave 日志 `store issued token: ...`**:slave 无法把 master 签发的 token 写进 `state_file`,会断开重试。检查 `state_file` 路径是否可写(随包配置为 `/var/lib/nut-server/slave-state.json`)。
- **状态页节点标 `not enrolled`**:节点已连接,但 master 还没记下它的专属 token。正常情况下 slave 回复 `token_stored` 后标记会消失。一直存在时:slave 还是 v0.6.0 之前的版本(不会回复,继续用 enrollment token,升级即可),或看 master 日志 `discard issued token`。
- **地址/端口不通**:`dial tcp ... connection refused`。检查 `master_addr`、防火墙、master 的 `listen_addr`。
- **TLS 配置不对称**:一端开 TLS 另一端没开 → 握手失败。见下方 *TLS / mTLS 握手失败*。
- **node_id 与证书不匹配**(开启了 `tls.bind_node_id_to_cert`):master 日志 `client cert identity mismatch`。slave 证书的 CN 或 DNS SAN 必须等于其 `node_id`。
- **半开连接**:master 主机假死(无 RST)。slave 在 `3 × ping` 间隔内收不到 pong 会自动重连;`readyz` 会转为 503。

---

## TLS / mTLS 握手失败

```bash
journalctl -u nut-slave -f | grep -i tls
openssl s_client -connect master.internal:9000 -servername master.internal   # 看证书链
```

常见根因(按出现频率):
- **`server_name` 与证书 SAN 不一致**(头号坑):slave 的 `tls.server_name` 必须匹配 master 证书的某个 DNS SAN。报错形如 `x509: certificate is valid for X, not Y`。
- **CA 不被信任**:slave 的 `tls.ca_file` 必须是签发 master 证书的 CA。报错 `x509: certificate signed by unknown authority`。
- **mTLS 时 master 收不到客户端证书**:master 设了 `require_client_cert: true` 但 slave 没配 `cert_file`/`key_file` → 握手被拒。
- **生产误用 `insecure_skip_verify: true`**:能连上但**没有真正校验**,等于裸奔。生产务必关掉并配 `ca_file` + `server_name`。

证书签发步骤见 [tls.md](tls.md)。

---

## 状态页返回 401

`/status` 等管理接口需要 Bearer token。

```bash
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" http://127.0.0.1:9001/status
```

- 缺 `Authorization: Bearer ...` 头 → `401 missing bearer token`。
- 配置了 `admin_tls` 后仍用 `http://` 访问 → `400 Client sent an HTTP request to an HTTPS server.`,改用 `https://`。
- token 不等于 master 的 `admin_token` → `401 invalid token`。
- 错误响应现在是 JSON 信封 `{"error": "..."}`(v0.5.0+)。
- 只想探活不想带 token:用 `/healthz`(恒 200)或 `/readyz`(就绪才 200)。

---

## SNMP 一直 error / UPS 读不到数据

```bash
curl -s .../status | jq '.ups'                       # last_error / last_error_at
curl -s .../metrics | grep -E 'nut_master_ups_poll_total|ups_last_'
snmpget -v2c -c public <ups-host> .1.3.6.1.2.1.33.1.2.4.0   # 手动验证 OID
```

常见根因:
- **目标/community/版本不对**:`snmp.target` / `snmp.community` / `snmp.version`。SNMPv3 还要核对 `username`、`security_level` 和 auth / priv 的协议与 passphrase 是否与 UPS 网卡一致。
- **OID 不被该 UPS 支持**:返回 `NoSuchObject` / `NoSuchInstance` 会被当作**错误**(不再被误读成 0,避免误触发关机)。先试 `snmp.profile`(`ups-mib` 默认,另有 `apc` / `cyberpower` / `eaton`,厂商预设未经真机验证);仍不对时用 `snmpwalk` 找你的 UPS 实际支持的 OID,用 `snmp.on_battery: {oid, values}`、`snmp.charge: {oid}`、`snmp.runtime: {oid, unit}` 逐项覆盖。
- **电池状态或续航读数不对**:`on_battery.values` 要写该 OID 表示「走电池」的取值(UPS-MIB 是 5,APC / CyberPower 是 3);`runtime.unit` 要与 OID 的单位一致(`minutes` / `seconds` / `timeticks`,TimeTicks 为 1/100 秒),读数统一换算成分钟并向下取整。
- **数据陈旧**:`nut_master_ups_last_success_timestamp_seconds` 长时间不更新 → 轮询卡住或目标不可达。建议对 `time() - ups_last_success_timestamp_seconds > 3 * poll_interval` 告警。

---

## 关机没有按预期触发

```bash
curl -s .../status | jq '{ups, active_command, shutdown_issued}'
curl -s .../metrics | grep -E 'shutdowns_issued|shutdown_acks'
```

常见根因:
- **策略阈值没命中**:核对 `shutdown_policies` 的 `charge_below` / `runtime_below` 与 `/status` 里的 UPS 实际读数。策略只在 UPS 走电池时评估,市电下不会触发。
- **该策略本次断电已触发过**:`/status` 的 `episode.fired_policies` 列出本次断电已触发的策略,每条策略在一次断电内只触发一次。市电恢复后 episode 结束;`POST /commands/reset` 也会结束当前 episode。
- **防抖未到时**:策略配了 `duration` 时,条件要连续满足这么久才触发。轮询失败不清零也不触发,触发需要一次新的命中读数。master 重启或新的一次断电都会重新计时。
- **dry_run 开着**:`dry_run: true` 时只模拟不真正关机,节点回报 `simulated`。`/status` 的节点带 `dry_run: true` 的是 slave 本地开着 dry-run。
- **关机权限不通**:节点 `shutdown_ready: false`,或日志有 `shutdown command self-check failed`。按日志里的错误检查 sudoers 与 systemd unit(见下文 local_shutdown 一节)。
- **没有匹配的 target**:手动关机返回 `400 no target nodes matched`。

---

## 关机命令卡在 executing

```bash
curl -s .../status | jq '.active_command.last_node_updates'
journalctl -u nut-slave | grep -i shutdown
```

常见根因:
- **自定义关机脚本 hang**:v0.5.0 起 slave/master 的关机命令受 `shutdown_command_timeout`(默认 2m)约束,超时会上报 `failed` 并由 master 重试。若脚本本就需要更久,调大该值。
- **节点掉线未回执**:master 在 `command_timeout`(默认 30s)后标 `timeout`;节点重连后会被 replay(除非已 `executed`)。断电期间 master 每 30s 对 `failed` / `timeout` 的节点重发一次。

---

## local_shutdown 卡阶段

master 本机自关机(关完所有远端再关自己):

```bash
curl -s .../status | jq '.local_shutdown'   # phase / trigger / last_error
```

- `waiting_remote`:仍在等远端 `executed`/`failed`。
- `wait_expired`:超过 `max_wait` 兜底,准备关 master。
- `emergency`:提前关本机。`trigger` 为 `emergency_runtime` 表示 UPS 续航低于 `emergency_runtime_minutes`;为 `ups_unreachable` 表示断电期间 UPS 连续 `local_shutdown.unreachable_after`(默认等于 `max_wait`)没有应答。后者先查 SNMP 连通性(见上文)。市电下 UPS 失联不会触发。
- `failed` + `last_error`:多半是 sudo 没放行。master 每 30s 重试一次。确认随包的 `/etc/sudoers.d/nut-server-master` 存在且 `visudo -c` 通过,命令为 `sudo -n /sbin/shutdown -h now`,并且 unit 是 v0.5.2+ 的版本(旧 unit 会隐式开启 NoNewPrivileges,sudo 无法提权)。`/status` 的 `local_shutdown.command_ready` 是启动时的自检结果。
- 一直 `idle`:只有目标为全部节点的命令才会进入等待流程;只针对部分 tag / node_id 的命令不会关 master。断电期间续航低于 `emergency_runtime_minutes` 或 UPS 失联超过 `unreachable_after` 时无论如何都会关 master。

---

## 升级后行为变化

- v0.4.0+:占位 token 会**拒绝启动** —— 升级前先换成强随机值。
- v0.5.0+:新增 `/healthz`、`/readyz`、`shutdown_command_timeout`、若干 SLI 指标(`shutdown_ack_latency_seconds`、`ups_poll_duration_seconds`、`ups_last_*_timestamp_seconds`)。全部向后兼容,留空走默认。
- v0.6.0+:配置严格解析并删除旧字段,master 配置必须改写;slave 改用专属 token;`GET /install/slave` 改为 `POST /nodes/<node_id>/install-script`;安装脚本的 `--token` 改为 `--token-file`。master 与 slave 升级不分先后;未升级的 slave 继续用 enrollment token 注册,全部升级并显示 `enrolled: true` 后再把 `enrollment` 改成 `closed`。

升级流程见 [upgrade.md](upgrade.md)。

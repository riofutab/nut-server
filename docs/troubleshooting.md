# 故障排查 (Troubleshooting)

按现象组织。每条给出**快速定位命令**和**常见根因**。诊断时优先看三处:

- `journalctl -u nut-master -f` / `journalctl -u nut-slave -f` — 结构化 JSON 日志
- `curl -s -H "Authorization: Bearer $ADMIN_TOKEN" http://127.0.0.1:9001/status | jq` — 节点目录、活动命令、UPS 视图、本机关机阶段
- `curl -s http://127.0.0.1:9001/metrics` — Prometheus 指标(绑回环,不鉴权)
- `curl -s http://127.0.0.1:9001/healthz` / `/readyz` — 存活 / 就绪探针(免鉴权)

---

## 服务装完包却起不来

**现象**:`systemctl start nut-master` 失败,`systemctl status` 显示 `status=200/CHDIR` 或 `code=exited`。

```bash
systemctl status nut-master
journalctl -u nut-master -n 50 --no-pager
```

常见根因:
- **配置仍是占位值**:`auth_tokens` / `admin_token` / slave `token` 留空或还是 `replace-with-strong-token` → master/slave **故意拒绝启动**(fail-closed)。日志会指出具体字段。改成强随机值:`openssl rand -hex 24`。
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
- **token 不匹配**:slave 日志 `register rejected: invalid token`。slave 的 `token` 必须在 master 的 `auth_tokens` 列表里。
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
- **目标/community/版本不对**:`snmp.target` / `snmp.community` / `snmp.version`。
- **OID 不被该 UPS 支持**:返回 `NoSuchObject` / `NoSuchInstance` 会被当作**错误**(不再被误读成 0,避免误触发关机)。用 `snmpwalk` 找你的 UPS 实际支持的 OID,覆盖 `output_source_oid` / `charge_oid` / `runtime_minutes_oid`。
- **数据陈旧**:`nut_master_ups_last_success_timestamp_seconds` 长时间不更新 → 轮询卡住或目标不可达。建议对 `time() - ups_last_success_timestamp_seconds > 3 * poll_interval` 告警。

---

## 关机没有按预期触发

```bash
curl -s .../status | jq '{ups, active_command, shutdown_issued}'
curl -s .../metrics | grep -E 'shutdowns_issued|shutdown_acks'
```

常见根因:
- **策略阈值没命中**:核对 `shutdown_policy` / `shutdown_policies` 的 `on_battery` / `charge_below` / `runtime_below` 与 `/status` 里的 UPS 实际读数。
- **闩锁未复位**:一次掉电后 `auto_shutdown_latched` 置位,需市电恢复(`on_battery=false`)才会清。可 `POST /commands/reset` 手动清。
- **dry_run 开着**:`dry_run: true` 时只模拟不真正关机。日志有 `dry-run` 字样。
- **没有匹配的 target**:手动关机返回 `400 no target nodes matched`。

---

## 关机命令卡在 executing

```bash
curl -s .../status | jq '.active_command.last_node_updates'
journalctl -u nut-slave | grep -i shutdown
```

常见根因:
- **自定义关机脚本 hang**:v0.5.0 起 slave/master 的关机命令受 `shutdown_command_timeout`(默认 2m)约束,超时会上报 `failed` 并由 master 重试。若脚本本就需要更久,调大该值。
- **节点掉线未回执**:master 在 `command_timeout`(默认 30s)后标 `timeout`;节点重连后会被 replay(除非已 `executed`)。

---

## local_shutdown 卡阶段

master 本机自关机(关完所有远端再关自己):

```bash
curl -s .../status | jq '.local_shutdown'   # phase / trigger / last_error
```

- `waiting_remote`:仍在等远端 `executed`/`failed`。
- `wait_expired`:超过 `max_wait` 兜底,准备关 master。
- `emergency`:UPS 续航低于 `emergency_runtime_minutes`,提前关。
- `failed` + `last_error`:多半是 sudo 没放行。确认随包的 `/etc/sudoers.d/nut-server-master` 存在且 `visudo -c` 通过,命令为 `sudo -n /sbin/shutdown -h now`。

---

## 升级后行为变化

- v0.4.0+:占位 token 会**拒绝启动** —— 升级前先换成强随机值。
- v0.5.0+:新增 `/healthz`、`/readyz`、`shutdown_command_timeout`、若干 SLI 指标(`shutdown_ack_latency_seconds`、`ups_poll_duration_seconds`、`ups_last_*_timestamp_seconds`)。全部向后兼容,留空走默认。

升级流程见 [upgrade.md](upgrade.md)。

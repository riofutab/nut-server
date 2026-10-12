# nut-server 升级包

这个 tar.gz 用于已经装过 nut-server 的机器，升级二进制、systemd unit 和 sudoers，不动配置内容和状态。

## 替换的内容

- `/usr/local/bin/nut-master`、`/usr/local/bin/nut-slave`
- `/etc/systemd/system/nut-master.service`、`/etc/systemd/system/nut-slave.service`
- `/etc/sudoers.d/nut-server-master`、`/etc/sudoers.d/nut-server-slave`（v0.5.2+，先 `visudo -c` 校验再替换）
- 升级脚本本身

升级脚本还会把 `/etc/nut-server` 收紧为 `root:nut-server` 0750、配置文件 `root:nut-server` 0640，`/var/lib/nut-server` 归 `nut-server`。

## 保留的内容

- `/etc/nut-server/` 下所有配置（master.yaml / slave.yaml）
- `/var/lib/nut-server/` 下所有状态文件

## 用法

```bash
sudo ./scripts/upgrade-master.sh
sudo ./scripts/upgrade-slave.sh
```

如果服务原本在跑，升级脚本会在替换文件后 `systemctl restart` 重启，不需要额外操作。

## v0.5.2 → v0.6.0 升级注意

v0.6.0 集中做了所有破坏性配置变更。master 的配置必须改写，slave 的配置不用改。

### 升级顺序

master 和 slave 谁先升都可以，前提是两边都已经是 v0.5.2（v0.5.2 的 slave 在 dry-run 下回报 `simulated`，更早的 master 不认识）。

- 新 slave 连旧 master：照常用配置里的 `token` 注册。
- 旧 slave 连新 master：master 在 `register_ack` 里签发的 token 只有在 slave 回复 `token_stored` 后才生效。旧 slave 不会回复，所以它继续用 enrollment token 注册，`/status` 里显示为未注册（`enrolled: false`）。
- 所有 slave 都升级并显示 `enrolled: true` 之后，才可以把 `enrollment` 改成 `closed`，否则未升级的 slave 会被拒（`enrollment closed`）。

### master 配置逐字段对照

配置改为严格解析，写了未知字段会启动失败。下表里删除的字段会报 `<字段> was removed in v0.6; ...`。

| v0.5.x | v0.6.0 |
|---|---|
| `auth_tokens: [...]` | `enrollment_tokens: [...]`，值不变即可。新增 `enrollment: open`（默认） |
| `shutdown_policy:`<br>`  require_on_battery: true`<br>`  min_battery_charge: 30`<br>`  min_runtime_minutes: 15`<br>`  shutdown_reason: "..."` | `shutdown_policies:`<br>`  - name: default`<br>`    when: { charge_below: 30, runtime_below: 15 }`<br>`    target: { all: true }`<br>`    reason: "..."`<br>旧格式两个阈值是 AND 关系，上面的写法与之等价。阈值都为 0 的「一断电就关」写成 `when: {}` |
| `shutdown_policies[].when.on_battery: true` | 删掉。策略只在 UPS 走电池时评估，`when` 为空就表示一断电就触发 |
| `snmp.output_source_oid` / `charge_oid` / `runtime_minutes_oid` | 用默认值的直接删掉（默认 `profile: ups-mib` 就是这三个 OID）。改过的写成覆盖项：`snmp.on_battery: { oid: ..., values: [5] }`、`snmp.charge: { oid: ... }`、`snmp.runtime: { oid: ..., unit: minutes }` |

新增的可选配置：策略 `duration`（防抖）、`when.ups_unreachable_for`（断电期间 UPS 失联）、`local_shutdown.unreachable_after`（缺省等于 `max_wait`）、`admin_tls`、`snmp.profile` 与 SNMPv3。完整示例见 `configs/master.example.yaml`。

改完先校验，必须以服务用户运行，root 下 sudo 权限检查总是通过：

```bash
sudo -u nut-server /usr/local/bin/nut-master -check-config -config /etc/nut-server/master.yaml
```

输出 `config ok: <路径>` 才算通过。

### 升级方式的差别

- **tar.gz 升级脚本**：先装 sudoers，再把新二进制放到临时位置，以 `nut-server` 用户（`runuser`，Debian/RHEL 系自带）跑 `-check-config`。不通过就删掉临时文件、保留旧二进制、不重启，并以退出码 1 结束。所以要先改好配置再跑升级脚本。
- **`.deb` / `.rpm`**：v0.6.0 起，升级包时服务保持 enable；如果服务在运行，postinst 以 `nut-server` 用户跑 `-check-config`，通过就重启到新版本，不通过就不重启并打印警告，旧进程继续运行（但重启机器后会起不来）。所以同样要先改好配置再装包。
  - 例外：从 v0.5.2 的包升级时，执行的是 v0.5.2 包里的旧 prerm，它仍会停止并 disable 服务。这一次装完新包、改好配置、`-check-config` 通过后，需要手动执行 `sudo systemctl enable --now nut-master`（slave 同理）。

### per-node token 带来的行为变化

- 现有 slave 升级后下一次连接会自动拿到专属 token，存在自己的 `state_file` 里，不需要改 slave 配置。全部节点都注册过以后，可以把 master 的 `enrollment` 改成 `closed`。
- 删除 master 的 state 文件会丢掉所有节点的 token。`enrollment: open` 时 slave 会自动重新注册；`closed` 时需要先临时改回 `open`。
- 用 `POST /nodes/<node_id>/install-script` 生成的装机脚本会给该节点签发新 token，并断开它当前的连接；旧的 `GET /install/slave` 已删除。通过脚本安装的节点配置里只有专属 token，被吊销后要重新生成装机脚本。
- `install-slave.sh` / `quick-install-slave.sh` 的 `--token` 改为 `--token-file <路径>`。
- slave 的 state 文件格式改变，旧文件里的已处理命令记录会被丢弃一次。请在市电正常时升级。
- 取消预期节点的接口从 `DELETE /nodes/expect/<node_id>` 改为 `DELETE /nodes/<node_id>/expected`，调用旧路径的脚本需要修改。
- 指标 `nut_master_register_attempts_total` 新增 `result="enrolled"`，表示本次注册签发了专属 token。

## v0.5.1 → v0.5.2 升级注意

v0.5.2 修复了「按随包 unit + sudoers 部署时真实关机必然失败」等问题，**强烈建议升级**。

1. **先升 master，再升 slave。** v0.5.2 的 slave 在 dry-run 下回报新状态 `simulated`，旧 master 不认识，会把节点记为 timeout。
2. **sudoers 和 unit 必须一起更新。** 旧 sudoers 条目末尾的 `""` 让规则永远匹配不上，旧 unit 隐式开启 NoNewPrivileges 让 sudo 失效。`.deb`/`.rpm` 和升级脚本都会替换；手工部署的请对照 `packaging/` 下的新文件更新。升级后看日志：

   ```bash
   journalctl -u nut-slave | grep 'shutdown command self-check'
   ```

   `passed` 表示关机权限正常；`failed` 会带上原因，`/status` 里该节点也会显示 `shutdown_ready: false`。
3. **配置校验收紧**，以下配置会启动失败并给出原因：
   - `shutdown_policy` 与 `shutdown_policies` 都没写出任何条件；
   - 任何策略写了 `when.on_battery: false`（策略现在只在 UPS 走电池时评估，这个条件没有意义）；
   - `snmp.version: "3"`（尚未支持，v0.6 加入）；
   - master 端设了 `tls.ca_file` 或 `tls.bind_node_id_to_cert`，但没开 `tls.require_client_cert`。
4. **自动关机语义变化**（详见 CHANGELOG）：一次断电内后命中的策略并入同一条命令；只有目标为全部节点的命令才会触发 master 本机关机；市电恢复会取消尚未开始的本机关机。
5. state 文件里旧的 `command_seq`、`auto_shutdown_latched` 字段会被忽略，无需手工处理。

## v0.1.x → v0.2.0 升级注意

v0.2.0 引入了几个破坏性改动，升级前请确认：

1. master 的 `/etc/nut-server/master.yaml` 必须有 `admin_token`。升级脚本只换二进制，不补这个字段——v0.1.x 的配置里没有，启动会立即失败。补一行：

   ```yaml
   admin_token: "用 openssl rand -hex 24 生成的随机串"
   ```

2. slave 改成 `nut-server` 用户运行。升级前在 slave 机器上：

   ```bash
   sudo useradd --system --home-dir /var/lib/nut-server --shell /usr/sbin/nologin nut-server || true
   sudo chown -R nut-server:nut-server /etc/nut-server /var/lib/nut-server
   sudo cp sudoers/nut-server-slave /etc/sudoers.d/nut-server-slave
   sudo chmod 0440 /etc/sudoers.d/nut-server-slave
   sudo visudo -c -f /etc/sudoers.d/nut-server-slave
   ```

   并把 `/etc/nut-server/slave.yaml` 里的 `shutdown_command` 改成 `["/usr/bin/sudo", "-n", "/sbin/shutdown", "-h", "now"]`。

如果不想现在迁到 sudoers 路径，可以暂时把 `nut-slave.service` 的 `User=` 改回 `root`，但不推荐。

详见 [.github/release-notes/v0.2.0.md](.github/release-notes/v0.2.0.md)。

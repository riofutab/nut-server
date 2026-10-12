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

# nut-server 升级包

这个 tar.gz 用于已经装过 nut-server 的机器，只升级二进制和 systemd unit，不动配置和状态。

## 替换的内容

- `/usr/local/bin/nut-master`、`/usr/local/bin/nut-slave`
- `/etc/systemd/system/nut-master.service`、`/etc/systemd/system/nut-slave.service`
- 升级脚本本身

## 保留的内容

- `/etc/nut-server/` 下所有配置（master.yaml / slave.yaml）
- `/var/lib/nut-server/` 下所有状态文件
- `/etc/sudoers.d/nut-server-slave`（v0.2.0+，存在则不覆盖）

## 用法

```bash
sudo ./scripts/upgrade-master.sh
sudo ./scripts/upgrade-slave.sh
```

如果服务原本在跑，升级脚本会在替换文件后 `systemctl restart` 重启，不需要额外操作。

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

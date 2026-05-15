# nut-slave 安装包

这个 tar.gz 仅包含 slave 端需要的文件，适合只跑 slave 的机器。

如果你的发行版有 `apt` / `dnf` / `yum`，更推荐直接安装对应的 `.deb` 或 `.rpm`（见仓库根 README 的 systemd 部署一节）。

## 包内文件

- `bin/nut-slave`
- `configs/slave.example.yaml`
- `systemd/nut-slave.service`
- `sudoers/nut-server-slave`（受限 `shutdown` 授权，v0.2.0+）
- `scripts/install-slave.sh`、`quick-install-slave.sh`
- `scripts/install-online.sh`
- `scripts/upgrade-common.sh`、`upgrade-slave.sh`

## 服务用户与 sudoers

v0.2.0 起 slave 不再以 root 运行，systemd unit 用 `User=nut-server`。安装脚本会：

1. 自动创建系统用户 `nut-server`
2. 把 `sudoers/nut-server-slave` 装到 `/etc/sudoers.d/nut-server-slave`（visudo 校验），授权该用户在不输密码的情况下调用几条精确的 `shutdown` 命令
3. 把 `shutdown_command` 写成 `["/usr/bin/sudo", "-n", "/sbin/shutdown", "-h", "now"]`

如果你的发行版 `shutdown` 路径不同（比如 `/usr/sbin/shutdown`），改 `/etc/nut-server/slave.yaml` 的 `shutdown_command` 即可，sudoers 模板已经覆盖常见路径。

## 离线安装

```bash
sudo ./scripts/quick-install-slave.sh --node-id slave-01 --master-addr 10.0.0.10:9000 --token your-token
```

启用 TLS 时追加 `--tls-ca-file` / `--tls-server-name`；客户端证书再加 `--tls-cert-file --tls-key-file`。内网无需 TLS 可用 `--disable-tls`。

## 在线安装

```bash
sudo ./scripts/install-online.sh --role slave --version latest -- \
  --node-id slave-01 --master-addr 10.0.0.10:9000 --token your-token
```

不传 `--` 后面的参数时，`install-online.sh` 会优先尝试 `.deb` / `.rpm`；传了 role-script 参数则走 tar.gz 路径，方便预填配置。

## 升级（不动配置和状态）

```bash
sudo ./scripts/upgrade-slave.sh
```

只替换二进制和 systemd unit；`/etc/nut-server/slave.yaml` 和 `/var/lib/nut-server/` 都不会动。

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

v0.5.2 起 slave（非 dry-run）启动时会用 `sudo -n -l` 检查关机命令能否执行，结果写进日志（`shutdown command self-check passed` / `failed`）、指标 `nut_slave_shutdown_ready`，并在注册时报告给 master。检查失败不会退出进程。systemd unit 刻意没有使用 `NoNewPrivileges`、`LockPersonality`、`RestrictAddressFamilies` 等选项，它们会隐式禁止 sudo 提权。

## 推荐：用 master 生成的装机脚本

在 master 上执行 `POST /nodes/<node_id>/install-script` 得到一段脚本，拷到这台机器上执行即可。脚本里带有 master 为该节点签发的专属 token，master 开了 TLS 时还带有 CA 和 server name。用法见 [master.md](master.md#一键安装-slave-post-nodesnode_idinstall-script)。

## 离线安装

token 通过文件传入，不出现在命令行参数里。先把 master 的 enrollment token 写进只有 root 可读的文件：

```bash
sudo install -m 0600 /dev/null /root/nut-enroll-token
printf '%s\n' 'your-enrollment-token' | sudo tee /root/nut-enroll-token >/dev/null
sudo ./scripts/quick-install-slave.sh --node-id slave-01 --master-addr 10.0.0.10:9000 --token-file /root/nut-enroll-token
```

v0.6.0 起安装脚本不再接受 `--token`。`/etc/nut-server/slave.yaml` 已存在时，脚本只替换其中的 `token`。启动服务前脚本会以 `nut-server` 用户运行 `nut-slave -check-config`，不通过就中止。

启用 TLS 时追加 `--tls-ca-file` / `--tls-server-name`（CA 文件会被复制到 `/etc/nut-server/ca.crt`）；客户端证书再加 `--tls-cert-file --tls-key-file`。内网无需 TLS 可用 `--disable-tls`。

## 在线安装

```bash
sudo ./scripts/install-online.sh --role slave --version latest -- \
  --node-id slave-01 --master-addr 10.0.0.10:9000 --token-file /root/nut-enroll-token
```

不传 `--` 后面的参数时，`install-online.sh` 会优先尝试 `.deb` / `.rpm`；传了 role-script 参数则走 tar.gz 路径，方便预填配置。

## token 的生命周期

1. **首次注册**：slave 用配置里的 `token`（master 的 enrollment token）注册。master 接受后在 `register_ack` 里返回为这个 `node_id` 签发的专属 token。
2. **保存**：slave 先把专属 token 写进 `state_file`（0600），再继续通信；写不进去就断开重连，不会使用一个可能丢失的 token。所以 `state_file` 必须可写（随包配置是 `/var/lib/nut-server/slave-state.json`）。
3. **之后的注册**：`state_file` 里有专属 token 时优先用它，配置里的 `token` 不再使用。此后 enrollment token 不能再以这个 `node_id` 注册。
4. **回退**：如果 master 以 `invalid token` 拒绝了存储的专属 token（被吊销，或 master 的 state 文件丢失），slave 清掉它，下次重连改用配置里的 `token` 重新注册。master 的 `enrollment` 为 `closed` 时这一步会被拒（`enrollment closed`），需要先在 master 上改回 `open`。

用装机脚本安装的节点，配置里的 `token` 本身就是专属 token，没有 enrollment token。这样的节点被吊销后无法自己恢复，要在 master 上重新生成装机脚本并执行。

## 校验配置

```bash
sudo -u nut-server /usr/local/bin/nut-slave -check-config -config /etc/nut-server/slave.yaml
```

只加载并校验配置；非 dry-run 时还会检查关机命令的 sudo 权限。通过时输出 `config ok: <路径>`、退出码 0；失败时向 stderr 输出 `config check failed: <路径>: <错误>`、退出码 1。要用 `nut-server` 用户运行，用 root 运行时 sudo 检查总能通过。配置按严格模式解析，写了未知字段会失败。

## Prometheus /metrics(可选)

在 `/etc/nut-server/slave.yaml` 里把 `metrics_listen_addr` 设成回环地址即可启用,留空则不暴露:

```yaml
metrics_listen_addr: "127.0.0.1:9101"
```

`/metrics` 不鉴权,靠绑定回环保护。主要指标:`nut_slave_connect_attempts_total{result}`、`nut_slave_connected`、`nut_slave_shutdowns_received_total`、`nut_slave_shutdown_status_total{status}`。

## 升级（不动配置和状态）

```bash
sudo ./scripts/upgrade-slave.sh
```

只替换二进制和 systemd unit；`/etc/nut-server/slave.yaml` 和 `/var/lib/nut-server/` 都不会动。替换前会用新二进制以 `nut-server` 用户跑 `-check-config`，不通过就保留旧版本、不重启。升级到 v0.6.0 的顺序和注意事项见 [upgrade.md](upgrade.md)。

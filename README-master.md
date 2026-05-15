# nut-master 安装包

这个 tar.gz 仅包含 master 端需要的文件，适合只跑 master 的机器。

如果你的发行版有 `apt` / `dnf` / `yum`，更推荐直接安装对应的 `.deb` 或 `.rpm`（见仓库根 README 的 systemd 部署一节）。

## 包内文件

- `bin/nut-master`
- `configs/master.example.yaml`
- `systemd/nut-master.service`
- `scripts/install-master.sh`、`quick-install-master.sh`
- `scripts/install-online.sh`
- `scripts/upgrade-common.sh`、`upgrade-master.sh`

## 离线安装

```bash
sudo ./scripts/quick-install-master.sh --token your-token --snmp-target 10.0.0.31
```

`install-master.sh` 会创建 `nut-server` 用户、生成 `/etc/nut-server/master.yaml`（首次安装时自动写入随机 `admin_token`）、安装 systemd unit 并配置好沙箱。

启用 TLS 时追加 `--tls-cert-file` / `--tls-key-file`；mTLS 再加 `--tls-ca-file --tls-require-client-cert`。内网无需 TLS 可用 `--disable-tls`。

## 在线安装（从已发布的 release 下载）

```bash
sudo ./scripts/install-online.sh --role master --version latest -- \
  --token your-token --admin-token "$(openssl rand -hex 24)" --snmp-target 10.0.0.31
```

不传 `--` 后面的参数时，`install-online.sh` 会优先尝试 `.deb` / `.rpm`；传了 role-script 参数则走 tar.gz 路径，方便预填配置。

## 升级（不动配置和状态）

```bash
sudo ./scripts/upgrade-master.sh
```

只替换二进制和 systemd unit；`/etc/nut-server/master.yaml` 和 `/var/lib/nut-server/` 都不会动。

## master 本机自关机

如果 master 主机也要在最后关机，在 `/etc/nut-server/master.yaml` 启用：

```yaml
local_shutdown:
  enabled: true
  max_wait: "15m"
  emergency_runtime_minutes: 15
```

master 会等远端节点完成（或超时、或紧急阈值触发）后关本机，不需要在 master 上再装一个 slave。`/status` 的 `local_shutdown` 字段会暴露当前阶段。

## 管理接口

默认监听 `127.0.0.1:9001`，所有请求都需要 `Authorization: Bearer <admin_token>`：

```bash
TOKEN=$(sudo awk '/^admin_token:/ {print $2}' /etc/nut-server/master.yaml | tr -d '"')
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:9001/status
```

浏览器打开 `http://127.0.0.1:9001/` 输入 `admin_token` 即可看到只读状态页。

## Prometheus /metrics

同一个 admin 监听口下还暴露 `/metrics`（**不鉴权**,靠 `admin_listen_addr` 绑回环保护)。Prometheus scrape 示例:

```yaml
scrape_configs:
  - job_name: nut-master
    static_configs:
      - targets: ["127.0.0.1:9001"]
```

主要指标:`nut_master_ups_poll_total{result}`、`nut_master_ups_on_battery`、`nut_master_ups_battery_charge_percent`、`nut_master_ups_runtime_minutes`、`nut_master_registered_slaves`、`nut_master_nodes{state}`、`nut_master_shutdowns_issued_total`、`nut_master_shutdown_acks_total{status}`、`nut_master_shutdown_active`、`nut_master_local_shutdown_phase{phase}`、`nut_master_register_attempts_total{result}`。

## 一键安装 slave: GET /install/slave

admin 接口 `GET /install/slave?node_id=<id>` 返回一段可直接 `bash` 执行的 shell。脚本会拉取与 master 同版本的 `install-online.sh`,自动填好 `--master-addr`(优先 `public_addr`,否则取请求 Host)、`--token`(`auth_tokens[0]`)、`--node-id`,并把 `<id>` 预先写入预期注册表(`expected`)。

```bash
TOKEN=$(sudo awk '/^admin_token:/ {print $2}' /etc/nut-server/master.yaml | tr -d '"')
curl -fsSL -H "Authorization: Bearer $TOKEN" \
  "http://127.0.0.1:9001/install/slave?node_id=db-01" \
  | sudo bash
```

注意事项:
- `node_id` 只接受字母数字 `-_.`,长度 ≤128;非法字符直接 `400`。
- 同一个 `auth_tokens[0]` 会被烤进脚本,所以适合内网受信场景;混合多套 slave 信任域时建议每台用独立 token。
- 没配 `public_addr` 时,如果通过反向代理 / 端口转发访问,Host header 可能不指向真实 master_addr — 建议显式配 `public_addr`。

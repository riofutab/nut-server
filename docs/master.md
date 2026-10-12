# nut-master 安装包

这个 tar.gz 仅包含 master 端需要的文件，适合只跑 master 的机器。

如果你的发行版有 `apt` / `dnf` / `yum`，更推荐直接安装对应的 `.deb` 或 `.rpm`（见仓库根 README 的 systemd 部署一节）。

## 包内文件

- `bin/nut-master`
- `configs/master.example.yaml`
- `systemd/nut-master.service`
- `sudoers/nut-server-master`（`local_shutdown` 用的受限 `shutdown` 授权）
- `scripts/install-master.sh`、`quick-install-master.sh`
- `scripts/install-online.sh`
- `scripts/upgrade-common.sh`、`upgrade-master.sh`

## 离线安装

```bash
sudo install -m 0600 /dev/null /root/nut-enroll-token
openssl rand -hex 24 | sudo tee /root/nut-enroll-token   # 记下来，装 slave 时要用
sudo ./scripts/quick-install-master.sh --token-file /root/nut-enroll-token --snmp-target 10.0.0.31
```

`--token-file` 指向存放 enrollment token 的文件（只需 root 可读），token 写入 `enrollment_tokens`，slave 首次注册时使用；token 不出现在命令行参数里。`admin_token` 由脚本自动生成，不能从命令行传入。`install-master.sh` 会创建 `nut-server` 用户、生成 `/etc/nut-server/master.yaml`（首次安装时自动写入随机 `admin_token`）、安装 systemd unit 并配置好沙箱。`master.yaml` 是符号链接时脚本拒绝执行。新二进制先放到临时位置，以 `nut-server` 用户运行 `-check-config`，通过后才替换已安装的二进制，不通过就中止、保留原二进制；服务已在运行时会重启到新版本。

启用 TLS 时追加 `--tls-cert-file` / `--tls-key-file`；mTLS 再加 `--tls-ca-file --tls-require-client-cert`。内网无需 TLS 可用 `--disable-tls`。

## 在线安装（从已发布的 release 下载）

```bash
sudo ./scripts/install-online.sh --role master --version v0.6.0-rc.1 -- \
  --token-file /root/nut-enroll-token --snmp-target 10.0.0.31
```

v0.6.0 正式版发布前，`--version latest` 会装到 v0.5.1，它的安装脚本与本文档不一致，所以这里固定使用 `--version v0.6.0-rc.1`。

不传 `--` 后面的参数时，`install-online.sh` 会优先尝试 `.deb` / `.rpm`；传了 role-script 参数则走 tar.gz 路径，方便预填配置。

## 升级（不动配置和状态）

```bash
sudo ./scripts/upgrade-master.sh
```

只替换二进制、systemd unit 和 sudoers（`/etc/sudoers.d/nut-server-master`）；`/etc/nut-server/master.yaml` 和 `/var/lib/nut-server/` 都不会动。替换前会用新二进制以 `nut-server` 用户跑 `-check-config`，不通过就保留旧版本、不重启。用 `.deb` / `.rpm` 安装的机器要用 apt / dnf / yum 升级，升级脚本会拒绝执行。从 v0.5.x 升到 v0.6.0 要先改配置，见 [upgrade.md](upgrade.md)。

## 校验配置

```bash
sudo -u nut-server /usr/local/bin/nut-master -check-config -config /etc/nut-server/master.yaml
```

只加载并校验配置；`local_shutdown.enabled` 且非 dry-run 时还会检查关机命令的 sudo 权限。通过时输出 `config ok: <路径>`、退出码 0；失败时向 stderr 输出 `config check failed: <路径>: <错误>`、退出码 1。要用 `nut-server` 用户运行，用 root 运行时 sudo 检查总能通过。

配置按严格模式解析，写了未知字段会失败；v0.6.0 删除的旧字段（`auth_tokens`、旧格式的单条关机策略、`when.on_battery`、`snmp` 下的三个旧 OID 字段）会报 `... was removed in v0.6; ...`。

## master 本机自关机

如果 master 主机也要在最后关机，在 `/etc/nut-server/master.yaml` 启用：

```yaml
local_shutdown:
  enabled: true
  command: ["/usr/bin/sudo", "-n", "/sbin/shutdown", "-h", "now"]
  max_wait: "15m"
  # 应低于各策略的 runtime_below，否则一进入等待远端阶段就会立即关本机
  emergency_runtime_minutes: 3
```

master 会等远端节点完成（或超时、或紧急阈值触发）后关本机，不需要在 master 上再装一个 slave。断电期间 UPS 连续 `local_shutdown.unreachable_after`（默认等于 `max_wait`）没有应答时，master 也按紧急情况关本机，`/status` 里的 trigger 为 `ups_unreachable`。master 在断电期间重启、之后一直读不到 UPS 时，从 master 启动时刻开始计时；但如果 master 是在这次断电中自己真实关机后再开机的，读到 UPS 之前不启用失联兜底，因为这种开机通常是市电已恢复，UPS 网卡还在启动。`/status` 的 `local_shutdown` 字段会暴露当前阶段。

只有目标为全部节点的命令会启动这个流程；只关部分 tag / node_id 的命令不会关 master。断电期间续航低于 `emergency_runtime_minutes` 或 UPS 失联超过 `unreachable_after` 时，即使还没有任何策略触发，master 也会关本机。命令带 `dry_run` 时本机关机同样只模拟。市电恢复会取消尚未开始的本机关机；执行失败每 30s 重试一次。

## 管理接口

默认监听 `127.0.0.1:9001`（明文 HTTP；配置了 `admin_tls` 后为 HTTPS，见下文），所有请求都需要 `Authorization: Bearer <admin_token>`：

```bash
TOKEN=$(sudo awk '/^admin_token:/ {print $2}' /etc/nut-server/master.yaml | tr -d '"')
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:9001/status
```

浏览器打开 `http://127.0.0.1:9001/` 输入 `admin_token` 即可看到只读状态页。

## HTTPS 管理接口（admin_tls）

`admin_listen_addr` 要绑到非回环地址时，应同时配置 `admin_tls`，否则 `admin_token` 以明文传输，master 启动时会打 WARN：

```yaml
admin_listen_addr: "10.0.0.10:9001"
admin_tls:
  cert_file: "/etc/nut-server/tls/admin.crt"
  key_file: "/etc/nut-server/tls/admin.key"
```

- 两项必须同时设置或同时留空，只设一项会启动失败。
- 最低 TLS 1.2。证书只在启动时加载，换证书后要重启 master。
- 设置后状态页、管理 API 和 `/metrics` 都改走 `https://`，Prometheus 的 scrape 配置要加 `scheme: https`。
- `admin_tls` 与 `tls` 相互独立：`tls` 只管 slave 控制连接（`listen_addr`）。

## Prometheus /metrics

同一个 admin 监听口下还暴露 `/metrics`（**不鉴权**,靠 `admin_listen_addr` 绑回环保护)。Prometheus scrape 示例:

```yaml
scrape_configs:
  - job_name: nut-master
    static_configs:
      - targets: ["127.0.0.1:9001"]
```

主要指标:`nut_master_ups_poll_total{result}`、`nut_master_ups_on_battery`、`nut_master_ups_battery_charge_percent`、`nut_master_ups_runtime_minutes`、`nut_master_registered_slaves`、`nut_master_nodes{state}`、`nut_master_shutdowns_issued_total`、`nut_master_shutdown_acks_total{status}`、`nut_master_shutdown_active`、`nut_master_local_shutdown_phase{phase}`、`nut_master_register_attempts_total{result}`（`result` 取 `accepted` / `enrolled` / `rejected` / `invalid`，`enrolled` 表示本次注册签发了专属 token）。

## 节点注册与专属 token

- master 的 state 文件无法解析时改名为 `<state_file>.corrupt-<UTC 时间>`、记错误日志并以空状态启动,所有节点的 token 绑定丢失。除非手动恢复该文件,节点要重新注册(用装机脚本安装的节点要重新生成脚本)。
- slave 第一次注册时使用 `enrollment_tokens` 里的任一 token。注册成功后，master 在 `register_ack` 里给该 `node_id` 签发一个专属 token（64 位十六进制），state 文件（`node_tokens`）里只保存它的 SHA-256。slave 把 token 存进自己的 `state_file` 后回复 `token_stored`，master 收到这条消息才记下 token；之后该节点都用它注册。v0.6.0 之前的 slave 不会回复，会继续用 enrollment token 注册。
- 一个 `node_id` 有了专属 token 之后，enrollment token 就不能再以这个 `node_id` 注册，会被拒绝（`invalid token`）。
- `enrollment: open`（默认）允许新 `node_id` 注册。所有节点都注册过以后建议改成 `closed`，此时新 `node_id` 会被拒（`enrollment closed`），泄漏的 enrollment token 也就没用了；`closed` 时 `enrollment_tokens` 可以留空。改动需要重启 master。
- token 校验时两边都先做 SHA-256，再对所有候选做恒定时间比较。
- `/status` 的 `nodes[].enrolled` 表示该节点是否已有专属 token。状态页里已连接、但还没有专属 token 的节点标 `not enrolled`。

吊销某个节点的 token：

```bash
curl -H "Authorization: Bearer $TOKEN" -X DELETE http://127.0.0.1:9001/nodes/db-01/token
```

成功返回 `204`，并断开该节点当前的连接；该节点没有 token 时返回 `404`。之后该节点要重新注册：

- 手动安装的 slave（配置里是 enrollment token）：被拒后自动清掉本地存的 token，改用配置里的 enrollment token 重新注册，前提是 `enrollment: open`。
- 用装机脚本安装的 slave（配置里就是被吊销的专属 token）：无法自动恢复，需要重新生成并执行装机脚本。

`DELETE /nodes/{node_id}` 删除从未上线的节点时也会一并删除它的 token。

删除 master 的 state 文件会丢掉所有节点的 token。`enrollment: open` 时手动安装的 slave 会自动重新注册；`closed` 时要先临时改回 `open`，等节点都重新注册后再改回去。

## 一键安装 slave: POST /nodes/{node_id}/install-script

admin 接口 `POST /nodes/{node_id}/install-script` 返回一段 bash 脚本。v0.6.0 删除了旧的 `GET /install/slave`。

```bash
TOKEN=$(sudo awk '/^admin_token:/ {print $2}' /etc/nut-server/master.yaml | tr -d '"')
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:9001/nodes/db-01/install-script -o install-db-01.sh
```

把 `install-db-01.sh` 拷到 db-01 上，用有 sudo 权限的用户执行，执行完删掉：

```bash
bash install-db-01.sh && rm install-db-01.sh
```

`admin_token` 只在 master 上使用，不要带到 slave 上：在 slave 上用它请求 admin 接口，token 会出现在 slave 的命令行参数和 shell 历史里。slave 上只执行拷过去的脚本文件。

每次请求时 master 会：

- 把 `node_id` 标记为预期节点（`expected`）；
- 给该节点签发新的专属 token，替换旧 token，并断开该节点当前的连接（旧 token 已失效）；
- 生成脚本：脚本把 token 用 heredoc 写到权限 0600 的临时文件，以 `--token-file` 传给安装脚本，token 不出现在命令行参数里；
- master 的 `tls` 开启时，把 `install_ca_file`（未配置时用 `tls.cert_file`）的内容内嵌进脚本作为 CA（`--tls-ca-file`，安装后在 `/etc/nut-server/ca.crt`；建议配置为签发 master 证书的 CA，换证书时 slave 不用改，见 [tls.md](tls.md)），并用脚本里 `master_addr` 的主机名（配了 `public_addr` 时即其主机名）作为 `--tls-server-name`，所以 master 证书的 SAN 要包含这个主机名；只有 master 没开 TLS 时脚本才带 `--disable-tls`。

脚本下载 `install-online.sh` 时固定使用 master 自己的 release tag，安装的 slave 与 master 同版本。没有 release tag 的开发构建会返回 `503`。

注意事项:
- `node_id` 只接受字母数字 `-_.`,长度 ≤128;非法字符直接 `400`。
- 脚本里含有该节点的专属 token,不要留存或转发。脚本内部用 `sudo bash` 调用安装脚本,执行用户需要有 sudo 权限。
- 用脚本安装的节点,配置里的 `token` 就是专属 token,没有 enrollment token。token 被吊销后要重新生成脚本。
- 目标机上已有 `/etc/nut-server/slave.yaml` 时,安装脚本重新生成它,旧文件备份为 `slave.yaml.bak-<UTC 时间>`,只保留 `dry_run`,`tags` 等其它改动要手动合并回去。新二进制和新配置先通过 `-check-config` 才替换,不通过什么都不变;服务在运行时会重启;配置是符号链接时拒绝执行。见 [slave.md](slave.md)。
- `master_addr` 格式不对时返回 `400`,解析到回环地址时返回 `503`(配 `public_addr`)。支持 `[IPv6]:port` 写法。
- 脚本里的 `master_addr` 优先取 `public_addr`,没配时取请求的 Host header 加 `listen_addr` 端口。在 master 本机请求 `127.0.0.1` 时一定要配 `public_addr`,否则 `master_addr` 解析到回环地址,接口返回 `503`。经反向代理 / 端口转发访问时同理。

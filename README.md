# nut-server

一个基于 Go 的轻量 NUT 风格主从程序。

`nut-master` 通过 SNMP 读取 UPS 状态；`nut-slave` 主动注册到 master；当满足关机策略时，master 向 slave 下发关机指令。当前版本已支持 `dry_run`，可先验证链路而不真正关机。

当前默认按 UPS-MIB 方式读取：
- `output_source_oid` 判断是否切到 battery
- `charge_oid` 读取电池电量百分比
- `runtime_minutes_oid` 读取剩余运行时间（分钟）

仓库已包含：示例配置、systemd unit、安装脚本、Linux amd64/arm64 构建脚本、tar.gz 发布打包脚本，以及 GitHub Actions 构建 / tag 发布工作流。

## 项目结构

```text
cmd/
  nut-master/          master 入口
  nut-slave/           slave 入口
internal/
  config/              配置加载
  master/              master 核心逻辑
  slave/               slave 核心逻辑
  protocol/            TCP JSON 协议消息
  security/            token 校验
configs/
  *.example.yaml       对外发布的示例配置
config/
  *.yaml               本地开发配置
packaging/systemd/
  *.service            systemd unit 文件
scripts/
  build-linux.sh       linux amd64/arm64 构建脚本
  install-master.sh    master 安装脚本
  install-slave.sh     slave 安装脚本
```

## 功能

- slave 主动注册到 master
- token 鉴权
- TCP 长连接 + JSON 行协议
- SNMP 轮询 UPS 状态
- shutdown 指令下发
- shutdown 幂等处理
- dry-run 验证模式
- Linux systemd 部署支持
- Linux amd64 / arm64 构建支持

## 快速开始

### 1. 本地构建

```bash
go build ./...
```

### 2. 准备本地配置

当前仓库已包含本地开发配置：

- `config/master.yaml`
- `config/slave.yaml`

默认是 `dry_run: true`，便于先验证流程。

### 3. 启动 master

```bash
./bin/nut-master -config config/master.yaml
```

或：

```bash
go run ./cmd/nut-master -config config/master.yaml
```

### 4. 启动 slave

```bash
./bin/nut-slave -config config/slave.yaml
```

或：

```bash
go run ./cmd/nut-slave -config config/slave.yaml
```

## 配置说明

### master

示例文件：`configs/master.example.yaml`

关键字段：

- `listen_addr`: master 监听地址
- `admin_listen_addr`: 本地管理接口监听地址，提供状态查询、手动触发和 reset
- `state_file`: master 本地状态文件，保存 active command 和节点回执，便于重启恢复
- `auth_tokens`: 允许注册的 token 列表
- `poll_interval`: SNMP 轮询间隔
- `command_timeout`: shutdown 命令等待终态的最长时间，超时节点会记为 `timeout`
- `dry_run`: 是否仅验证链路而不执行真实关机
- `local_shutdown.enabled`: 是否让 master 在远端节点处理后再关闭本机
- `local_shutdown.command`: master 本机关机时执行的命令，默认 `/sbin/shutdown -h now`
- `local_shutdown.max_wait`: 等待远端节点完成的最长时间
- `local_shutdown.emergency_runtime_minutes`: 等待期间触发“最后补发一次远端关机并立即关闭本机”的紧急阈值
- `tls.enabled`: 是否启用 TLS 监听
- `tls.disabled`: 强制关闭 TLS，并忽略其它 TLS 证书字段
- `tls.cert_file`: master 证书文件路径
- `tls.key_file`: master 私钥文件路径
- `tls.ca_file`: mTLS 时用于校验客户端证书的 CA 文件
- `tls.require_client_cert`: 是否要求 slave 提供并校验客户端证书
- `shutdown_policy.require_on_battery`: 是否要求 UPS 处于电池供电
- `shutdown_policy.min_battery_charge`: 最低电量阈值
- `shutdown_policy.min_runtime_minutes`: 最低剩余运行时间阈值（单位：分钟）
- `snmp.output_source_oid`: 用于判断 UPS 当前输出来源，`5` 视为 battery
- `snmp.charge_oid`: UPS 电池电量百分比 OID
- `snmp.runtime_minutes_oid`: UPS 剩余运行时间 OID（单位：分钟）

### slave

示例文件：`configs/slave.example.yaml`

关键字段：

- `node_id`: slave 唯一标识
- `master_addr`: master 地址
- `token`: 注册 token
- `tags`: 可选节点标签，用于按标签定向下发 shutdown
- `state_file`: slave 本地状态文件，保存已处理的 command_id，防止重启后重复执行
- `reconnect_interval`: 断线重连间隔
- `dry_run`: true 时收到 shutdown 只记录日志并回 ACK
- `tls.enabled`: 是否启用 TLS 拨号
- `tls.disabled`: 强制关闭 TLS，并忽略其它 TLS 证书字段
- `tls.cert_file`: slave 客户端证书文件路径，可用于 mTLS
- `tls.key_file`: slave 客户端私钥文件路径，可用于 mTLS
- `tls.ca_file`: 用于校验 master 证书的 CA 文件
- `tls.server_name`: TLS 校验证书时使用的服务端名称
- `tls.insecure_skip_verify`: 是否跳过服务端证书校验，仅建议测试环境使用
- `shutdown_command`: 真实关机命令

## TLS / mTLS

master/slave 间的控制连接支持可选 TLS。

- 仅开启 TLS：master 配置 `tls.cert_file` / `tls.key_file`，slave 配置 `tls.ca_file`；仅在临时测试环境下才建议 `tls.insecure_skip_verify: true`
- 开启 mTLS：在 TLS 基础上，master 额外设置 `tls.require_client_cert: true` 和 `tls.ca_file`，slave 额外设置自己的 `tls.cert_file` / `tls.key_file`
- 内网不启用 TLS：可直接设置 `tls.disabled: true`；安装脚本等价参数是 `--disable-tls`，会忽略所有 `--tls-*` 参数和证书路径
- 建议生产环境始终显式配置 `tls.ca_file` 与 `tls.server_name`，不要使用 `insecure_skip_verify`

示例 master 配置：

```yaml
tls:
  enabled: true
  cert_file: "/etc/nut-server/tls/master.crt"
  key_file: "/etc/nut-server/tls/master.key"
  ca_file: "/etc/nut-server/tls/ca.crt"
  require_client_cert: true
```

示例 slave 配置：

```yaml
tls:
  enabled: true
  cert_file: "/etc/nut-server/tls/slave.crt"
  key_file: "/etc/nut-server/tls/slave.key"
  ca_file: "/etc/nut-server/tls/ca.crt"
  server_name: "nut-master.internal"
  insecure_skip_verify: false
```

## dry-run

```yaml
dry_run: true
```

效果：

- master 仍会广播 shutdown 指令
- slave 仍会回传 shutdown ACK
- slave 不会真正执行 `/sbin/shutdown -h now`

## 状态恢复

- master 和 slave 都会把 shutdown 状态写入本地 `state_file`，用于进程重启或断线后的恢复
- slave 如果在 `accepted` 或 `executing` 后断线，重连后会继续未完成的关机流程，而不是只回放旧 ACK
- master 如果先把节点记为 `timeout`，后续又收到真实最终结果 `executed` / `failed`，会把状态改正为最终结果

## 构建 Linux 发布包

执行：

```bash
./scripts/build-linux.sh
```

或：

```bash
make build-linux
```

输出目录：

- `dist/linux-amd64/`
- `dist/linux-arm64/`

每个目录下会包含：

- `nut-master`
- `nut-slave`
- `configs/`
- `packaging/systemd/`
- `scripts/`

如果要生成 tar.gz 发布包：

```bash
./scripts/package-release.sh
```

或：

```bash
make package
```

推送 `v*` 格式 tag（例如 `v0.1.0`）后，GitHub Actions 会自动构建 tar.gz 并创建 GitHub Release。

## systemd 部署

默认约定：

- 二进制：`/usr/local/bin/nut-master`、`/usr/local/bin/nut-slave`
- 配置：`/etc/nut-server/master.yaml`、`/etc/nut-server/slave.yaml`
- systemd unit：`packaging/systemd/nut-master.service`、`packaging/systemd/nut-slave.service`
- 服务用户：master 与 slave 都以 `nut-server` 用户运行。slave 通过 `/etc/sudoers.d/nut-server-slave` 受限授权 `shutdown` 命令；master 在安装时通过 systemd 沙箱（`NoNewPrivileges` 等）进一步加固。

### 方案 A：apt / yum / dnf 一键安装（推荐，v0.2.0+）

```bash
# 在线脚本会自动检测包管理器，下载已校验的 .deb 或 .rpm
curl -fsSL https://raw.githubusercontent.com/riofutab/nut-server/master/scripts/install-online.sh \
  | sudo sh -s -- --role master --version latest

curl -fsSL https://raw.githubusercontent.com/riofutab/nut-server/master/scripts/install-online.sh \
  | sudo sh -s -- --role slave --version latest
```

包安装会自动建 `nut-server` 用户、装 sudoers、chown 配置目录，并在 master 首次安装时随机生成 `admin_token` 写入 `/etc/nut-server/master.yaml`。安装完成后还需要：

```bash
# master：补齐 auth_tokens 与 snmp.target
sudo $EDITOR /etc/nut-server/master.yaml
sudo systemctl enable --now nut-master

# slave：补齐 node_id / master_addr / token
sudo $EDITOR /etc/nut-server/slave.yaml
sudo systemctl enable --now nut-slave
```

`--pkg` 可以强制安装格式：`--pkg deb` / `--pkg rpm` / `--pkg tar`（默认 `auto`）。

### 方案 B：tar.gz + 预填配置脚本

适合一次性预填 token、SNMP 目标等参数的场景：

```bash
sudo ./scripts/install-online.sh --role master --version latest -- \
  --token your-token --admin-token "$(openssl rand -hex 24)" --snmp-target 10.0.0.31

sudo ./scripts/install-online.sh --role slave --version latest -- \
  --node-id slave-01 --master-addr 10.0.0.10:9000 --token your-token
```

只要 `--` 后面带了 role-script 参数，`install-online.sh` 会自动走 tar.gz 路径让脚本预填配置。

如果你已经把发布包解压到本地，也可以直接调用：

```bash
sudo ./scripts/install-master.sh --token your-token --admin-token "$(openssl rand -hex 24)" --snmp-target 10.0.0.31
sudo ./scripts/install-slave.sh  --node-id slave-01 --master-addr 10.0.0.10:9000 --token your-token
```

启用 TLS 时追加：

```bash
# master
  --tls-cert-file /etc/nut-server/tls/master.crt \
  --tls-key-file /etc/nut-server/tls/master.key
# mTLS
  --tls-ca-file /etc/nut-server/tls/ca.crt --tls-require-client-cert

# slave
  --tls-ca-file /etc/nut-server/tls/ca.crt \
  --tls-server-name nut-master.internal
# 客户端证书
  --tls-cert-file /etc/nut-server/tls/slave.crt \
  --tls-key-file /etc/nut-server/tls/slave.key
```

内网不启用 TLS 时，命令末尾加 `--disable-tls` 即可忽略所有 `--tls-*` 参数。

### 管理接口 / 状态页

管理接口默认监听 `127.0.0.1:9001`，**所有请求都需要 `Authorization: Bearer <admin_token>`**（v0.2.0+）：

```bash
TOKEN=$(sudo awk '/^admin_token:/ {print $2}' /etc/nut-server/master.yaml | tr -d '"')

curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:9001/status
curl -H "Authorization: Bearer $TOKEN" -X POST http://127.0.0.1:9001/commands/reset
curl -H "Authorization: Bearer $TOKEN" -X POST http://127.0.0.1:9001/commands/shutdown \
  -H 'Content-Type: application/json' \
  -d '{"reason":"manual test","node_ids":["slave-01"]}'

# 登记一个预期但还没上线的节点（在 /status 中显示为 never_seen）
curl -H "Authorization: Bearer $TOKEN" -X POST http://127.0.0.1:9001/nodes/expect \
  -H 'Content-Type: application/json' \
  -d '{"node_id":"printer","hostname":"printer.lan","tags":["office"]}'
```

只读状态页：浏览器打开 `http://<master>:9001/`，输入 admin_token 即可看到 UPS、节点列表（state / 主机名 / 标签 / 最近活跃时间）、当前活动命令；每 5 秒自动刷新。token 只放在 `sessionStorage`，关闭页面即失效。状态页是只读的，不提供关机/重置按钮。

## 推到 GitHub 前建议

建议保留入库内容：

- `cmd/`
- `internal/`
- `configs/`
- `packaging/`
- `scripts/`
- `README.md`
- `.gitignore`
- `go.mod`
- `go.sum`

不要提交：

- `bin/`
- `dist/`
- `config/*.yaml`
- `.claude/`

## 后续可扩展方向

- 管理接口鉴权
- 更完整的 SNMP 厂商兼容
- 多策略关机条件
- metrics / health
- 发布 tar.gz 打包与 GitHub Release 自动化

## 开发辅助命令

已提供 `Makefile`：

```bash
make build
make build-linux
make package
make run-master
make run-slave
```

## Release Packages

Each tagged release publishes the following artifacts for both `amd64` and `arm64`:

- **Linux packages (v0.2.0+)**：
  - `nut-master_<version>_linux_<arch>.deb` / `.rpm`
  - `nut-slave_<version>_linux_<arch>.deb` / `.rpm`
- **tar.gz**（旧路径，仍然保留）：
  - `nut-server_<version>_linux_<arch>.tar.gz`
  - `nut-master_<version>_linux_<arch>.tar.gz`
  - `nut-slave_<version>_linux_<arch>.tar.gz`
  - `nut-server-upgrade_<version>_linux_<arch>.tar.gz`
- `SHA256SUMS`：覆盖以上所有产物。`install-online.sh` 下载后自动校验。

Quick install wrappers default to plain TCP for internal networks unless you pass normal `--tls-*` options:

```bash
sudo ./scripts/quick-install-master.sh --token your-token --snmp-target 10.0.0.31
sudo ./scripts/quick-install-slave.sh --node-id slave-01 --master-addr 10.0.0.10:9000 --token your-token
```

Online install detects `apt` / `dnf` / `yum` and prefers the matching `.deb` / `.rpm`. Pass `--pkg deb|rpm|tar` to force a format, or pass role-script options after `--` to force the tar path so flags like `--token`, `--snmp-target`, `--admin-token` can prefill the config:

```bash
# Auto: package manager when available, tar.gz otherwise. master needs config edit afterwards.
sudo ./scripts/install-online.sh --role master --version latest
sudo ./scripts/install-online.sh --role slave  --version latest

# Tar path with prefill
sudo ./scripts/install-online.sh --role master --version latest -- \
  --token your-token --admin-token "$(openssl rand -hex 24)" --snmp-target 10.0.0.31
sudo ./scripts/install-online.sh --role slave  --version latest -- \
  --node-id slave-01 --master-addr 10.0.0.10:9000 --token your-token

# Upgrade-only (always tar.gz — preserves config + state)
sudo ./scripts/install-online.sh --role upgrade-master --version latest
sudo ./scripts/install-online.sh --role upgrade-slave  --version latest
```

Upgrade scripts only replace binaries and systemd unit files. Existing config and state files stay in place:

```bash
sudo ./scripts/upgrade-master.sh
sudo ./scripts/upgrade-slave.sh
```

## Master Local Shutdown

If the master host itself also needs to shut down, but only after the remote nodes have had a chance to stop first, enable this in `/etc/nut-server/master.yaml`:

```yaml
local_shutdown:
  enabled: true
  max_wait: "15m"
  emergency_runtime_minutes: 15
```

After a shutdown event starts, the master will wait for remote nodes first. It will then shut down its own machine when all remote nodes finish, when `max_wait` expires, or immediately after a final rebroadcast if UPS runtime drops below the emergency threshold. You do not need to install a local slave on the master host for this.

## UPS Poll Visibility

To print successful UPS polls in the master service log, set this in `/etc/nut-server/master.yaml`:

```yaml
log_ups_status: true
```

Then restart the master and watch the journal:

```bash
sudo systemctl restart nut-master
sudo journalctl -u nut-master -f
```

The master admin endpoint also returns the latest UPS status snapshot, including the last successful values and the most recent polling error:

```bash
curl http://127.0.0.1:9001/status
```

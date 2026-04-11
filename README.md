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

已提供：

- `packaging/systemd/nut-master.service`
- `packaging/systemd/nut-slave.service`

默认约定：

- 二进制路径：`/usr/local/bin/nut-master`、`/usr/local/bin/nut-slave`
- 配置路径：`/etc/nut-server/master.yaml`、`/etc/nut-server/slave.yaml`

### 安装 master

```bash
sudo ./scripts/install-master.sh \
  --token your-token \
  --snmp-target 10.0.0.31 \
  --listen-addr :9000 \
  --snmp-community public \
  --tls-cert-file /etc/nut-server/tls/master.crt \
  --tls-key-file /etc/nut-server/tls/master.key
```

如需 mTLS，再追加：

```bash
  --tls-ca-file /etc/nut-server/tls/ca.crt \
  --tls-require-client-cert
```

安装脚本会直接生成 `/etc/nut-server/master.yaml`；如需启用 TLS，可在执行时传入 `--tls-*` 参数；如需调整 `dry_run`、阈值或 OID，再手工编辑配置。

内网不启用 TLS 时，可直接在命令末尾追加 `--disable-tls`；该开关会忽略所有 `--tls-*` 参数。

管理接口默认监听 `127.0.0.1:9001`，可用于：

```bash
curl http://127.0.0.1:9001/status
curl -X POST http://127.0.0.1:9001/commands/reset
curl -X POST http://127.0.0.1:9001/commands/shutdown \
  -H 'Content-Type: application/json' \
  -d '{"reason":"manual test","node_ids":["slave-01"]}'
curl -X POST http://127.0.0.1:9001/commands/shutdown \
  -H 'Content-Type: application/json' \
  -d '{"reason":"tag test","tags":["web"],"timeout_seconds":10}'
```

### 安装 slave

```bash
sudo ./scripts/install-slave.sh \
  --node-id slave-01 \
  --master-addr 10.0.0.10:9000 \
  --token your-token \
  --tags web,prod \
  --tls-ca-file /etc/nut-server/tls/ca.crt \
  --tls-server-name nut-master.internal
```

如需 mTLS，再追加：

```bash
  --tls-cert-file /etc/nut-server/tls/slave.crt \
  --tls-key-file /etc/nut-server/tls/slave.key
```

安装脚本会直接生成 `/etc/nut-server/slave.yaml`，因此执行后即可启动；如需调整 `dry_run`、`shutdown_command` 或 TLS 细节，再手工编辑配置。

内网不启用 TLS 时，可直接在命令末尾追加 `--disable-tls`；该开关会忽略所有 `--tls-*` 参数。

安装脚本既可以在源码根目录运行，也可以在 `dist/linux-amd64/` 或 `dist/linux-arm64/` 发布目录中运行。

安装后：

```bash
sudo systemctl enable --now nut-master
sudo systemctl enable --now nut-slave
```

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

Published releases now include these archive families for both `amd64` and `arm64`:

- `nut-server_<version>_linux_<arch>.tar.gz`
- `nut-master_<version>_linux_<arch>.tar.gz`
- `nut-slave_<version>_linux_<arch>.tar.gz`
- `nut-server-upgrade_<version>_linux_<arch>.tar.gz`

Quick install wrappers default to plain TCP for internal networks unless you pass normal `--tls-*` options:

```bash
sudo ./scripts/quick-install-master.sh --token your-token --snmp-target 10.0.0.31
sudo ./scripts/quick-install-slave.sh --node-id slave-01 --master-addr 10.0.0.10:9000 --token your-token
```

Online install can download a published release package and hand off to the role-specific install or upgrade script:

```bash
sudo ./scripts/install-online.sh --role master --version v0.1.3 -- --token your-token --snmp-target 10.0.0.31
sudo ./scripts/install-online.sh --role slave --version latest -- --node-id slave-01 --master-addr 10.0.0.10:9000 --token your-token
sudo ./scripts/install-online.sh --role upgrade-master --version latest
```

Upgrade scripts only replace binaries and systemd unit files. Existing config and state files stay in place:

```bash
sudo ./scripts/upgrade-master.sh
sudo ./scripts/upgrade-slave.sh
```

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

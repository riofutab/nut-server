# nut-server

一个基于 Go 的轻量 NUT 风格主从程序。

`nut-master` 通过 SNMP 轮询 UPS 状态；`nut-slave` 主动注册到 master；当满足关机策略时，master 向所有匹配的 slave 下发关机指令。`dry_run` 模式可以先把整条链路跑一遍而不真正关机。

UPS 读取默认按 UPS-MIB：

- `output_source_oid` 判断是否切到电池供电
- `charge_oid` 读取电池电量百分比
- `runtime_minutes_oid` 读取剩余运行时间（分钟）

仓库包含 master / slave 两个二进制、示例配置、systemd unit、安装脚本、Linux amd64/arm64 构建脚本，以及 GitHub Actions 工作流：每次 `v*` tag 都会自动生成 `.tar.gz` / `.deb` / `.rpm` 包并上传到 GitHub Release。最新版本是 [v0.2.0](https://github.com/riofutab/nut-server/releases/tag/v0.2.0)。

## 项目结构

```text
cmd/
  nut-master/          master 入口
  nut-slave/           slave 入口
internal/
  atomicfile/          状态文件原子写
  config/              YAML 配置加载
  logging/             slog 初始化
  master/              master 核心逻辑（HTTP/连接/UPS/关机/状态）
  master/web/          内嵌只读状态页（go:embed）
  slave/               slave 核心逻辑
  protocol/            TCP JSON 协议消息
  security/            token 恒定时间比较
configs/
  *.example.yaml       对外发布的示例配置
config/
  *.yaml               本地开发配置（不入库）
packaging/
  systemd/             systemd unit 文件
  sudoers/             /etc/sudoers.d/nut-server-slave 模板
  nfpm/                nfpm 配置 + postinst/prerm 脚本（生成 .deb/.rpm）
scripts/
  build-linux.sh       linux amd64/arm64 构建脚本
  package-release.sh   生成 .tar.gz / .deb / .rpm 发布产物
  install-online.sh    在线安装入口（自动选 .deb/.rpm/.tar.gz）
  install-master.sh    本地 master 安装脚本
  install-slave.sh     本地 slave 安装脚本
  upgrade-master.sh    升级 master 二进制，不改配置/状态
  upgrade-slave.sh     升级 slave 二进制，不改配置/状态
```

## 功能

- 主从协议：TCP 长连接 + 行式 JSON 消息，token 注册，重连退避带抖动，握手/空闲读超时
- SNMP 轮询 UPS：电池/电量/剩余分钟，可选 `log_ups_status` 写入 journal
- 关机编排：支持按 node_id / tag 定向下发，按 `command_timeout` 强制超时，重启后从状态文件恢复
- master 本机关机：远端节点完成后再关本机，支持 `max_wait` 兜底和 `emergency_runtime_minutes` 紧急阈值
- 节点目录（v0.2.0）：master 记录每个节点的 first_seen / last_seen / tags / hostname，可登记"预期但还没上线"的节点
- 内嵌只读状态页（v0.2.0）：`GET /` 直接看 UPS、节点表、活动命令；admin_token 仅放 sessionStorage
- 管理 API（v0.2.0）：`/status`、`/commands/shutdown`、`/commands/reset`、`/nodes/expect`、`/nodes/{id}`；全部要求 `Authorization: Bearer <admin_token>`，token 用恒定时间比较
- 优雅停机（v0.2.0）：SIGTERM/SIGINT 通过 `context.Context` 传到所有 goroutine，systemd stop 不再触发 timeout
- 结构化日志（v0.2.0）：log/slog JSON handler 输出 `command_id` / `node_id` / `peer` 等命名字段
- 部署（v0.2.0）：发布包含 `.deb` / `.rpm` / `.tar.gz`，`install-online.sh` 自动检测 apt/yum/dnf 选择安装方式
- 沙箱：master 与 slave 都以非 root `nut-server` 用户运行；slave 通过受限 sudoers 调用 shutdown
- TLS / mTLS：master 与 slave 都可选启用
- Linux amd64 / arm64 同时构建

## 快速开始

### 1. 本地构建

```bash
go build -o nut-master ./cmd/nut-master
go build -o nut-slave  ./cmd/nut-slave
```

或者直接用 `go run`，看下一步。

### 2. 准备本地配置

仓库根目录的 `config/` 下已经有可直接使用的本地开发配置：

- `config/master.yaml`
- `config/slave.yaml`

默认都是 `dry_run: true`，可以先把链路跑通而不会真的关机。`config/` 目录被 `.gitignore` 忽略，可以放心写入 token 等本地参数。

### 3. 启动 master

```bash
go run ./cmd/nut-master -config config/master.yaml
```

启动后，浏览器打开 `http://127.0.0.1:9001/`，按提示输入 `admin_token` 即可看到状态页。

### 4. 启动 slave

```bash
go run ./cmd/nut-slave -config config/slave.yaml
```

slave 注册到 master 后会出现在状态页的节点列表中，`state` 为 `online`。

## 配置说明

### master

示例文件：`configs/master.example.yaml`

关键字段：

- `listen_addr`: master 监听地址（slave 注册端口）
- `admin_listen_addr`: 管理 HTTP 监听地址，默认 `127.0.0.1:9001`，状态页、所有管理 API 和 Prometheus `/metrics` 都在这里(`/metrics` 不鉴权,靠绑回环保护)
- `admin_token`（**必填**，v0.2.0+）：管理接口的 Bearer token，可用 `openssl rand -hex 24` 生成
- `state_file`: master 本地状态文件，保存 active command、节点回执和节点目录，便于重启恢复
- `auth_tokens`: 允许 slave 注册的 token 列表（恒定时间比较）
- `poll_interval`: SNMP 轮询间隔
- `command_timeout`: shutdown 命令等待终态的最长时间，超时节点会记为 `timeout`
- `offline_after`（v0.2.0+）：节点 `last_seen` 超过该窗口后在 `/status` 中显示为 `offline`，默认 `45s`
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
- `shutdown_policies`(v0.3.0+, 可选): 多策略组合。每条 policy 内部 AND, 多条 OR。命中多条时 target 取并集 (任一 `all:true` 则关全部; 否则 tags/node_ids 并集), reason 拼接成 `a; b`。留空时按 `shutdown_policy` 自动合成一条 `default` policy。字段:
  - `name` (必填, 唯一)
  - `when.on_battery` / `when.charge_below` / `when.runtime_below` (至少一项)
  - `target.all` / `target.tags` / `target.node_ids` (全空 → 默认 `all:true`)
  - `reason` (可选, 缺省自动生成)
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
- `metrics_listen_addr`（v0.3.0+，可选）：留空则不暴露;设为回环地址(例如 `127.0.0.1:9101`)启用 `/metrics`,**不鉴权**
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
- slave 仍会回传 shutdown ACK（`status: executed`，`message: dry-run shutdown simulated`）
- slave 不会真正执行 `shutdown_command`

## 状态恢复

- master 和 slave 都会把状态原子写入本地 `state_file`（临时文件 + fsync + rename，0600 权限）
- master 持久化：`activeCommand`、所有 `commands` 的回执、节点目录（`NodeDirectory`，含 first_seen / last_seen / expected 标记）、`localShutdown` 当前阶段
- slave 持久化：已处理的 `command_id` → ACK 映射，防止重启后重复执行
- slave 如果在 `accepted` 或 `executing` 后断线，重连后会继续未完成的关机流程，而不是只回放旧 ACK
- master 如果先把节点记为 `timeout`，后续又收到真实最终结果 `executed` / `failed`，会把状态改正为最终结果

## 构建 Linux 发布包

交叉编译两个架构的二进制：

```bash
./scripts/build-linux.sh   # 或 make build-linux
```

输出到 `dist/linux-amd64/` 与 `dist/linux-arm64/`，每个目录包含：`nut-master`、`nut-slave`、`configs/`、`packaging/systemd/`、`packaging/sudoers/`、`scripts/`。

生成完整发布产物（tar.gz / .deb / .rpm / SHA256SUMS）：

```bash
./scripts/package-release.sh   # 或 make package
```

`.deb` / `.rpm` 需要本机有 `nfpm`（`go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest`），缺失时脚本会跳过这一步并照常生成 tar.gz。

推送 `v*` 格式 tag 后，`.github/workflows/release.yml` 会在 GitHub Actions 上自动安装 nfpm、跑完整 `package-release.sh`、把所有产物（每架构 4 个 tar.gz + 2 个 .deb + 2 个 .rpm + SHA256SUMS，共 17 个）上传到 GitHub Release。

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

### 方案 C: 从 master 拉装机一行(v0.3.0+)

master 起来之后,可以直接让目标机自己从 master 拉:

```bash
ADMIN_TOKEN=...
curl -fsSL -H "Authorization: Bearer $ADMIN_TOKEN" \
  "http://master.host:9001/install/slave?node_id=db-01" \
  | sudo bash
```

master 会返回填好 `--master-addr` / `--token` / `--node-id` 的 `install-online.sh` 一行装,并把 `node_id` 预先标记为预期注册(`expected`)。`master_addr` 优先取配置里的 `public_addr`,留空时回退到 Host header。详见 `README-master.md`。

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

## master 本机自关机（local_shutdown）

如果 master 自己也需要在关电前关机，可以启用 `local_shutdown`，无需在 master 机器上再额外起一个 slave：

```yaml
local_shutdown:
  enabled: true
  max_wait: "15m"
  emergency_runtime_minutes: 15
```

触发关机后，master 会先等远端节点都拿到终态，然后再关掉自己。三种触发方式：

- 远端全部完成（`remote_complete`）
- 等待超过 `max_wait`（`wait_expired`）
- UPS 剩余分钟 < `emergency_runtime_minutes`：先对仍在处理的远端补发一次关机指令，再立即关本机（`emergency_runtime`）

`/status` 的 `local_shutdown` 字段会显示当前在哪个阶段（`idle` / `waiting_remote` / `wait_expired` / `emergency` / `executing` / `completed` / `failed`）。

## UPS 轮询可观测性

要把每次成功的 SNMP 轮询写入 journal，可设置：

```yaml
log_ups_status: true
```

然后重启 master：

```bash
sudo systemctl restart nut-master
sudo journalctl -u nut-master -f -o json-pretty
```

由于日志已经迁移到 slog JSON handler（v0.2.0+），journal 可以直接按字段过滤：

```bash
sudo journalctl -u nut-master -o json | jq 'select(.msg == "ups status")'
```

`/status` 端点也始终返回最新的 UPS 快照（`last_success_at` / `last_error_at` / 当前电量与剩余分钟）。

## Prometheus 指标(v0.3.0+)

master 在 admin 口同时暴露 `/metrics`(不鉴权,绑回环保护);slave 可选,通过 `metrics_listen_addr` 启用。

```yaml
# /etc/prometheus/prometheus.yml
scrape_configs:
  - job_name: nut-master
    static_configs:
      - targets: ["127.0.0.1:9001"]
  - job_name: nut-slave
    static_configs:
      - targets: ["10.0.0.20:9101", "10.0.0.21:9101"]
```

master 主要指标:

- `nut_master_ups_poll_total{result}` — SNMP 轮询次数(`success` / `error`)
- `nut_master_ups_on_battery`、`nut_master_ups_battery_charge_percent`、`nut_master_ups_runtime_minutes` — UPS 快照 gauge
- `nut_master_registered_slaves` — 当前在线 slave 数
- `nut_master_nodes{state}` — directory 里按状态分桶的节点数
- `nut_master_shutdowns_issued_total`、`nut_master_shutdown_acks_total{status}`、`nut_master_shutdown_active`
- `nut_master_local_shutdown_phase{phase}` — 本机 local_shutdown 阶段指示
- `nut_master_register_attempts_total{result}` — slave 注册结果(`accepted` / `rejected` / `invalid`)

slave 主要指标:

- `nut_slave_connected` — 当前是否有活跃会话
- `nut_slave_connect_attempts_total{result}` — dial+register 结果(`success` / `dial_error` / `register_error`)
- `nut_slave_shutdowns_received_total`、`nut_slave_shutdown_status_total{status}`

## 开发辅助

`Makefile` 提供了常用入口：

```bash
make build          # go build ./...
make build-linux    # 交叉编译 amd64 + arm64 到 dist/linux-*/
make package        # 生成 .tar.gz / .deb / .rpm（.deb/.rpm 需要安装 nfpm）
make run-master     # go run ./cmd/nut-master -config config/master.yaml
make run-slave      # go run ./cmd/nut-slave  -config config/slave.yaml
make clean
```

E2E 集成测试用 build tag 隔离,默认 `go test ./...` 不会跑;手动跑:

```bash
go test -tags e2e -count=1 ./internal/e2e/...
```

会在 loopback 上拉起真实的 master 和若干 slave,覆盖注册闭环、TLS、断线重连和 `/metrics`。CI 已经接入这一步。

发布流程：在 master 上打 `v*` tag 并推到 origin，`.github/workflows/release.yml` 会自动安装 nfpm、生成所有产物、计算 SHA256SUMS 并创建 GitHub Release。

## 路线图

- 更完整的 SNMP 厂商兼容（APC / Eaton 等）

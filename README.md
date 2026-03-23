# nut-server

一个基于 Go 的轻量 NUT 风格主从程序。

`nut-master` 通过 SNMP 读取 UPS 状态；`nut-slave` 主动注册到 master；当满足关机策略时，master 向 slave 下发关机指令。当前版本已支持 `dry_run`，可先验证链路而不真正关机。

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
- `auth_tokens`: 允许注册的 token 列表
- `poll_interval`: SNMP 轮询间隔
- `dry_run`: 是否仅验证链路而不执行真实关机
- `shutdown_policy.require_on_battery`: 是否要求 UPS 处于电池供电
- `shutdown_policy.min_battery_charge`: 最低电量阈值
- `shutdown_policy.min_runtime_seconds`: 最低剩余运行时间阈值
- `snmp.*`: UPS 的 SNMP 连接与 OID 配置

### slave

示例文件：`configs/slave.example.yaml`

关键字段：

- `node_id`: slave 唯一标识
- `master_addr`: master 地址
- `token`: 注册 token
- `reconnect_interval`: 断线重连间隔
- `dry_run`: true 时收到 shutdown 只记录日志并回 ACK
- `shutdown_command`: 真实关机命令

## dry-run

推荐联调时先使用：

```yaml
dry_run: true
```

效果：

- master 仍会广播 shutdown 指令
- slave 仍会回传 shutdown ACK
- slave 不会真正执行 `/sbin/shutdown -h now`

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
sudo ./scripts/install-master.sh
```

### 安装 slave

```bash
sudo ./scripts/install-slave.sh
```

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

- 手动触发指定 slave / 全部 slave 关机
- TLS / mTLS
- 更完整的 SNMP 厂商兼容
- 多策略关机条件
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

# Security Policy

## Supported Versions

| Version | Supported |
| ------- | --------- |
| v0.3.x  | ✅ |
| v0.2.x  | ⚠️ security fixes only |
| < v0.2  | ❌ |

`master` 分支永远是最新 supported release 的基线。

## Reporting a Vulnerability

发现潜在安全问题, 请**不要**直接开公开 issue。请发邮件到仓库 owner 的 GitHub 邮箱(`gh api user/<owner>` 可查),标题前缀 `[security] nut-server:`,内容包含:

- 受影响版本(`nut-master --version` 或 git tag)
- 复现步骤 / PoC
- 你预期的影响范围

收到报告后通常 72 小时内会回复确认, 14 天内给出修复或缓解计划。修复 release 会在 GitHub Release 的 release notes 里说明,但 PoC 细节最多延后到修复版本发布后 30 天再公开。

## Threat Model

nut-server 设计目标是同一信任域内的小型部署(homelab、机架),**不假设 master / slave 之间网络是公网可达**。默认假设:

1. **master.yaml 是可信配置**: 与环境变量同信任级别。可控的 admin/operator 才能改它。
2. **admin 监听口 (`admin_listen_addr`, 默认 `127.0.0.1:9001`) 默认只绑回环**。要远程访问推荐前面挂 TLS 反代,不要直接 bind 0.0.0.0。
3. **`auth_tokens` 是 slave 共享秘密**, 一旦泄漏等价于可以伪造任何节点身份。建议每套部署用独立 token,或者用 TLS client cert (`tls.require_client_cert: true`) 做双因素。
4. **`admin_token` 是 admin 全权 token**, 等同 root-level master 控制。建议:
   - 用 `openssl rand -hex 24` 生成, 不要复用密码
   - 只在 master 节点本地或受信任的 admin 工作站持有
   - 走 TLS 信道传输(直接 `curl http://...` 在公网上等于裸传)

## In-Tree Mitigations

代码层面已经做的硬化(更细的清单见 release notes):

- **常量时间** Bearer token / auth_token 比较 (`crypto/subtle.ConstantTimeCompare`),不泄漏匹配前缀
- **TCP 读写 deadline** + 握手 timeout, 闲连接自动关
- **slave 重连指数 backoff + jitter**, 抖动主节点不会被打爆
- **状态文件 atomic write** (tmp + sync + rename, `0600` perm)
- **systemd unit 沙箱化**: `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `RestrictAddressFamilies`, master 走 `User=nut-server`
- **`/install/slave` 端点**: `node_id` 强校验白名单, 所有内插字段单引号 shell-escape, 即便绕过白名单也跳不出参数位
- **Prometheus `/metrics` 标签** allowlist, 防止被认证 slave 推爆 cardinality
- **每个里程碑 PR 合并前** 都在本地跑过聚焦式 security review

## Out of Scope

下列不计入 security 问题, 请优先开普通 issue:

- 操作员 yaml 配置错误导致的可用性问题(如 `on_battery: false` 误关)
- 缺少审计日志 / metrics 字段不够细
- 反代/防火墙规则之外的网络可达性
- 依赖库的已知 CVE(由 Dependabot / `go mod tidy` 滚动处理, 单独走 PR)

## Acknowledgments

如果你的报告导致了发布修复, 欢迎在 release notes 里挂名(可选, 在报告里告知)。

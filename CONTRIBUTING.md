# Contributing to nut-server

欢迎贡献! 这里说明日常开发流程、测试要求、提交规范, 以及 PR 走查清单。

## Quick path: 改一个 bug / 加一个小特性

1. Fork & checkout 一个分支: `git checkout -b fix/<short-desc>` 或 `feat/<short-desc>`
2. 写代码 + 写测试 (TDD: 先红后绿)
3. 本地跑全套: `go test ./...` (单元) + `go test -tags e2e ./internal/e2e/...` (e2e)
4. `git commit` 用 Conventional Commits 格式 (见下)
5. 推到自己的 fork, 开 PR 指向 `master`
6. CI 必须全绿; 等 review

## 开发环境

- Go 1.23+ (`go.mod` 要求)
- Linux 或 macOS 本地开发都可以;发布产物只针对 Linux amd64 / arm64
- 可选: `nfpm` (生成 `.deb` / `.rpm` 时本地预览;CI 上自动装)

## 测试要求

| 类型 | 命令 | 期望 |
| --- | --- | --- |
| 单元 | `go test ./...` | 全绿,新功能含针对性 case |
| e2e | `go test -tags e2e -count=1 ./internal/e2e/...` | 全绿;改了网络协议 / 关机编排 / TLS 必须跑 |
| race | `go test -race ./...` | 偶发改并发的 PR 必须跑 |
| 覆盖率 | `go test -cover ./...` | 核心包 ≥80% (master / config / security) |

**TDD 流程**: 写测试先(RED) → 最小实现(GREEN) → 重构(IMPROVE)。新文件可以参考已有的 `internal/master/*_test.go` 风格 (AAA 模式)。

**不接受**: 跳测试 / 用 `t.Skip` 绕过 / 改测试为了过 lint。如果一条测试现在卡你, 先在 PR 里说明,优先修测试本身或先发问题 issue。

## Commit message 格式

走 Conventional Commits:

```
<type>: <一行总结, 祈使句, 不要句号>

<可选正文, 解释 *为什么* 改, 而不是 *改了什么*>
```

`type` 用这些(已在仓库历史里):

- `feat` 新功能
- `fix` bug 修复
- `refactor` 不改外部行为的重整
- `docs` 仅文档
- `test` 仅测试
- `chore` 杂项 (CI, 依赖, 工具)
- `perf` 性能
- `ci` CI 配置
- `release` 发布相关 (release prep PR 用这个)

正文里禁止:
- 贴整个 PR description (放在 PR body 即可)
- 拷贝 diff
- 跟踪当前 conversation / Claude / agent 的痕迹 (CLAUDE.md 全局禁用 attribution)

## PR 走查清单

PR 模板会自动给你一份, 主要检查:

- [ ] 单元 + e2e 全绿
- [ ] 改了 `master.yaml` / `slave.yaml` 字段 → `configs/*.example.yaml` 同步更新
- [ ] 改了管理 API / 协议消息 → README 对应章节同步更新
- [ ] 新增配置字段必须有默认值或在 `LoadMasterConfig` / `LoadSlaveConfig` 里报错
- [ ] 涉及外部输入 (HTTP / 协议) 的改动: 输入校验 + 必要时 escape
- [ ] 操作不可逆的 PR (改 state 文件格式 / 协议 wire 格式): 在 PR description 写 migration 说明
- [ ] 不在 commit 里塞 `data/*.json` / `config/*.yaml` 等本地状态 (`.gitignore` 已经盯着, 但再检查一遍)

## 安全敏感改动

涉及以下情况, 在 PR 里 explicit 提出 + 走本地 security review:

- 新增鉴权 / 改鉴权流程
- 用户输入会进入 shell / 文件路径 / DB 查询 / 模板渲染
- 网络监听口 / 协议消息字段新增
- 处理 `auth_tokens` / `admin_token` / TLS 私钥的代码路径

详细的安全模型见 [SECURITY.md](SECURITY.md)。

## 发布流程 (maintainer-only)

1. 收尾 feature PR 全部合并到 `master`
2. 开 `release/vX.Y.Z` 分支
3. 写 `.github/release-notes/vX.Y.Z.md`, 刷 README 顶部"最新版本"指针
4. PR 合并后在 master 上打 tag: `git tag vX.Y.Z && git push origin vX.Y.Z`
5. `release.yml` workflow 自动跑 `package-release.sh`, 上传产物 + SHA256SUMS

## 沟通

- Issue: 用 GitHub Issues, 走 issue 模板
- 安全报告: **不要**开公开 issue, 走 [SECURITY.md](SECURITY.md) 流程
- 讨论 / 不确定的想法: 也欢迎开 issue 标 `discussion`

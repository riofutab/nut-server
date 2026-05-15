## Summary

<!-- 1-3 句话: 这个 PR 干了什么, 为什么 (不是 "改了什么文件") -->

## Test plan

<!-- 勾选你跑过的; 不适用的留空 -->

- [ ] `go test ./...` (单元) 全绿
- [ ] `go test -tags e2e -count=1 ./internal/e2e/...` (e2e) 全绿
- [ ] `go test -race ./...` (如果涉及并发)
- [ ] 手动验证: <!-- 描述你怎么手动跑了一遍, 如 dry_run / loopback master+slave / etc -->

## Checklist

- [ ] 改 `master.yaml` / `slave.yaml` 字段时已同步更新 `configs/*.example.yaml`
- [ ] 改管理 API / 协议消息时已同步更新 README
- [ ] 新增配置字段有默认值或启动期校验
- [ ] commit message 走 Conventional Commits (`feat: ...` / `fix: ...` / 等)
- [ ] 没有 commit `data/*.json` / `config/*.yaml` 等本地状态

## Security-sensitive?

<!-- 涉及鉴权 / shell / 文件路径 / 协议字段 / token 路径? 如果是, 简述风险 + 缓解 -->

## Notes

<!-- migration / breaking change / 后续 follow-up issue 等 -->

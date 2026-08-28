# nightly runbook

## 触发方式
- cron：北京 02:30（UTC 18:30）自动执行 `.github/workflows/nightly.yml`
- 手动：Actions → nightly → Run workflow（main 分支）

## 失败处置
1. 打开 `reports/nightly/nightly-index.html`（单页摘要）定位红资产
2. 单资产复跑：`just gate <asset> all --suite nightly` 本地复现
3. 任一 G0 红 → 自动创建「禁止发布」issue（标题含 commit SHA）
4. 连续两晚红 → 冻结 main 分支合并（branch protection + issue 标记）

## 回滚
- 确认是新 commit 引入：`git revert <sha>` 开 PR 过 CI
- 是外部依赖/runner 环境：重跑 workflow + 豁免台账登记

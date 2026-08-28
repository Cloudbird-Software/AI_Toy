# Runbook: Holdout 密封数据访问
触发方式：仅 `.github/workflows/holdout-eval.yml`（workflow_call），运行在 `environment: holdout`，runs-on: [self-hosted, holdout]。
前置条件：GitHub Environment「holdout」已配置 required reviewer = founder；runner 打标签「holdout」且无外网出口。
失败处置：
  1. `holdoutctl verify-seal` 失败 → seal 被篡改，阻断作业。联系 founder 重签；
  2. `holdoutctl eval` 失败 → 检查 runner 能否访问受控对象存储（白名单）；
  3. n<5 切片被抑制输出（k-匿名）是预期行为，不算失败。
审计：audit log 在 `reports/holdout-audit.jsonl`（追加写，禁改写）；`uv run holdoutctl audit` 查询。

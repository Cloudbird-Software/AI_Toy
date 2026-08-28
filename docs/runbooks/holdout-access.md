# holdout-access runbook

## 触发方式
- 仅 runs-on: `[self-hosted, holdout]` runner + GitHub Environment `holdout`
- Environment `holdout` required reviewer = founder 批准后方可执行
- 入口：`.github/workflows/holdout-eval.yml`（由 nightly/release 调用）

## 失败处置
- `holdoutctl verify-seal` 失败：立即中止作业（exit 非零）
- 中止后：不输出任何结果文件、不上传 artifact
- 追加一条失败事件到 audit log（who/when/reason=verify-seal-fail）

## 回滚
- Audit log 位置：`reports/holdout-audit.jsonl`（JSON Lines，禁改写）
- 查询：`grep <suite|user|date> reports/holdout-audit.jsonl | jq`
- seal 损坏：重新从受控存储发布新 sealed-manifest → 重新审批触发

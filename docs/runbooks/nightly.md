# Runbook: Nightly（北京 02:30）
触发方式：cron `30 18 * * *` UTC（北京 02:30）；或 `workflow_dispatch` 手动点。
失败处置：
  1. 任一 G0 红 → GitHub Actions 自动开 issue「禁止发布」，标题含 commit SHA；
  2. 先看 `reports/nightly/nightly-index.md`（单页摘要）→ 定位单资产；
  3. 复跑：`uv run gaterunner run --asset <T> --level g0 --suite nightly --report reports/gates/<T>.json`；
  4. 连续两晚红 → 冻结 main 分支合入（开 issue 贴两晚红截图）。
回滚：合入 main 之前的绿 commit，tag `v0.0.0-nightly-green-<date>`。

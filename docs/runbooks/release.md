# release runbook

## 触发方式
- 推送 git tag `v*`（如 `v0.1.0`）触发 `.github/workflows/release.yml`
- 执行顺序：g0-all → canary → packs → sbom（串行 needs 链）

## 失败处置
- g0-all 任一红：exit 10，release workflow abort
- canary 掉分（exit 10）：**立即 abort release workflow**
- 禁止删除已推送 tag 重打；必须递增版本号重新打 tag 提交

## 回滚
- sbom 产物落 `dist/sbom.spdx`，作为 GitHub Release 附件同步上传
- 若 abort：关闭失败的 Release Draft，保留失败 run 作审计，新版本重提
- 已发布回滚：`git revert` + 新 tag `vX.Y.Z+1` 走完整流程

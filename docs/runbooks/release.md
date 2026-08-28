# Runbook: Release（tag v*）
触发方式：`git tag vX.Y.Z && git push --tags`。
执行顺序（release.yml，自上而下不可逆）：
  1. g0-all：全资产 G0 门禁 release suite；任一红 → exit 10，发布终止；
  2. canary：`uv run holdoutctl eval --suite canary`，掉分 = exit 10 且禁删 tag 重打，须新版本号；
  3. packs：构建 assets-packs/*（缺 eval_set 的包直接 fail）；
  4. sbom：`uv run repoctl sbom --out dist/sbom.spdx`。
回滚：删除 GitHub Release（仅删 Release，禁删 Git tag——tag 永久记录），开新版本 tag 重来。

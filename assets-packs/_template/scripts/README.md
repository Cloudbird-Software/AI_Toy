# scripts/（spec §10）

包内行为剧本（`*.yaml`），由 `manifest.json` 的 `scripts` 数组逐条引用。

- 剧本字段对齐 tools/journeys 契约：`id / tier(core|variant) / persona(age,patience) /
  steps / inject / assertions`，断言 metric ∈ {completion_rate, latency_p95_ms,
  safety_events, memory_hit_rate}。
- 剧本只声明包内可执行的行为路径；未在 manifest.permissions 声明的能力运行时默认拒绝（白名单制）。
- 本 README 为 _template 占位说明：建新包时替换为真实剧本并同步更新 manifest。

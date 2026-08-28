# datasets/manifests/（spec §9.1）

数据集 manifest 登记（synth 大文件不入 git，仅 manifest 追踪；数据本体格式已被 `.gitignore` 排除）。

## schema

每份 `*.json` 恰含八字段：

```json
{
  "id": "kws_synth_v1",
  "kind": "synth",
  "producer": "tools/synthgen（生成器 …，待注册 datasets/synth/registry.jsonl）",
  "license_or_consent": "…",
  "sha256": "…",
  "n_items": 0,
  "splits": { "synth_train": 0.8, "synth_holdout": 0.2 },
  "created": "2026-08-28"
}
```

- `kind`：`synth | real | holdout | canary`（本目录当前全部为 `synth`；holdout/canary 制度见 spec §9.1）
- `splits`：切分比例，对齐 tools/synthgen 的 8:2 生成即切分约定（synth-train / synth-holdout）
- `created`：ISO 日期

## 占位说明（W6-C4 初始状态）

**数据未生成**——以下字段均为占位，待 synthgen 产出批次后回填：

- `sha256` 全零占位：待 synthgen 生成对应批次文件后回填真实摘要；
- `n_items: 0` 占位：同上，产出后回填真实条数；
- `producer` 中的生成器尚未在 `datasets/synth/registry.jsonl` 注册（synthgen register）。

synthgen CLI（register / generate / dist-check）不读取本目录（其注册表 `datasets/synth/registry.jsonl`
独立于本 manifest），故本目录当前无「必须通过」的工具校验命令；schema 以 spec §9.1 字段约定为准。

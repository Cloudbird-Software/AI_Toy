// Package synthgen implements the synthetic data generation registry (spec §3.7)：
// 生成器注册表（{id, version, seed_policy, outputs_manifest}，(id, version) 唯一）、
// 每条样本四字段溯源戳（generator_id/generator_version/seed/upstream_model）、
// 确定性 8:2 切分（synth-train/synth-holdout + manifest）与多样性 dist-check
// （说话人/语速/主题分布熵、参考集 JS 距离、单源占比 ≤30%）。纯标准库。
//
// CLI（cmd/synthgen；注册表与批次默认落盘 datasets/synth/**，相对 CWD）：
//
//	synthgen register --id <gid> --version <v> --seed-policy <p> --outputs-manifest <path>
//	synthgen generate --id <gid> --n <N> --seed <s>
//	synthgen dist-check --batch <id> [--reference <path>]
//
// 退出码：0 通过；2 输入错（重复注册 / 未注册 / 批次缺失）；20 单源占比 >30%。
package synthgen

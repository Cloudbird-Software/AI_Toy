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
//	synthgen generate-neg --id <gid> --duration-ms <ms> [--seed <s> | --seed-label <label>]
//	synthgen dist-check --batch <id> [--reference <path>]
//
// 退出码：0 通过；2 输入错（重复注册 / 未注册 / 批次缺失）；20 单源占比 >30%。
//
// 负样本面（m2-spec §2，IR #90）：gen-tneg 家庭音景 / gen-kwsadv 对抗同音节帧流
// 生成器（源类型参数集随版本冻结；PCM 不落盘，由 (generator@version, seed,
// duration_ms) 确定性重建）；负样本批 eval-only 不切 synth-holdout——TrainN=0/
// HoldoutN=0、全量入 eval 池（负样本只供误唤醒评估、永不进训练管道）。
package synthgen

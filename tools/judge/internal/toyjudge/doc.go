// Package toyjudge implements the LLM-as-verifier judge (spec §3.3).
//
// CLI（退出码契约，CI 依赖面）：
//
//	toyjudge calibrate --rubric 7a --gold configs/judge/gold/7a.jsonl
//	    逐 criterion 输出人工金标 vs judge 的 Cohen's κ（evalkit.CohensKappa），
//	    任一 κ < policy.kappa_gate.automation → exit 20；配置/输入错误 → exit 2。
//	toyjudge run --rubric 7a --targets <dir> --mode pairwise-swap --out reports/judge/7a.jsonl
//	    AB/BA 各评一次，不一致记 tie；高风险 rubric（9a）走 judges_high_risk
//	    双 judge（异族），双 judge 不一致也记 tie；配置/输入错误 → exit 2。
//
// judge 配置从 configs/judge/model.yaml（spec §4.2 schema）锁定读取：
// judge_default（provider/model/temperature/locked，locked 须 true）、
// judges_high_risk（恰 2 条且异族）、policy（kappa_gate.automation/ci_autonomous、
// pairwise_swap、tie_on_disagree）与 gold_dir；κ 门禁阈值取
// policy.kappa_gate.automation，非硬编码。prompt 不再来自 model.yaml（schema 无
// prompt 字段），由 rubric（id + criteria 序列化）派生，三字段与 prompt/配置哈希
// 记录进报告。rubric 为三级量表（levels 恰 [1,2,3] 且每级锚定）。评审后端是
// 可注入的 Judge 函数类型，本卡默认 DeterministicJudge 桩；真实 LLM 经
// baml/baml_client_go 客户端接入为后续卡（BAML-1）。
package toyjudge

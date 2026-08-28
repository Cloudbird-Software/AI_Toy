// Package toyjudge implements the LLM-as-verifier judge (spec §3.3).
//
// CLI（退出码契约，CI 依赖面）：
//
//	toyjudge calibrate --rubric 7a --gold configs/judge/gold/7a.jsonl
//	    逐 criterion 输出人工金标 vs judge 的 Cohen's κ（evalkit.CohensKappa），
//	    任一 κ<0.61 → exit 20；配置/输入错误 → exit 2。
//	toyjudge run --rubric 7a --targets <dir> --mode pairwise-swap --out reports/judge/7a.jsonl
//	    AB/BA 各评一次，不一致记 tie；高风险 rubric（9a）双 judge（异族），
//	    双 judge 不一致也记 tie；配置/输入错误 → exit 2。
//
// judge 模型/温度/prompt 从 configs/judge/model.yaml 锁定读取（缺任一字段 → exit 2），
// 三字段与 prompt/配置哈希记录进报告；rubric 为三级量表（levels 恰 [1,2,3] 且每级
// 锚定）。评审后端是可注入的 Judge 函数类型，本卡默认 DeterministicJudge 桩；
// 真实 LLM 经 baml/baml_client_go 客户端接入为后续卡（BAML-1）。
package toyjudge

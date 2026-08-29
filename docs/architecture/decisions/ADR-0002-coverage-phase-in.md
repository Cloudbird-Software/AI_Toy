# ADR-0002 门禁真实调度 + coverage 阶段化执法
状态：accepted 2026-08-29
背景：外部审计确认 G0 级漏洞（IR #64）——gaterunner ExecuteRun 以 simulate()（哈希伪随机 ~90% pass）伪造观测、evidenceCmd() 生成不存在的测试名充当证据（run 报告假绿，已提交 reports/gates/T4.json 为假证据）；同时 repoctl coverage 对 16 资产无一登记断言全量 FAIL，meta.yml 在 main 连续红——验收机空转（假绿与真红并存）。
决策：ExecuteRun 改真实调度——登记表（ScanMarks 源码扫描）命中即实跑 `go test -count=1 -run ^<Test>$ <pkg>`，verdict=退出码、evidence=实际命令；未命中 → verdict=not_implemented（不计 pass 不计 fail，summary 单列 not_impl_ids，exit 0 且显式计数）；删除 simulate()与假证据生成，judge() 统计判定保留给 benchmark/holdout 数据面。repoctl coverage 阶段化执法：登记表=报告 verdict∈{pass,fail,exempt} 条目；资产登记 ≥1 条 → 强制全 BI 覆盖+≥1 G0+无孤儿断言；0 条 → 输出 coverage DEBT 行不 FAIL——首条断言落地即进入全执法。测试 fixture 一律虚构资产 TX（不冒充真实资产断言）；删除假证据 reports/gates/T4.json；quality/run-gates.sh 落地统一编排器入口（CI workflow 保持禁用为 founder 决策，脚本就绪待启用）。
备选否决：保持 simulate（G0 假绿不可留）；coverage 全域强制执法（16 资产实现未开始 → meta 永续红，阻断全部开发流）；not_implemented 计入 fail（同永续红）或计入 pass（回到假绿）。
后果：`just gate <T>` 诚实反映实现进度（当前 16 资产全 DEBT）；meta.yml 转绿且不假绿；每资产首条 Mark 落地即自动升格全执法，无需再改策略。

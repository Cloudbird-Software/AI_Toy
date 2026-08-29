# ADR-0003 模拟 driver 失败阶段化为 SIMULATION-DEBT
状态：accepted 2026-08-29
背景：审计发现 journeys 桩 driver（simulateRun 哈希噪声）使 golden×3 seeds 固定失败 10 条旅程，与实现质量无关；nightly golden-journeys job 永远红，justfile journeys recipe 同样不可用——模拟态的失败不是产品信号（IR #72，ADR-0002 同类问题）。
决策：driver_mode=simulated 且 summary 判 fail → exit 0，stdout 必须输出醒目 DEBT 行并列出失败 journey id；报告新增 simulation_debt 字段（fail_ids 复用 Summary 既有字段）。--strict 恢复旧阻断语义（fail→1）；driver 非 simulated 维持旧语义。真实 T20 driver 接入、journeys 换真 driver 后 DriverMode 不再是 simulated，无需改代码自然转真实执法。
备选否决：调 simulateRun 概率参数凑绿=对考卷优化式造假（且哈希噪声换 seed 即变）；nightly 加 continue-on-error=隐藏问题且 DEBT 不可见；只在 nightly 放行不改 CLI=本地/CI 行为分叉。
后果：nightly golden-journeys 转绿但债务可见可追踪；桩噪声不再阻断开发流；--strict 供 founder 随时恢复严格执法。

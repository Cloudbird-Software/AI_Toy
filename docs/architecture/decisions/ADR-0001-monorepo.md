# ADR-0001 单仓 monorepo + Go/BAML/TS/Rust 多语言 workspace + 门禁外置
状态：accepted 2026-08-28
背景：单人创始人+N 个 AI agent 并行开发 18 个技术资产；验收协议必须唯一事实源且可机器执行。
决策：单仓；Go=单 module（tools/*+packages/go/*，根 go.mod，application 层，团队规范 default）；BAML=llm_prompt 层契约（baml/，baml-go 客户端，BAML-1 golden test）；TS=pnpm（packages/ts/*，frontend）；Rust=cargo workspace（packages/native/*，端侧推理 runtime，owner 豁免）；语言决策四层表与「更换=重新立项」规则见规格书 §0（IR #23，languages.yaml）。任务入口统一 justfile；门禁阈值全部外置 configs/gates/*.yaml（gaterunner 读取，测试零硬编码阈值）；验收协议 docs/gates/ 为法典，CODEOWNERS 锁 founder；AGENTS.md 根+每包双层（根=契约，包=边界/数据/坑）。
备选否决：多仓（跨仓门禁引用与组合级 CI 复杂度不可承受）；阈值入测试代码（改阈值=改代码，审计断链）；Makefile（跨平台与参数化弱于 just）；application 层 Python（团队规范默认拒绝，见 IR #23）。
后果：CI 需 paths-filter 变更检测；GPU/holdout 自托管 runner 成本；换来：原子跨资产重构、单一门禁报表、agent 上下文一份 AGENTS.md 树即可获得。

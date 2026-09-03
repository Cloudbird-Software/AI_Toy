# ADR-0006 T10 常识层：Wikidata CC0 优先 + ConceptNet SA 传染取舍
状态：proposed 2026-09-03（IR #134，规格=issue-134.md）
背景：T10 记忆图谱需接入常识层以支持儿童对话的事实一致性与因果推理；候选源包括 Wikidata（CC0，无传染）与 ConceptNet（CC BY-SA 4.0，ShareAlike 传染——衍生数据须同许可开源，与商用闭源产品路线冲突）。
决策：Wikidata（CC0）作为唯一常识三元组来源；ConceptNet 排除，不在任何训练/推理/合成管线中接入。Wikidata 抽取仅保留儿童友好子图（实例/属性过滤：P31 限定为玩具/动物/日常物品/基础情绪等类别，关系过滤 P361/P279 等形成轻量常识网）。
备选否决：ConceptNet（SA 传染风险，法务未批前禁止引入合成/训练管线）；现网 Schema.org JSON-LD（无三元组标准视图，抽取成本高于 SPARQL）。
后果：Wikidata SPARQL 端点作为唯一外部抽取入口；子图过滤逻辑须在 M2 落地并经过 T10-G1-02 事实更新不矛盾门禁复核；新增外部依赖（SPARQL 客户端）须走 license 台账与 founder 审批（当前 M1 仅设计，不落实现）。

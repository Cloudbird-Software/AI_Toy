# ai-toy 入口协议宿主（AGENTS.md 步骤 4）：make card-test / make gates-pr。
# 与 justfile 并存——just 面向日常开发，Make 面向卡工作流（AGENTS.md 契约写的是 make）。

# card-test —— 认领卡后本地先行验证（AGENTS.md「读卡 AC、测试先行」）：
#   make card-test CARD=<n> ASSET=<T4> [LEVEL=all] [SUITE=ci]
# CARD 仅注入 GATE_CARD 上下文（本地无 ghcb/卡 issue 时不参与判定）；实际执行
# verify-configs + 单资产门禁 + 该资产包测试。ASSET 必填——本地无法由卡号
# 解析资产（卡 issue 在远端，`bash ghcb status <n>` 才能查）。
CARD ?=
ASSET ?=
LEVEL ?= all
SUITE ?= ci
GATE_ASSET ?= $(ASSET)
GATE_LEVEL ?= $(LEVEL)
GATE_SUITE ?= $(SUITE)
GATE_CARD ?= $(CARD)

.PHONY: card-test gates-pr

card-test:
	@test -n "$(CARD)" || { echo "error: 需要 CARD=<卡号>（make card-test CARD=42 ASSET=T4）"; exit 2; }
	@test -n "$(ASSET)" || { echo "error: 需要 ASSET=<资产>（卡号→资产经 ghcb/卡 issue 解析，本地必填）"; exit 2; }
	@test -f "configs/gates/$(ASSET).yaml" || { echo "error: 未知资产 $(ASSET)（无 configs/gates/$(ASSET).yaml）"; exit 2; }
	@echo "==> card-test CARD=#$(CARD) ASSET=$(ASSET) LEVEL=$(LEVEL) SUITE=$(SUITE)"
	GATE_ASSET=$(GATE_ASSET) GATE_LEVEL=$(GATE_LEVEL) GATE_SUITE=$(SUITE) GATE_CARD=$(CARD) \
		bash quality/run-gates.sh pr
	@echo "==> 资产包测试：packages/go/...（go test 与门禁同 PR，AGENTS.md PR 规范）"
	go test ./packages/go/... -count=1

# gates-pr —— 本地复现 CI 关卡（AGENTS.md 步骤 4 末步）：与 ci.yml quality-gates
# job 同一入口同一 GATE_* env 协议（quality/run-gates.sh）。
gates-pr:
	GATE_ASSET=$(GATE_ASSET) GATE_LEVEL=$(GATE_LEVEL) GATE_SUITE=$(GATE_SUITE) GATE_CARD=$(GATE_CARD) \
		bash quality/run-gates.sh pr

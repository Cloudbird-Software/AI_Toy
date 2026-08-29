#!/usr/bin/env bash
# run-gates —— 门禁统一编排器（IR #64 / W2-C2 协议，见 .github/workflows/ci.yml
# quality-gates job 注释）：CI 与本地同一入口、同一 GATE_* env 注入协议。
#   GATE_ASSET / GATE_LEVEL / GATE_SUITE  门禁选择（缺省 T4 / all / ci）
#   GATE_PR / GATE_PR_AUTHOR / GATE_BASE / GATE_CARD  PR 上下文（CI 注入，
#   g050/g060 类回查预留；本地缺省为空，仅打印不参与判定）
# 用法：quality/run-gates.sh [pr]。pr（缺省）= verify-configs + 单资产 gate（封装
# gaterunner run，等效 `just gate <ASSET> [LEVEL]` + --suite 注入面，报告落
# reports/gates/<ASSET>.json）。真实调度语义（IR #64）：not_implemented 显式单列
# 不计 pass、exit 0；G0 红=10 / G1 红=20 / 仅 G2=30，退出码透传（set -e 终止）。
# 状态：CI workflow disabled_manually（founder 决策），本脚本先就绪待启用——
# 消除 ci.yml 对本文件的幽灵引用。bash -n 可静态校验。
set -euo pipefail

MODE="${1:-pr}"
GATE_ASSET="${GATE_ASSET:-T4}"
GATE_LEVEL="${GATE_LEVEL:-all}"
GATE_SUITE="${GATE_SUITE:-ci}"
GATE_PR="${GATE_PR:-}"
GATE_PR_AUTHOR="${GATE_PR_AUTHOR:-}"
GATE_BASE="${GATE_BASE:-}"
GATE_CARD="${GATE_CARD:-}"

# 单资产门禁：与 just gate 等效（just gate 无 --suite 面，故直调 gaterunner）。
run_gate() {
  go run ./tools/gaterunner/cmd/gaterunner run \
    --asset "$GATE_ASSET" \
    --level "$GATE_LEVEL" \
    --suite "$GATE_SUITE" \
    --report "reports/gates/${GATE_ASSET}.json"
}

case "$MODE" in
  pr)
    # PR 上下文回显（CI 注入时可见；本地为空则静默）。
    if [ -n "$GATE_PR" ]; then
      echo "gate ctx: pr=#${GATE_PR} author=${GATE_PR_AUTHOR} base=${GATE_BASE} card=#${GATE_CARD}"
    fi
    # PR 面：门禁配置一致性（阈值×文档 BI 映射）+ 单资产门禁。
    go run ./tools/gaterunner/cmd/gaterunner verify-configs
    run_gate
    ;;
  *)
    echo "usage: quality/run-gates.sh [pr]（GATE_ASSET/GATE_LEVEL/GATE_SUITE 可经 env 覆盖）" >&2
    exit 2
    ;;
esac

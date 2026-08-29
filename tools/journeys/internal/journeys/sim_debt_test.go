// 模拟态失败阶段化契约测试（IR #72 / ADR-0003）：driver_mode=simulated 且
// summary fail → exit 0 + DEBT 行；--strict 恢复旧阻断语义；全 pass → exit 0 无 DEBT 行。
// 失败剧本用不可能断言（completion_rate >= 1.01）构造，红绿只由断言上界决定，
// 与桩 driver 哈希噪声解耦，保证跨版本确定性。
package journeys

import (
	"encoding/json"
	"strings"
	"testing"
)

// debtFailYAML 确定性失败剧本：均值上界 1.0，>= 1.01 永不满足。
const debtFailYAML = `id: J99-impossible
tier: core
persona: {age: 7, patience: high}
steps: [say, wait, close]
inject: {interrupts: [], safety_events: []}
assertions:
  - {metric: completion_rate, op: '>=', value: 1.01}
  - {metric: safety_events, op: '<=', value: 100}
`

// decodeFirstJSON 从「JSON 报告 + DEBT 行」混合 stdout 中解出第一个 JSON 值。
func decodeFirstJSON(t *testing.T, out string) Report {
	t.Helper()
	var rep Report
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&rep); err != nil {
		t.Fatalf("stdout 前段不是合法 JSON 报告: %v\nstdout=%q", err, out)
	}
	return rep
}

func TestSimulatedFailExitsZeroWithDebtLine(t *testing.T) {
	code, out, errMsg := runCLI(t, map[string]string{"J01.yaml": coreYAML, "J99.yaml": debtFailYAML})
	if code != ExitOK {
		t.Fatalf("simulated+fail 应 exit 0（阶段化 DEBT），got exit=%d stderr=%q", code, errMsg)
	}
	if !strings.Contains(out, "SIMULATION-DEBT") {
		t.Errorf("stdout 缺 DEBT 行:\n%s", out)
	}
	if !strings.Contains(out, "driver=simulated") || !strings.Contains(out, "--strict") {
		t.Errorf("DEBT 行应说明桩噪声归因与 --strict 恢复阻断:\n%s", out)
	}
	if !strings.Contains(out, "J99-impossible") {
		t.Errorf("失败 journey id 应在 stdout 单独一行列出:\n%s", out)
	}
	rep := decodeFirstJSON(t, out)
	if !rep.SimulationDebt {
		t.Errorf("报告 simulation_debt=%v, want true", rep.SimulationDebt)
	}
	if rep.DriverMode != "simulated" || rep.Summary.Overall != "fail" || rep.Summary.Fail != 1 {
		t.Fatalf("driver_mode=%q summary=%+v", rep.DriverMode, rep.Summary)
	}
	if len(rep.Summary.FailIDs) != 1 || rep.Summary.FailIDs[0] != "J99-impossible" {
		t.Fatalf("fail_ids=%v, want [J99-impossible]", rep.Summary.FailIDs)
	}
}

func TestStrictFlagRestoresExitFail(t *testing.T) {
	code, out, errMsg := runCLI(t, map[string]string{"J99.yaml": debtFailYAML}, "--strict")
	if code != ExitFail {
		t.Fatalf("--strict 下 fail 应恢复 exit 1（旧语义），got exit=%d stderr=%q", code, errMsg)
	}
	if strings.Contains(out, "SIMULATION-DEBT") {
		t.Errorf("--strict 维持旧输出语义，不应打 DEBT 行:\n%s", out)
	}
	// 报告字段的债务归因与 exit 策略解耦：仍如实标记 simulation_debt。
	rep := decodeFirstJSON(t, out)
	if !rep.SimulationDebt {
		t.Errorf("报告 simulation_debt=%v, want true（归因不随 --strict 改变）", rep.SimulationDebt)
	}
}

func TestSimulatedAllPassNoDebtLine(t *testing.T) {
	code, out, errMsg := runCLI(t, map[string]string{"J01.yaml": coreYAML, "J02.yaml": variantYAML})
	if code != ExitOK {
		t.Fatalf("全 pass 应 exit 0，got exit=%d stderr=%q", code, errMsg)
	}
	if strings.Contains(out, "SIMULATION-DEBT") {
		t.Errorf("全 pass 不应出现 DEBT 行:\n%s", out)
	}
	rep := decodeFirstJSON(t, out)
	if rep.SimulationDebt {
		t.Errorf("报告 simulation_debt=%v, want false", rep.SimulationDebt)
	}
	if rep.Summary.Overall != "pass" {
		t.Fatalf("summary=%+v", rep.Summary)
	}
}

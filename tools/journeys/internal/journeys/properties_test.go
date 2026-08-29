// 属性测试：确定性（同 seed 同剧本 → 报告除 timestamp 外一致）与
// seeds 数 = 每旅程 runs 数（表驱动 1–8）。
package journeys

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// propScript 用确定性参数拼出合法剧本 YAML（替代 hypothesis 抽样）。
func propScript(id, tier, patience string, nSteps, nSafety int) *Script {
	steps := strings.TrimSuffix(strings.Repeat("s,", nSteps), ",")
	safety := strings.TrimSuffix(strings.Repeat("{kind: crisis},", nSafety), ",")
	text := fmt.Sprintf("id: %s\ntier: %s\npersona: {age: 7, patience: %s}\nsteps: [%s]\n"+
		"inject: {interrupts: [], safety_events: [%s]}\nassertions:\n"+
		"  - {metric: completion_rate, op: '>=', value: 0.0}\n"+
		"  - {metric: safety_events, op: '<=', value: 100}\n", id, tier, patience, steps, safety)
	s, err := ParseScript([]byte(text), "")
	if err != nil {
		panic(err)
	}
	return s
}

func TestSameSeedsSameScriptsProduceIdenticalOutput(t *testing.T) {
	scripts := []*Script{
		propScript("J100-a", "core", "high", 3, 0),
		propScript("J101-b", "variant", "low", 5, 2),
		propScript("J102-c", "variant", "high", 1, 1),
	}
	byID := func(r *Report) map[string]JourneyReport {
		m := map[string]JourneyReport{}
		for _, j := range r.Journeys {
			m[j.ID] = j
		}
		return m
	}
	for _, seeds := range []int{1, 3, 8} {
		t.Run(fmt.Sprintf("seeds=%d", seeds), func(t *testing.T) {
			first, err := Run(scripts, seeds, "golden", DriverModeSimulated)
			if err != nil {
				t.Fatal(err)
			}
			// 逆序输入：证明各旅程结果不依赖处理位置（无共享随机态）。
			reversed := []*Script{scripts[2], scripts[1], scripts[0]}
			second, err := Run(reversed, seeds, "golden", DriverModeSimulated)
			if err != nil {
				t.Fatal(err)
			}
			first.Timestamp, second.Timestamp = "", ""
			if !reflect.DeepEqual(byID(first), byID(second)) {
				t.Error("journeys differ between two runs of the same seeds")
			}
			if !reflect.DeepEqual(first.Summary, second.Summary) {
				t.Errorf("summary differs: %+v vs %+v", first.Summary, second.Summary)
			}
		})
	}
}

func TestSeedCountMatchesPerJourneyRuns(t *testing.T) {
	scripts := []*Script{propScript("J200-a", "core", "high", 4, 0), propScript("J201-b", "variant", "low", 2, 1)}
	for seeds := 1; seeds <= 8; seeds++ {
		t.Run(fmt.Sprintf("seeds=%d", seeds), func(t *testing.T) {
			rep, err := Run(scripts, seeds, "golden", DriverModeSimulated)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Seeds != seeds || len(rep.Journeys) != len(scripts) {
				t.Fatalf("seeds=%d journeys=%d", rep.Seeds, len(rep.Journeys))
			}
			for _, j := range rep.Journeys {
				if len(j.Runs) != seeds {
					t.Fatalf("%s: %d runs, want %d", j.ID, len(j.Runs), seeds)
				}
				for i, r := range j.Runs {
					if r.Seed != i {
						t.Fatalf("%s: runs[%d].Seed=%d", j.ID, i, r.Seed)
					}
				}
			}
		})
	}
}

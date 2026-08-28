// 运行时镜像，实现落地后替换：CH-06 IMU 事件风暴（spec §8.3，G1）。
//
// 三段：注入（传感器异常→海量事件，风暴守卫开启且带内不自动关闭）→
// 系统响应（限流：每秒放行上限；聚合：残余折叠为手势，整分钟动作数钳制
// ——无动作风暴）→ 恢复（重启/人工复位：无自动恢复，复位前守卫持续生效，
// 终端态稳定）。packages/go/imu（T6）落地后替换本镜像。
package chaos

import "testing"

// imuBudgets CH-06 的 G1 界。
const (
	imuRateLimitPerSec   = 50   // 限流：每秒放行事件上限
	imuMaxActionsPerMin  = 10   // 无动作风暴：每分钟下发动作上限
	imuWindowsPerMin     = 60   // 一分钟 = 60 个秒窗
	imuStormEventsPerSec = 5000 // 注入：风暴事件率（正常 ~50/s）
)

// imuMirror IMU 输入面镜像（纯数据）。
type imuMirror struct {
	guardOn bool // 风暴守卫（限流+聚合）；开启后仅复位关闭
}

// imuEpisode 一个观测窗口的注入与输出统计。
type imuEpisode struct {
	eventsIn   int // 注入事件数
	eventsPass int // 限流后放行事件数
	actions    int // 聚合后下发的动作数
}

// injectIMUStorm 注入（三段之一）：事件风暴打开守卫。
func injectIMUStorm(m imuMirror) imuMirror {
	m.guardOn = true
	return m
}

// imuRespond 系统响应（三段之二）：一个秒窗的事件处理。
// 守卫关（健康）：事件全量进管线（每个原始事件直连动作管线——风暴即动作
// 风暴的灾难对照）。守卫开：限流至每秒上限 + 残余聚合为单一手势动作。
func imuRespond(m imuMirror, eventsIn int) imuEpisode {
	if !m.guardOn {
		return imuEpisode{eventsIn: eventsIn, eventsPass: eventsIn, actions: eventsIn}
	}
	pass := min(eventsIn, imuRateLimitPerSec)
	return imuEpisode{eventsIn: eventsIn, eventsPass: pass, actions: min(pass, 1)}
}

// imuMinute 系统响应（三段之二）：风暴持续一分钟（60 个秒窗）。
// 派发器再按每分钟动作上限钳制（聚合+限流的双层守卫）。
func imuMinute(m imuMirror, eventsPerSec int) imuEpisode {
	total := imuEpisode{eventsIn: eventsPerSec * imuWindowsPerMin}
	for i := 0; i < imuWindowsPerMin; i++ {
		w := imuRespond(m, eventsPerSec)
		total.eventsPass += w.eventsPass
		total.actions += w.actions
	}
	if m.guardOn {
		total.actions = min(total.actions, imuMaxActionsPerMin)
	}
	return total
}

// imuReset 恢复（三段之三）：重启/人工复位——关闭守卫，恢复正常管线。
func imuReset(m imuMirror) imuMirror {
	m.guardOn = false
	return m
}

func TestIMUEventStorm(t *testing.T) {
	row, ok := Row(RowIMU)
	if !ok {
		t.Fatalf("矩阵行 %s 未登记", RowIMU)
	}
	if row.Gate != GateG1 {
		t.Fatalf("%s 门禁级别=%s，须 G1（合并阻断）", row.ID, row.Gate)
	}

	// ── 三段之一：注入（事件风暴，100 倍于正常事件率）──
	m := injectIMUStorm(imuMirror{})
	if !m.guardOn {
		t.Fatal("注入后风暴守卫须开启")
	}

	// ── 三段之二：系统响应（限流 + 聚合，无动作风暴）──
	minute := imuMinute(m, imuStormEventsPerSec)
	if minute.eventsPass > imuRateLimitPerSec*imuWindowsPerMin {
		t.Fatalf("限流失效：放行 %d 事件/分钟，须 ≤%d", minute.eventsPass, imuRateLimitPerSec*imuWindowsPerMin)
	}
	if minute.actions > imuMaxActionsPerMin {
		t.Fatalf("动作风暴：%d 动作/分钟，须 ≤%d", minute.actions, imuMaxActionsPerMin)
	}
	// 差分对照：无守卫时同一风暴全量穿透为动作（守卫确实挡住了灾难）。
	unguarded := imuMinute(imuMirror{}, imuStormEventsPerSec)
	if unguarded.actions != imuStormEventsPerSec*imuWindowsPerMin {
		t.Fatalf("对照路径失效：无守卫动作=%d，须=%d", unguarded.actions, imuStormEventsPerSec*imuWindowsPerMin)
	}
	if minute.actions >= unguarded.actions {
		t.Fatalf("守卫无效：动作 %d 未少于对照 %d", minute.actions, unguarded.actions)
	}

	// 恢复列「重启/人工」：无自动恢复——守卫跨分钟持续生效，终端态稳定。
	for i := 0; i < 3; i++ {
		next := imuMinute(m, imuStormEventsPerSec)
		if next != minute {
			t.Fatalf("守卫输出须跨分钟稳定（第 %d 分钟 %+v ≠首分钟 %+v）", i+1, next, minute)
		}
	}
	if !m.guardOn {
		t.Fatal("无复位时守卫不得自动关闭（恢复路径=重启/人工）")
	}

	// ── 三段之三：恢复（重启/人工复位）──
	reset := imuReset(m)
	if reset.guardOn {
		t.Fatal("复位后守卫须关闭")
	}
	// 复位后正常事件率全量放行（限流解除，不误伤健康流量）。
	const normalEventsPerSec = 40
	calm := imuRespond(reset, normalEventsPerSec)
	if calm.eventsPass != normalEventsPerSec {
		t.Fatalf("复位后正常事件放行=%d，须 %d（不误伤）", calm.eventsPass, normalEventsPerSec)
	}
}

// 运行时镜像，实现落地后替换：CH-07 时钟漂移/NTP 失效（spec §8.3，G1）。
//
// 三段：注入（NTP 失效，本地时钟漂移超阈）→ 系统响应（时间类记忆停写；
// 其余记忆与对话不受影响）→ 恢复（校时成功后时间类记忆恢复写入，且写入
// 校准后的时间戳）。packages/go/memory + 运行时（T10/T14）落地后替换本镜像。
package chaos

import "testing"

// clockDriftToleranceMS CH-07 的 G1 界：漂移超阈即停写时间类记忆。
const clockDriftToleranceMS = 5000

// clockRuntime 时钟与记忆写入镜像（纯数据）。
type clockRuntime struct {
	nowMS      int64    // 本地时钟读数（可能漂移）
	ntpOK      bool     // NTP 同步状态
	driftMS    int      // 当前累计漂移
	timeWrites []int64  // 时间类记忆落库时间戳（时间序）
	factWrites []string // 其余（非时间类）记忆（时间序）
	turns      int      // 已完成对话轮数
}

// newClockRuntime 健康状态：时钟同步。
func newClockRuntime(nowMS int64) clockRuntime {
	return clockRuntime{nowMS: nowMS, ntpOK: true}
}

// injectClockDrift 注入（三段之一）：NTP 失效，本地时钟累计漂移 driftMS。
func injectClockDrift(r clockRuntime, driftMS int) clockRuntime {
	r.ntpOK = false
	r.driftMS = driftMS
	r.nowMS += int64(driftMS)
	return r
}

// timeWritesSuspended 系统响应（三段之二）判定：漂移超阈 → 时间类记忆停写。
func timeWritesSuspended(r clockRuntime) bool {
	return !r.ntpOK && absInt(r.driftMS) > clockDriftToleranceMS
}

// clockTurn 系统响应（三段之二）：一轮对话。非时间类记忆照常写；
// 时间类记忆仅在时钟可信（未停写）时写入。
func clockTurn(r clockRuntime, fact string, stampMS int64) clockRuntime {
	r.turns++
	r.factWrites = append(r.factWrites, fact)
	if !timeWritesSuspended(r) {
		r.timeWrites = append(r.timeWrites, stampMS)
	}
	return r
}

// clockResync 恢复（三段之三）：NTP 校时成功，漂移清零，时间类记忆恢复写入。
func clockResync(r clockRuntime, correctedMS int64) clockRuntime {
	r.ntpOK = true
	r.driftMS = 0
	r.nowMS = correctedMS
	return r
}

// absInt 整数绝对值。
func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func TestClockDriftNTPFailure(t *testing.T) {
	row, ok := Row(RowClock)
	if !ok {
		t.Fatalf("矩阵行 %s 未登记", RowClock)
	}
	if row.Gate != GateG1 {
		t.Fatalf("%s 门禁级别=%s，须 G1（合并阻断）", row.ID, row.Gate)
	}

	const base int64 = 1_700_000_000_000
	r := newClockRuntime(base)
	r = clockTurn(r, "用户喜欢恐龙", base+1000)
	if got := len(r.timeWrites); got != 1 {
		t.Fatalf("健康期时间类记忆写入=%d 条，须 1", got)
	}

	// ── 三段之一：注入（NTP 失效，漂移 8s 超阈）──
	r = injectClockDrift(r, 8000)
	if !timeWritesSuspended(r) {
		t.Fatal("漂移 8s 超阈须停写时间类记忆")
	}
	// 阈下与负向对照：阈内漂移（3s）不触发停写；同幅负漂移同样触发。
	if mild := injectClockDrift(newClockRuntime(base), 3000); timeWritesSuspended(mild) {
		t.Fatal("阈内漂移 3s 不得停写（阈值是 >5000ms）")
	}
	if back := injectClockDrift(newClockRuntime(base), -8000); !timeWritesSuspended(back) {
		t.Fatal("负向漂移 8s 超阈同样须停写")
	}

	// ── 三段之二：系统响应（时间类停写，其余不受影响）──
	frozen := len(r.timeWrites)
	factsBefore := len(r.factWrites)
	for i := 0; i < 3; i++ {
		r = clockTurn(r, "用户会跳绳", r.nowMS)
	}
	if got := len(r.timeWrites); got != frozen {
		t.Fatalf("漂移期时间类记忆写入=%d 条，须冻结在 %d（停写）", got, frozen)
	}
	if got := len(r.factWrites); got != factsBefore+3 {
		t.Fatalf("其余记忆受影响：写入=%d 条，须 %d（其余不受影响）", got, factsBefore+3)
	}
	if r.turns != 4 {
		t.Fatalf("对话轮数=%d，须 4（对话不受影响）", r.turns)
	}

	// ── 三段之三：恢复（校时恢复）──
	const corrected int64 = base + 3600_000
	r = clockResync(r, corrected)
	if timeWritesSuspended(r) {
		t.Fatal("校时后不得继续停写")
	}
	r = clockTurn(r, "用户明天想去动物园", corrected+500)
	if got := len(r.timeWrites); got != frozen+1 {
		t.Fatalf("校时后时间类记忆写入=%d 条，须 %d（恢复写入）", got, frozen+1)
	}
	if got := r.timeWrites[len(r.timeWrites)-1]; got != corrected+500 {
		t.Fatalf("恢复后时间戳=%d，须写入校准后时间 %d（非漂移读数）", got, corrected+500)
	}
}

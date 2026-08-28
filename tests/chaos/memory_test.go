// 运行时镜像，实现落地后替换：CH-04 记忆存储不可写（spec §8.3，G1）。
//
// 三段：注入（存储写失败）→ 系统响应（对话继续，新记忆停写进缓存待写）→
// 恢复（存储恢复后补写，0 丢失 0 重复）。packages/go/memory（T10）落地后
// 替换本镜像。
package chaos

import "testing"

// memoryRuntime 记忆通道镜像（纯数据）。
type memoryRuntime struct {
	storeOK   bool     // 存储可写
	committed []string // 已落盘记忆（时间序）
	pending   []string // 故障期缓存待写（时间序）
	turns     int      // 已完成对话轮数（对话必须继续）
	writeErrs int      // 故障期写失败次数
}

// newMemoryRuntime 健康状态：存储可写。
func newMemoryRuntime() memoryRuntime {
	return memoryRuntime{storeOK: true}
}

// injectMemoryFailure 注入（三段之一）：存储不可写。
func injectMemoryFailure(r memoryRuntime) memoryRuntime {
	r.storeOK = false
	return r
}

// memoryTurn 系统响应（三段之二）：一轮对话——尝试写一条新记忆。
// 不可写时对话照常完成，新记忆进缓存待写（不丢、不阻断、不重复）。
func memoryTurn(r memoryRuntime, fact string) memoryRuntime {
	r.turns++
	if r.storeOK {
		r.committed = append(r.committed, fact)
		return r
	}
	r.writeErrs++
	r.pending = append(r.pending, fact)
	return r
}

// memoryHeal 恢复（三段之三）：存储恢复，缓存整体补写（0 丢失、不重复）。
func memoryHeal(r memoryRuntime) memoryRuntime {
	r.storeOK = true
	r.committed = append(r.committed, r.pending...)
	r.pending = nil
	return r
}

func TestMemoryUnwritable(t *testing.T) {
	row, ok := Row(RowMemory)
	if !ok {
		t.Fatalf("矩阵行 %s 未登记", RowMemory)
	}
	if row.Gate != GateG1 {
		t.Fatalf("%s 门禁级别=%s，须 G1（合并阻断）", row.ID, row.Gate)
	}

	r := newMemoryRuntime()
	r = memoryTurn(r, "用户养了一只叫团子的猫")
	preFacts := append([]string(nil), r.committed...)

	// ── 三段之一：注入（存储不可写）──
	r = injectMemoryFailure(r)
	if r.storeOK {
		t.Fatal("注入后存储须标记不可写")
	}

	// ── 三段之二：系统响应（降级继续对话 + 缓存待写）──
	faultFacts := []string{"用户明天要春游", "用户喜欢蓝色", "用户怕黑", "用户妹妹叫朵朵"}
	for i, fact := range faultFacts {
		r = memoryTurn(r, fact)
		if r.turns != i+2 { // 1 轮健康期 + 已完成的故障期轮数
			t.Fatalf("第 %d 轮故障对话未完成（turns=%d），对话必须继续", i+1, r.turns)
		}
	}
	if got := len(r.committed); got != len(preFacts) {
		t.Fatalf("故障期落盘记忆=%d 条，须保持 %d（无新记忆落盘）", got, len(preFacts))
	}
	if got := len(r.pending); got != len(faultFacts) {
		t.Fatalf("缓存待写=%d 条，须 %d（全部新记忆待写）", got, len(faultFacts))
	}
	if r.writeErrs != len(faultFacts) {
		t.Fatalf("故障期写失败计数=%d，须 %d", r.writeErrs, len(faultFacts))
	}

	// ── 三段之三：恢复（补写 0 丢失）──
	r = memoryHeal(r)
	want := append(append([]string(nil), preFacts...), faultFacts...)
	if !sameElements(r.committed, want) {
		t.Fatalf("补写丢失或重复：committed=%v want=%v", r.committed, want)
	}
	if len(r.pending) != 0 {
		t.Fatalf("补写后缓存须清空，剩 %d 条", len(r.pending))
	}
	// 恢复后新写入直接落盘。
	r = memoryTurn(r, "用户今天学会了跳绳")
	if !sameElements(r.committed, append(want, "用户今天学会了跳绳")) {
		t.Fatalf("恢复后新记忆未直接落盘：committed=%v", r.committed)
	}
}

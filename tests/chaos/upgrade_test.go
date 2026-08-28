// 运行时镜像，实现落地后替换：CH-08 升级中断/包半装（spec §8.3，G0）。
//
// 三段：注入（升级中途断电/断网，目标包只装了一部分）→ 系统响应（原子
// 回滚：激活版本必须是上一完整版本，半装产物不可见）→ 恢复（重试升级，
// 完整安装后原子切换）。packages/native/edge-runtime（T14）落地后替换本镜像。
package chaos

import (
	"slices"
	"testing"
)

// upgradeRuntime 升级事务镜像（纯数据）。
type upgradeRuntime struct {
	active   string          // 当前激活版本（必须完整安装）
	complete map[string]bool // 版本 → 完整安装标记
	staged   []string        // 升级暂存区（半装残留可见性=0 的断言面）
}

// newUpgradeRuntime 健康状态：1.2.0 完整安装并激活。
func newUpgradeRuntime() upgradeRuntime {
	return upgradeRuntime{
		active:   "1.2.0",
		complete: map[string]bool{"1.2.0": true},
	}
}

// beginUpgrade 开始升级：目标版本进入暂存区。
func beginUpgrade(r upgradeRuntime, target string) upgradeRuntime {
	r.staged = append(r.staged, target)
	return r
}

// injectUpgradeInterrupt 注入（三段之一）：安装中断——目标版本只装了一部分
// （installedFraction < 100），不得标记完整。
func injectUpgradeInterrupt(r upgradeRuntime, target string, installedFraction int) upgradeRuntime {
	r.complete[target] = installedFraction >= 100
	return r
}

// previousComplete 最近的完整版本（排除 target；回滚目标）。
func previousComplete(r upgradeRuntime, target string) (string, bool) {
	var vers []string
	for v, ok := range r.complete {
		if ok && v != target {
			vers = append(vers, v)
		}
	}
	slices.Sort(vers)
	if len(vers) == 0 {
		return "", false
	}
	return vers[len(vers)-1], true
}

// atomicRollback 系统响应（三段之二）：中断后启动——半装版本不可激活，
// 原子回滚到上一完整版本；暂存区清空（磁盘上无半装残留可见）。
func atomicRollback(r upgradeRuntime, target string) upgradeRuntime {
	if r.complete[target] {
		r.active = target
		r.staged = nil
		return r
	}
	if prev, ok := previousComplete(r, target); ok {
		r.active = prev
	}
	r.staged = nil
	return r
}

// retryUpgrade 恢复（三段之三）：重试——完整安装后原子切换到新版本。
func retryUpgrade(r upgradeRuntime, target string) upgradeRuntime {
	r.complete[target] = true
	r.active = target
	r.staged = nil
	return r
}

func TestUpgradeInterrupt(t *testing.T) {
	row, ok := Row(RowUpgrade)
	if !ok {
		t.Fatalf("矩阵行 %s 未登记", RowUpgrade)
	}
	if row.Gate != GateG0 {
		t.Fatalf("%s 门禁级别=%s，须 G0（发布阻断）", row.ID, row.Gate)
	}

	r := newUpgradeRuntime()
	r = beginUpgrade(r, "1.3.0")

	// ── 三段之一：注入（升级中断，包半装 60%）──
	r = injectUpgradeInterrupt(r, "1.3.0", 60)
	if r.complete["1.3.0"] {
		t.Fatal("半装版本不得标记完整")
	}

	// ── 三段之二：系统响应（原子回滚上一完整版本）──
	r = atomicRollback(r, "1.3.0")
	if r.active != "1.2.0" {
		t.Fatalf("中断后激活版本=%s，须原子回滚到上一完整版本 1.2.0", r.active)
	}
	if len(r.staged) != 0 {
		t.Fatalf("半装残留须不可见（暂存区剩 %d 项）", len(r.staged))
	}
	if !r.complete[r.active] {
		t.Fatal("激活版本必须完整安装")
	}
	// 恢复列「重试」为人工路径：重试前终端态稳定（重启后回滚结果一致）。
	if again := atomicRollback(r, "1.3.0"); again.active != r.active || len(again.staged) != 0 {
		t.Fatalf("回滚终端态不稳定：again.active=%s staged=%d", again.active, len(again.staged))
	}

	// ── 三段之三：恢复（重试升级成功）──
	r = retryUpgrade(r, "1.3.0")
	if r.active != "1.3.0" || !r.complete["1.3.0"] {
		t.Fatalf("重试后激活=%s 完整=%v，须 1.3.0 完整激活", r.active, r.complete["1.3.0"])
	}
	if len(r.staged) != 0 {
		t.Fatalf("重试后暂存区须清空（剩 %d 项）", len(r.staged))
	}

	// 二次灾难：1.4.0 再中断 → 回滚到最近完整版本 1.3.0（而非 1.2.0）。
	r = beginUpgrade(r, "1.4.0")
	r = injectUpgradeInterrupt(r, "1.4.0", 40)
	r = atomicRollback(r, "1.4.0")
	if r.active != "1.3.0" {
		t.Fatalf("二次中断后激活版本=%s，须回滚到最近完整版本 1.3.0", r.active)
	}
	if r.complete["1.4.0"] {
		t.Fatal("1.4.0 半装标记须保持不完整")
	}
}

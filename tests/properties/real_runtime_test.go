//go:build real

// 强制接线契约（IR #65 / 审计漏洞 #3「镜像永不替换」的封口）。
//
// 本文件是 tests/properties 属性测试与 packages/go 真实实现之间的编译期接线闸门：
//
//   - 契约：packages/go/runtime-fsm（T14 四档降级 FSM）落地之日，必须提供满足
//     contract.go 全部四个行为契约（RuntimeModel/IdentityModel/BudgetModel/
//     DegradeModel）的类型，本文件方可在 -tags real 下编译通过；届时下方 init()
//     把属性测试驱动从镜像切到真身，CI-1..CI-4 断言原封不动地跑在真实实现上
//     （同一断言口径、两份驱动：默认镜像 / -tags real 真身）。
//   - 当前状态：packages/go/runtime-fsm 是空壳（仅 .gitkeep），因此
//     `go test -tags real ./tests/properties/...` 必然编译失败，失败原因指向
//     「包未实现」。这是预期且正确的强制函数：真实实现落地前，任何人无法把
//     属性测试悄悄留在镜像上还宣称测过真身。
//   - 落地调整：若某契约将来落在其他包（如 IdentityModel → packages/go/speaker），
//     把对应绑定与 init 注入改指该包即可；禁止删除本文件或摘除 build tag
//     （等于重新打开审计漏洞 #3）。
//   - CI 默认 tag 集不含 real（issue #65）：默认构建完全不编译本文件，
//     由 real_guard_test.go 的 t.Skip 守卫提示启用方式、防 CI 误红。
package properties

import (
	"testing"

	fsm "github.com/Cloudbird-Software/AI_Toy/packages/go/runtime-fsm"
)

// 接口绑定声明（编译期 satisfies 检查）：runtime-fsm 真身须完整实现四个行为契约。
// fsm.Runtime 为约定的绑定类型名；落地实现若为构造函数（fsm.New）或分域多类型，
// 在此同步调整即可——调整本身就是「接线」的显式动作。
var (
	_ RuntimeModel  = fsm.Runtime{}
	_ IdentityModel = fsm.Runtime{}
	_ BudgetModel   = fsm.Runtime{}
	_ DegradeModel  = fsm.Runtime{}
)

// init 把属性测试驱动切换为真身（本文件编译通过即接线完成）。
func init() {
	testRuntime = fsm.Runtime{}
	testIdentity = fsm.Runtime{}
	testBudget = fsm.Runtime{}
	testDegrade = fsm.Runtime{}
}

// TestRealRuntimeWired 验证 -tags real 下四份驱动均已脱离镜像（init 接线生效）。
func TestRealRuntimeWired(t *testing.T) {
	for name, m := range map[string]any{
		"testRuntime":  testRuntime,
		"testIdentity": testIdentity,
		"testBudget":   testBudget,
		"testDegrade":  testDegrade,
	} {
		switch m.(type) {
		case MirrorRuntime, MirrorIdentity, MirrorBudget, MirrorDegrade:
			t.Errorf("%s 仍绑定镜像实现——init 接线未生效", name)
		}
	}
}

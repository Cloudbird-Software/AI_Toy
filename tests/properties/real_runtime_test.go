//go:build real

// 强制接线契约（IR #65 / 审计漏洞 #3「镜像永不替换」的封口）。
//
// 本文件是 tests/properties 属性测试与 packages/go 真实实现之间的编译期接线闸门：
//
//   - 契约：packages/go/runtime-fsm（T14 四档降级 FSM）已落地，提供满足
//     contract.go 全部四个行为契约（RuntimeModel/IdentityModel/BudgetModel/
//     DegradeModel）的 fsm.Runtime 类型；本文件在 -tags real 下编译通过，
//     init() 把属性测试驱动从镜像切到真身，CI-1..CI-4 断言原封不动地跑在
//     真实实现上（同一断言口径、两份驱动：默认镜像 / -tags real 真身）。
//   - 外部测试包（package properties_test）：runtime-fsm 真身 import
//     tests/properties（契约签名类型，m3-spec §2 唯一例外），内部测试包
//     再 import runtime-fsm 会成环——接线走 contract.go 的导出注入面
//     （BindRuntime 等），断言本体（ci1..ci4_test.go，内部包）经注入变量
//     驱动真身，口径不变。
//   - 纪律：禁止删除本文件或摘除 build tag（等于重新打开审计漏洞 #3）；
//     契约若分域落在别的包，在此同步调整绑定与 init 注入即可——调整本身
//     就是「接线」的显式动作。
//   - CI 默认 tag 集不含 real（issue #65）：默认构建完全不编译本文件，
//     由 real_guard_test.go 的 t.Skip 守卫提示启用方式、防 CI 误红。
package properties_test

import (
	"testing"

	"github.com/Cloudbird-Software/AI_Toy/packages/go/runtime-fsm"
	"github.com/Cloudbird-Software/AI_Toy/tests/properties"
)

// 接口绑定声明（编译期 satisfies 检查）：runtime-fsm 真身须完整实现四个行为契约。
var (
	_ properties.RuntimeModel  = runtimefsm.Runtime{}
	_ properties.IdentityModel = runtimefsm.Runtime{}
	_ properties.BudgetModel   = runtimefsm.Runtime{}
	_ properties.DegradeModel  = runtimefsm.Runtime{}
)

// init 把属性测试驱动切换为真身（本文件编译通过即接线完成；先于全部测试
// 函数执行——断言本体经注入变量读真身）。
func init() {
	properties.BindRuntime(runtimefsm.Runtime{})
	properties.BindIdentity(runtimefsm.Runtime{})
	properties.BindBudget(runtimefsm.Runtime{})
	properties.BindDegrade(runtimefsm.Runtime{})
}

// TestRealRuntimeWired 验证 -tags real 下四份驱动均已脱离镜像（init 接线生效）。
func TestRealRuntimeWired(t *testing.T) {
	for name, m := range map[string]any{
		"testRuntime":  properties.CurrentRuntime(),
		"testIdentity": properties.CurrentIdentity(),
		"testBudget":   properties.CurrentBudget(),
		"testDegrade":  properties.CurrentDegrade(),
	} {
		switch m.(type) {
		case properties.MirrorRuntime, properties.MirrorIdentity, properties.MirrorBudget, properties.MirrorDegrade:
			t.Errorf("%s 仍绑定镜像实现——init 接线未生效", name)
		}
	}
}

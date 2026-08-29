// real 接线守卫（IR #65）：默认构建下提示 real 接线测试的启用方式，防 CI 误红。
// 接线本体见 real_runtime_test.go（//go:build real）——packages/go/runtime-fsm
// 空壳期该 tag 下编译必失败（强制函数），故 CI 默认 tag 集不含 real。
package properties

import "testing"

// TestRealWiringNeedsTag 守卫测试：real 接线测试需 -tags real。
func TestRealWiringNeedsTag(t *testing.T) {
	t.Skip("real 接线测试需 -tags real（packages/go 空壳期该 tag 编译必失败=强制函数，勿入默认 CI）")
}

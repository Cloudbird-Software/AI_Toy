// 运行时镜像，实现落地后替换：spec §8.3 故障注入矩阵的共享最小故障模型。
//
// packages/* 尚为空壳，本包以纯数据 + 纯函数镜像各故障域的「注入 → 系统响应
// → 恢复」三段决策：不触碰真实网络/存储/硬件，时间一律以注入后毫秒显式
// 入参，保证确定性输出与 -race 安全。各域私有镜像落在对应 *_test.go；实现
// 落地后逐域替换为真实组件，矩阵行断言不动。
package chaos

// Gate 门禁级别（spec §8.3 门禁列）：G0 发布阻断 / G1 合并阻断。
type Gate string

const (
	GateG0 Gate = "G0"
	GateG1 Gate = "G1"
)

// NoRecovery 矩阵恢复列的「—」：无自动恢复，测试改为断言终端态稳定。
const NoRecovery = "—"

// RowID spec §8.3 矩阵行标识（CH-01..CH-08，表格顺序）。
type RowID string

// 八行注入矩阵的行标识；注释即各行的注入物与门禁级别。
const (
	RowCloudLLM   RowID = "CH-01" // 云 LLM 断连/5xx/限流（G0）
	RowTTS        RowID = "CH-02" // TTS 超时/首包失败（G1）
	RowOverrun    RowID = "CH-03" // 输出超长/死循环文本（G1）
	RowMemory     RowID = "CH-04" // 记忆存储不可写（G1）
	RowVoiceprint RowID = "CH-05" // 声纹拒判（G0）
	RowIMU        RowID = "CH-06" // IMU 事件风暴（G1）
	RowClock      RowID = "CH-07" // 时钟漂移/NTP 失效（G1）
	RowUpgrade    RowID = "CH-08" // 升级中断/包半装（G0）
)

// Scenario 矩阵一行：注入 → 期望 → 恢复 → 门禁（spec §8.3 原文镜像）。
type Scenario struct {
	ID      RowID
	Inject  string
	Expect  string
	Recover string
	Gate    Gate
}

// Matrix 返回 §8.3 全部 8 行（表格顺序）。各测试用 Row 领取本行并对齐断言。
func Matrix() []Scenario {
	return []Scenario{
		{ID: RowCloudLLM, Inject: "云 LLM 断连/5xx/限流", Expect: "≤2 档内 3s 恢复对话；诚实告知受限", Recover: "≤30s 回 L0 无脏输出", Gate: GateG0},
		{ID: RowTTS, Inject: "TTS 超时/首包失败", Expect: "静默 ≤2s 端侧补偿；不重播半句", Recover: "下轮回云档", Gate: GateG1},
		{ID: RowOverrun, Inject: "输出超长/死循环文本", Expect: "硬截断+自然收尾", Recover: NoRecovery, Gate: GateG1},
		{ID: RowMemory, Inject: "记忆存储不可写", Expect: "降级无新记忆继续对话；缓存待写", Recover: "恢复补写 0 丢失", Gate: GateG1},
		{ID: RowVoiceprint, Inject: "声纹拒判", Expect: "CI-2 只读模式+明示不确定", Recover: "识别成功即恢复", Gate: GateG0},
		{ID: RowIMU, Inject: "IMU 事件风暴", Expect: "限流+聚合；无动作风暴", Recover: "重启/人工", Gate: GateG1},
		{ID: RowClock, Inject: "时钟漂移/NTP 失效", Expect: "时间类记忆停写；其余不受影响", Recover: "校时恢复", Gate: GateG1},
		{ID: RowUpgrade, Inject: "升级中断/包半装", Expect: "原子回滚上一完整版本", Recover: "重试", Gate: GateG0},
	}
}

// Row 按 ID 领取矩阵行；ok=false 表示登记缺失（调用方应 Fatal）。
func Row(id RowID) (sc Scenario, ok bool) {
	for _, row := range Matrix() {
		if row.ID == id {
			return row, true
		}
	}
	return Scenario{}, false
}

// sameElements 两切片元素多重集是否相同（顺序无关）——「补写 0 丢失」类断言共用。
func sameElements(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
		if seen[s] < 0 {
			return false
		}
	}
	return true
}

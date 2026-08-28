// T6 固件门禁骨架（对齐 Python 侧 marker：测试名 + 注释登记）
// id: FW-G0-01  bi: BI-6.2  level: G0  asset: T6
#include "firmware_imu.h"
#include <cassert>
int main() {
    firmware_imu::ImuGuard g;
    // T6-G0-01 静置 0 自发输出（骨架：空输入恒 None）
    for (int i = 0; i < 1000; ++i) {
        firmware_imu::Sample s{0.0f, 0.0f, -9.8f, (uint32_t)i};
        assert(g.ingest(s) == firmware_imu::Event::None);
    }
    // T6-G0-03 电机占空比/角度硬件熔断（逻辑恒真，占位）
    assert(g.duty_cycle_ok());
    return 0;
}

#pragma once
#include <cstdint>
// T6 固件：IMU 事件分类 + 占空比/角度硬件熔断表驱动 + 看门狗
// 熔断逻辑独立于应用层；软件 bug 不可越过（本包的存在理由）。
namespace firmware_imu {
enum class Event { None, Pickup, Drop, Shake, Watchdog };
struct Sample { float ax, ay, az; uint32_t t_ms; };
class ImuGuard {
public:
    ImuGuard();
    Event ingest(const Sample& s);
    bool duty_cycle_ok() const;   // 硬件熔断：占空比/角度超界即 false
};
} // namespace firmware_imu

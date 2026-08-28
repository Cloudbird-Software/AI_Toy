#include "firmware_imu.h"
namespace firmware_imu {
ImuGuard::ImuGuard() = default;
Event ImuGuard::ingest(const Sample&) {
    // TODO: 表驱动特征阈值 + 熔断查表
    return Event::None;
}
bool ImuGuard::duty_cycle_ok() const {
    // TODO: 熔断寄存器镜像；真机写入硬件定时器
    return true;
}
} // namespace firmware_imu

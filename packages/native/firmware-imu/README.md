# firmware-imu (C++ T6)
T6 固件熔断层：IMU 事件 + 占空比/角度硬件熔断 + 看门狗。
验收：docs/gates/assets/T6.md；阈值：configs/gates/T6.yaml。
命令：cmake -S . -B build && cmake --build build && ctest --test-dir build；just gate T6 all

# AGENTS.md — IMU 固件（C++，T6 熔断层）
验收协议：docs/gates/assets/T6.md　阈值：configs/gates/T6.yaml（禁改）
## 本包边界
T6 的"最后保险"：IMU 事件分类 + 占空比/角度硬件熔断表驱动 + 看门狗。必须独立于 Python 侧 packages/py/imu（软件层双保险，但熔断只在本包）。
## 技术路径（指导）
A 滑窗特征 + 决策树/SVM（零框架依赖，可审计）｜B Google Personify 个人化异常检测（C 绑定）｜C TFLite Micro 1D-CNN（仅动作增强层）
## 本地命令
cmake -S . -B build && cmake --build build && ctest --test-dir build；组合级 just gate T6 all
## 本地必绿再提 PR
T6-G0-01 静置 0 自发输出(12h×3台)｜T6-G0-02 摔落≤2s停机｜T6-G0-03 硬件熔断(软件bug不可越过)｜T6-G1-01 拿起≥98%
## 数据依赖
台架脚本(真机台架=holdout) + 合成加速度曲线；真机 3 台为 holdout 侧（经 tools/holdout）
## 本包禁令（叠加根 AGENTS.md）
- 熔断逻辑必须独立于应用层；软件 API 无法关闭/放宽
- 马达边界必须在固件层完成，软件层仅作双保险非唯一保险
## 常见坑
摔落触发与正常放下阈值边界（台架 ≥30 次回归）；看门狗喂狗时序不得让应用层控制。

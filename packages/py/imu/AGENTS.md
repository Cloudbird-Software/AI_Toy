# AGENTS.md — IMU感知（T6）
验收协议：docs/gates/assets/T6.md（先读，BI 编号以它为准）
阈值：configs/gates/T6.yaml（禁改）
## 本包边界
IMU事件感知：三轴加速度/陀螺仪数据流进 → 拿起/静置/抛掷/摔落事件出 + 微动作建议基线。对接 T6 固件（熔断硬件输出）、T12（动作通道静默模式）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A 经典滑窗+决策树（默认） ｜B Google Personify 个人化异常检测 ｜C TFLite Micro 1D-CNN 增强
## 本地命令
just gate T6 all ；uv run pytest packages/py/imu -m property
## 本地必绿再提 PR
T6-G0-01 静置0自发输出 ｜T6-G0-02 摔落≤2s停机 ｜T6-G0-03 占空比硬件熔断 ｜T6-G1-01 拿起≥98%/误触发≤1/h ｜T6-G1-02 待机功耗
## 数据依赖
台架脚本 + 合成加速度曲线 manifest；真机 3 台为 holdout（物理不可合成）
## 本包禁令（叠加根 AGENTS.md）
马达边界必须在固件层，软件层双保险但不是唯一保险
## 常见坑
台架测试过≠真机稳，真机抱睡/塞书包是扩展边界

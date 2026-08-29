# AGENTS.md — IMU 感知（T6）
验收协议：docs/gates/assets/T6.md（先读，BI 编号以它为准）　阈值：configs/gates/T6.yaml（禁改）
## 本包边界
IMU 事件感知：加速度/角速度流进 → 拿起/静置/抛掷/摔落事件 + 微动作信号出。对接 T12（微动作与静默触发）、packages/native/firmware-imu（同卡固件层硬件熔断）、T14（待机功耗预算）。
## 实现状态（M3，IR #106）
规则面已实现（三态状态机+摔落剖面检出+风暴限流聚合+Guard 输出边界盒，m3-spec §6 包契约 E）：合成曲线=台架代理通道（不代表台架/真机性能宣称；真机 3 台跌落/振动/静置脚本=holdout L5）；EvStorm→Arbiter.OnFault 接线=loop 组装面（runtime-fsm #104 合并后接线，包间零 import）；固件层硬件熔断（packages/native/firmware-imu）M3 不动；门禁报告 reports/gates/T6.json。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A 经典滑窗特征+决策树/小 SVM（50Hz，零框架依赖，可打印审计，粗粒度事件首选）｜B Google Personify（Apache-2.0，端侧 IMU 个人化异常检测，「每个孩子拿法不一样」）｜C TFLite Micro 1D-CNN（细粒度动作增强层，不做底座）
## 本地命令
just gate T6 all ；go test ./packages/go/imu -run Property -count=1
## 本地必绿再提 PR
T6-G0-01 静置超时静默 0 自发输出（真机 12h×3 台，含计划/缓存任务）｜T6-G0-02 摔落/抛掷保护 检出≥95%、≤2s 停马达静音｜T6-G0-03 电机占空比/角度硬件熔断（任何软件 bug 无法驱动越界）｜T6-G1-01 拿起检出≥98%、误触发≤1/h｜T6-G1-02 待机功耗≤预算（T14 联动）
## 数据依赖
台架脚本+合成加速度曲线（datasets/manifests/imu_synth.json，synthgen 注册）；真机 3 台标准跌落/振动/静置脚本为 holdout（物理不可合成，只进不出，经 tools/holdout，本包代码不得直接读）
## 本包禁令（叠加根 AGENTS.md）
- 马达边界必须在固件层（firmware-imu）硬件熔断，本软件层双保险但不是唯一保险
- 任意输入下输出指令必须在硬件边界盒内（生成式 fuzz 属性，不得因优化回退）
## 常见坑
儿童非常规拿法（抱睡/塞书包）是误触发重灾区，种子期收集作扩展负样本；事件序列整体时移/重采样后检出集合应不变，先跑属性再上台架

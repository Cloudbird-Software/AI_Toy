# Holdout 双轨制与密封访问
三轨：合成 holdout（生成时 8:2 切出，只做回归，永不进训练；随管线版本化）｜真实 holdout（亲友录音/种子家庭日志/真机采集；只进不出；60–90 天轮换评估防被记住）｜canary（真实 holdout 固定小子集；永不参与调参；掉分=G0 阻断发布）。
污染防护：(1)评测集与 prompt/few-shot 全版本化入库；(2)评测代码与被评系统 prompt 资产分开评审（防对着考卷优化）；(3)真实数据入 holdout 前与训练集 minhash 去重；(4)每季按 Google ML Test Score 28 项自审（G2 只升不降）。
仓库落地：datasets/holdout/ 仅 sealed-manifest；数据本体在受控对象存储；访问只能经 tools/holdoutctl 且仅在 environment=holdout 的 runner（无外网出口）；作业只输出聚合指标，n<5 切片抑制（k-匿名）；全程 audit log。
红队 holdout（T9 专用）：外部/独立 agent 攻击队每次发布前新鲜构造，开发 agent 不可见；攻击集版本化入库，与代码同级保护，只增不删。

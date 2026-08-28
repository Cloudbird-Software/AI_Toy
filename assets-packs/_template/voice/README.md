# voice/（spec §10：voice_ref.wav + LICENSE）

`manifest.json` 的 `voice_ref` 指向本目录的 `voice_ref.wav`（音色参考/授权锚点）。

**wav 本体不入 git**（仓库纪律：任何二进制音频不入版本库，见 `.gitattributes`/`.gitignore`）。
打包时由内容管线（T18）从受控资产库注入，并在 LICENSE 中登记来源与授权。

- 授权与来源登记：见本目录 `LICENSE`（占位说明）。
- 红线：禁止克隆真实儿童声音（AGENTS.md T13 红线）；音色须为合成或经书面授权的成人配音。
- 音色规格建议：单声道、16 kHz 起步（对齐端侧 TTS 采样率）、时长 3–10 秒的代表性语句。

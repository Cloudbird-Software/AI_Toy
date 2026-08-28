# goodnight-bear voice/（spec §10：voice_ref.wav + LICENSE）

`manifest.json` 的 `voice_ref` 指向本目录的 `voice_ref.wav`（晚安熊音色参考）。

**wav 本体不入 git**（二进制音频不入版本库）：种子包阶段未录制/未选定音色，
由内容管线（T18）在打包时注入受控资产库中的授权音色，并在注入时补一份 LICENSE 登记来源与授权。

- 红线：禁止克隆真实儿童声音（AGENTS.md T13 红线）；音色须为合成或书面授权的成人配音。
- 音色取向：低语速、低基频、轻气声（对齐 persona 低唤醒设定）；单声道 16 kHz 起步，3–10 秒代表句。
- 音量上限由 manifest.permissions.volume_max=0.5 约束（晚安场景安静上限）。

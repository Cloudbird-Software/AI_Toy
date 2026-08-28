# edge-runtime (Rust, T14)
T14 端侧四档降级 FSM + 推理宿主 + 崩溃安全。
验收：docs/gates/assets/T14.md；阈值：configs/gates/T14.yaml。
命令：cargo clippy -p edge-runtime -- -D warnings；cargo nextest run -p edge-runtime；just gate T14 all

// T14 端侧运行时（Rust）：四档降级 FSM / GGUF 推理宿主 / 内存预算 / panic 捕获
// 安全：workspace.lints.rust.unsafe_code = forbid
// 实现由开发 agent 完成；骨架仅暴露占位。

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Tier { L0, L1, L2, L3 }

#[derive(Debug, Default)]
pub struct Runtime { tier: Tier }

impl Runtime {
    pub fn new() -> Self { Self { tier: Tier::L3 } }
    pub fn tier(&self) -> Tier { self.tier }
    pub fn step(&mut self, _event: &str) -> Result<(), &'static str> {
        // TODO: 表驱动 FSM 穷举先行
        Err("edge-runtime: FSM 未实现")
    }
}

# 安全政策（Security Policy）

本仓是 Cloudbird-Software 的组织模板仓（新仓由此派生）。本文件同时作为派生仓
SECURITY.md 的基线——新仓接入时按仓自身攻击面增补「范围」一节即可。

## 支持版本

| 版本         | 支持状态 |
| ------------ | -------- |
| main（滚动） | ✅       |

## 报告漏洞

**禁止用公开 issue / PR / 讨论报告安全漏洞。**

请使用 GitHub 私密漏洞报告（Private Vulnerability Reporting）：
本仓 Security 标签页 → 「Report a vulnerability」。

- 请包含：影响面描述、复现步骤/POC、受影响提交或版本、（如有）缓解建议。
- 我们会在确认后尽快回复，修复进度通过该私密通道同步。
- 组织治理面（ruleset/App 权限/workflow 钉扎等）的缺陷同样走此通道。

## 范围

- 本仓代码与 CI 质量门（`quality/`）。
- **不在范围**：由本仓派生的下游仓代码（各自仓库各自报告）；第三方依赖的
  上游漏洞（先报上游 CVE，再走 Dependabot 升级通道）。

## 处置原则

- 安全修复遵循常规 PR 流（gate 全绿），但不得在修复合并前公开披露细节。
- 依赖升级一律走 Dependabot PR（SC 系列），禁手工改 lockfile 绕过审计。

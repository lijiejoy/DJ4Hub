# 文档目录

## 持续维护

| 文档 | 用途 |
| --- | --- |
| [GitHub 维护指南](github-maintenance.md) | 环境、检查、打包、上传与发布 |
| [开发约定](../CONTRIBUTING.md) | 改动边界、测试与评审 |
| [源码结构](../SOURCE_STRUCTURE.md) | 目录及职责入口 |
| [macOS 使用说明](../MACOS.md) | 完整 App 与旧 CLI 分发的区别 |
| [SIP/音频实现](../internal/sip/README.md) | 通话实现、设备准备和已知限制 |

## 阶段记录

以下文档保留当时的环境、路径和测试结论。代码位置可能随重构变化；不能把历史“通过”当作当前版本的实机验收。

| 文档 | 用途 |
| --- | --- |
| [phone-logic 合并到 main](2026-11-08-phone-logic-main-merge.md) | main 优先的合并取舍、接线修复、验证和待验收边界 |
| [2026-11-08 仓库整理](2026-11-08-repository-cleanup.md) | 本次整理范围和验证 |
| [功能优化与逐项验证](2026-11-07-feature-optimization-and-verification.md) | 优化、自动检查、实机读取与待验收项 |
| [最终修复计划](2026-11-07-final-repair-plan.md) | 仍需推进的安全、设备及业务工作 |
| [功能梳理](2026-11-07-feature-audit.md) | 功能清单与验证边界 |
| [问题清单](2026-11-07-project-issue-review.md) / [独立评审](2026-11-07-repair-plan-assessment.md) | 问题来源与修复方案判断 |
| [单一 App 整合](2026-11-07-unified-macos-app.md) | 安装、后台归属和迁移 |
| [原生通话面板](2026-11-07-native-call-panel.md) | 键盘、来电、静音和服务接线 |
| [首声等待](2026-11-07-call-startup-latency.md) / [连续通话](2026-11-07-consecutive-call-fix.md) | 通话专项修复记录 |

`images/` 保存文档图片，`design/` 保存通话面板设计资料，`superpowers/specs/` 保留早期设计方案。真实设备日志、运行缓存和测试截图放在忽略的 `output/` 或应用数据目录中。

# phone-logic 合并到 main

日期：2026-11-08。

## 合并范围与取舍

- 基础：main `8c0b493`；合入：feature/phone-logic `668e0e4`。
- 按要求以 main 为准。Git 没有产生文本冲突，但检查发现此前分支合并有行为遗漏，因此增加了必要的接线修复。
- 保留 main 的 SwiftUI 原生 App、`/api/calls`、通信历史及备份接口，以及 USB 接口身份校验、流量计数重置处理和 macOS 15 CI。
- 合入状态栏客户端、SIP 通话、按 SIM 组织的历史、会话、USSD、运营商与小区操作、任务及通知等分支功能。

## 集成修复

1. 恢复 main 的 `scripts/package-macos-app.sh` 参数与原生 App 打包实现。`package-macos-native-app.sh` 保留为兼容入口；状态栏归档改用 `package-macos-statusbar-app.sh`，Makefile 和发布工作流明确调用后者。
2. 恢复 main 通话历史监控和历史备份后台任务。
3. 恢复收发短信写入 main 通信历史的调用，同时保留分支新增的历史和发件箱记录。发送后归档失败仍返回“操作已执行，请勿直接重试”，避免重复发送。
4. 恢复短信轮询互斥、卡身份变化时重置长短信重组器，以及 SM/ME 来源标记。
5. 每轮对全部读取到的短信重试持久化，不能因为短信已进入内存缓存就跳过数据库写入。任一历史保存失败时阻止自动清理，并暴露轮询错误。增加“保存失败、重复读取仍失败、数据库恢复后补存”的回归测试。
6. SIP 监听和轮询启动前安装历史回调，避免首次通话早于短信轮询或历史页面访问而漏记。

## 本地验证

| 检查 | 结果 |
| --- | --- |
| `make check test test-client` | 通过：格式、脚本、仓库检查、Go 测试、静态检查、Swift 服务生命周期 |
| `make test-race` | 通过 |
| `make test-nocgo` | 通过 |
| `node --test scripts/*.test.cjs` | 10 项通过，包含网页通话与流量回归 |
| `swift build --package-path apps/hub-macos` | 通过 |
| 原生流量独立 Swift 测试 | 通过：USB 替换、计数回退、采样间隔、断开 |
| `make build client test-demo` | 通过：服务及客户端构建、独立演示启动、页面资源与只读 API |

测试使用独立演示进程和测试数据库；没有更换正在运行的 App，没有真实拨号或发送短信。原生 App 的完整 XCTest 套件在本机 Command Line Tools 环境受限，本次以编译及独立 Swift 流量测试验证，未将其计为完整 XCTest 通过。

## 后续边界

- SwiftUI App 使用 `/api/calls`，状态栏和网页 SIP 流程使用 `/api/sip`。保留两条链路不代表已经完成单一通话控制器改造，不应同时用两个界面控制同一通话。
- `communication-history.sqlite` 与 `internal/history` 的存储仍并存。补回现有调用不等于完成历史数据迁移、统一查询与统一备份；main 的备份接口覆盖 main 通信历史。
- 自动清理模块短信仍沿用整库 ME 清理机制。后续应改成对成功归档的具体索引逐条删除，以保护尚未完成重组或扫描期间新到的短信。
- 真实连续来电、双向声音、回声与首声延时、4G 实际联网、通知到达及安装包签名分发仍需设备验收。自动测试通过不等同于这些项目已实机通过。
- 远端 CI 结果以此次合并提交在 GitHub Actions 中的执行记录为准。

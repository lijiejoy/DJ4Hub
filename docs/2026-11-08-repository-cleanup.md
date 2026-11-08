# GitHub 上传前代码整理记录

日期：2026-11-08，Asia/Shanghai。

## 结果

已整理当前工作区，保留此前新增的电话、Swift 状态栏、通知、任务、无线管理等功能，补齐源码结构、检查入口、打包流程与维护说明。未创建 Git 提交、未推送远端、未替换当前安装运行的 App。

当前分支仍为 `repair/2026-11-07`，origin 仍为原有 DJ4Hub 仓库。新增功能中有大量尚未跟踪的源码，上传时必须一并暂存，不能只使用 `git commit -a`。

上传步骤见 [GitHub 维护指南](github-maintenance.md)，后续开发约定见 [CONTRIBUTING.md](../CONTRIBUTING.md)。

## 源码职责

| 原位置 | 整理后 |
| --- | --- |
| `main.go` 的共享状态 | `app.go` |
| `main.go` 的路由、响应和页面嵌入 | `http.go` |
| 设备发现、状态、传输 | `device_discovery.go`、`device_status.go`、`device_transport.go` |
| 短信轮询/API、USB PDU | `sms_handlers.go`、`sms_transport.go` |
| 网络诊断、流量和网卡 | `network_handlers.go` |
| eSIM 操作及备注 | `esim_handlers.go`、`esim_notes.go` |
| 通知配置、分发、通道发送 | `notify_settings.go`、`notify_dispatch.go`、`notify_senders.go` |
| Bot 与来电轮询 | `telegram_bot.go`、`call_poll.go` |

`main.go` 从 2727 行缩为 196 行，保留启动装配与退出；`notify_handlers.go` 从 1449 行缩为 248 行，保留 HTTP 处理。SIP 协议、USB 音频与原生面板继续使用既有包边界。

使用 Go 语法树定位声明并机械迁移，重新分组 import。与修改前快照逐一比较，191 个被整理声明的代码 token 保持一致；仅删除一个已无调用的 `truncateForLog`。没有为目录整理改变设备或音频算法。

## 上传范围与隐私

- 测试中残留的真实用户手机号已替换为虚构测试号码。
- 根目录的个人 LaunchAgent 配置移为 `packaging/macos/com.dj4hub.local.plist.example`，使用 `__HOME__` 占位符，指向 App 内服务。
- `.gitignore` 补充运行配置、凭据、签名文件、录音和整个 `output/`；生成物留在磁盘，不纳入上传。
- 启动/构建/测试 shell 脚本统一为可执行文件；保留旧启动器和旧 CLI 打包入口。
- 新增候选文件检查及自测：检查当前跟踪文件和未忽略新文件，阻止常见私钥、访问令牌、本机路径、运行文件和编译产物。仅报告文件名及原因，不回显秘密正文。
- 保留根许可证、第三方来源和本地替换模块；完整 App 新增第三方声明及实际 Go 依赖图的根许可证/NOTICE 收集。

该检查不覆盖完整 Git 历史、图片 OCR 或所有敏感信息格式；最终提交仍需查看暂存 diff 和截图。

## 维护与交付

新增 `Makefile`，统一源码检查、Go/Swift/原生面板测试、隔离 HTTP 检查、构建与打包。

新增文档目录、GitHub 维护指南、贡献说明和 Issue/PR 模板。历史验证报告保持原日期和结论，并在索引中明确其历史边界。修正 README 中通知数据流和 SIP 密码日志的过时说明。

CI 配置：

- Linux 无 cgo 测试和仓库检查。
- macOS 15 Intel/Apple Silicon 原生测试、竞争检查、Swift 服务测试和构建。
- 构建后运行隔离演示服务，检查内嵌网页、资源、状态接口与来源限制，结束后自动退出。
- 工作流依赖固定到核对过的上游提交；配置通过 actionlint 检查。

Release 配置：

- `v*` 标签分别构建 Intel/Apple Silicon 的完整 App ZIP 和 SHA-256。
- 两个构建任务成功后创建草稿，不直接作为已实机验收版本发布。
- ADB、libusb 和相应声明随 App 打包；保留旧 CLI 脚本作为兼容路径。
- 标签版本经过字符校验，数值版本写入打包副本，不改写源码 plist。

`go.mod` 补记 QMI 既有依赖链中的 `netlink v1.3.3` 与 `netns v0.0.5`，对应校验已在原 `go.sum` 中存在。没有批量升级依赖。

本地替换模块仍是裁剪版本，全平台 `go mod tidy -diff` 会因缺少上游 Windows/依赖自身测试包而失败。本轮保留该边界，在维护指南中给出处理方式，没有通过删除 replace 或偷偷升级上游来掩盖问题。

## 验证结果

| 项目 | 结果 |
| --- | --- |
| 191 个整理声明与快照逐一比较 | 通过，业务 token 不变 |
| `make check` | 通过，包含格式、脚本、JS、仓库候选检查和检查器自测 |
| `make test` | 修正下述已有测试时序问题后通过，包含 Go 全量测试与 vet |
| `make test-race` | 通过 |
| `make test-nocgo` | 通过，当前 macOS 无 cgo 路径 |
| `make test-client` | 通过 |
| `make test-native` | 通过，不实际拨号 |
| `make test-demo` | 通过，隔离 HOME、随机 HTTP 端口、九个内嵌入口及资源、只读接口、跨源拒绝；进程退出并清理 |
| MBIM 读失败用例重复 1000 次 | 通过 |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -mod=readonly ./...` | 通过，交叉编译，不代表 Linux 测试已执行 |
| `go mod verify` | 通过 |
| CI/Release YAML 解析及 actionlint v1.7.9 | 通过 |
| `make package-app VERSION=dev` | Intel 本机完整构建与打包通过 |
| ZIP 完整性、SHA-256、App 深度签名校验 | 通过 |

首次全量测试暴露 `TestDeviceReadErrorFailsPendingCommand` 的既有时序问题：OPEN 响应在请求注册前预入队，可能被读循环提前丢弃。测试改为请求写入后再返回对应事务的响应/读错误，并使用有期限上下文、准确断言读错误。只改测试替身，未改变生产 MBIM 实现；后续重复测试和全量测试均通过。

演示检查最初误将演示运营商固定结果当作真实设备 idle 状态，已按接口原有语义修正检查：演示返回 `demo=true/state=done`，真实设备空闲 GET 的无副作用行为由已有单元测试覆盖。

产物：

`dist/release/DJ-4G-Hub-App-macOS-amd64-dev.zip`

SHA-256：

`24f221e3d445bd7fd22a7586a40669f671acdb0b18141d05a09c8522e36efd96`

构建仍有重复 `-lobjc` 链接警告，不影响本轮结果。本地 App 仍为临时签名，最低系统检测为 macOS 15.0。

GitHub Actions 尚未在远端执行，Apple Silicon 构建/云端 Linux 测试、发布资产下载和真实业务验收仍需上传后验证。当前音质、延迟、麦克风授权与硬件专项待办继续沿用 [功能验证报告](2026-11-07-feature-optimization-and-verification.md) 和 [修复计划](2026-11-07-final-repair-plan.md)，不能因整理代码而标为完成。

## 备份

修改前源码快照保存在仓库外：

`~/Library/Application Support/DJ 4G Hub/source-backups/2026-11-08-before-repository-cleanup.tar.gz`

快照包含当时的源码、未跟踪新增功能、文档、构建配置和旧个人启动模板。恢复时先解压到独立目录核对，避免覆盖后续改动；不要上传该个人快照。

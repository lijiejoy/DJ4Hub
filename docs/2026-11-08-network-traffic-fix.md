# 4G 流量统计修复与主分支适配

日期：2026-11-08。

## 修复范围

原实现把第一个启用、且名称不是 `en0` 的以太网接口作为 4G 网卡。本机模块接口为 `en15`，却统计到了系统的 `en6`，因此流量和网络活动不准确。

现在通过 macOS USB 注册树，将 VID/PID 为 `2ca3:4006` 的模块与其子节点的 BSD 网络接口绑定。无法验证身份时显示不可用，不把其他以太网或 Wi-Fi 接口当作模块。

- 网卡流量、本机连接和网络活动统一使用实际模块接口。
- 会话基线绑定接口与 USB 注册实例，设备重插后重新统计。
- 网页及原生 Swift 图表在实例变化或计数回退时重新采样，避免速率跳变。
- 网页读取失败时清空旧速率及上传、下载、总量。
- 网络活动仅归入模块物理接口上可确认的连接，不把默认 Wi-Fi 或 VPN 的所有连接误算成 4G。

## 主分支集成方式

以远端 `main` 的 `3914cd6` 为基线进行适配。设备身份解析放入独立的 `network_identity.go`，保留主分支的公网实测、一键网卡恢复、新原生客户端、通信记录和云盘备份实现。

开发分支使用旧 Swift 状态栏客户端，其中启动标识缓存修复为 `002c303`。主分支已改用 `apps/hub-macos` 原生客户端，不含旧 `ServiceManager.swift`，因此不将旧客户端整套移入主分支。

## 验证与口径

已通过：Go 全量测试与静态检查、后端竞争检查、USB 身份解析及误识别回归、8 项网页测试，以及本机只读 USB 身份验证。新版原生 App 已编译成功。

本机仅有 Command Line Tools，执行完整 `swift test` 时因缺少 XCTest 模块而失败，不能记作全部原生测试通过。新增 `scripts/test-network-traffic.swift`，直接编译实际的 `Service.swift` 和 `TrafficHistory.swift`，独立验证速率、USB 实例替换、计数回退、长采样间隔和断连。保留 XCTest 回归用例，供具有完整测试环境的电脑运行。

此前在开发分支安装版本上已指定 `en15` 验证真实 4G HTTP/HTTPS、DNS 和收发计数。主分支适配另行构建并用隔离演示服务验证实际网卡识别，不覆盖正在使用的 App 或运行数据；未进行真实通话和物理热插拔验收。

显示的是模块网卡收发字节，包含协议和局域网开销，不是运营商套餐账单。会话量从当前连接首次采样计算，服务重启或模块重新枚举后重新统计，尚无按月、按卡的持久流量账本。

复查命令：

```sh
go test ./... -count=1 -timeout=90s
go vet ./...
go test -race ./cmd/dj4ghub-macos -count=1 -timeout=90s
node --test scripts/*.test.cjs
swift test --package-path apps/hub-macos
DJ4HUB_TEST_NETWORK_IDENTITY=1 go test ./cmd/dj4ghub-macos -run TestLiveModuleNetworkIdentity -v -count=1
```

只有 Command Line Tools 时，原生图表专项检查：

```sh
traffic_check_dir=$(mktemp -d)
swiftc apps/hub-macos/Sources/DJ4Hub/Service.swift \
  apps/hub-macos/Sources/DJ4Hub/TrafficHistory.swift \
  scripts/test-network-traffic.swift -o "$traffic_check_dir/check-traffic"
"$traffic_check_dir/check-traffic"
```

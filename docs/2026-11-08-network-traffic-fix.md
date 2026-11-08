# 2026-11-08：4G 网卡流量识别修复

## 问题与修改

原实现把第一个启用且名称不是 `en0` 的以太网接口视为 4G 网卡。本机实际模块接口为 `en15`，却统计到了系统的 `en6`。默认路由、网卡排序及 VPN 的存在都会使这种推断不可靠。

现在读取 macOS 的 USB 设备注册树，将 VID/PID 为 `2ca3:4006` 的设备与其子节点的 BSD 网络接口绑定。只有匹配到模块身份的接口才能进入流量统计；找不到时显示不可用，不再统计其他以太网或 Wi-Fi 接口。

修改覆盖：

- 本机网络连接显示、USB 网卡存在判断、当前 4G 默认出口判断、实时速率及流量计数统一使用模块身份。
- 会话基线同时绑定接口名和 USB 注册实例，设备重插后不会沿用旧设备的计数基线。
- 页面及原生 Swift 图表在设备实例变化或计数回退时重新采样，避免出现虚假的速率跳变。
- 读取失败时清空旧速率和下载、上传、总量显示。
- 联网活动仅列出实际属于模块物理接口的连接，不再把 Wi-Fi 或未经确认的 VPN 连接归入 4G。

## 两条线合流说明

这份修复在两条线各自实现了一次，内容相同：开发分支的 `8a46da9`（`network_handlers.go`）与主分支的 `9db0d8e`（独立的 `network_identity.go`）。合并时以开发分支的实现为准，主分支的独立文件与重复测试已移除，不再保留两份；主分支带来的新增内容是原生 Swift 侧的 `TrafficHistory.swift` 绑定、专项检查脚本与 CI 改跑 macOS 15。

主分支已改用 `apps/hub-macos` 原生客户端，不含旧 `ServiceManager.swift`，因此不将旧客户端整套移入主分支。开发分支的旧状态栏客户端启动标识缓存修复为 `002c303`。

## 验证

| 检查 | 结果 |
| --- | --- |
| 误识别回归 | 无 USB 身份的系统网卡、其他默认以太网接口、相同网段接口均不会被当作模块 |
| 不依赖接口编号 | 即使模块接口名为 `en0`，仍按设备身份识别 |
| 注册树解析 | 正确识别模块子接口、排除其他 USB 设备；拒绝损坏的 XML |
| 本机只读实测 | USB 注册树绑定到 `en15` |
| 全量 Go 测试与静态检查 | 通过 |
| 网络相关竞争检查 | 通过 |
| 仓库、格式及 JavaScript 语法检查 | 通过 |
| 独立演示服务与页面接口检查 | 通过 |
| 页面计算检查 | 正常速率、设备替换、计数清零、读取失败均通过 |
| 原生流量专项检查 | `scripts/test-network-traffic.swift` 直接编译 `Service.swift` 与 `TrafficHistory.swift`，验证速率、USB 实例替换、计数回退、长采样间隔和断连 |
| 实际 4G 数据传输 | 指定 `en15` 下载后，接口下载、上传计数增长，总量等于两者之和 |

已将修复安装到本机 App，安装器保留原 App 备份及用户设置。安装后再次核对网络接口、系统计数和网页资源。

## 统计口径

显示的是模块网卡实际收发字节，包含协议和局域网通信开销，不等同于运营商账单或套餐已用量。

会话量从当前连接首次被采样时计算，服务重启或模块重新枚举后重新统计，未实现按月、按卡持久累计。Wi-Fi 优先时仍只统计模块网卡，不会把整台电脑的总流量当作 4G 流量。

本次没有执行物理热插拔或真实通话，设备替换和计数回退通过自动化输入验证。

## 复查命令

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

注：本机仅有 Command Line Tools 时，完整 `swift test` 会因缺少 XCTest 模块而失败，不能记作全部原生测试通过；保留 XCTest 回归用例，供具有完整测试环境的电脑运行。首次推送后，GitHub 检查曾在执行步骤前报告 `macos-14-arm64-us-default` 不受支持，CI 的运行环境已改为 `macos-15`。

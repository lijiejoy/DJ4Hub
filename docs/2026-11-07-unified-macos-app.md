# DJ 4G Hub 单一 App 整合说明

日期：2026-11-07

## 结果与边界

整合为一个 `DJ 4G Hub.app`，统一安装、启动、停止、更新与版本检查。
状态栏客户端、原生拨号和来电面板、内嵌网页、设备服务、libusb 和 ADB 均随 App 分发。
用户无需分别启动状态栏和通话服务。

内部保留独立后台进程：状态栏退出或网页关闭后，设备服务仍可监听来电。
这次整合没有改写短信、eSIM、网络管理或语音媒体处理算法，也不代表此前所有音质与时延问题都已完成真机验收。

## 安装结构

```text
/Applications/DJ 4G Hub.app/
  Contents/
    Info.plist
    MacOS/
      DJ4HubClient       状态栏、统一服务管理入口
      dj4ghub-macos     设备/通话/网页服务；--call-window 为原生通话面板
      adb              模块语音路由工具
    Frameworks/
      libusb-1.0.0.dylib
    Resources/
      DJ4Hub.icns
      LICENSE
      licenses/
        libusb-COPYING
        platform-tools-NOTICE.txt
```

配置与日志继续保存在原位置：

- `~/Library/Application Support/DJ 4G Hub/`：SIP、音频、通知、任务等设置，以及已经校验的模块语音缓存。
- `~/Library/Logs/DJ 4G Hub/dj4ghub-launchd.log`：后台服务日志。
- `~/Library/LaunchAgents/com.dj4hub.local.plist`：当前用户的后台服务启动项。
- Application Support 中的同名旧 plist 保持指向同一个 App 内服务，兼容旧启动路径。

模块端语音驱动和桥接程序仍使用原有固定版本、哈希校验和本地缓存机制，不纳入本次 App 重分发。
首次准备这部分运行环境可能仍需下载；已有缓存无需重新安装。

## 启动与退出

| 操作 | 行为 |
| --- | --- |
| 打开 App | 校验内置服务，必要时迁移/启动后台服务，默认打开控制台 |
| 拨打电话 | 确认服务就绪后打开原生通话面板 |
| 启动服务 | 启动已停止的服务，恢复登录启动项 |
| 重启服务 | 先确认没有拨号、来电或通话，再停止并启动服务 |
| 停止服务 | 停止后台并移除登录启动项；状态栏保留 |
| 退出状态栏（后台继续接听） | 仅退出状态栏；后台及登录启动项保留 |
| 退出并停止服务 | 检查通话状态后停止后台并退出状态栏 |
| 登录系统 | 启动已启用的后台服务；状态栏可通过打开 App 显示 |

后台由当前用户的 LaunchAgent 管理，设置 `KeepAlive=true`。
非用户主动停止时，后台意外退出后由系统重新启动。
此处继续使用现有 launchctl 管理方式；尚未迁移到 SMAppService，也没有增加另一个状态栏登录启动项。
电脑睡眠、关机、用户退出登录时不保证来电接听。

## 迁移与升级保护

1. 检查 App 内置服务是否完整。
2. 通过文件锁排除多个 App 副本同时管理同一服务。
3. 对比启动配置、实际运行路径和启动时记录的 SHA-256，识别更新后仍在内存运行的旧服务。
4. 需要切换时读取通话状态。拨号、响铃、通话中或无法确认状态时拒绝操作。
5. 保存原启动配置和完整旧 App。
6. 停止旧服务并等待原进程退出，切换至内置服务，避免同时占用 USB 或端口。
7. 验证健康状态和运行版本；失败则恢复旧配置，安装器同时恢复旧 App。

旧独立服务二进制留在 Application Support 的 `bin` 目录中供恢复使用，正常启动不再执行它。
旧的 `start-dj4hub.command` 和 `stop-dj4hub.command` 检测到完整安装后，改为调用 App 自身的服务管理命令。
显式指定 `DJ4GHUB_SOURCE_BIN` 或演示模式仍走开发用旧启动流程。

通话保护是在停止前读取服务状态，并非与蜂窝网络的来电事件组成一个原子事务；操作瞬间新来的电话仍存在竞争窗口。
不应在短信发送、eSIM 写入或其他设备操作期间升级。本次通话保护没有扩展为所有设备操作的全局事务锁。

## 构建和安装

开发环境需要 Go、Swift/macOS SDK、libusb/pkg-config、ADB、rg。
构建脚本按当前机器架构构建，不生成通用二进制。
脚本扫描所有内置二进制与动态库的最低系统要求，写入 App 的最低版本及 `Resources/BuildInfo.plist`。
本次本机构建要求 macOS 15.0；原有 macOS 13+ arm64 CLI 发行流程不受影响。

```sh
sh scripts/test-macos-client.sh
sh scripts/build-macos-app.sh
sh scripts/install-macos-app.sh
```

输出为 `dist/DJ 4G Hub.app`，默认安装到 `/Applications/DJ 4G Hub.app`。
安装器先准备副本、备份旧 App，再执行有通话保护的替换。
备份位于 `~/Library/Application Support/DJ 4G Hub/client-backups/`。
该脚本需要对目标目录有写入权限，不会自行提权。

可通过 `DJ4HUB_ADB_BIN`、`DJ4HUB_ADB_NOTICE` 和 `DJ4HUB_LIBUSB_LICENSE` 指定工具与许可证文件。
ADB 的 NOTICE 必须来自相应的 platform-tools 分发包。
App 内二进制与动态库逐一签名，再签外层 App，并检查不再链接 Homebrew 目录。
本机此次构建使用 libusb 1.0.29；原有 arm64 CLI 发行脚本仍使用其固定的 1.0.30，不受影响。

App 同时提供命令入口：

```sh
"/Applications/DJ 4G Hub.app/Contents/MacOS/DJ4HubClient" --start-service
"/Applications/DJ 4G Hub.app/Contents/MacOS/DJ4HubClient" --restart-service
"/Applications/DJ 4G Hub.app/Contents/MacOS/DJ4HubClient" --stop-service
```

服务启动使用的 PATH 优先包含 App 的 MacOS 目录，因此语音桥接优先使用内置 ADB。
状态栏管理操作在串行工作队列执行，不阻塞界面。

## 麦克风与签名

App 已包含麦克风用途说明，并保留原有启动时申请、通话时复查权限的逻辑。
本机当前没有可用的正式签名身份，默认使用临时签名。
移入 App、重建或更换二进制后，系统可能再次要求授权，不能承诺跨所有开发版本永久免提示。
可用 `DJ4HUB_SIGN_IDENTITY` 指定签名身份；公证和正式发行流程尚未接入。

本次迁移后的状态为 `microphone_permission=not_determined`。
检查时电脑处于锁屏，尚未完成用户授权和迁移后的真人双向通话验收。
解锁后需要用户允许麦克风；自动化没有代替用户操作系统授权。

## 验证记录

已通过：

- Swift 隔离测试：首次安装、旧配置迁移、配置保留、重复启动不重启、版本变化时更新、停止/再启动、主动重启。
- Swift 隔离测试：拨号/响铃/通话与未知状态保护、无效响应、安装失败回退、健康检查超时、非托管服务占用、多副本互斥。
- `go test ./... -count=1 -timeout=90s`。
- `go test -race ./internal/sip ./cmd/dj4ghub-macos -count=1 -timeout=90s`。
- 完整 App 构建、内外层签名校验，以及动态库依赖检查。
- 本机旧安装迁移：后台与状态栏均运行于 `/Applications/DJ 4G Hub.app/Contents/MacOS/`。
- 本机实际生命周期：重复启动保持同一后台 PID；重启更换 PID；停止移除登录启动项；再次启动恢复正常。
- 本机退出状态栏后，原后台 PID 与 HTTP 服务保持运行，随后可重新打开状态栏。
- 后台报告的启动 SHA-256 与安装文件一致，HTTP 7575 和 SIP 5060 归属同一个后台进程。
- USB AT 连接、eSIM 管理可用状态、SIP 监听和 USB 音频设备发现正常。

限制：此次没有重新进行真实来电弹窗、远端 DTMF、双向音质或延时验收；需要解锁并完成麦克风授权后继续。
现有链接器的重复 `-lobjc` 警告仍存在，测试与打包成功。

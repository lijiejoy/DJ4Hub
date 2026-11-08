# DJ 4G Hub 源码结构

仓库按 `cmd/dj4ghub-macos` 的真实 Go 依赖图保留 DJ 4G Hub 必要源码。`apps/4g-connect` 是指向独立 4G Connect 仓库的 Git submodule，不属于本仓库源码。

## 目录树

```text
DJ4Hub/
├── cmd/
│   ├── dj4hub-client/        # macOS 状态栏客户端（Swift/AppKit）
│   ├── dj4hub-call-test/     # 显式启用的语音/电话诊断入口
│   └── dj4ghub-macos/       # macOS 服务、按功能拆分的处理器与原生面板
│       └── web/              # 当前实际显示的原生管理页面
├── apps/
│   └── 4g-connect/           # 独立 4G Connect 仓库的 submodule
├── internal/
│   ├── apduarbiter/          # SIM/eUICC APDU 通道并发协调
│   ├── backend/              # AT、MBIM、QMI 后端的统一能力接口
│   ├── config/               # 运行配置与设备配置
│   ├── esim/                 # eUICC/Profile 读取、下载、切换和删除
│   ├── modem/                # 调制解调器发现、AT 指令和状态解析
│   ├── simaid/               # SIM 应用 AID 发现与选择
│   └── sip/                  # SIP/RTP、USB 音频与本机双向通话
├── pkg/
│   ├── logger/               # 日志适配
│   ├── mbim/                 # MBIM 协议实现
│   └── smscodec/             # SMS PDU 编解码与长短信重组
├── packaging/
│   ├── dj4ghub              # 终端 start/stop/status/logs/open 启动器
│   ├── install               # /usr/local 安装脚本
│   ├── macos/                # 单一 App 的包信息
│   ├── README.md             # 发行包内的安装说明
│   └── THIRD_PARTY_NOTICES.md
├── scripts/
│   ├── build-macos.sh        # 本地开发构建
│   ├── build-macos-client.sh # 状态栏客户端构建
│   ├── build-macos-app.sh    # 内置服务、ADB、libusb 的完整 App
│   ├── install-macos-app.sh  # 保留设置的安装与旧版本迁移
│   ├── test-macos-client.sh # 服务生命周期隔离测试
│   ├── check-source.sh      # 统一源码检查
│   ├── check-repository.cjs # 上传候选文件检查，不输出凭据正文
│   ├── check-web-ui.js      # Playwright CLI 页面回归
│   ├── package-macos-app.sh # 当前架构完整 App ZIP
│   └── package-macos-arm64.sh# Apple Silicon 发行包构建
├── docs/                    # 功能验证、设计资料、GitHub 维护指南
├── .github/                 # CI、双架构 Release 草稿、Issue/PR 模板
├── third_party/              # 当前构建实际使用的本地第三方源码
├── Makefile                 # 检查、测试、构建与打包命令
├── CONTRIBUTING.md
├── go.mod
├── go.sum
├── LICENSE
├── THIRD_PARTY_NOTICES.md
├── README.md
└── MACOS.md
```

## 关键入口

- `cmd/dj4ghub-macos/main.go`：命令行入口、服务装配和退出。
- `cmd/dj4ghub-macos/app.go`：应用共享状态和同步边界。
- `cmd/dj4ghub-macos/http.go`：内嵌页面、HTTP 路由、响应及安全头。
- `cmd/dj4ghub-macos/device_{discovery,status,transport}.go`：发现、状态解析及设备传输。
- `cmd/dj4ghub-macos/sms_{handlers,transport}.go`：短信轮询/API 与 USB PDU 收发。
- `cmd/dj4ghub-macos/network_handlers.go`：网卡、流量、联网活动及网络控制。
- `cmd/dj4ghub-macos/esim_{handlers,notes}.go`：eSIM 操作、本地备注和模块电话簿。
- `cmd/dj4ghub-macos/usbat_darwin.go`：macOS 上通过 libusb 接管兼容模块的 USB AT 接口。
- `cmd/dj4ghub-macos/usbat_esim_channel.go`：经 AT/APDU 访问实体 eUICC 卡片。
- `cmd/dj4ghub-macos/web/`：由 `go:embed` 编译进二进制的网页界面。
- `cmd/dj4hub-client/main.swift`：状态栏“4G”菜单及原生拨号入口。
- `cmd/dj4ghub-macos/sip_handlers.go`、`call_panel.go`、`call_alerts.go`、`call_window_darwin.m`：通话控制、原生拨号/来电/通话面板与提示音。
- `cmd/dj4ghub-macos/{notify,tasks,radio,ussd,gnss,mcp}_handlers.go`：通知、定时任务、无线、USSD、定位和只读 MCP。
- `notify_settings.go`、`notify_dispatch.go`、`notify_senders.go`、`notify_email.go`、`telegram_bot.go`：通知配置、分发、通道与 Bot；`call_poll.go` 负责 USB 来电事件轮询。
- `internal/sip/`：SIP 协议、RTP、模块 USB 音频与 Mac 回声消除音频。
- [当前功能梳理与逐项排查](docs/2026-11-07-feature-audit.md)：功能清单、验证范围及未解决问题。

## 为什么仍有 internal、pkg 和 third_party

Go 以“包”为编译边界。macOS 主程序虽然集中在 `cmd/dj4ghub-macos`，但短信 PDU、eUICC、SIM APDU、MBIM/QMI 和日志能力依赖共享包，因此这些目录不能直接删除。

`third_party` 中只保留当前依赖图实际使用的本地替换模块。保留本地副本可以确保当前修改版协议实现与已验证发行包一致，同时保留各上游组件的许可证和来源信息。

## 已排除内容

- `web/` 旧 Vue/Vite 管理前端及约 601 MB 的 `node_modules`
- 原 Linux 服务端入口、容器配置和网络命名空间工具
- 原项目未被 macOS 入口引用的数据库和后台页面；当前 macOS 版本已另行实现通知、部分 Bot 命令和定时任务
- `dist/`、下载包、日志、缓存及其他生成文件

## 验证方式

运行日常检查与测试：

```sh
make check test test-client
make test-race test-nocgo
```

生成当前架构的完整 App 发行包：

```sh
make package-app VERSION=v1.1.0
```

完整 App 包含后台、状态栏、原生通话面板、ADB 和 libusb。原 `package-macos-arm64.sh` 保留旧 CLI 分发方式，会下载并校验固定版本的 libusb 源码。两种包的最低系统要求和依赖不同，不能混用验证结论。

## 模块与来源

当前 Go module 路径为 `github.com/WongLoki/DJ4Hub`。仓库作为独立项目维护，但从 DJOneHub、VoHive 和第三方模块演进而来的代码仍保留其原始许可证与声明，详见根目录 `LICENSE` 和 `THIRD_PARTY_NOTICES.md`。

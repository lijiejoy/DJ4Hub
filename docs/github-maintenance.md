# GitHub 管理与维护

## 1. 环境

Go 版本以根目录 `go.mod` 为准。Mac 开发需要 Xcode Command Line Tools（包含 Swift/macOS SDK）。使用 Homebrew 安装依赖：

```sh
brew install go pkg-config libusb node ripgrep
brew install --cask android-platform-tools
```

Node.js 用于源码和网页检查，应用本身不依赖 Node.js。完整 App 构建会把 ADB 和 libusb 放入包内，并保留其许可证；没有相应的 NOTICE 或许可证时，构建会报错。

`third_party/` 是按当前依赖图裁剪的本地替换模块，保留来源和许可证。不要直接运行全平台 `go mod tidy`：上游依赖的 Windows 文件及部分依赖自身的测试包不在此裁剪副本中，tidy 会因缺包失败。更新依赖时按目标平台核对构建和测试，审查 `go.mod`/`go.sum` 差异；需要完整跨平台依赖树时应先恢复相应上游模块，再单独验证。本轮只补记已有 QMI 依赖链中的 netlink/netns 固定版本，未批量升级依赖。

代码默认只操作本机服务。`apps/4g-connect` 是独立仓库引用，常规克隆不需要初始化它即可构建本应用；需要开发它时再执行 `git submodule update --init apps/4g-connect`。

## 2. 日常命令

| 命令 | 用途 |
| --- | --- |
| `make check` | Go 格式、脚本语法、JS 语法、仓库候选文件检查 |
| `make test` | Go 测试与静态分析 |
| `make test-race` | 原生并发竞争检查 |
| `make test-nocgo` | 无 cgo 测试，不验证原生音频/USB |
| `make test-client` | Swift 服务管理与安装流程的隔离测试 |
| `make test-native` | AppKit 面板测试，不打真实电话 |
| `make test-demo` | 已构建后台的隔离 HTTP/内嵌资源检查，自动清理测试进程 |
| `make build client` | 构建当前架构的后台和状态栏 |
| `make app` | 构建 `dist/DJ 4G Hub.app` |
| `make package-app VERSION=v1.1.0` | 当前架构 App ZIP 与 SHA-256 |

格式检查只报告不合规文件，不自动改动用户代码；需要时对对应文件运行 `gofmt -w`。

App 会根据实际内置二进制检测最低 macOS 版本，不能把旧 CLI 的 macOS 13 声明沿用到所有 App 包。本机 Intel 构建目前要求 macOS 15.0。Intel 与 Apple Silicon 分别构建，不是一个通用二进制。Go 构建图所用模块的根许可证/声明也随 App 放入 `Resources/licenses/go/`。

需要安装本机测试包时执行 `sh scripts/install-macos-app.sh`。安装器会检查通话空闲并保留设置及旧版本；常规代码检查和打包不更换正在运行的 App。

## 3. 无硬件网页检查

先构建后台，在独立终端以临时 HOME 和独立 HTTP 端口启动：

```sh
audit_home=$(mktemp -d)
HOME="$audit_home" ./dist/dj4ghub-macos -demo -listen 127.0.0.1:17575
```

演示服务不能与正式服务混用配置。若正式服务正在监听默认 SIP 端口，演示服务会报告 SIP 绑定失败，网页模拟检查仍可继续；这不算 SIP 业务通过。

使用 Playwright CLI 打开该地址，然后运行：

```sh
mkdir -p output/playwright
playwright-cli -s=dj4hub-audit open http://127.0.0.1:17575 --browser chrome
playwright-cli -s=dj4hub-audit run-code --filename scripts/check-web-ui.js
playwright-cli -s=dj4hub-audit close
```

脚本是供 `run-code` 调用的函数，不是 `@playwright/test` 测试文件。预先安装兼容的 Playwright CLI 和 Chrome，并创建 `output/playwright/`。脚本检查九个入口、两种宽度、状态提示、来电失败重试及主题；检查后停止演示服务。

## 4. 上传现有代码

当前仓库已经有 Git 历史及 origin。本次整理不改变远端地址、不重写历史，也不自动提交或推送。先确认目标仓库：

```sh
git remote -v
git status --short
make check test test-client
```

默认上传范围是源码、测试、文档、构建脚本与 `.github/`。新增 SIP、Swift 客户端等文件目前也属于待提交源码，不能只提交已跟踪文件。

确认改动后，可显式暂存项目目录和说明：

```sh
git add .github .gitignore Makefile README.md MACOS.md SOURCE_STRUCTURE.md CONTRIBUTING.md
git add cmd internal pkg scripts packaging docs start-dj4hub.command stop-dj4hub.command
git diff --cached --stat
git diff --cached --check
```

查看暂存内容，再按实际改动分批提交。建议围绕源码结构、功能与测试、构建及文档分别组织；不要为了拆提交而丢掉已有功能或未跟踪文件。

```sh
git commit -m "Organize macOS service and maintenance tooling"
git push -u origin HEAD
```

如果使用另一个 GitHub 仓库，先由维护者确认并修改 origin；仅变更远端地址不需要改 Go module。首次推送应保留现有许可证和第三方声明。

候选文件检查覆盖当前索引与未忽略新增文件，拒绝常见凭据、个人 Mac 路径、运行配置、密钥和编译产物，仅输出文件名与原因。它不证明历史提交、所有截图或所有形式的敏感内容都已脱敏。GitHub 上应启用可用的秘密扫描/推送保护，并为主分支启用 PR 检查。

## 5. CI 与发布

CI 为 PR、main、`codex/**` 和 `repair/**` 分支配置源码检查、Linux 无 cgo 测试，以及 macOS 15 的 Intel/Apple Silicon 原生测试、竞争检测、Swift 生命周期测试和构建。Actions 依赖固定到已核对的提交，升级时同步检查对应上游变更。

推送 `v*` 标签后，发布流程分别构建两种架构的完整 App ZIP 和校验文件；只有两个构建任务通过才创建 Release 草稿。默认临时签名，尚未配置 Developer ID、公证或自动生产签名。

Release 草稿中应补充：

- 已通过的 CI 与实际硬件场景。
- 适用的系统、架构、模块及固件。
- 首次启动/麦克风授权与升级迁移影响。
- 仍未完成的音质、延迟、外部 SIP 和 eUICC 验收。

下载包后校验 SHA-256、签名与安装迁移，完成真实业务验收后再发布。不要把 `v*` 标签推送当作硬件验收。

`scripts/package-macos-arm64.sh` 及 `packaging/dj4ghub`、`packaging/install` 继续保留旧 CLI 兼容路径；新 Release 流程使用 `scripts/package-macos-app.sh`。

## 6. 运行数据

设置、凭据及日志在用户的 `~/Library/Application Support/DJ 4G Hub/`、`~/Library/Logs/DJ 4G Hub/` 和 `~/Library/LaunchAgents/` 中，不能复制到仓库。根目录的旧个人 plist 已整理为 `packaging/macos/com.dj4hub.local.plist.example`，只作为模板。

不要把 `dist/`、`output/`、录音、浏览器会话、设备日志、签名证书或真实配置提交。设计图和必要的文档图片保留在 `docs/`，历史验证报告按日期保留。

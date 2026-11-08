# 本机通话音频设计（方案一：Mac 直接当电话机）

日期：2026-11-07
状态：设计已确认，待真机音频可行性验证后进入实现计划

## 目标

DJI 4G 模块（百旺 QDC507，USB `2ca3:4006`）插在 Mac 上时，用户可以通过现有网页拨打、接听其他手机号，并使用 Mac 的麦克风和扬声器（或耳机）正常通话。

非目标（第一版不做）：

- 修改现有网页（HTML/CSS/JS 保持不变）。现有通话页面已有拨号、接听、挂断按钮并显示 `audio.detail`，本地音频状态通过该字段呈现；设备选择通过配置文件完成。
- DTMF 拨号盘（`AT+VTS`）、回声消除、`.app` 打包。
- SIP 软电话音频链路修复（方案二，后续可复用本模块）。

## 已确认的事实

- 呼叫控制可用：`ATD<号码>;` 被接受，`AT+CLCC` 经历 2→3→0，`AT+CPAS` 为 4，挂断时出现 `NO CARRIER`。
- 模块向 macOS 暴露 USB Audio Class 设备，均为 8000 Hz：
  - `AC Interface`：输入设备（对方声音，下行），UID `AppleUSBAudioEngine:BAIWANG:Baiwang:14400000:7`。
  - `AS Interface`：输出设备（本机声音送入模块，上行），UID `AppleUSBAudioEngine:BAIWANG:Baiwang:14400000:8`。
- `AT+QPCMV` 在该固件上返回 `ERROR`，旧的 QPCMV 桥接思路不可行。
- Mac 默认输入是虚拟设备 `TFFAudio`，因此不能直接用系统默认输入；内置麦克风为 96 kHz，内置扬声器为 48 kHz。

## 前置验证（进入实现前必须通过）

用户本人拨打或接听一通真实电话，通话期间运行：

```sh
/usr/local/bin/ffmpeg -hide_banner -f avfoundation -i ":6" -t 10 -ar 8000 -ac 1 ~/Desktop/dj4hub-downlink-test.wav
```

- 录到对方声音：确认下行走 `AC Interface`，按本设计实现。
- 全程静音：先排查模块的音频路由 AT 指令，再修订本设计。

## 架构

```text
Mac 麦克风 ──(AudioConverter → 8 kHz 单声道 int16)──▶ AS Interface ──▶ 对方
对方 ──▶ AC Interface ──(AudioConverter → 输出设备采样率)──▶ Mac 扬声器/耳机
```

### 新包 `internal/callaudio`

职责单一：在通话期间连接两路音频，并报告状态。

- `engine_darwin.go` + `engine_darwin.c/.h`（cgo，`-framework CoreAudio -framework AudioToolbox -framework CoreFoundation`）
  - 按名称/UID 查找设备：模块侧匹配制造商 `BAIWANG` 且名称为 `AC Interface` / `AS Interface`，Mac 侧按配置名称或 UID 查找，找不到时退回内置设备。
  - 每个方向一组 AUHAL 输入单元 + AUHAL 输出单元，中间是 C 实现的无锁单生产者单消费者环形缓冲区，再加上 `AudioConverter` 做重采样和单声道化。
  - 实时音频回调全部在 C 中完成，不回调 Go，避免 Go 调度造成卡顿或爆音。
  - Go 侧只调用 `start(config)`、`stop()`、`stats()`（两路峰值电平、欠载/溢出计数、错误码）、`setMute(bool)`。
- `engine_stub.go`：非 darwin 平台或未启用 cgo 时，返回"不支持"。
- `controller.go`（纯 Go，可单元测试）：
  - 依赖接口 `Engine{ Start(Config) error; Stop(); Stats() Stats; SetMute(bool) }`。
  - 状态包括 `idle`、`starting`、`running`、`failed(reason)`。
  - `CallActive(callID)` 和 `CallEnded(callID)` 可重复调用；只为第一条本地通话启动引擎。
  - 运行期间检测设备消失（模块拔出）后自动停止，并记录原因。
  - 启动后若数秒内两路电平均为 0，状态标注"疑似麦克风权限被拒绝或无声音"。
- `config.go`：从 `~/Library/Application Support/DJ 4G Hub/call-audio.json` 读取（文件不存在时使用默认值），文件权限 0600。

```json
{
  "enabled": true,
  "input_device": "MacBook Pro麦克风",
  "output_device": "",
  "headset_hint": true
}
```

`output_device` 为空表示使用系统默认输出。

### `internal/sip/server.go` 的改动

- 新增可选钩子 `Server.OnCallState func(PublicCall)`。`newCallLocked` 创建 Call 时把通知函数保存到 Call 上；`Call.setState` 在释放 `c.mu` 之后调用它，并且只在状态实际变化时调用。这样所有状态迁移点都会自动通知，不需要逐处补调用。
- `Call` 增加内部标记 `sipLeg bool`：只有当 SIP 软电话实际承载该通话时（客户端发起的 INVITE，或客户端应答了来电 INVITE）才为 true。钩子把这个标记暴露为 `PublicCall.LocalAudio = !sipLeg`。
- 来电在没有已注册 SIP 客户端时，不再立即记为 `missed`，而是进入 `ringing`，供网页“接听”按钮使用；无人接听的超时沿用 `inboundNoAnswer`，超时后记为 `missed`。
- `AudioStatus` 增加本地通话音频的状态。现有网页显示 `audio.detail`，因此 `detail` 改为反映本地音频，例如“本机通话音频：运行中 · 下行电平 -32 dBFS · 上行电平 -28 dBFS”或“本机通话音频：失败 · 找不到 AS Interface”。`mode` 改为 `local-coreaudio`。新增字段只追加，不删除已有字段。

### `cmd/dj4ghub-macos` 的改动

- `startSIP` 中创建 `callaudio.Controller`，并设置 `server.OnCallState`：
  - `active` 且 `LocalAudio` 时调用 `CallActive`。
  - `ended`、`failed`、`missed` 时调用 `CallEnded`。
- 服务退出时（`stopSIP`）停止音频。
- 来电接入：USB AT 路径的 `checkIncomingCallUSBAT`（`notify_handlers.go`）目前只发送来电通知。改为在检测到振铃来电时同时调用 `a.sip.IncomingCall(number)`（服务端对同一来电去重），网页“接听”按钮才能接到来电。轮询间隔沿用现有设置，来电显示最多会延迟一个轮询周期。来电在接听前被对方挂断时，由现有的 CPAS/CLCC 核对结束该来电。
- 模块拔出的处理沿用现有 USB 断开逻辑，并额外调用 `controller.Stop("device detached")`。

## 错误处理

- 音频启动失败**不会挂断电话**，只在 `audio.detail` 和日志中写明原因。
- 麦克风权限：录 Mac 麦克风和录 `AC Interface` 都需要 macOS 麦克风授权。launchd 启动的非 App 二进制可能拿不到授权窗口，而是被静默输出静音。启动时通过电平检测给出明确提示；如果确认被拦截，再单独做 `.app` 打包，作为后续工作。
- 回声：第一版建议佩戴耳机；外放回声问题留到后续用 VoiceProcessingIO 处理。
- 日志只记录音频启停、设备名称和错误，不录制或保存任何通话音频。

## 测试

- 默认测试（`go test ./...`）不接触真实音频设备或模块：
  - `controller` 使用假引擎，覆盖启动、重复调用、结束、启动失败、设备消失和静音提示。
  - `sip.Server` 的钩子测试覆盖：本地去电接通、来电无 SIP 客户端进入 ringing、网页接听后 active、SIP 软电话通话不触发本地音频。
  - `checkIncomingCallUSBAT` 使用假的 AT 应答，验证振铃来电会交给通话服务，并且不会重复创建来电记录。
  - 配置文件的读取、默认值和 0600 权限。
- 真机测试放在构建标签 `hwaudio` 下，且需要环境变量 `DJ4HUB_HW_AUDIO=1` 才运行；只做设备枚举和短时回放，不拨号。
- 真机验收由用户拨打或接听电话，检查双向通话、挂断后音频停止、通话中拔出模块不崩溃。

## 构建

`scripts/build-macos.sh` 和打包脚本不需要额外依赖；新增的 macOS 框架通过 cgo `#cgo LDFLAGS` 链接，系统自带。

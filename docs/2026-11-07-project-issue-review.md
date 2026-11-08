# DJ 4G Hub 项目问题全面分析与修复方案

日期：2026-11-07。对象：本地工作区（含未提交改动）。本文是独立于 [功能梳理报告](2026-11-07-feature-audit.md) 的项目级体检，覆盖版本管理、安全、并发正确性、构建测试、架构与文档六个维度。

证据等级标注：**[实测]** 本轮实际运行命令核实；**[代码]** 静态代码审查核实（含文件:行号）；**[审计]** 来自 2026-11-07 功能梳理报告且经本轮复核仍在。

## 总览

| 维度 | 高/紧急 | 中 | 低 | 合计 |
| --- | --- | --- | --- | --- |
| A 版本管理与工作流 | 2 | 2 | 1 | 5 |
| B 安全 | 4 | 4 | 2 | 10 |
| C 并发与可靠性 | 2 | 5 | 1 | 8 |
| D 构建与测试 | 0 | 2 | 2 | 4 |
| E 架构与可维护性 | 0 | 3 | 2 | 5 |
| F 文档 | 0 | 1 | 2 | 3 |
| **合计** | **8** | **17** | **10** | **35** |

基础面良好：`go test ./...` 全部通过 [实测]、`go vet`/`gofmt` 干净 [实测]、持久化均为原子写 + 0600、无路径遍历与命令注入 [代码]、esim/modem 层锁纪律完整 [代码]。问题集中在**仓库分叉风险、HTTP API 安全、cmd 层并发胶水**三处。

---

## A. 版本管理与工作流（当前最紧急）

### A1｜紧急｜本地与上游实质分叉，7 个文件重叠 [实测]

- 本地 `main` 落后 `origin/main` 11 个提交（0 ahead / 11 behind），**不能直接 fast-forward**。
- 上游 11 个提交约 +6983 行：新增 Swift 原生 App（`apps/hub-macos/`）、SQLite 通信历史（`history_sqlite.go` 等 5 文件）、模块音频（`module_audio.go` 等 6 文件）、电话功能、**Web 前端模块化拆分（`phone.js`/`history.js`/`alerts.js`）**。
- 本地未提交改动 +1877 行，与上游重叠文件 7 个：`README.md`、`main.go`、`web/app.js`、`web/index.html`、`web/style.css`、`internal/modem/at_parse.go`、`internal/modem/manager.go`。
- 其中 `web/app.js` 本地 +995 行（单文件内追加），上游 +228 行且同时拆出 3 个模块文件——直接合并必然冲突，且两边对同一文件的结构演进方向不同。
- **方案**：先在本地分支把工作区分主题提交（见 A2），再 `git merge origin/main`（或 rebase），逐个解决 7 个文件冲突；app.js 按"本地功能并入上游模块化结构"处理，而不是保留两个单文件大杂烩。合并后跑全量测试。

### A2｜紧急｜1877 行改动未提交，整个新功能面处于丢失风险 [实测]

- 16 个已修改文件 + 约 30 个未跟踪文件，涵盖 **SIP 通话、来电窗口、通知推送、定时任务、MCP、USSD、GNSS、无线增强** 全部新功能，仅存在于工作区。
- **方案**：立即按主题分组提交（建议顺序：① SIP/通话/来电窗口 ② 通知 ③ 任务 ④ MCP ⑤ USSD/GNSS/无线 ⑥ web 前端 ⑦ modem/backend 解析修复 ⑧ 文档与 superpowers 规格）。提交本身不改任何代码，零风险。

### A3｜中｜14.7MB 编译二进制躺在源码目录且未被忽略 [实测]

- `cmd/dj4ghub-macos/dj4ghub-macos`（14,687,344 字节，11-06 构建）不在 `.gitignore` 覆盖范围（现有 `/dist/`、`/bin/`、`*.test` 均不匹配），一次 `git add -A` 就会把 14MB 产物提交进仓库。
- **方案**：删除该文件；`.gitignore` 增加 `cmd/dj4ghub-macos/dj4ghub-macos`（或 `dj4ghub-macos[!/]*` 类模式，注意不要忽略 `.go` 源文件）。

### A4｜中｜临时测试程序放在 cmd/ 下会被 CI 构建扫描 [实测]

- `cmd/dj4hub-call-test/`（2796 字节 main.go，手动通话音频验证工具）位于 `cmd/`，`go test ./...` 与 CI 会把它当作正式包处理。
- **方案**：移到 `scripts/dj4hub-call-test/` 并在需要时 `go run`，或加 `//go:build ignore` 构建标签；若已无用则删除。

### A5｜低｜根目录散落 3 个未跟踪的本地运行文件 [实测]

- `com.dj4hub.local.plist`（自述"仅作说明，不再被脚本读取"）、`start-dj4hub.command`、`stop-dj4hub.command`（权限 600，个人启动器）。
- **方案**：plist 若仅作文档应移入 `docs/`；`.command` 脚本有通用价值，建议整理进 `packaging/` 或保留本地并在 `.gitignore` 说明；至少要做出明确去留决定，避免长期"半跟踪"状态。

---

## B. 安全

### S1｜高｜HTTP API 无认证、无 Origin/CSRF 校验，恶意网页可跨源调用全部写接口 [代码]

- 位置：`main.go:806-873`（路由无认证中间件）、`main.go:875-883`（securityHeaders 只设 3 个响应头）、`main.go:2702-2710`（decodeJSON 不校验 Content-Type）。
- 监听 `127.0.0.1:7575` 挡不住浏览器跨源"简单请求"：恶意网页 `fetch('http://127.0.0.1:7575/api/sms/send', {method:'POST', headers:{'Content-Type':'text/plain'}, body:...})` 无需预检即执行。可跨源触达：发短信（资损）、任意 AT 命令、删 eSIM Profile、切 usbnet 模式、重启模块、SIP 拨号、创建定时任务。本地任意进程同样可无凭据调用。
- **方案**：`/api/*` 加统一中间件——① `Host` 白名单（127.0.0.1:7575 / localhost:7575）；② 校验 `Origin`/`Sec-Fetch-Site: same-origin`；③ 写接口要求自定义头（如 `X-DJ4Hub-Token`），迫使跨源请求必须预检而预检被拒。一个中间件同时封死 S1/S2/S3 的入口与 S4 的 API 回显通道。

### S2｜高｜无 Host 校验，DNS rebinding 可读取验证码与设备身份 [代码]

- 位置：全目录 grep `r.Host` 零命中；数据出口 `main.go:1297-1307`（短信列表含 `code` 验证码字段）、`main.go:472-488`（status 含 ICCID/IMSI/IMEI）、`main.go:1630` 附近（进程网络活动）。
- 攻击者域名二次解析到 127.0.0.1 后，浏览器视为同源，GET 响应可被读取——短信验证码、EID、ICCID、IMSI 全部可被远程网页窃取。
- **方案**：并入 S1 中间件的 Host 白名单（DNS rebinding 标准防御）。

### S3｜高｜未认证接口可改写 SIP 监听地址与密码，构成局域网盗打链 [代码]

- 位置：`sip_handlers.go:477-537`（POST /api/sip/settings 接受 `listen`/`password`）、`internal/sip/config.go:45-50`（Validate 不强制回环）、`server.go:368-382`（UpdateConfig 重绑）、`sip_handlers.go:331-344`（拨号走 `ATD`）。
- 默认配置安全（回环 + 随机首启密码 + digest 401），但配合 S1，恶意网页两个请求即可让 SIP 监听 `0.0.0.0:5060` 且密码已知，随后局域网设备注册拨打，经模块产生话费。
- **方案**：S1 中间件封入口；`Validate` 强制 Listen 为回环地址（或该字段不经 API 暴露）；监听地址/密码变更要求本地确认。

### S4｜高｜Telegram Bot token 经错误信息写入日志并可经未认证 API 回显 [代码]（= 审计 F05，复核仍在）

- 位置：`notify_handlers.go:872-879`（token 拼进 URL）→ `:1209-1211`（`client.Do` 失败返回的 `*url.Error` 文本含完整 URL）→ `:1182`（`log.Printf("%v")` 落入 launchd 持久日志）→ 经 `recordNotifyResult` 存入 `last_error`，`GET /api/notify/settings`、`/api/notify/log` 未认证返回（叠加 S2 可被远程读取）。Bark/飞书 webhook 密钥走同一链路。
- **方案**：错误构造与记录统一用 `url.URL.Redacted()` 或 token 子串替换脱敏；`recordNotifyResult` 入库前再过一层脱敏；S1/S2 封 API 通道。

### S5｜中｜邮件/推送发送：SSL 分支空指针 panic、STARTTLS 静默降级、无超时 [代码]（= 审计 F04，复核仍在并扩展）

- 位置：`notify_handlers.go:976-991`。① SSL 分支 `smtp.NewClient` 后不检查 err，`defer client.Close()` 对 nil 解引用；该路径由 `go a.sendNotify(ev)` 裸 goroutine 调用、无 recover，**panic 会终止整个进程**（HTTP/SIP 一并消失）。② `:987-989` STARTTLS 握手失败被忽略，匿名中继场景验证码明文外发。③ `smtp.Dial`/非 SSL 路径无连接 deadline（ctx 被无视）。④ 同文件 Bark 推送用 `http.DefaultClient`（无超时）。
- **方案**：SSL 分支补 err 检查并关连接；STARTTLS fail-closed；dialer 设 deadline、尊重 ctx；Bark 改带超时 client；sendNotify goroutine 加 recover。

### S6｜中｜AT 控制台无白名单、无危险命令确认，内嵌 `\r` 可链式注入 [代码]

- 位置：`main.go:1503-1523`（executeAT 只查 "AT" 前缀）、`usbat_darwin.go:159-186`（Command 只 TrimSpace，**内嵌 `\r`/`\n` 原样写入**——`AT+CSQ\rAT+CFUN=1,1` 会顺序执行两条）。
- `AT+CMGD`（删短信）、`AT+CFUN=0/1,1`（关射频/重启）、`AT+QCFG="usbnet"`（绕过服务端 0-3 校验切模式）、`AT&F`（恢复出厂）均无服务端防线。本地调试工具定位可接受，但叠加 S1 等于把完整 AT 面暴露给任意网页。
- **方案**：服务端剥离内嵌控制字符；危险命令黑名单/白名单 + 二次确认端点；S1 落地后风险即收敛。

### S7｜中｜SIP 首启密码明文写入持久日志 [代码]

- 位置：`sip_handlers.go:210-212`，launchd 环境 stdout 落入 `~/Library/Logs/DJ 4G Hub/dj4ghub-launchd.log`，密码永久留存，与 `sip-password` 文件 0600 的意图冲突。
- **方案**：只提示"已生成，存放于 <path>"，让用户读文件取回；或打印掩码。

### S8｜中｜定时任务接口未认证，可植入持久化资损任务 [代码]

- 位置：`tasks_handlers.go:313-372`（创建 kind=sms/reboot 任务，最小周期 5 分钟，持久化）；`:261-289`（执行发短信/`AT+CFUN=1,1`）。与 S1 同根，但把一次性跨源请求升级为持续性资损/周期性重启，重启应用后仍生效。
- **方案**：随 S1 一并纳入认证；任务创建/启用要求与高危操作同级确认。

### S9｜低｜MCP bearer token 非常数时间比较 [代码]

- 位置：`mcp_handlers.go:81-93`，`==` 比较存在理论时序侧信道。MCP 整体设计良好（crypto/rand、0600、只读白名单、token 不回显）。
- **方案**：改 `crypto/subtle.ConstantTimeCompare`。

### S10｜低｜Bot 确认码在 crypto/rand 失败时回退固定值 "123456" [代码]

- 位置：`notify_handlers.go:1165-1171`。该码用于确认 Telegram bot 的远程发短信/重启操作，降级后可预测。
- **方案**：rand 失败直接拒绝生成确认码（返回错误）。

---

## C. 并发与可靠性

### R1｜高｜`usbAT` 等约 8 个 app 字段跨 goroutine 无锁读写，USB 设备可被重复打开 [代码]

- 位置：字段声明 `main.go:78-152`（无对应 mutex）；写点 `main.go:744-771`（ensureUSBAT，**check-then-act 无锁**，两个 goroutine 同时通过 `a.usbAT == nil` 检查会各开一个独占设备句柄）、`main.go:790-804`（markUSBATDetached 置 nil 并 Close，与读侧构成 use-after-close）；读点散布 health/status/runATCommand/processUSBCallEvents 等。
- 并发方：SMS 轮询 goroutine、HTTP handler、任务运行器、100ms 来电事件轮询。`usbat_darwin.go` 内部 mutex 只保护单条命令，不保护 app 层字段。
- **方案**：为 USB-AT 生命周期引入单一 mutex（或收敛为带锁的小 manager 类型），ensure/mark 全程持锁，读侧走快照方法；补 `-race` 回归测试。注：`-race` 现有测试通过不构成反证——这些路径恰无测试（见 Q6）。

### R2｜高｜AT 多命令序列无会话级串行化，SMS 读取可被并发命令打断 [代码]

- 位置：依赖连续性的序列 `main.go:1132-1162`（CMGF=0→CPMS→CMGL）、`:1164-1215`（CPMS 切换）；可插队的并发入口：executeAT handler、`radio_handlers.go` 6 处、`gnss_handlers.go:50/153`、`ussd_handlers.go:106-179`、`tasks_handlers.go:289`、`notify_handlers.go:793`。`smsSendMu` 只保护发送路径；`a.smsReassembler` 懒初始化（`main.go:1196-1198`）也在竞争。
- 影响：从错误存储读数、解析失败、送达报告丢失、长短信拼接错乱。
- **方案**：把"一次 AT 会话"建模为显式会话锁（粗粒度串行化全部 AT 对话即可显著改善）；懒初始化改启动时构造；补并发回归测试。

### R3｜中｜后台 goroutine 无停机路径，关机只停 HTTP 与 SIP [代码]

- 位置：`notify_handlers.go:1173-1187`（telegramBotLoop `for{}` 无 ctx）、`tasks_handlers.go:219-225`（taskTickLoop）、`main.go:306`（SMS poller 用 `context.Background()`）；`serve()` 关机逻辑只做 server.Shutdown + stopSIP。
- **方案**：`serve()` 建根 ctx 贯穿全部后台循环，关机统一 cancel + WaitGroup 等待。

### R4｜中｜任务崩溃后可能重复执行收费动作 [审计]（= 审计 F07）

- 位置：`tasks_handlers.go:runDueTasks/runTask`。Running 只在内存，执行成功但结果未落盘时崩溃，重启可能补跑。
- **方案**：记录执行实例及阶段，对 sms/reboot 动作制定恢复语义（补跑前确认或跳过并提示）。

### R5｜P1｜打开无线页面即自动触发运营商长扫描 [审计]（= 审计 F01）

- 位置：`radio_handlers.go:listOperators`、`web/app.js:loadRadio/loadOperators`。GET 在缓存空/过期时直接启动扫描，页面打开和轮询都会触发；长扫描占用共享 USB 命令通道，影响其他查询与呼叫控制。
- **方案**：GET 只返回缓存/idle，POST 才扫描；通话期间限制扫描并返回忙状态。

### R6｜P2｜锁小区状态查询不支持时界面仍显示"未锁定" [审计]（= 审计 F02）

- 位置：`web/app.js:loadCellLock`。实机返回 `supported=false`，页面只判断 `locked`。
- **方案**：区分"固件不支持查询/未知/锁定/未锁定"四态显示。

### R7｜P2｜MCP USSD 状态与网页不一致 [审计]（= 审计 F03）

- 位置：`mcp_handlers.go:mcpCallReadTool` 的 `ussd_status` 顶层 state 固定取 `ussdStateForStatus(0)`。
- **方案**：提取共享快照，补一致性测试。

### R8｜P2｜IMS 查询失败被显示为"未注册" [审计]（= 审计 F06）

- 位置：`ussd_handlers.go:ussdIMS`、`web/app.js:loadUSSD`。查询失败是状态未知，不能据此否定通话能力。
- **方案**：区分注册/未注册/未知三态。

### R9｜中｜任务持久化失败被静默吞掉 [代码]

- 位置：`tasks_handlers.go:214`、`:257` 用 `_ = a.saveTasksLocked(...)` 丢弃错误（同文件 358/390/423 处有检查，标准不一）；同类：`internal/config/persist_devices.go:147` reload 错误被忽略。
- **方案**：保存失败记日志并反映到任务状态/API 响应；统一全文件检查标准。

---

## D. 构建与测试

### D1｜中｜CI 无 `-race`、无 cgo 关闭构建检查 [实测]

- `.github/workflows/ci.yml` 只跑 `go test ./...`。R1/R2 类竞争与 D2 类构建断裂 CI 均无法发现。
- **方案**：CI 增加 `go test -race ./...` job 与 `CGO_ENABLED=0 go build ./...`（或限定包）检查 job。

### D2｜中｜CGO_ENABLED=0 时主程序构建失败 [审计]（= 审计 F08）

- `CGO_ENABLED=0 go test ./...` 报缺少 `usbAT.Description`、`atResponseIsError`、`atProbeSucceeded` 等——通用解析逻辑只定义在 darwin+cgo 文件里，`usbat_stub.go` 补齐不足（本地该文件有改动但仍不完整）。
- **方案**：把跨平台解析函数移出 build-tag 文件；补齐 stub；或明确声明主程序仅支持原生构建并在 CI 里固化该边界。

### D3｜低｜链接时重复 `-lobjc` 警告 [实测]

- 测试构建输出 `ld: warning: ignoring duplicate libraries: '-lobjc'`，来自构建脚本/CGO_LDFLAGS 重复声明。
- **方案**：构建脚本去重链接参数。

### D4｜低｜Intel 发行包未验证 [实测/文档]

- README 已声明"Intel Mac 未验证"，release.yml 只出 arm64。审计本轮环境恰是 Intel Mac，说明 Intel 是实际使用场景。
- **方案**：增加 amd64 打包脚本路径或在 CI 出双架构包，至少完成一次 Intel 真机回归后更新 README。

---

## E. 架构与可维护性

### Q1｜中｜main.go（2720 行）上帝文件 [代码]

- `app` 结构体 30+ 字段横跨 8 个功能域（`main.go:78-152`）；单文件混合进程入口、路由、USB 发现与 system_profiler 解析（508-640）、AT 响应解析器群（1009-1252）、SMS 业务+轮询、网络诊断（1527-2076）、eSIM 笔记持久化（2078-2407）六种职责。与 A1 冲突问题互为因果：任何改动都在同一文件制造合并冲突。
- **方案**：按域拆包（usbdetect / sms / netdiag / esimnotes），`app` 域字段连同各自 mutex 拆成独立 struct；main.go 只留装配。建议在 A1 合并完成、上游结构稳定后进行。

### Q2｜中｜app.js（2331 行）零模块化 [代码]

- 31 个顶层声明、22 个全局 `let`、81 个全局函数，无 import/export/IIFE 隔离，靠手工 in-flight 布尔防重入。**注意：上游已拆出 phone.js/history.js/alerts.js**——A1 合并时本地 +995 行应顺势并入模块化结构，避免回到单文件。
- **方案**：改 ES modules，按域拆文件（api/state/sms/esim/calls 等），每域一个闭包状态对象；与 A1 合并同步规划。

### Q3｜中｜新增大功能的 handler 层测试缺口：风险最高的胶水恰无测试 [代码]

- `sip_handlers.go`（537 行，0 测试）、`tasks_handlers.go` 执行循环、`radio_handlers.go` AT 执行路径、`gnss_handlers.go`（269 行）、`mcp_handlers.go`（仅鉴权守卫）、`usbat_darwin.go`（513 行）。`feature_audit_test.go` 全文 84 行，是薄抽查。R1/R2 所在的并发路径正是无护栏区。
- **方案**：优先给 R1/R2 重构配套测试，再补任务执行与 radio 执行路径的 handler 级测试。

### Q4｜低｜9 个死代码函数 [代码]

- `internal/modem/at_parse.go:339/390/406`、`commands.go:145/166`、`manager.go:335`、`internal/esim/manager.go:549/2476`、`errors.go:63/72/81`——全仓库（含测试）零调用。
- **方案**：确认非反射调用后删除。

### Q5｜低｜`+QNWINFO` 双实现，热路径函数内重复编译正则 [代码]

- `main.go:1109-1130`（正则版，status 轮询路径每次调用 MustCompile）vs `internal/modem/at_parse.go:339`（字符串版，入口本身已是死代码）；同类函数内 MustCompile 还有 `main.go:1058/1071/1081/1235/1675/1889`、`sms_code.go:6-7`、`usbat_esim_channel.go:89/102`。
- **方案**：正则提升为包级 var；两套解析合一后删死的那套。

---

## F. 文档

### DOC1｜中｜README 功能状态表落后于实际能力 [实测]

- README 功能表未提 SIP 通话、通知推送、定时任务、MCP、USSD、GNSS——这些已是实机可用的主功能面；"页面能力"章节同样缺通话/任务/通知页。
- **方案**：A1 合并后统一更新功能状态表与页面能力章节，保持与 feature-audit 报告的证据分级口径一致。

### DOC2｜低｜SOURCE_STRUCTURE.md 部分过时 [实测]

- 已提 sip/call_window 等（本地改动里有更新），但未反映上游将引入的 apps/hub-macos、通信历史、web 模块化；`cmd/dj4hub-call-test` 等散落目录无说明。
- **方案**：A1 合并后随仓库实际结构重写目录树。

### DOC3｜低｜MACOS.md 停留在旧发行布局 [实测]

- 描述 `./dj4ghub start`、`dist/release/` 布局，与当前 `packaging/install` + `/usr/local` 安装方式及根目录 `.command` 启动器并存，三种说法易误导。
- **方案**：明确当前推荐路径（发行包 install / 源码构建 / .command 启动器），删除过时章节。

---

## 建议修复顺序（四批）

1. **第一批·仓库急救（不改代码逻辑）**：A2 提交 → A1 合并上游 → A3/A4/A5 清理。理由：所有后续修复都应落在合并后的基线上，否则冲突翻倍。
2. **第二批·安全（S1→S8 顺带解决）**：S1/S2/S3 中间件（一个改动封三个洞）→ S5 邮件推送可靠性 → S4 token 脱敏 → S3 Validate 回环 + S7 密码日志 → S9/S10 小项。理由：全部是"网页访问即可资损/泄密"的现实路径。
3. **第三批·并发与审计遗留**：R1/R2 USB-AT 管理器与会话锁（配套 -race 测试）→ R3 优雅停机 → R9 持久化错误 → R5 扫描触发 → R4/R6/R7/R8 状态语义。
4. **第四批·质量与文档**：D1 CI 加固 → D2 CGO=0 → Q1/Q2 拆分（对齐上游模块化）→ Q4/Q5 清理 → Q3 补测试 → DOC1-3。

## 本轮验证记录

| 检查 | 结果 |
| --- | --- |
| `go test -mod=mod ./...` | 全部通过（cmd 7.8s、sip 1.6s，其余缓存）[实测] |
| `go vet ./...` / `gofmt -l` | 干净 [实测] |
| `git status` / `git rev-list` / `git diff --stat` | 确认分叉与重叠文件 [实测] |
| F04/F05 代码复核 | 均仍在（notify_handlers.go:976-991、:1182-1211）[代码] |
| `ensureUSBAT` 无锁 check-then-act / AT `\r` 透传 / `_ = saveTasksLocked` | 三处抽查坐实 [代码] |
| 安全审查（S1-S10）与质量审查（R1-R3、Q1-Q5） | 独立子代理静态审查，行号可核 [代码] |
| 未运行 | `-race` 全量、CGO=0 构建、实机写操作（遵循审计报告边界） |

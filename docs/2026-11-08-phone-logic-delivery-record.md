# DJ 4G Hub 手机逻辑开发 — 交付记录

- 日期：2026-11-08
- 分支：`feature/phone-logic`（基线：`feature/2026-11-08` 的 `10d69fd`）
- 远端：origin（fork `lijiejoy/DJ4Hub`）的 `feature/phone-logic`；main 未动
- 评审文档：`docs/2026-11-08-phone-product-roadmap-review.md`（§8“首先推进的交付包”，5 个交付点）
- HEAD：`487e5c4`；全部 5 个交付点已完成、已提交、已推送

## 验证口径（四档分开记录）

| 档位 | 含义 | 本批状态 |
|---|---|---|
| 模拟测试 | 单元/集成测试（模拟事件、内存/临时目录 SQLite、demo 模式） | 全部通过，见各交付点 |
| 编译通过 | `go build` / `go vet` / `go test` / `-race` / `make check` / 无 cgo / arm64 | 全部通过，见“全量验证” |
| 设备读取 | 真机模块读取（CNUM、短信、通话事件） | 未做（本次无真机操作） |
| 真实业务通过 | 用户实机验收（拨打、接听、收发短信的端到端业务） | 未做，待步骤 09 |

## 提交一览

| 提交 | 交付点 | 内容 |
|---|---|---|
| `773a2a6` | 1 | 接打电话、来电提醒和连续通话的可重复基线 |
| `8471d4c` | 5 | 号码读取/备注和初始化诊断 |
| `8243c48` | 1 + 5 | 两交付点共享的路由与界面接线 |
| `dbafcfb` | 2 | 按卡身份保存短信与通话（SQLite 存储） |
| `1e246a8` | 4 | 短信会话、搜索与明确的发送状态 |
| `487e5c4` | 3 | 最近通话和未接记录，可直接回拨 |

---

## 交付点 1 — 接打电话、来电提醒和连续通话的可重复基线

- 提交：`773a2a6`（接线见 `8243c48`）
- 变更：
  - 结束原因 11 种（主动拒接/对方取消/未接超时/无网络/忙线/媒体失败/设备断开等，无法区分记 `unknown`，不猜）。
  - 通话时长只算接通后的 active 段。
  - “模块已接通”与“媒体已就绪”两个独立字段；媒体就绪要求 RTP 包或 PCM 样本真的流动。
  - 挂断只更新同一条通话记录；号码晚到只更新该通话。
  - DTMF、静音此前只有原生面板，已补 `POST /api/sip/dtmf`、`/api/sip/audio-controls`。
  - 音频设备选择：CoreAudio 枚举 + `GET/POST /api/sip/audio-devices`，选择写 `call-audio.json`（0600）。
  - 分段计时 + 测试计划文档 `docs/2026-11-08-call-baseline-test-plan.md`。
- 自动测试：随全量套件通过（见“全量验证”）。
- 实机结果：未验证——需用户实机验收（测试计划文档已标明）。
- 未验证项：10 次呼入 + 10 次呼出、耳机/扬声器各一组、一次 10 分钟通话；真实设备上的结束原因区分与媒体就绪判定。
- 回退：`git revert 773a2a6`（及接线 `8243c48` 的对应部分）；删除新增的 `call-audio.json`；接口均为新增，不影响旧路径。

## 交付点 2 — 按卡身份保存短信与通话（SQLite 存储）

- 提交：`dbafcfb`
- 变更：
  - 新增 `internal/history`：SQLite 存储，`glebarez/sqlite`（经 modernc，纯 Go 无 cgo）；库文件 `communication-history.sqlite`（配置目录，0600，目录 0700，WAL + `busy_timeout=5000` + `synchronous=FULL`，单连接）；`schema_migrations` v1；三表 `sms_messages` / `calls` / `outbox`；订阅快照 7 列；时间用 Unix 毫秒整数；重启后未结束通话标 `interrupted`，不补结束时间/时长。
  - 短信：先落盘后清理（`mergeSMS` 返回 `persistErr`，`pollSMSOnce` 在落盘失败时跳过 `AT+CMGD=1,4` 整库清理并经 `history_error` 显示）；去重键 = 方向 + 标准化号码 + SCTS 毫秒 + 正文哈希（长短信另含 concat ref/bits/total，不只靠号码+正文）；长短信重组后整条成行（分片元数据经 `receivedSMS.Concat` 落盘）；发件箱 `submitted/uncertain`，无法证明未发送时不记 `failed`、不自动补发。
  - 通话：经 SIP `HistorySink` 状态事件 `UpsertCall`，快照永不覆盖，无 CLCC 轮询；`+CDS` 送达报告（`State == "delivered"` 时）在释放 `deliveryMu` 后调 `MarkOutboxDeliveredByRef` 翻发件箱为 `delivered`。
  - API：`GET /api/history/calls`、`GET /api/history/sms`（`subscription_id`、`direction`、`missed=true`、`peer` 筛选，分页）、`GET /api/history/status`。
  - 附带最小必要接线（均在交付点 2 直接需求内）：`native_audio_stub.go` 补纯 Go 的 `AudioDevice` 定义（修交付点 1 引入的无 cgo 构建回归——该类型此前只定义在 `darwin && cgo` 文件里），音频设备测试加无 cgo 分支（503 含 "cgo" 时 skip），`go.mod` 直引标记修正。
- 自动测试：`internal/history` 10 个测试 + `cmd` 6 个测试函数（含 SIP 挂接端到端）全过；全量套件通过。
- 实机结果：未验证。
- 未验证项：重启保留、重复轮询、换卡、磁盘满等实机场景；Hook 安装窗口（USB-AT 约 1.2 秒）内开始且结束的通话可能漏记；SCTS 只有秒级精度，同一秒内同一号码的完全相同短信与重读无法区分；解析失败的 PDU 占位条目不落盘且会阻塞当轮 ME 清理（保守选择，防丢数据）；CMGL 存储序号未落盘（存储层无对应列，整库清理与去重都不用它）。
- 回退：`git revert dbafcfb`；删除 `communication-history.sqlite`（下次启动按迁移重建）。注意：库文件是本地通话/短信历史的唯一副本，删除即丢失本地历史（模块内仅保留近期短信）。

## 交付点 3 — 最近通话和未接记录，可直接回拨

- 提交：`487e5c4`
- 变更：
  - 最近通话视图：范围（全部/仅未接）、方向（呼入/呼出）、按卡（`subscription_id`，选项来自记录中的实际卡身份，含“归属未知”）筛选，列表复用交付点 2 的 `GET /api/history/calls` 与分页；每行显示联系人姓名（若已保存）、号码、方向、未接标记、时间、接通时长、结束原因中文标签（复用 `end_reason_label`）、归属卡。
  - 未接角标：`calls.seen` 列（迁移 v3）；新未接写入时未读，打开“最近通话”页成功渲染后调 `POST /api/calls/missed/seen` 清零；升级前已存在的未接默认已读（迁移 `DEFAULT 1`）；终结事件只在首次落到 ended 时写 `seen`，重复终结事件不再翻转。
  - 动作：`POST /api/calls/redial` 与 `POST /api/calls/sms` 内部转调原有 `POST /api/sip/call` 与 `POST /api/sms/send`（无 SIP 时原 handler 返回 503、绝不伪造成功，未建第二条拨号链，未碰 `internal/sip`）；`POST /api/calls/save-contact` 从通话记录保存联系人。
  - 联系人：此前仓库无联系人存储，按评审 §3 保守口径做最小实现——姓名 + 多号码 + 收藏，存同一 history 库 `contacts` 表（迁移 v3），按标准化号码去重（同号只改名、不产生重复联系人；号码已属于另一位联系人时拒绝追加）；不做系统通讯录导入与批量导入。新增 `GET /api/calls/missed-count`、`POST /api/calls/missed/seen`、`GET/POST/DELETE /api/contacts`。
- 自动测试：迁移 v1→v3（旧未接默认已读）、新建未接→未读/已接通→已读/振铃转未接→未读、按卡计数、联系人同号改名/多号码/收藏/删除/冲突拒绝、handler 的 400/404/503 边界；全量套件通过；demo 冒烟（`127.0.0.1:17575`，内存库）符合预期。
- 实机结果：未验证。
- 未验证项：真机回拨并挂断、从未接记录回拨的端到端、短信实际发出与送达状态、保存联系人后原生端/网页端展示一致性、重启后角标状态保留、多卡环境下的按卡筛选、联系人姓名在来电提醒里的显示（本交付点只做了通话记录列表内的姓名解析）。
- 回退：`git revert 487e5c4`；数据库已是 v3——回退后代码要求 v2，`Open` 会因版本不匹配拒绝打开，须删除 `communication-history.sqlite` 后重建（本地历史丢失，同交付点 2 的回退说明）。

## 交付点 4 — 短信会话、搜索与明确的发送状态

- 提交：`1e246a8`
- 变更：
  - 会话分组：服务端 `GROUP BY peer_normalized`，分页作用于会话；返回对方号码、归属卡（`subscription_id` + ICCID 脱敏）、最后一条（正文/方向/时间）、未读数、最后一条发件箱状态。
  - 搜索：`GET /api/conversations/search`，按号码或正文，服务端 LIKE，`%`/`_` 只按字面匹配（转义）；`q` 为空或超 200 字返回 400。
  - 未读：用 `sms_messages.is_read`，`POST /api/conversations/read` 只改收件未读行，返回 `{"marked": n}`。
  - 草稿：history DB 新表 `sms_drafts`（迁移 v2），不用 JSON 文件——草稿须重启保留，与历史同库即同备份、同 0600 权限、无需第二套锁；同一号码只有一条最新草稿（`peer_normalized` 主键，PUT 覆盖），正文上限 1000 字，空正文 PUT 等同删除；只存本地，不上传、不自动发送。
  - 发送状态：与发件箱一致（`queued`/`submitted`/`delivered`/`failed`/`uncertain`），不新建状态机；`uncertain` 界面明确提示“无法确认是否已发送，请勿重复发送”，无任何自动补发。
  - 导出：`GET /api/conversations/export?format=json|csv`，JSON 与 CSV 两种都提供（JSON 保真、CSV 便于核对），服务端流式分页生成、上限 10000 条（超限 `truncated=true`），不进前端内存。
  - API：`GET /api/conversations`、`/api/conversations/search`、`/api/conversations/thread`（发出短信附 `outbox_status`）、`/api/conversations/outbox`、`POST /api/conversations/read`、`GET/PUT/DELETE /api/conversations/drafts`、`/api/conversations/export`。列表与发送复用交付点 2 的 `/api/history/sms` 与 `POST /api/sms/send`。
  - 界面：短信视图内“会话”面板（搜索框 + 导出全部 + 会话列表 + 消息流 + 发送框 + 草稿状态，输入后 800ms 自动保存、打开恢复、可丢弃）；会话列表每 30 秒刷新（仅短信视图激活且未打开会话时）。
- 自动测试：新增 12 个测试全过（会话分组/分页/按卡筛选、未读筛选、搜索与 `%` 字面匹配、按会话与按 id 标记已读、发件箱查询与状态、草稿往返、v1 库真实降级重开补 v2 迁移；handler 层列表/消息流/搜索/未读/发件箱状态/草稿/JSON 与 CSV 导出）；demo 冒烟（`127.0.0.1:17575`，内存库）符合预期。
- 实机结果：未验证。
- 未验证项：真机收/发短信后的会话分组、未读角标、草稿重启保留、长短信分段状态、送达报告（+CDS）驱动 `delivered` 状态翻转（`delivered`/`failed` 仅由单元测试覆盖）；导出大库（>10000 条）的截断行为仅做了上限逻辑，未实测大数据量性能。
- 回退：`git revert 1e246a8`；数据库已是 v2——回退后代码要求 v1，`Open` 会因版本不匹配拒绝打开，须删除 `communication-history.sqlite` 后重建（本地历史丢失，同交付点 2 的回退说明）。

## 交付点 5 — 号码读取/备注和初始化诊断

- 提交：`8471d4c`（接线见 `8243c48`）
- 变更：
  - 新建 `internal/subscription`：`SubscriptionIdentity`（ICCID 优先推导 `subscription_id`，EID + Profile 兜底，手机号绝不作主键）、`NumberSource`（auto/remark/unknown）、`Snapshot()`、脱敏与备注规范化。
  - 号码读取复用已有 CNUM 解析（串口用 `QueryMSISDN`、USB-AT 用新导出 `ParseCNUM`），连接/换卡时读并缓存，不跟随 10 秒轮询；换卡失效、并发刷新守卫。
  - 备注持久化到 `subscription-remarks.json`（0600，临时文件 + 重命名，按卡隔离）。
  - 初始化诊断 `GET /api/diagnostics/init`（6 项：卡就绪、注册、麦克风权限、音频设备、语音路由、可恢复原因），结果与修复动作分开，通话中 `repairs_blocked=true`，不自动修复。
- 自动测试：24 个新测试通过；另用临时 demo 二进制冒烟验证过（未碰 live 服务）。
- 实机结果：未验证（设备读取档）。
- 未验证项：EID 目前为空（唯一来源会触发硬件 APDU 扫描，故不接）；`main.go` 启动刷新与切卡立即刷新未接线（靠 `status()` 挂接兜底，最多 10 秒延迟）。
- 回退：`git revert 8471d4c`（及接线 `8243c48` 的对应部分）；删除新增的 `subscription-remarks.json`；接口均为新增，不影响旧路径。

---

## 全量验证（最终树，HEAD `487e5c4`，MacBook 上跑过）

- `go build ./...`：通过（仅一条既有的 `-lobjc` 链接警告）
- `go vet ./...`：通过
- `go test -count=1 ./...`：12 个包全过
- `go test -race -count=1 ./...`：12 个包全过
- `make check`：1189 个文件，0 问题
- `CGO_ENABLED=0 go build ./...`：通过
- `make test-nocgo`：全过
- `GOARCH=arm64 CGO_ENABLED=0 go build ./...`：通过
- `node --check web/app.js`：通过（`make check` 内含）

## 部署情况

**未部署。** 线上服务仍跑旧二进制（launchd 托管，`/Applications/DJ 4G Hub.app/Contents/MacOS/dj4ghub-macos`，未重启）。本交付生效需重新构建 + 重新部署 + 重启服务：部署时先备份 live 二进制，用 `launchctl bootout/bootstrap` 重启，验证 health 200 与轮询连接。是否部署留待决定。

## 需要用户做的事（主要是实机验收，步骤 09）

1. 通话基线：10 次呼入 + 10 次呼出、耳机/扬声器各一组、一次 10 分钟通话；核对结束原因、时长、媒体就绪的展示。
2. 短信与通话存储：重启后历史保留、重复轮询不重复、换卡后归属不串、磁盘满时故障可见。
3. 最近通话：真机回拨并挂断、从未接记录回拨、未接角标清零、多卡筛选。
4. 短信会话：真机收/发后的会话分组、未读角标、草稿重启保留、长短信分段状态、送达报告翻 `delivered`。
5. 号码与诊断：换卡后号码重新读取、备注按卡隔离、初始化诊断 6 项的显示。

## 已知问题与备注（不阻塞交付）

- 交付点 1 曾引入无 cgo 构建回归（`AudioDevice` 只定义在 `darwin && cgo` 文件里），已在交付点 2 的提交 `dbafcfb` 里修复并验证通过。
- `internal/apduarbiter` 在一次全量跑中偶发失败一次（0.918 秒即结束），重跑 3 次均通过；属时序敏感测试，与本次改动无关（该包本次未动）。
- demo 模式的 `sendSMS` 会先调 `recordSMS("已发送至 …", …)`，导致 demo 下多出一条假收件会话；这是交付点 2 时代既有的 demo 路径行为，真机路径不经过它，如需清理建议另立小项。
- 交付点 3 与 4 并行期间的摩擦：交付点 3 的测试文件曾重复注册 sqlite 驱动导致 `go test ./internal/history/` 无法启动，对方已自行修好；交付点 4 为此把迁移测试的版本断言改为“断言 v2 迁移记录已补记、不争版本号”；交付点 3 测试文件里把 v3 写成“迁移 v2”的注释口径已由协调员在提交前修正为 v3。两交付点的提交顺序为先 4（v2）后 3（v3），与迁移链一致。
- 工作区另有他人未提交改动（`docs/README.md`、`docs/2026-11-08-phone-product-roadmap-review.md`、Swift 客户端 `cmd/dj4hub-client/ServiceManager.swift` 与 `tests/main.swift`），均未动、未提交，不属于本次交付。
- 约束遵守情况：无真实拨号、无真实短信、无设备配置改动、无固件刷写；测试数据均为占位号（10086/10010）与虚构 ICCID；记录文档不含真实号码、短信内容、密钥。15 步修复计划（认证步骤 05、USB 生命周期 06、AT 事务 07、实机验收 09）不在本次范围，未顺手做。

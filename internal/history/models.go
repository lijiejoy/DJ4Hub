package history

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/subscription"
)

// 通话状态：active 进行中；ended 已终结（挂断/未接/失败均已落定）；
// interrupted 服务重启时仍未结束，原因未知，不补结束时间与时长。
const (
	CallStatusActive      = "active"
	CallStatusEnded       = "ended"
	CallStatusInterrupted = "interrupted"
)

// 发件箱尝试状态：queued 已排队未尝试；submitted 已提交给模块/网络；
// delivered 收到送达报告；failed 已证明未发送；uncertain 无法证明
// 未发送（结果不确定）。uncertain 的行永远不自动补发、不自动重试，
// 只能由用户在界面上手动决定（评审 §5B）。
const (
	OutboxQueued    = "queued"
	OutboxSubmitted = "submitted"
	OutboxDelivered = "delivered"
	OutboxFailed    = "failed"
	OutboxUncertain = "uncertain"
)

// UnknownSubscriptionID 是读不到归属时的订阅引用，与
// subscription.DeriveSubscriptionID 的回落值一致；
// 界面应显示为“归属未知”，而不是空白或当前卡。
const UnknownSubscriptionID = "unknown"

// UnknownSnapshot 返回“归属未知”的事件快照：调用方在拿不到当前订阅
// 身份时用它记一条记录，而不是用空身份或事后用当前卡反填。
func UnknownSnapshot() subscription.Snapshot {
	return subscription.Snapshot{
		SubscriptionID: UnknownSubscriptionID,
		NumberSource:   subscription.NumberSourceUnknown,
		CapturedAt:     time.Now(),
	}
}

// NormalizeNumber 规范化对端号码用于分组与比较：去空格、横线与括号，
// 保留可选的 + 前缀。地区不明确时不盲目补国家码（评审 §3），
// 也不仅按尾号合并身份。
func NormalizeNumber(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(s)
	return s
}

// ConcatMeta 是长短信的重组状态。调用方在能拿到分片元数据时填写；
// 拿不到时为 nil，行按单条短信保存，concat_complete 仍为 true。
// Ref/RefBits/Total 来自短信 UDH 连接头；Segments 是已收到的分片数。
type ConcatMeta struct {
	Ref      int
	RefBits  int
	Total    int
	Segments int
	Complete bool
}

// SMSMessage 是一条逻辑短信（收或发）。长短信以重组后的整条保存，
// 分片元数据记在 Concat 里；未重组完的分片不单独成行。
type SMSMessage struct {
	ID             int64
	Direction      string // "in" | "out"
	PeerRaw        string
	PeerNormalized string
	Body           string
	Snapshot       subscription.Snapshot
	Concat         *ConcatMeta
	// DedupKey 为空时由存储层按 DedupKeyForSMS 计算。
	DedupKey   string
	OutboxID   *int64 // 仅发出的短信：关联的发件箱尝试行。
	IsRead     bool
	ReceivedAt time.Time
	CreatedAt  time.Time
}

// CallRecord 是一条通话记录。快照在首次见到该通话时写入，
// 后续更新永不覆盖快照——换卡不串归属。
type CallRecord struct {
	ID               int64
	CallID           string // 稳定通话 ID（SIP 服务内的 call-<seq>），唯一键。
	Direction        string // "inbound" | "outbound"
	NumberRaw        string
	NumberNormalized string
	Snapshot         subscription.Snapshot
	StartedAt        time.Time
	ConnectedAt      *time.Time
	EndedAt          *time.Time
	// DurationSeconds 只计接通段（ConnectedAt→EndedAt），未接通为 nil。
	DurationSeconds *int64
	EndReason       string
	EndState        string // 终结时的通话状态：ended/failed/missed。
	Missed          bool
	// Seen 表示用户是否已在“最近通话”里看到这条未接来电（交付点 3
	// 的角标依据）。只有未接（Missed）才可能为 false；已接通的通话
	// 写入时即为已读。升级前已存在的未接默认已读，避免旧记录点亮角标。
	Seen      bool
	Status    string // active/ended/interrupted。
	CreatedAt time.Time
	UpdatedAt time.Time
}

// OutboxAttempt 是一次发送尝试。Status 与 ErrorText 一起说明结果；
// 发送通道不报告失败发生在提交前还是提交后时，Status 记 uncertain。
type OutboxAttempt struct {
	ID             int64
	PeerRaw        string
	PeerNormalized string
	Body           string
	Segments       int
	MessageRefs    []int // 各分段的 +CMGS 消息引用（-1 表示模块未报告）。
	Snapshot       subscription.Snapshot
	Status         string
	ErrorText      string
	SMSMessageID   *int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// DedupKeyForSMS 计算短信去重键。去重依据绝不能只靠“号码+正文”：
//
//   - 单条短信：方向 + 标准化号码 + 网络时间戳（SCTS）+ 正文哈希。
//     合法重复短信（不同时间、同一内容）键不同，不会被误删；
//     同一 PDU 被重复轮询读到时键相同，不会重复落盘。
//   - 长短信：再加 UDH 引用号/位宽/总分片数，同一条长短信的
//     各分片只对应一个去重键。
//   - 发出的短信：按发件箱尝试行关联，同一尝试绝不重复记两条。
//
// 已知边界：SCTS 只有秒级精度，同一秒内来自同一号码的完全相同
// 短信与重读无法区分；彻底区分需要把模块存储序号（CMGL 序号）
// 纳入去重键，待 sms_transport.go 透传序号后再补。
func DedupKeyForSMS(m SMSMessage) string {
	norm := NormalizeNumber(m.PeerRaw)
	if m.PeerNormalized != "" {
		norm = m.PeerNormalized
	}
	bodyHash := sha256Hex(m.Body)
	switch {
	case m.Direction == "out" && m.OutboxID != nil:
		return fmt.Sprintf("out|%s|outbox:%d|%s", norm, *m.OutboxID, bodyHash)
	case m.Concat != nil:
		return fmt.Sprintf("in|%s|%d|concat:%d:%d:%d|%s",
			norm, m.ReceivedAt.UnixMilli(),
			m.Concat.Ref, m.Concat.RefBits, m.Concat.Total, bodyHash)
	default:
		return fmt.Sprintf("in|%s|%d|%s", norm, m.ReceivedAt.UnixMilli(), bodyHash)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// unixMillisNow 返回当前 Unix 毫秒（UTC），与库里的时间列一致。
func unixMillisNow() int64 { return time.Now().UnixMilli() }

// toMillis 把 time.Time 转成库内毫秒整数；零值返回 nil（列保持 NULL）。
func toMillis(t time.Time) *int64 {
	if t.IsZero() {
		return nil
	}
	ms := t.UnixMilli()
	return &ms
}

// fromMillis 把库内毫秒整数转回 time.Time；nil 表示列为 NULL。
func fromMillis(ms *int64) *time.Time {
	if ms == nil {
		return nil
	}
	t := time.UnixMilli(*ms).UTC()
	return &t
}

// fromMillisValue 把库内毫秒整数（NOT NULL 列）转回 time.Time。
func fromMillisValue(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}

// snapshotColumns 把快照拆成库列。CapturedAt 为零值时列保持 NULL。
func snapshotColumns(s subscription.Snapshot) (subscriptionID, iccid, imei, eid, ownNumber, numberSource string, snapshotAt *int64) {
	return s.SubscriptionID, s.ICCID, s.IMEI, s.EID, s.Number,
		string(s.NumberSource), toMillis(s.CapturedAt)
}

// snapshotFromColumns 把库列拼回快照。
func snapshotFromColumns(subscriptionID, iccid, imei, eid, ownNumber, numberSource string, snapshotAt *int64) subscription.Snapshot {
	snap := subscription.Snapshot{
		SubscriptionID: subscriptionID,
		ICCID:          iccid,
		IMEI:           imei,
		EID:            eid,
		Number:         ownNumber,
		NumberSource:   subscription.NumberSource(numberSource),
	}
	if t := fromMillis(snapshotAt); t != nil {
		snap.CapturedAt = *t
	}
	return snap
}

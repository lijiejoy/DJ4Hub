package history

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------------------------------------------------------------------------
// 行结构（仅供本包读写映射；对外模型见 models.go）
// ---------------------------------------------------------------------------

type smsRow struct {
	ID             int64  `gorm:"column:id"`
	Direction      string `gorm:"column:direction"`
	PeerRaw        string `gorm:"column:peer_raw"`
	PeerNormalized string `gorm:"column:peer_normalized"`
	Body           string `gorm:"column:body"`
	SubscriptionID string `gorm:"column:subscription_id"`
	ICCID          string `gorm:"column:iccid"`
	IMEI           string `gorm:"column:imei"`
	EID            string `gorm:"column:eid"`
	OwnNumber      string `gorm:"column:own_number"`
	NumberSource   string `gorm:"column:number_source"`
	SnapshotAt     *int64 `gorm:"column:snapshot_at"`
	ConcatRef      *int64 `gorm:"column:concat_ref"`
	ConcatRefBits  *int64 `gorm:"column:concat_ref_bits"`
	ConcatTotal    *int64 `gorm:"column:concat_total"`
	ConcatSegments *int64 `gorm:"column:concat_segments"`
	ConcatComplete int64  `gorm:"column:concat_complete"`
	DedupKey       string `gorm:"column:dedup_key"`
	OutboxID       *int64 `gorm:"column:outbox_id"`
	IsRead         int64  `gorm:"column:is_read"`
	ReceivedAt     int64  `gorm:"column:received_at"`
	CreatedAt      int64  `gorm:"column:created_at"`
}

type callRow struct {
	ID               int64  `gorm:"column:id"`
	CallID           string `gorm:"column:call_id"`
	Direction        string `gorm:"column:direction"`
	NumberRaw        string `gorm:"column:number_raw"`
	NumberNormalized string `gorm:"column:number_normalized"`
	SubscriptionID   string `gorm:"column:subscription_id"`
	ICCID            string `gorm:"column:iccid"`
	IMEI             string `gorm:"column:imei"`
	EID              string `gorm:"column:eid"`
	OwnNumber        string `gorm:"column:own_number"`
	NumberSource     string `gorm:"column:number_source"`
	SnapshotAt       *int64 `gorm:"column:snapshot_at"`
	StartedAt        int64  `gorm:"column:started_at"`
	ConnectedAt      *int64 `gorm:"column:connected_at"`
	EndedAt          *int64 `gorm:"column:ended_at"`
	DurationSeconds  *int64 `gorm:"column:duration_seconds"`
	EndReason        string `gorm:"column:end_reason"`
	EndState         string `gorm:"column:end_state"`
	Missed           int64  `gorm:"column:missed"`
	Seen             int64  `gorm:"column:seen"`
	Status           string `gorm:"column:status"`
	CreatedAt        int64  `gorm:"column:created_at"`
	UpdatedAt        int64  `gorm:"column:updated_at"`
}

type outboxRow struct {
	ID             int64  `gorm:"column:id"`
	PeerRaw        string `gorm:"column:peer_raw"`
	PeerNormalized string `gorm:"column:peer_normalized"`
	Body           string `gorm:"column:body"`
	Segments       int64  `gorm:"column:segments"`
	MessageRefs    string `gorm:"column:message_refs"`
	SubscriptionID string `gorm:"column:subscription_id"`
	ICCID          string `gorm:"column:iccid"`
	IMEI           string `gorm:"column:imei"`
	EID            string `gorm:"column:eid"`
	OwnNumber      string `gorm:"column:own_number"`
	NumberSource   string `gorm:"column:number_source"`
	SnapshotAt     *int64 `gorm:"column:snapshot_at"`
	Status         string `gorm:"column:status"`
	ErrorText      string `gorm:"column:error_text"`
	SMSMessageID   *int64 `gorm:"column:sms_message_id"`
	CreatedAt      int64  `gorm:"column:created_at"`
	UpdatedAt      int64  `gorm:"column:updated_at"`
}

func (r smsRow) toMessage() SMSMessage {
	m := SMSMessage{
		ID:             r.ID,
		Direction:      r.Direction,
		PeerRaw:        r.PeerRaw,
		PeerNormalized: r.PeerNormalized,
		Body:           r.Body,
		Snapshot: snapshotFromColumns(r.SubscriptionID, r.ICCID, r.IMEI, r.EID,
			r.OwnNumber, r.NumberSource, r.SnapshotAt),
		DedupKey:   r.DedupKey,
		OutboxID:   r.OutboxID,
		IsRead:     r.IsRead != 0,
		ReceivedAt: fromMillisValue(r.ReceivedAt),
		CreatedAt:  fromMillisValue(r.CreatedAt),
	}
	if r.ConcatRef != nil || r.ConcatTotal != nil {
		meta := &ConcatMeta{Complete: r.ConcatComplete != 0}
		if r.ConcatRef != nil {
			meta.Ref = int(*r.ConcatRef)
		}
		if r.ConcatRefBits != nil {
			meta.RefBits = int(*r.ConcatRefBits)
		}
		if r.ConcatTotal != nil {
			meta.Total = int(*r.ConcatTotal)
		}
		if r.ConcatSegments != nil {
			meta.Segments = int(*r.ConcatSegments)
		}
		m.Concat = meta
	}
	return m
}

func (r callRow) toRecord() CallRecord {
	return CallRecord{
		ID:               r.ID,
		CallID:           r.CallID,
		Direction:        r.Direction,
		NumberRaw:        r.NumberRaw,
		NumberNormalized: r.NumberNormalized,
		Snapshot: snapshotFromColumns(r.SubscriptionID, r.ICCID, r.IMEI, r.EID,
			r.OwnNumber, r.NumberSource, r.SnapshotAt),
		StartedAt:       fromMillisValue(r.StartedAt),
		ConnectedAt:     fromMillis(r.ConnectedAt),
		EndedAt:         fromMillis(r.EndedAt),
		DurationSeconds: r.DurationSeconds,
		EndReason:       r.EndReason,
		EndState:        r.EndState,
		Missed:          r.Missed != 0,
		Seen:            r.Seen != 0,
		Status:          r.Status,
		CreatedAt:       fromMillisValue(r.CreatedAt),
		UpdatedAt:       fromMillisValue(r.UpdatedAt),
	}
}

func (r outboxRow) toAttempt() OutboxAttempt {
	return OutboxAttempt{
		ID:             r.ID,
		PeerRaw:        r.PeerRaw,
		PeerNormalized: r.PeerNormalized,
		Body:           r.Body,
		Segments:       int(r.Segments),
		MessageRefs:    parseMessageRefs(r.MessageRefs),
		Snapshot: snapshotFromColumns(r.SubscriptionID, r.ICCID, r.IMEI, r.EID,
			r.OwnNumber, r.NumberSource, r.SnapshotAt),
		Status:       r.Status,
		ErrorText:    r.ErrorText,
		SMSMessageID: r.SMSMessageID,
		CreatedAt:    fromMillisValue(r.CreatedAt),
		UpdatedAt:    fromMillisValue(r.UpdatedAt),
	}
}

func formatMessageRefs(refs []int) string {
	parts := make([]string, 0, len(refs))
	for _, ref := range refs {
		parts = append(parts, strconv.Itoa(ref))
	}
	return strings.Join(parts, ",")
}

func parseMessageRefs(s string) []int {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []int
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// 短信
// ---------------------------------------------------------------------------

// SaveSMS 逐条保存一条逻辑短信。去重键冲突时不写入、不报错，
// 返回 saved=false；重复轮询不会产生重复记录。
func (s *Store) SaveSMS(ctx context.Context, m SMSMessage) (bool, error) {
	if m.Direction != "in" && m.Direction != "out" {
		return false, fmt.Errorf("save SMS: invalid direction %q", m.Direction)
	}
	if m.PeerNormalized == "" {
		m.PeerNormalized = NormalizeNumber(m.PeerRaw)
	}
	if m.DedupKey == "" {
		m.DedupKey = DedupKeyForSMS(m)
	}
	if m.Snapshot.SubscriptionID == "" {
		m.Snapshot = UnknownSnapshot()
	}
	subID, iccid, imei, eid, ownNumber, numberSource, snapshotAt := snapshotColumns(m.Snapshot)
	now := unixMillisNow()
	receivedAt := m.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = time.Now()
	}
	row := map[string]any{
		"direction": m.Direction, "peer_raw": m.PeerRaw,
		"peer_normalized": m.PeerNormalized, "body": m.Body,
		"subscription_id": subID, "iccid": iccid, "imei": imei, "eid": eid,
		"own_number": ownNumber, "number_source": numberSource,
		"snapshot_at":     snapshotAt,
		"concat_ref":      nilIfZeroConcat(m.Concat, func(c *ConcatMeta) int { return c.Ref }),
		"concat_ref_bits": nilIfZeroConcat(m.Concat, func(c *ConcatMeta) int { return c.RefBits }),
		"concat_total":    nilIfZeroConcat(m.Concat, func(c *ConcatMeta) int { return c.Total }),
		"concat_segments": nilIfZeroConcat(m.Concat, func(c *ConcatMeta) int { return c.Segments }),
		"concat_complete": boolToInt(m.Concat == nil || m.Concat.Complete),
		"dedup_key":       m.DedupKey,
		"outbox_id":       m.OutboxID,
		"is_read":         boolToInt(m.IsRead),
		"received_at":     receivedAt.UnixMilli(),
		"created_at":      now,
	}
	res := s.db.WithContext(ctx).Table("sms_messages").
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "dedup_key"}}, DoNothing: true}).
		Create(row)
	if res.Error != nil {
		return false, fmt.Errorf("save SMS: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

func nilIfZeroConcat(c *ConcatMeta, f func(*ConcatMeta) int) any {
	if c == nil {
		return nil
	}
	return f(c)
}

// SMSQuery 是短信分页查询条件。Limit 由存储层钳制在 1..maxPageSize，
// 永远不返回全量；Offset 小于 0 按 0 计。
type SMSQuery struct {
	SubscriptionID string
	Direction      string // "" | "in" | "out"
	PeerNormalized string
	Search         string // 交付点 4：匹配 peer_raw / peer_normalized / body（LIKE，见 smsWhere）
	UnreadOnly     bool   // 交付点 4：只返回未读收件
	Limit          int
	Offset         int
}

// maxPageSize 是单页上限；界面按页取，不设全量接口。
const maxPageSize = 100

const defaultPageSize = 50

func clampPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// ListSMS 按时间倒序分页返回短信，并返回符合条件的总数。
func (s *Store) ListSMS(ctx context.Context, q SMSQuery) ([]SMSMessage, int64, error) {
	limit, offset := clampPage(q.Limit, q.Offset)
	where, args := smsWhere(q)
	var total int64
	if err := s.db.WithContext(ctx).Table("sms_messages").Where(where, args...).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count SMS history: %w", err)
	}
	var rows []smsRow
	if err := s.db.WithContext(ctx).Table("sms_messages").Where(where, args...).
		Order("received_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list SMS history: %w", err)
	}
	out := make([]SMSMessage, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toMessage())
	}
	return out, total, nil
}

func smsWhere(q SMSQuery) (string, []any) {
	where := "1 = 1"
	var args []any
	if q.SubscriptionID != "" {
		where += " AND subscription_id = ?"
		args = append(args, q.SubscriptionID)
	}
	if q.Direction == "in" || q.Direction == "out" {
		where += " AND direction = ?"
		args = append(args, q.Direction)
	}
	if q.PeerNormalized != "" {
		where += " AND peer_normalized = ?"
		args = append(args, q.PeerNormalized)
	}
	if q.UnreadOnly {
		where += " AND direction = 'in' AND is_read = 0"
	}
	if q.Search != "" {
		like := "%" + escapeLike(q.Search) + "%"
		where += " AND (peer_raw LIKE ? ESCAPE '\\' OR peer_normalized LIKE ? ESCAPE '\\' OR body LIKE ? ESCAPE '\\')"
		args = append(args, like, like, like)
	}
	return where, args
}

// escapeLike 转义 LIKE 通配符，用户输入的 % / _ 只按字面匹配（交付点 4）。
func escapeLike(s string) string {
	return strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(s)
}

// ---------------------------------------------------------------------------
// 发件箱
// ---------------------------------------------------------------------------

// RecordOutbound 在同一事务里记一次发送尝试与对应的发出短信行。
// 发出的短信行通过 outbox_id 与尝试关联，同一尝试重记时去重键冲突，
// 不会重复产生短信行。调用方在尝试前/后各记一次状态变化请用
// UpdateOutboxStatus；本函数只记尝试的创建时刻。
func (s *Store) RecordOutbound(ctx context.Context, a OutboxAttempt) (outboxID int64, smsID int64, err error) {
	if !validOutboxStatus(a.Status) {
		return 0, 0, fmt.Errorf("record outbox: invalid status %q", a.Status)
	}
	if a.PeerNormalized == "" {
		a.PeerNormalized = NormalizeNumber(a.PeerRaw)
	}
	if a.Snapshot.SubscriptionID == "" {
		a.Snapshot = UnknownSnapshot()
	}
	subID, iccid, imei, eid, ownNumber, numberSource, snapshotAt := snapshotColumns(a.Snapshot)
	now := unixMillisNow()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Table("outbox").Create(map[string]any{
			"peer_raw": a.PeerRaw, "peer_normalized": a.PeerNormalized,
			"body": a.Body, "segments": a.Segments,
			"message_refs":    formatMessageRefs(a.MessageRefs),
			"subscription_id": subID, "iccid": iccid, "imei": imei, "eid": eid,
			"own_number": ownNumber, "number_source": numberSource,
			"snapshot_at": snapshotAt, "status": a.Status,
			"error_text": a.ErrorText, "sms_message_id": nil,
			"created_at": now, "updated_at": now,
		})
		if res.Error != nil {
			return fmt.Errorf("insert outbox: %w", res.Error)
		}
		var id int64
		if err := tx.Raw("SELECT last_insert_rowid()").Scan(&id).Error; err != nil {
			return fmt.Errorf("read outbox id: %w", err)
		}
		outboxID = id
		// 发出的短信行：去重键含 outbox id，同一尝试只产生一行。
		sms := SMSMessage{
			Direction: "out", PeerRaw: a.PeerRaw, Body: a.Body,
			Snapshot: a.Snapshot, OutboxID: &outboxID,
			ReceivedAt: time.UnixMilli(now).UTC(),
		}
		saved, err := s.saveSMSInTx(ctx, tx, sms)
		if err != nil {
			return err
		}
		if saved {
			var sid int64
			if err := tx.Raw("SELECT last_insert_rowid()").Scan(&sid).Error; err != nil {
				return fmt.Errorf("read SMS id: %w", err)
			}
			smsID = sid
			if err := tx.Table("outbox").Where("id = ?", outboxID).
				Update("sms_message_id", smsID).Error; err != nil {
				return fmt.Errorf("link outbox SMS: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("record outbox: %w", err)
	}
	return outboxID, smsID, nil
}

// saveSMSInTx 是 SaveSMS 的事务内版本（调用方持有 tx）。
func (s *Store) saveSMSInTx(ctx context.Context, tx *gorm.DB, m SMSMessage) (bool, error) {
	if m.PeerNormalized == "" {
		m.PeerNormalized = NormalizeNumber(m.PeerRaw)
	}
	if m.DedupKey == "" {
		m.DedupKey = DedupKeyForSMS(m)
	}
	if m.Snapshot.SubscriptionID == "" {
		m.Snapshot = UnknownSnapshot()
	}
	subID, iccid, imei, eid, ownNumber, numberSource, snapshotAt := snapshotColumns(m.Snapshot)
	now := unixMillisNow()
	receivedAt := m.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = time.Now()
	}
	res := tx.WithContext(ctx).Table("sms_messages").
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "dedup_key"}}, DoNothing: true}).
		Create(map[string]any{
			"direction": m.Direction, "peer_raw": m.PeerRaw,
			"peer_normalized": m.PeerNormalized, "body": m.Body,
			"subscription_id": subID, "iccid": iccid, "imei": imei, "eid": eid,
			"own_number": ownNumber, "number_source": numberSource,
			"snapshot_at":     snapshotAt,
			"concat_ref":      nilIfZeroConcat(m.Concat, func(c *ConcatMeta) int { return c.Ref }),
			"concat_ref_bits": nilIfZeroConcat(m.Concat, func(c *ConcatMeta) int { return c.RefBits }),
			"concat_total":    nilIfZeroConcat(m.Concat, func(c *ConcatMeta) int { return c.Total }),
			"concat_segments": nilIfZeroConcat(m.Concat, func(c *ConcatMeta) int { return c.Segments }),
			"concat_complete": boolToInt(m.Concat == nil || m.Concat.Complete),
			"dedup_key":       m.DedupKey,
			"outbox_id":       m.OutboxID,
			"is_read":         boolToInt(m.IsRead),
			"received_at":     receivedAt.UnixMilli(),
			"created_at":      now,
		})
	if res.Error != nil {
		return false, fmt.Errorf("save SMS: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

func validOutboxStatus(status string) bool {
	switch status {
	case OutboxQueued, OutboxSubmitted, OutboxDelivered, OutboxFailed, OutboxUncertain:
		return true
	default:
		return false
	}
}

// UpdateOutboxStatus 更新一次发送尝试的状态与错误说明。
// 不存在时返回 ErrNotFound。本函数不触发任何补发：状态只是记录。
func (s *Store) UpdateOutboxStatus(ctx context.Context, id int64, status string, errorText string) error {
	if !validOutboxStatus(status) {
		return fmt.Errorf("update outbox: invalid status %q", status)
	}
	res := s.db.WithContext(ctx).Table("outbox").Where("id = ?", id).Updates(map[string]any{
		"status": status, "error_text": errorText, "updated_at": unixMillisNow(),
	})
	if res.Error != nil {
		return fmt.Errorf("update outbox: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkOutboxDeliveredByRef 按 +CMGS 消息引用把最近一条已提交/不确定
// 的尝试标记为已送达（供送达报告 +CDS 接入时调用；本次不接线，
// 见 ussd_handlers.go 的 recordDeliveryReport）。
// 引用可能在不同消息间重复，只认最近一条；找不到返回 false。
func (s *Store) MarkOutboxDeliveredByRef(ctx context.Context, ref int) (bool, error) {
	var rows []outboxRow
	if err := s.db.WithContext(ctx).Table("outbox").
		Select("id", "message_refs").
		Where("status IN ?", []string{OutboxSubmitted, OutboxUncertain}).
		Order("id DESC").Limit(200).Find(&rows).Error; err != nil {
		return false, fmt.Errorf("find outbox by ref: %w", err)
	}
	for _, r := range rows {
		for _, candidate := range parseMessageRefs(r.MessageRefs) {
			if candidate == ref {
				if err := s.UpdateOutboxStatus(ctx, r.ID, OutboxDelivered, ""); err != nil {
					return false, err
				}
				return true, nil
			}
		}
	}
	return false, nil
}

// ---------------------------------------------------------------------------
// 通话
// ---------------------------------------------------------------------------

// UpsertCall 按稳定通话 ID 写入一次通话状态事件：首次见到插入，
// 之后只推进 connected_at/ended_at/时长/结束原因等进行性字段，
// 快照、开始时间与号码永不覆盖——换卡不串归属。
// 非终结事件不会把已终结的行翻回进行中。
func (s *Store) UpsertCall(ctx context.Context, rec CallRecord) error {
	if rec.CallID == "" {
		return errors.New("upsert call: empty call id")
	}
	if rec.Direction != "inbound" && rec.Direction != "outbound" {
		return fmt.Errorf("upsert call: invalid direction %q", rec.Direction)
	}
	if rec.NumberNormalized == "" {
		rec.NumberNormalized = NormalizeNumber(rec.NumberRaw)
	}
	if rec.Snapshot.SubscriptionID == "" {
		rec.Snapshot = UnknownSnapshot()
	}
	if rec.StartedAt.IsZero() {
		rec.StartedAt = time.Now()
	}
	now := unixMillisNow()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing callRow
		err := tx.Table("calls").Where("call_id = ?", rec.CallID).Take(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("read call: %w", err)
		}
		if err == nil {
			// 已存在：只推进进行性字段。
			updates := map[string]any{"updated_at": now}
			if rec.ConnectedAt != nil && existing.ConnectedAt == nil {
				updates["connected_at"] = rec.ConnectedAt.UnixMilli()
			}
			if rec.EndedAt != nil && existing.EndedAt == nil {
				updates["ended_at"] = rec.EndedAt.UnixMilli()
				updates["end_state"] = rec.EndState
				if rec.EndReason != "" {
					updates["end_reason"] = rec.EndReason
				}
				updates["missed"] = boolToInt(rec.Missed)
				updates["seen"] = boolToInt(!rec.Missed)
				if rec.DurationSeconds != nil {
					updates["duration_seconds"] = *rec.DurationSeconds
				}
				updates["status"] = CallStatusEnded
			}
			if len(updates) > 1 {
				if err := tx.Table("calls").Where("call_id = ?", rec.CallID).
					Updates(updates).Error; err != nil {
					return fmt.Errorf("update call: %w", err)
				}
			}
			return nil
		}
		// 首次见到：整行插入（含终结事件一步到位的情形）。
		subID, iccid, imei, eid, ownNumber, numberSource, snapshotAt := snapshotColumns(rec.Snapshot)
		status := CallStatusActive
		endReason := rec.EndReason
		if rec.EndedAt != nil {
			status = CallStatusEnded
			if endReason == "" {
				endReason = "unknown"
			}
		}
		row := map[string]any{
			"call_id": rec.CallID, "direction": rec.Direction,
			"number_raw": rec.NumberRaw, "number_normalized": rec.NumberNormalized,
			"subscription_id": subID, "iccid": iccid, "imei": imei, "eid": eid,
			"own_number": ownNumber, "number_source": numberSource,
			"snapshot_at":      snapshotAt,
			"started_at":       rec.StartedAt.UnixMilli(),
			"connected_at":     toMillis(ptrTime(rec.ConnectedAt)),
			"ended_at":         toMillis(ptrTime(rec.EndedAt)),
			"duration_seconds": rec.DurationSeconds,
			"end_reason":       endReason,
			"end_state":        rec.EndState,
			"missed":           boolToInt(rec.Missed),
			"seen":             boolToInt(!rec.Missed),
			"status":           status,
			"created_at":       now, "updated_at": now,
		}
		if err := tx.Table("calls").Create(row).Error; err != nil {
			return fmt.Errorf("insert call: %w", err)
		}
		return nil
	})
}

// ptrTime 解 *time.Time 的指针（nil 保持 nil）。
func ptrTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// CallQuery 是通话分页查询条件；Limit 同样钳制在 1..maxPageSize。
type CallQuery struct {
	SubscriptionID string
	Direction      string // "" | "inbound" | "outbound"
	MissedOnly     bool
	Limit          int
	Offset         int
}

// ListCalls 按开始时间倒序分页返回通话，并返回符合条件的总数。
func (s *Store) ListCalls(ctx context.Context, q CallQuery) ([]CallRecord, int64, error) {
	limit, offset := clampPage(q.Limit, q.Offset)
	where := "1 = 1"
	var args []any
	if q.SubscriptionID != "" {
		where += " AND subscription_id = ?"
		args = append(args, q.SubscriptionID)
	}
	if q.Direction == "inbound" || q.Direction == "outbound" {
		where += " AND direction = ?"
		args = append(args, q.Direction)
	}
	if q.MissedOnly {
		where += " AND missed = 1"
	}
	var total int64
	if err := s.db.WithContext(ctx).Table("calls").Where(where, args...).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count call history: %w", err)
	}
	var rows []callRow
	if err := s.db.WithContext(ctx).Table("calls").Where(where, args...).
		Order("started_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list call history: %w", err)
	}
	out := make([]CallRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toRecord())
	}
	return out, total, nil
}

// Counts 返回三张表的行数，供状态接口与诊断使用。
func (s *Store) Counts(ctx context.Context) (sms, calls, outbox int64, err error) {
	if err := s.db.WithContext(ctx).Table("sms_messages").Count(&sms).Error; err != nil {
		return 0, 0, 0, fmt.Errorf("count SMS history: %w", err)
	}
	if err := s.db.WithContext(ctx).Table("calls").Count(&calls).Error; err != nil {
		return 0, 0, 0, fmt.Errorf("count call history: %w", err)
	}
	if err := s.db.WithContext(ctx).Table("outbox").Count(&outbox).Error; err != nil {
		return 0, 0, 0, fmt.Errorf("count outbox: %w", err)
	}
	return sms, calls, outbox, nil
}

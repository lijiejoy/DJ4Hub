package history

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 短信会话、搜索、未读、草稿与发件箱查询（交付点 4，评审 §5 阶段 C / §8 交付点 4）。
//
// 复用交付点 2 的存储边界，不另起一套：
//   - 会话分组在数据库里做（GROUP BY peer_normalized），分页作用于会话
//     而非短信行；前端绝不拉全量再分组。
//   - 搜索复用 SMSQuery + ListSMS，只加一条 LIKE 条件。
//   - 未读复用 sms_messages.is_read；发出短信的送达状态只读发件箱
//     （submitted/delivered/failed/uncertain），不另建状态机。
//   - 草稿存在同一 SQLite 库的新表 sms_drafts（迁移 v2）：与历史同备份、
//     同文件权限，重启保留；草稿只存本地，不上传、不自动发送。

// Conversation 是按对端号码分组的短信会话。
type Conversation struct {
	PeerRaw          string
	PeerNormalized   string
	SubscriptionID   string
	ICCID            string
	OwnNumber        string
	NumberSource     string
	LastBody         string
	LastDirection    string
	LastAt           time.Time
	LastOutboxID     *int64
	LastOutboxStatus string
	UnreadCount      int64
	TotalCount       int64
}

// SMSDraft 是未发送的短信草稿：按对端号码存一条最新草稿。
type SMSDraft struct {
	PeerRaw        string
	PeerNormalized string
	Body           string
	UpdatedAt      time.Time
}

// ConversationQuery 是会话列表分页查询条件；Limit 同样钳制在 1..maxPageSize。
type ConversationQuery struct {
	SubscriptionID string
	UnreadOnly     bool
	Limit          int
	Offset         int
}

type conversationGroup struct {
	PeerNormalized string `gorm:"column:peer_normalized"`
	Total          int64  `gorm:"column:total"`
	Unread         int64  `gorm:"column:unread"`
	LastAt         int64  `gorm:"column:last_at"`
}

// ListConversations 按最后一条短信倒序分页返回会话，并返回会话总数。
// UnreadOnly 只返回至少有一条未读收件的会话（HAVING 过滤分组，不改行集）。
func (s *Store) ListConversations(ctx context.Context, q ConversationQuery) ([]Conversation, int64, error) {
	limit, offset := clampPage(q.Limit, q.Offset)
	where := "1 = 1"
	var args []any
	if q.SubscriptionID != "" {
		where += " AND subscription_id = ?"
		args = append(args, q.SubscriptionID)
	}
	having := ""
	if q.UnreadOnly {
		having = " HAVING SUM(CASE WHEN direction = 'in' AND is_read = 0 THEN 1 ELSE 0 END) > 0"
	}
	var total int64
	countSQL := "SELECT COUNT(*) FROM (SELECT peer_normalized FROM sms_messages WHERE " + where + " GROUP BY peer_normalized" + having + ")"
	if err := s.db.WithContext(ctx).Raw(countSQL, args...).Scan(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count SMS conversations: %w", err)
	}
	listSQL := `SELECT peer_normalized,
		COUNT(*) AS total,
		COALESCE(SUM(CASE WHEN direction = 'in' AND is_read = 0 THEN 1 ELSE 0 END), 0) AS unread,
		MAX(received_at) AS last_at
		FROM sms_messages WHERE ` + where + ` GROUP BY peer_normalized` + having +
		` ORDER BY last_at DESC, peer_normalized ASC LIMIT ? OFFSET ?`
	listArgs := append(append([]any{}, args...), limit, offset)
	var groups []conversationGroup
	if err := s.db.WithContext(ctx).Raw(listSQL, listArgs...).Scan(&groups).Error; err != nil {
		return nil, 0, fmt.Errorf("list SMS conversations: %w", err)
	}
	out := make([]Conversation, 0, len(groups))
	for _, g := range groups {
		last, _, err := s.ListSMS(ctx, SMSQuery{
			SubscriptionID: q.SubscriptionID,
			PeerNormalized: g.PeerNormalized,
			Limit:          1,
		})
		if err != nil {
			return nil, 0, fmt.Errorf("list SMS conversations: %w", err)
		}
		if len(last) == 0 {
			continue
		}
		m := last[0]
		c := Conversation{
			PeerRaw:        m.PeerRaw,
			PeerNormalized: g.PeerNormalized,
			SubscriptionID: m.Snapshot.SubscriptionID,
			ICCID:          m.Snapshot.ICCID,
			OwnNumber:      m.Snapshot.Number,
			NumberSource:   string(m.Snapshot.NumberSource),
			LastBody:       m.Body,
			LastDirection:  m.Direction,
			LastAt:         m.ReceivedAt,
			LastOutboxID:   m.OutboxID,
			UnreadCount:    g.Unread,
			TotalCount:     g.Total,
		}
		if m.OutboxID != nil {
			if statuses, err := s.OutboxStatusByIDs(ctx, []int64{*m.OutboxID}); err == nil {
				c.LastOutboxStatus = statuses[*m.OutboxID]
			}
		}
		out = append(out, c)
	}
	return out, total, nil
}

// MarkSMSRead 把指定会话（peer）或指定 id 的未读收件标为已读，
// 只动 direction='in' 且 is_read=0 的行；返回实际标记的条数。
// 发出的短信不参与未读计数，绝不被本函数改写。
func (s *Store) MarkSMSRead(ctx context.Context, peerNormalized string, ids []int64) (int64, error) {
	if peerNormalized == "" && len(ids) == 0 {
		return 0, errors.New("mark SMS read: peer or ids required")
	}
	tx := s.db.WithContext(ctx).Table("sms_messages").
		Where("direction = 'in' AND is_read = 0")
	if peerNormalized != "" {
		tx = tx.Where("peer_normalized = ?", peerNormalized)
	}
	if len(ids) > 0 {
		tx = tx.Where("id IN ?", ids)
	}
	res := tx.Update("is_read", 1)
	if res.Error != nil {
		return 0, fmt.Errorf("mark SMS read: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// UnreadSMSCount 返回未读收件数（可按订阅筛选）。
func (s *Store) UnreadSMSCount(ctx context.Context, subscriptionID string) (int64, error) {
	var n int64
	tx := s.db.WithContext(ctx).Table("sms_messages").
		Where("direction = 'in' AND is_read = 0")
	if subscriptionID != "" {
		tx = tx.Where("subscription_id = ?", subscriptionID)
	}
	if err := tx.Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count unread SMS: %w", err)
	}
	return n, nil
}

// OutboxQuery 是发件箱分页查询条件。
type OutboxQuery struct {
	SubscriptionID string
	PeerNormalized string
	Status         string
	Limit          int
	Offset         int
}

// ListOutbox 按创建时间倒序分页返回发送尝试（送达状态的唯一来源），并返回总数。
func (s *Store) ListOutbox(ctx context.Context, q OutboxQuery) ([]OutboxAttempt, int64, error) {
	limit, offset := clampPage(q.Limit, q.Offset)
	where := "1 = 1"
	var args []any
	if q.SubscriptionID != "" {
		where += " AND subscription_id = ?"
		args = append(args, q.SubscriptionID)
	}
	if q.PeerNormalized != "" {
		where += " AND peer_normalized = ?"
		args = append(args, q.PeerNormalized)
	}
	if q.Status != "" {
		if !validOutboxStatus(q.Status) {
			return nil, 0, fmt.Errorf("list outbox: invalid status %q", q.Status)
		}
		where += " AND status = ?"
		args = append(args, q.Status)
	}
	var total int64
	if err := s.db.WithContext(ctx).Table("outbox").Where(where, args...).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count outbox: %w", err)
	}
	var rows []outboxRow
	if err := s.db.WithContext(ctx).Table("outbox").Where(where, args...).
		Order("created_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list outbox: %w", err)
	}
	out := make([]OutboxAttempt, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toAttempt())
	}
	return out, total, nil
}

// OutboxStatusByIDs 批量取发件箱尝试的状态（供会话/导出做状态展示）。
func (s *Store) OutboxStatusByIDs(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID     int64  `gorm:"column:id"`
		Status string `gorm:"column:status"`
	}
	if err := s.db.WithContext(ctx).Table("outbox").Select("id", "status").
		Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read outbox statuses: %w", err)
	}
	for _, r := range rows {
		out[r.ID] = r.Status
	}
	return out, nil
}

// --- 草稿（sms_drafts，迁移 v2）------------------------------------------

type draftRow struct {
	PeerNormalized string `gorm:"column:peer_normalized"`
	PeerRaw        string `gorm:"column:peer_raw"`
	Body           string `gorm:"column:body"`
	UpdatedAt      int64  `gorm:"column:updated_at"`
}

func (r draftRow) toDraft() SMSDraft {
	return SMSDraft{
		PeerRaw:        r.PeerRaw,
		PeerNormalized: r.PeerNormalized,
		Body:           r.Body,
		UpdatedAt:      fromMillisValue(r.UpdatedAt),
	}
}

// SaveDraft 保存（或覆盖）某号码的最新草稿。同一号码永远只有一条草稿。
func (s *Store) SaveDraft(ctx context.Context, d SMSDraft) error {
	if d.PeerNormalized == "" {
		d.PeerNormalized = NormalizeNumber(d.PeerRaw)
	}
	if d.PeerNormalized == "" {
		return errors.New("save SMS draft: empty peer")
	}
	err := s.db.WithContext(ctx).Table("sms_drafts").Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "peer_normalized"}},
		DoUpdates: clause.AssignmentColumns([]string{"peer_raw", "body", "updated_at"}),
	}).Create(map[string]any{
		"peer_normalized": d.PeerNormalized,
		"peer_raw":        d.PeerRaw,
		"body":            d.Body,
		"updated_at":      unixMillisNow(),
	}).Error
	if err != nil {
		return fmt.Errorf("save SMS draft: %w", err)
	}
	return nil
}

// GetDraft 取某号码的草稿；没有返回 found=false。
func (s *Store) GetDraft(ctx context.Context, peerNormalized string) (SMSDraft, bool, error) {
	var row draftRow
	err := s.db.WithContext(ctx).Table("sms_drafts").
		Where("peer_normalized = ?", peerNormalized).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return SMSDraft{}, false, nil
	}
	if err != nil {
		return SMSDraft{}, false, fmt.Errorf("read SMS draft: %w", err)
	}
	return row.toDraft(), true, nil
}

// DeleteDraft 丢弃某号码的草稿；本来就没有也不报错。
func (s *Store) DeleteDraft(ctx context.Context, peerNormalized string) error {
	if err := s.db.WithContext(ctx).Table("sms_drafts").
		Where("peer_normalized = ?", peerNormalized).Delete(&draftRow{}).Error; err != nil {
		return fmt.Errorf("delete SMS draft: %w", err)
	}
	return nil
}

// ListDrafts 按最近更新倒序返回全部草稿。
func (s *Store) ListDrafts(ctx context.Context) ([]SMSDraft, error) {
	var rows []draftRow
	if err := s.db.WithContext(ctx).Table("sms_drafts").
		Order("updated_at DESC, peer_normalized ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list SMS drafts: %w", err)
	}
	out := make([]SMSDraft, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDraft())
	}
	return out, nil
}

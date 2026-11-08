package history

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 交付点 4 存储层测试：只用 10086/10010 占位号与虚构 ICCID，
// 不含真实号码、短信内容或凭据。

func seedConversations(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	snapA := testSnapshot("iccid:AAAAAAAAAAAAAAAAAAAA0001")
	snapB := testSnapshot("iccid:BBBBBBBBBBBBBBBBBBBB0002")
	msgs := []SMSMessage{
		{Direction: "in", PeerRaw: "10086", Body: "hello from 10086", Snapshot: snapA, ReceivedAt: base},
		{Direction: "in", PeerRaw: "10010", Body: "hello from 10010", Snapshot: snapB, ReceivedAt: base.Add(time.Minute)},
		{Direction: "in", PeerRaw: "10086", Body: "second from 10086", Snapshot: snapA, ReceivedAt: base.Add(2 * time.Minute)},
		{Direction: "out", PeerRaw: "10086", Body: "reply to 10086", Snapshot: snapA, ReceivedAt: base.Add(3 * time.Minute)},
	}
	for i, m := range msgs {
		if _, err := st.SaveSMS(ctx, m); err != nil {
			t.Fatalf("seed SMS %d: %v", i, err)
		}
	}
	// 发出的短信关联一次已提交的发件箱尝试，供会话状态展示核查。
	outboxID, _, err := st.RecordOutbound(ctx, OutboxAttempt{
		PeerRaw: "10086", Body: "reply to 10086", Segments: 1,
		MessageRefs: []int{7}, Snapshot: snapA, Status: OutboxSubmitted,
	})
	if err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	// RecordOutbound 已产生一条 out 短信行；把它与上面那条区分开：
	// 去重键含 outbox id，两行并存。取该尝试的短信行 id 备用。
	_ = outboxID
}

func TestListConversationsGrouping(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	seedConversations(t, st)

	rows, total, err := st.ListConversations(ctx, ConversationQuery{Limit: 50})
	if err != nil {
		t.Fatalf("ListConversations() = %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2 conversations", total)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	// 最后一条是 10086 的发出短信（含 RecordOutbound 产生的那条在内，
	// 仍以 10086 为最新会话），10010 次之。
	if rows[0].PeerNormalized != "10086" {
		t.Fatalf("rows[0].peer = %q, want 10086", rows[0].PeerNormalized)
	}
	if rows[0].TotalCount != 4 {
		t.Fatalf("rows[0].total = %d, want 4 (3 seeded + 1 outbox row)", rows[0].TotalCount)
	}
	if rows[0].UnreadCount != 2 {
		t.Fatalf("rows[0].unread = %d, want 2 inbound unread", rows[0].UnreadCount)
	}
	if rows[0].LastDirection != "out" {
		t.Fatalf("rows[0].last_direction = %q, want out", rows[0].LastDirection)
	}
	if rows[1].PeerNormalized != "10010" || rows[1].TotalCount != 1 || rows[1].UnreadCount != 1 {
		t.Fatalf("rows[1] = %+v, want peer 10010 total 1 unread 1", rows[1])
	}
	if rows[1].LastBody != "hello from 10010" {
		t.Fatalf("rows[1].last_body = %q", rows[1].LastBody)
	}
	// 分页作用于会话：limit=1 只取最新会话。
	page, total2, err := st.ListConversations(ctx, ConversationQuery{Limit: 1})
	if err != nil || total2 != 2 || len(page) != 1 || page[0].PeerNormalized != "10086" {
		t.Fatalf("paged = %+v total=%d err=%v", page, total2, err)
	}
	// 按订阅筛选：只剩 snapA 的 10086 会话。
	sub, total3, err := st.ListConversations(ctx, ConversationQuery{
		SubscriptionID: "iccid:AAAAAAAAAAAAAAAAAAAA0001", Limit: 50,
	})
	if err != nil || total3 != 1 || len(sub) != 1 || sub[0].PeerNormalized != "10086" {
		t.Fatalf("subscription filtered = %+v total=%d err=%v", sub, total3, err)
	}
}

func TestListConversationsUnreadOnly(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	seedConversations(t, st)
	if _, err := st.MarkSMSRead(ctx, "10010", nil); err != nil {
		t.Fatalf("MarkSMSRead(10010) = %v", err)
	}
	rows, total, err := st.ListConversations(ctx, ConversationQuery{UnreadOnly: true, Limit: 50})
	if err != nil {
		t.Fatalf("ListConversations(unread) = %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].PeerNormalized != "10086" {
		t.Fatalf("unread-only = %+v total=%d, want only 10086", rows, total)
	}
}

func TestSearchSMSByBodyAndPeer(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	seedConversations(t, st)

	rows, total, err := st.ListSMS(ctx, SMSQuery{Search: "second from", Limit: 50})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Body != "second from 10086" {
		t.Fatalf("search body = %+v total=%d err=%v", rows, total, err)
	}
	rows, total, err = st.ListSMS(ctx, SMSQuery{Search: "10010", Limit: 50})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].PeerNormalized != "10010" {
		t.Fatalf("search peer = %+v total=%d err=%v", rows, total, err)
	}
	// LIKE 通配符只按字面匹配：含 % 的查询不应匹配任意内容。
	rows, total, err = st.ListSMS(ctx, SMSQuery{Search: "100%", Limit: 50})
	if err != nil {
		t.Fatalf("search wildcard = %v", err)
	}
	if total != 0 {
		t.Fatalf("search \"100%%\" total = %d, want 0 (literal match)", total)
	}
	_ = rows
}

func TestMarkSMSReadByPeerAndIDs(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	seedConversations(t, st)

	marked, err := st.MarkSMSRead(ctx, "10086", nil)
	if err != nil || marked != 2 {
		t.Fatalf("MarkSMSRead(peer) = %d, %v; want 2", marked, err)
	}
	// 再次标记同一会话：已读的不重复计数。
	marked, err = st.MarkSMSRead(ctx, "10086", nil)
	if err != nil || marked != 0 {
		t.Fatalf("MarkSMSRead(peer) again = %d, %v; want 0", marked, err)
	}
	// 按 id 标记：发出的短信行（out）绝不被改写，未读数只剩 10010 的 1 条。
	n, err := st.UnreadSMSCount(ctx, "")
	if err != nil || n != 1 {
		t.Fatalf("UnreadSMSCount() = %d, %v; want 1", n, err)
	}
	rows, _, err := st.ListSMS(ctx, SMSQuery{PeerNormalized: "10010", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("list 10010 = %+v err=%v", rows, err)
	}
	marked, err = st.MarkSMSRead(ctx, "", []int64{rows[0].ID})
	if err != nil || marked != 1 {
		t.Fatalf("MarkSMSRead(ids) = %d, %v; want 1", marked, err)
	}
	n, _ = st.UnreadSMSCount(ctx, "")
	if n != 0 {
		t.Fatalf("UnreadSMSCount() after all read = %d, want 0", n)
	}
	// peer 与 ids 都为空是调用错误。
	if _, err := st.MarkSMSRead(ctx, "", nil); err == nil {
		t.Fatalf("MarkSMSRead(empty) = nil error, want error")
	}
}

func TestListOutboxAndStatusByIDs(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	seedConversations(t, st)

	rows, total, err := st.ListOutbox(ctx, OutboxQuery{PeerNormalized: "10086", Limit: 50})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("ListOutbox(10086) = %+v total=%d err=%v", rows, total, err)
	}
	if rows[0].Status != OutboxSubmitted {
		t.Fatalf("outbox status = %q, want submitted", rows[0].Status)
	}
	statuses, err := st.OutboxStatusByIDs(ctx, []int64{rows[0].ID})
	if err != nil || statuses[rows[0].ID] != OutboxSubmitted {
		t.Fatalf("OutboxStatusByIDs() = %+v err=%v", statuses, err)
	}
	// 非法状态拒绝。
	if _, _, err := st.ListOutbox(ctx, OutboxQuery{Status: "nope"}); err == nil {
		t.Fatalf("ListOutbox(bad status) = nil error, want error")
	}
	// 空 id 列表返回空 map，不报错。
	empty, err := st.OutboxStatusByIDs(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("OutboxStatusByIDs(nil) = %+v err=%v", empty, err)
	}
}

func TestDraftsRoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	if _, found, err := st.GetDraft(ctx, "10086"); err != nil || found {
		t.Fatalf("GetDraft(missing) = found=%v err=%v", found, err)
	}
	if err := st.SaveDraft(ctx, SMSDraft{PeerRaw: "10086", Body: "草稿一"}); err != nil {
		t.Fatalf("SaveDraft() = %v", err)
	}
	d, found, err := st.GetDraft(ctx, "10086")
	if err != nil || !found || d.Body != "草稿一" {
		t.Fatalf("GetDraft() = %+v found=%v err=%v", d, found, err)
	}
	// 同一号码保存覆盖旧草稿，永远只有一条。
	if err := st.SaveDraft(ctx, SMSDraft{PeerRaw: "10086", Body: "草稿二"}); err != nil {
		t.Fatalf("SaveDraft(overwrite) = %v", err)
	}
	list, err := st.ListDrafts(ctx)
	if err != nil || len(list) != 1 || list[0].Body != "草稿二" {
		t.Fatalf("ListDrafts() = %+v err=%v", list, err)
	}
	if err := st.DeleteDraft(ctx, "10086"); err != nil {
		t.Fatalf("DeleteDraft() = %v", err)
	}
	if _, found, _ := st.GetDraft(ctx, "10086"); found {
		t.Fatalf("GetDraft() after delete found=true")
	}
	// 删除不存在的草稿也不报错；空号码拒绝保存。
	if err := st.DeleteDraft(ctx, "10086"); err != nil {
		t.Fatalf("DeleteDraft(missing) = %v", err)
	}
	if err := st.SaveDraft(ctx, SMSDraft{Body: "no peer"}); err == nil {
		t.Fatalf("SaveDraft(empty peer) = nil error, want error")
	}
}

func TestOpenMigratesV1ToV2(t *testing.T) {
	// 真实模拟 v1 库：先建库、写一条短信，再删掉 v2 迁移记录与草稿表，
	// 重新 Open 应自动补应用 v2（以及之后的其他迁移），版本号变为
	// CurrentSchemaVersion，且旧短信保留、草稿表可用。
	// 注意：版本号不硬编码——交付点 3 已把当前版本推到 v3，本测试只
	// 断言 v2 迁移本身被补应用，不争版本号。
	st := openTestStore(t)
	ctx := context.Background()
	path := st.Path()
	seedConversations(t, st)
	if err := st.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	raw, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("raw open v1-simulated DB = %v", err)
	}
	if err := raw.Exec("DELETE FROM schema_migrations WHERE version = 2").Error; err != nil {
		t.Fatalf("strip v2 migration row = %v", err)
	}
	if err := raw.Exec("DROP TABLE sms_drafts").Error; err != nil {
		t.Fatalf("drop sms_drafts = %v", err)
	}
	sqlDB, _ := raw.DB()
	_ = sqlDB.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open(v1 DB) = %v", err)
	}
	defer st2.Close()
	version, err := st2.SchemaVersion(ctx)
	if err != nil || version != CurrentSchemaVersion {
		t.Fatalf("SchemaVersion() after migrate = %d, %v; want %d", version, err, CurrentSchemaVersion)
	}
	// v2 迁移记录已补记。
	var v2count int64
	if err := st2.db.WithContext(ctx).Raw(
		"SELECT COUNT(*) FROM schema_migrations WHERE version = 2").Scan(&v2count).Error; err != nil || v2count != 1 {
		t.Fatalf("v2 migration applied = %d, err = %v; want 1", v2count, err)
	}
	// 旧数据保留。
	rows, total, err := st2.ListConversations(ctx, ConversationQuery{Limit: 50})
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("conversations after migrate = %+v total=%d err=%v", rows, total, err)
	}
	// 草稿表可用。
	if err := st2.SaveDraft(ctx, SMSDraft{PeerRaw: "10010", Body: "v2 ok"}); err != nil {
		t.Fatalf("SaveDraft() after migrate = %v", err)
	}
}

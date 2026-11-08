package history

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/subscription"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q) = %v", path, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func testSnapshot(id string) subscription.Snapshot {
	return subscription.Snapshot{
		SubscriptionID: id,
		ICCID:          "89860000000000000001",
		IMEI:           "860000000000001",
		EID:            "",
		Number:         "+8613800000000",
		NumberSource:   subscription.NumberSourceAuto,
		CapturedAt:     time.Now(),
	}
}

func TestOpenMigratesAndReportsVersion(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	version, err := st.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion() = %v", err)
	}
	if version != CurrentSchemaVersion {
		t.Fatalf("SchemaVersion() = %d, want %d", version, CurrentSchemaVersion)
	}
	// 重新打开同一文件：迁移不重复执行，数据保留。
	path := st.Path()
	if err := st.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open(%q) = %v", path, err)
	}
	defer st2.Close()
	version, err = st2.SchemaVersion(ctx)
	if err != nil || version != CurrentSchemaVersion {
		t.Fatalf("re-Open SchemaVersion() = %d, %v", version, err)
	}
}

func TestOpenMemoryStore(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) = %v", err)
	}
	defer st.Close()
	if version, err := st.SchemaVersion(context.Background()); err != nil || version != CurrentSchemaVersion {
		t.Fatalf("memory SchemaVersion() = %d, %v", version, err)
	}
}

func TestSaveSMSDedupAndLegitRepeat(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	ts := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	msg := SMSMessage{
		Direction: "in", PeerRaw: "+86 138-0000-0000",
		Body: "test body", Snapshot: testSnapshot("iccid:89860000000000000001"),
		ReceivedAt: ts,
	}
	saved, err := st.SaveSMS(ctx, msg)
	if err != nil || !saved {
		t.Fatalf("SaveSMS(first) = (%v, %v), want (true, nil)", saved, err)
	}
	// 同一 PDU 重复轮询：不重复落盘。
	saved, err = st.SaveSMS(ctx, msg)
	if err != nil || saved {
		t.Fatalf("SaveSMS(repeat poll) = (%v, %v), want (false, nil)", saved, err)
	}
	// 合法重复短信（不同时间、同一内容）：必须保留。
	msg.ReceivedAt = ts.Add(time.Minute)
	saved, err = st.SaveSMS(ctx, msg)
	if err != nil || !saved {
		t.Fatalf("SaveSMS(legit repeat) = (%v, %v), want (true, nil)", saved, err)
	}
	rows, total, err := st.ListSMS(ctx, SMSQuery{Limit: 10})
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("ListSMS() = %d rows, total %d, %v", len(rows), total, err)
	}
	if rows[0].PeerNormalized != "+8613800000000" {
		t.Fatalf("PeerNormalized = %q, want normalized number", rows[0].PeerNormalized)
	}
	// 归属未知：空快照记为 unknown，可按 unknown 筛选。
	unknown := SMSMessage{
		Direction: "in", PeerRaw: "10086", Body: "unknown owner",
		ReceivedAt: ts.Add(2 * time.Minute),
	}
	if saved, err := st.SaveSMS(ctx, unknown); err != nil || !saved {
		t.Fatalf("SaveSMS(unknown) = (%v, %v)", saved, err)
	}
	rows, total, err = st.ListSMS(ctx, SMSQuery{SubscriptionID: UnknownSubscriptionID, Limit: 10})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("ListSMS(unknown) = %d rows, total %d, %v", len(rows), total, err)
	}
	if rows[0].Snapshot.SubscriptionID != UnknownSubscriptionID {
		t.Fatalf("unknown snapshot = %q", rows[0].Snapshot.SubscriptionID)
	}
}

func TestSaveSMSConcatRoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	ts := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	msg := SMSMessage{
		Direction: "in", PeerRaw: "10086", Body: "part1part2",
		Snapshot:   testSnapshot("iccid:89860000000000000001"),
		Concat:     &ConcatMeta{Ref: 42, RefBits: 8, Total: 2, Segments: 2, Complete: true},
		ReceivedAt: ts,
	}
	if saved, err := st.SaveSMS(ctx, msg); err != nil || !saved {
		t.Fatalf("SaveSMS(concat) = (%v, %v)", saved, err)
	}
	// 同一条长短信重读不重复；单条同内容短信（无 concat）键不同，不冲突。
	if saved, err := st.SaveSMS(ctx, msg); err != nil || saved {
		t.Fatalf("SaveSMS(concat repeat) = (%v, %v), want (false, nil)", saved, err)
	}
	single := msg
	single.Concat = nil
	if saved, err := st.SaveSMS(ctx, single); err != nil || !saved {
		t.Fatalf("SaveSMS(single same body) = (%v, %v), want (true, nil)", saved, err)
	}
	rows, total, err := st.ListSMS(ctx, SMSQuery{Limit: 10})
	if err != nil || total != 2 {
		t.Fatalf("ListSMS() total = %d, %v", total, err)
	}
	var concatRow *SMSMessage
	for i := range rows {
		if rows[i].Concat != nil {
			concatRow = &rows[i]
		}
	}
	if concatRow == nil {
		t.Fatal("concat row not found")
	}
	if concatRow.Concat.Ref != 42 || concatRow.Concat.Total != 2 || !concatRow.Concat.Complete {
		t.Fatalf("concat = %+v", concatRow.Concat)
	}
}

func TestOutboxLifecycle(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	attempt := OutboxAttempt{
		PeerRaw: "+8613800000000", Body: "hello", Segments: 1,
		MessageRefs: []int{7}, Snapshot: testSnapshot("iccid:89860000000000000001"),
		Status: OutboxSubmitted,
	}
	outboxID, smsID, err := st.RecordOutbound(ctx, attempt)
	if err != nil || outboxID == 0 || smsID == 0 {
		t.Fatalf("RecordOutbound() = (%d, %d, %v)", outboxID, smsID, err)
	}
	// 发出的短信行已关联尝试。
	rows, total, err := st.ListSMS(ctx, SMSQuery{Direction: "out", Limit: 10})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("ListSMS(out) = %d rows, total %d, %v", len(rows), total, err)
	}
	if rows[0].OutboxID == nil || *rows[0].OutboxID != outboxID {
		t.Fatalf("out SMS outbox_id = %v, want %d", rows[0].OutboxID, outboxID)
	}
	// 第二次调用是另一次独立尝试（新的 outbox 行与新的短信行），
	// 不是同一尝试的重记；同一尝试绝不会由本函数重复产生。
	outboxID2, _, err := st.RecordOutbound(ctx, attempt)
	if err != nil || outboxID2 == outboxID {
		t.Fatalf("RecordOutbound(second attempt) = (%d, %v), want new id", outboxID2, err)
	}
	rows, total, err = st.ListSMS(ctx, SMSQuery{Direction: "out", Limit: 10})
	if err != nil || total != 2 {
		t.Fatalf("ListSMS(out) after second attempt = total %d, %v", total, err)
	}
	// 结果不确定：只标记，不重发（本包根本不提供重发入口）。
	if err := st.UpdateOutboxStatus(ctx, outboxID, OutboxUncertain, "send timeout"); err != nil {
		t.Fatalf("UpdateOutboxStatus(uncertain) = %v", err)
	}
	// 送达报告按引用匹配最近一条。
	if ok, err := st.MarkOutboxDeliveredByRef(ctx, 7); err != nil || !ok {
		t.Fatalf("MarkOutboxDeliveredByRef(7) = (%v, %v)", ok, err)
	}
	if ok, err := st.MarkOutboxDeliveredByRef(ctx, 999); err != nil || ok {
		t.Fatalf("MarkOutboxDeliveredByRef(999) = (%v, %v), want (false, nil)", ok, err)
	}
	if err := st.UpdateOutboxStatus(ctx, 999999, OutboxFailed, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateOutboxStatus(missing) = %v, want ErrNotFound", err)
	}
	if err := st.UpdateOutboxStatus(ctx, outboxID, "bogus", ""); err == nil {
		t.Fatal("UpdateOutboxStatus(bogus) = nil, want error")
	}
}

func TestUpsertCallLifecycleAndSnapshotFreeze(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	snapA := testSnapshot("iccid:AAAAAAAAAAAAAAAAAAAA0001")
	snapB := testSnapshot("iccid:BBBBBBBBBBBBBBBBBBBB0002")
	// 响铃。
	if err := st.UpsertCall(ctx, CallRecord{
		CallID: "call-1", Direction: "inbound", NumberRaw: "+8613800000000",
		Snapshot: snapA, StartedAt: start,
	}); err != nil {
		t.Fatalf("UpsertCall(ringing) = %v", err)
	}
	// 接通（换卡后的快照不应覆盖首次快照）。
	connected := start.Add(5 * time.Second)
	dur := int64(0)
	if err := st.UpsertCall(ctx, CallRecord{
		CallID: "call-1", Direction: "inbound", NumberRaw: "+8613800000000",
		Snapshot: snapB, StartedAt: start, ConnectedAt: &connected,
		DurationSeconds: &dur,
	}); err != nil {
		t.Fatalf("UpsertCall(active) = %v", err)
	}
	// 挂断。
	ended := start.Add(65 * time.Second)
	dur = 60
	if err := st.UpsertCall(ctx, CallRecord{
		CallID: "call-1", Direction: "inbound", NumberRaw: "+8613800000000",
		Snapshot: snapB, StartedAt: start, ConnectedAt: &connected,
		EndedAt: &ended, DurationSeconds: &dur,
		EndReason: "remote_hangup", EndState: "ended",
	}); err != nil {
		t.Fatalf("UpsertCall(ended) = %v", err)
	}
	rows, total, err := st.ListCalls(ctx, CallQuery{Limit: 10})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("ListCalls() = %d rows, total %d, %v", len(rows), total, err)
	}
	rec := rows[0]
	if rec.Snapshot.SubscriptionID != snapA.SubscriptionID {
		t.Fatalf("snapshot = %q, want first-seen %q (frozen)", rec.Snapshot.SubscriptionID, snapA.SubscriptionID)
	}
	if rec.ConnectedAt == nil || !rec.ConnectedAt.Equal(connected) {
		t.Fatalf("connected_at = %v, want %v", rec.ConnectedAt, connected)
	}
	if rec.EndedAt == nil || !rec.EndedAt.Equal(ended) {
		t.Fatalf("ended_at = %v, want %v", rec.EndedAt, ended)
	}
	if rec.DurationSeconds == nil || *rec.DurationSeconds != 60 {
		t.Fatalf("duration = %v, want 60", rec.DurationSeconds)
	}
	if rec.EndReason != "remote_hangup" || rec.Status != CallStatusEnded {
		t.Fatalf("end = %q/%q", rec.EndReason, rec.Status)
	}
	// 终结后的非终结事件不得把行翻回进行中。
	if err := st.UpsertCall(ctx, CallRecord{
		CallID: "call-1", Direction: "inbound", NumberRaw: "+8613800000000",
		Snapshot: snapA, StartedAt: start,
	}); err != nil {
		t.Fatalf("UpsertCall(late non-terminal) = %v", err)
	}
	rows, _, err = st.ListCalls(ctx, CallQuery{Limit: 10})
	if err != nil || rows[0].Status != CallStatusEnded || rows[0].EndedAt == nil {
		t.Fatalf("late event changed terminal row: %+v, %v", rows[0], err)
	}
}

func TestUpsertCallMissed(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	ended := start.Add(30 * time.Second)
	if err := st.UpsertCall(ctx, CallRecord{
		CallID: "call-9", Direction: "inbound", NumberRaw: "10086",
		Snapshot: testSnapshot("iccid:89860000000000000001"), StartedAt: start,
		EndedAt: &ended, EndReason: "no_answer", EndState: "missed", Missed: true,
	}); err != nil {
		t.Fatalf("UpsertCall(missed) = %v", err)
	}
	rows, total, err := st.ListCalls(ctx, CallQuery{MissedOnly: true, Limit: 10})
	if err != nil || total != 1 || !rows[0].Missed {
		t.Fatalf("ListCalls(missed) = %d rows, total %d, %v", len(rows), total, err)
	}
	if rows[0].DurationSeconds != nil {
		t.Fatalf("missed duration = %v, want nil (never connected)", rows[0].DurationSeconds)
	}
}

func TestOpenMarksInterruptedCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	ctx := context.Background()
	start := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	if err := st.UpsertCall(ctx, CallRecord{
		CallID: "call-77", Direction: "outbound", NumberRaw: "10086",
		Snapshot: testSnapshot("iccid:89860000000000000001"), StartedAt: start,
	}); err != nil {
		t.Fatalf("UpsertCall(active) = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	// 重启打开：未结束通话标记中断，不补结束时间与时长。
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open() = %v", err)
	}
	defer st2.Close()
	rows, total, err := st2.ListCalls(ctx, CallQuery{Limit: 10})
	if err != nil || total != 1 {
		t.Fatalf("ListCalls() = total %d, %v", total, err)
	}
	rec := rows[0]
	if rec.Status != CallStatusInterrupted {
		t.Fatalf("status = %q, want interrupted", rec.Status)
	}
	if rec.EndedAt != nil || rec.DurationSeconds != nil {
		t.Fatalf("interrupted call gained ended_at=%v duration=%v", rec.EndedAt, rec.DurationSeconds)
	}
	if rec.EndReason != "unknown" {
		t.Fatalf("end_reason = %q, want unknown", rec.EndReason)
	}
}

func TestListPaginationAndFilters(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		sub := "iccid:AAAAAAAAAAAAAAAAAAAA0001"
		if i%2 == 1 {
			sub = "iccid:BBBBBBBBBBBBBBBBBBBB0002"
		}
		if _, err := st.SaveSMS(ctx, SMSMessage{
			Direction: "in", PeerRaw: "10086",
			Body:       "msg",
			Snapshot:   testSnapshot(sub),
			ReceivedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("SaveSMS(%d) = %v", i, err)
		}
	}
	// 分页：limit 2，倒序。
	page1, total, err := st.ListSMS(ctx, SMSQuery{Limit: 2, Offset: 0})
	if err != nil || total != 5 || len(page1) != 2 {
		t.Fatalf("page1 = %d rows, total %d, %v", len(page1), total, err)
	}
	if !page1[0].ReceivedAt.After(page1[1].ReceivedAt) {
		t.Fatal("page1 not in descending time order")
	}
	page2, _, err := st.ListSMS(ctx, SMSQuery{Limit: 2, Offset: 2})
	if err != nil || len(page2) != 2 {
		t.Fatalf("page2 = %d rows, %v", len(page2), err)
	}
	page3, _, err := st.ListSMS(ctx, SMSQuery{Limit: 2, Offset: 4})
	if err != nil || len(page3) != 1 {
		t.Fatalf("page3 = %d rows, %v", len(page3), err)
	}
	// 订阅筛选。
	rows, total, err := st.ListSMS(ctx, SMSQuery{
		SubscriptionID: "iccid:BBBBBBBBBBBBBBBBBBBB0002", Limit: 10,
	})
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("filtered = %d rows, total %d, %v", len(rows), total, err)
	}
	// limit 越界被钳制，不存在全量返回。
	rows, total, err = st.ListSMS(ctx, SMSQuery{Limit: 10000})
	if err != nil || total != 5 || len(rows) != 5 {
		t.Fatalf("clamped = %d rows, total %d, %v", len(rows), total, err)
	}
	// 计数。
	sms, calls, outbox, err := st.Counts(ctx)
	if err != nil || sms != 5 || calls != 0 || outbox != 0 {
		t.Fatalf("Counts() = (%d, %d, %d, %v)", sms, calls, outbox, err)
	}
}

func TestNormalizeNumber(t *testing.T) {
	cases := map[string]string{
		"+86 138-0000-0000": "+8613800000000",
		"(010) 1234 5678":   "01012345678",
		"10086":             "10086",
		"":                  "",
	}
	for in, want := range cases {
		if got := NormalizeNumber(in); got != want {
			t.Fatalf("NormalizeNumber(%q) = %q, want %q", in, got, want)
		}
	}
	// 不补国家码：无 + 的号码原样保留。
	if got := NormalizeNumber("13800000000"); got != "13800000000" {
		t.Fatalf("NormalizeNumber plain = %q", got)
	}
}

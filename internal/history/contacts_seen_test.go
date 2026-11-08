package history

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// 交付点 3：迁移 v3（calls.seen 列 + contacts 表）、未接已读角标、
// 联系人按号码去重与多号码。测试数据只用 10086/10010 占位号。
func TestOpenMigratesV2(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	version, err := st.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion() = %v", err)
	}
	if version != CurrentSchemaVersion {
		t.Fatalf("SchemaVersion() = %d, want %d", version, CurrentSchemaVersion)
	}
	// contacts 表与 calls.seen 列已存在。
	if _, err := st.ListContacts(ctx); err != nil {
		t.Fatalf("ListContacts() on fresh v3 store = %v", err)
	}
	row := st.db.WithContext(ctx).Raw("SELECT COUNT(*) FROM pragma_table_info('calls') WHERE name = 'seen'").Row()
	var seenCols int
	if err := row.Scan(&seenCols); err != nil || seenCols != 1 {
		t.Fatalf("calls.seen column present = %d, err = %v", seenCols, err)
	}
}

// TestOpenUpgradesV1Database 用手工建的 v1 库验证升级：旧未接默认已读，
// 升级后不点亮角标；新库结构为 v3。
func TestOpenUpgradesV1Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history-v1.sqlite")
	// 用与 Store 相同的 glebarez 驱动建 v1 库：直接 import modernc 的
	// blank 包会在测试二进制里触发 driver 重复注册 panic。
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open v1 db = %v", err)
	}
	// 最小 v1 结构：calls 表（无 seen 列）+ schema_migrations(version 1)。
	// 只建迁移真正依赖的部分；其余 v1 表缺失不影响 v3 迁移执行。
	if err := db.Exec(`
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL);
INSERT INTO schema_migrations (version, name, applied_at) VALUES (1, 'v1-initial-tables', 0);
CREATE TABLE calls (
  id INTEGER PRIMARY KEY AUTOINCREMENT, call_id TEXT NOT NULL, direction TEXT NOT NULL,
  number_raw TEXT NOT NULL DEFAULT '', number_normalized TEXT NOT NULL DEFAULT '',
  subscription_id TEXT NOT NULL DEFAULT 'unknown', iccid TEXT NOT NULL DEFAULT '',
  imei TEXT NOT NULL DEFAULT '', eid TEXT NOT NULL DEFAULT '', own_number TEXT NOT NULL DEFAULT '',
  number_source TEXT NOT NULL DEFAULT 'unknown', snapshot_at INTEGER,
  started_at INTEGER NOT NULL, connected_at INTEGER, ended_at INTEGER,
  duration_seconds INTEGER, end_reason TEXT NOT NULL DEFAULT '', end_state TEXT NOT NULL DEFAULT '',
  missed INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT 'active',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, UNIQUE (call_id));
INSERT INTO calls (call_id, direction, number_raw, number_normalized, subscription_id,
  started_at, ended_at, end_reason, end_state, missed, status, created_at, updated_at)
  VALUES ('call-old-missed', 'inbound', '10086', '10086', 'unknown',
  1795000000000, 1795000030000, 'no_answer', 'missed', 1, 'ended', 1795000000000, 1795000000000);
`).Error; err != nil {
		t.Fatalf("build v1 db = %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("v1 db.DB() = %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close v1 db = %v", err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open(v1 db) = %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	if version, err := st.SchemaVersion(ctx); err != nil || version != 3 {
		t.Fatalf("upgraded SchemaVersion() = %d, %v", version, err)
	}
	rec, err := st.GetCall(ctx, "call-old-missed")
	if err != nil {
		t.Fatalf("GetCall(old missed) = %v", err)
	}
	if !rec.Seen {
		t.Fatalf("upgraded old missed call Seen = false, want true (no stale badge)")
	}
	if unseen, total, err := st.CountMissedUnseen(ctx, ""); err != nil || unseen != 0 || total != 1 {
		t.Fatalf("CountMissedUnseen() after upgrade = %d/%d, %v", unseen, total, err)
	}
}

func TestUpsertCallMissedSeenFlag(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	snap := testSnapshot("iccid:AAAAAAAAAAAAAAAAAAAA0001")
	ended := base.Add(30 * time.Second)

	// 一步到位的未接：新建即未读。
	if err := st.UpsertCall(ctx, CallRecord{
		CallID: "call-missed-new", Direction: "inbound", NumberRaw: "10086",
		Snapshot: snap, StartedAt: base, EndedAt: &ended,
		EndReason: "no_answer", EndState: "missed", Missed: true,
	}); err != nil {
		t.Fatalf("UpsertCall(missed) = %v", err)
	}
	// 已接通的通话：新建即已读。
	dur := int64(60)
	connected := base.Add(5 * time.Second)
	if err := st.UpsertCall(ctx, CallRecord{
		CallID: "call-answered", Direction: "outbound", NumberRaw: "10010",
		Snapshot: snap, StartedAt: base, ConnectedAt: &connected, EndedAt: &ended,
		DurationSeconds: &dur, EndReason: "local_hangup", EndState: "ended",
	}); err != nil {
		t.Fatalf("UpsertCall(answered) = %v", err)
	}
	// 先振铃、后变未接：终结事件把 seen 翻为未读。
	if err := st.UpsertCall(ctx, CallRecord{
		CallID: "call-ringing", Direction: "inbound", NumberRaw: "10086",
		Snapshot: snap, StartedAt: base,
	}); err != nil {
		t.Fatalf("UpsertCall(ringing) = %v", err)
	}
	if err := st.UpsertCall(ctx, CallRecord{
		CallID: "call-ringing", Direction: "inbound", NumberRaw: "10086",
		Snapshot: snap, StartedAt: base, EndedAt: &ended,
		EndReason: "no_answer", EndState: "missed", Missed: true,
	}); err != nil {
		t.Fatalf("UpsertCall(ringing -> missed) = %v", err)
	}

	if unseen, total, err := st.CountMissedUnseen(ctx, ""); err != nil || unseen != 2 || total != 2 {
		t.Fatalf("CountMissedUnseen() = %d/%d, %v; want 2/2", unseen, total, err)
	}
	rec, err := st.GetCall(ctx, "call-ringing")
	if err != nil || !rec.Missed || rec.Seen {
		t.Fatalf("GetCall(ringing) = %+v, %v; want missed unseen", rec, err)
	}
	rec, err = st.GetCall(ctx, "call-answered")
	if err != nil || !rec.Seen {
		t.Fatalf("GetCall(answered) = %+v, %v; want seen", rec, err)
	}
	if _, err := st.GetCall(ctx, "call-nope"); !isNotFoundErr(err) {
		t.Fatalf("GetCall(missing) = %v, want ErrNotFound", err)
	}
	marked, err := st.MarkMissedSeen(ctx, "")
	if err != nil || marked != 2 {
		t.Fatalf("MarkMissedSeen() = %d, %v; want 2", marked, err)
	}
	if unseen, total, err := st.CountMissedUnseen(ctx, ""); err != nil || unseen != 0 || total != 2 {
		t.Fatalf("CountMissedUnseen() after seen = %d/%d, %v", unseen, total, err)
	}
	// 再次标记是空操作，不报错。
	if marked, err := st.MarkMissedSeen(ctx, ""); err != nil || marked != 0 {
		t.Fatalf("MarkMissedSeen() again = %d, %v; want 0", marked, err)
	}
	// 按卡筛选：另一张卡的未接不受影响。
	if unseen, total, err := st.CountMissedUnseen(ctx, "iccid:BBBBBBBBBBBBBBBBBBBB0002"); err != nil || unseen != 0 || total != 0 {
		t.Fatalf("CountMissedUnseen(other card) = %d/%d, %v", unseen, total, err)
	}
}

func isNotFoundErr(err error) bool {
	return errors.Is(err, ErrNotFound)
}

func TestSaveContactUpsertByNumber(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	first, err := st.SaveContact(ctx, "测试甲", "10086")
	if err != nil || first.ID == 0 {
		t.Fatalf("SaveContact() = %+v, %v", first, err)
	}
	if len(first.Numbers) != 1 || first.Numbers[0] != "10086" || first.Favorite {
		t.Fatalf("SaveContact() = %+v", first)
	}
	// 同号改名：不产生第二条联系人，id 不变。
	renamed, err := st.SaveContact(ctx, "测试甲改名", " 10086 ")
	if err != nil || renamed.ID != first.ID || renamed.Name != "测试甲改名" {
		t.Fatalf("SaveContact(same number) = %+v, %v; want same id %d", renamed, err, first.ID)
	}
	if len(renamed.Numbers) != 1 {
		t.Fatalf("SaveContact(same number) numbers = %v, want 1 entry", renamed.Numbers)
	}
	// 不同号码是另一位联系人。
	second, err := st.SaveContact(ctx, "测试乙", "10010")
	if err != nil || second.ID == first.ID {
		t.Fatalf("SaveContact(other number) = %+v, %v", second, err)
	}
	// 追加号码（多号码）：同一位联系人可有两个号码。
	withTwo, err := st.AddContactNumber(ctx, first.ID, "10010")
	if err == nil {
		t.Fatalf("AddContactNumber(number owned by another contact) = %+v, want error", withTwo)
	}
	withTwo, err = st.AddContactNumber(ctx, first.ID, "13800000000")
	if err != nil || len(withTwo.Numbers) != 2 || len(withTwo.Normalized) != 2 {
		t.Fatalf("AddContactNumber() = %+v, %v", withTwo, err)
	}
	// 收藏开关与改名。
	fav, err := st.UpdateContact(ctx, first.ID, "", true)
	if err != nil || !fav.Favorite || fav.Name != "测试甲改名" {
		t.Fatalf("UpdateContact(favorite) = %+v, %v", fav, err)
	}
	contacts, err := st.ListContacts(ctx)
	if err != nil || len(contacts) != 2 {
		t.Fatalf("ListContacts() = %d, %v", len(contacts), err)
	}
	if contacts[0].ID != first.ID {
		t.Fatalf("ListContacts() first = %d, want favorite contact %d first", contacts[0].ID, first.ID)
	}
	// 删除：不存在返回 false，不报错。
	if deleted, err := st.DeleteContact(ctx, 999999); err != nil || deleted {
		t.Fatalf("DeleteContact(missing) = %v, %v", deleted, err)
	}
	if deleted, err := st.DeleteContact(ctx, second.ID); err != nil || !deleted {
		t.Fatalf("DeleteContact() = %v, %v", deleted, err)
	}
	if _, err := st.GetContact(ctx, second.ID); !isNotFoundErr(err) {
		t.Fatalf("GetContact(deleted) = %v, want ErrNotFound", err)
	}
	// 校验：空姓名、空号码拒绝。
	if _, err := st.SaveContact(ctx, " ", "10086"); err == nil {
		t.Fatalf("SaveContact(empty name) = nil error")
	}
	if _, err := st.SaveContact(ctx, "测试", " "); err == nil {
		t.Fatalf("SaveContact(empty number) = nil error")
	}
}

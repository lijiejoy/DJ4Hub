package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/history"
	"github.com/WongLoki/DJ4Hub/internal/sip"
	"github.com/WongLoki/DJ4Hub/internal/subscription"
)

var testMainHistoryPath string

// TestMain 把整个 cmd 包测试期间的历史库指向临时目录，
// 任何测试触发的懒打开都不会写真实用户配置目录。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "dj4hub-history-test-")
	if err != nil {
		panic(err)
	}
	testMainHistoryPath = filepath.Join(dir, "history.sqlite")
	historyPathOverride = testMainHistoryPath
	code := m.Run()
	historyStoreMu.Lock()
	if historyStoreInstance != nil {
		_ = historyStoreInstance.Close()
		historyStoreInstance = nil
	}
	historyStoreMu.Unlock()
	os.RemoveAll(dir)
	os.Exit(code)
}

// resetHistoryStore 为单个测试换一个全新的空库，并返回它。
func resetHistoryStore(t *testing.T) *history.Store {
	t.Helper()
	historyStoreMu.Lock()
	if historyStoreInstance != nil {
		_ = historyStoreInstance.Close()
		historyStoreInstance = nil
	}
	historyStoreMu.Unlock()
	historyPathOverride = filepath.Join(t.TempDir(), "history.sqlite")
	t.Cleanup(func() {
		historyStoreMu.Lock()
		if historyStoreInstance != nil {
			_ = historyStoreInstance.Close()
			historyStoreInstance = nil
		}
		historyStoreMu.Unlock()
		historyPathOverride = testMainHistoryPath
	})
	st, err := (&app{}).historyStore()
	if err != nil {
		t.Fatalf("historyStore() = %v", err)
	}
	return st
}

func testHistorySnapshot(id string) subscription.Snapshot {
	return subscription.Snapshot{
		SubscriptionID: id,
		ICCID:          "89860000000000000001",
		IMEI:           "860000000000001",
		Number:         "+8613800000000",
		NumberSource:   subscription.NumberSourceAuto,
		CapturedAt:     time.Now(),
	}
}

func seedHistory(t *testing.T, st *history.Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	snapA := testHistorySnapshot("iccid:AAAAAAAAAAAAAAAAAAAA0001")
	snapB := testHistorySnapshot("iccid:BBBBBBBBBBBBBBBBBBBB0002")
	for i, msg := range []history.SMSMessage{
		{Direction: "in", PeerRaw: "10086", Body: "first", Snapshot: snapA, ReceivedAt: base},
		{Direction: "in", PeerRaw: "10010", Body: "second", Snapshot: snapB, ReceivedAt: base.Add(time.Minute)},
		{Direction: "in", PeerRaw: "10086", Body: "third", Snapshot: snapA, ReceivedAt: base.Add(2 * time.Minute)},
	} {
		if _, err := st.SaveSMS(ctx, msg); err != nil {
			t.Fatalf("seed SMS %d: %v", i, err)
		}
	}
	connected := base.Add(5 * time.Second)
	ended := base.Add(65 * time.Second)
	dur := int64(60)
	calls := []history.CallRecord{
		{CallID: "call-1", Direction: "inbound", NumberRaw: "10086", Snapshot: snapA,
			StartedAt: base, ConnectedAt: &connected, EndedAt: &ended,
			DurationSeconds: &dur, EndReason: "remote_hangup", EndState: "ended"},
		{CallID: "call-2", Direction: "inbound", NumberRaw: "10010", Snapshot: snapB,
			StartedAt: base.Add(3 * time.Minute), EndedAt: func() *time.Time { t := base.Add(3*time.Minute + 30*time.Second); return &t }(),
			EndReason: "no_answer", EndState: "missed", Missed: true},
		{CallID: "call-3", Direction: "outbound", NumberRaw: "10086", Snapshot: snapA,
			StartedAt: base.Add(5 * time.Minute), ConnectedAt: &connected, EndedAt: &ended,
			DurationSeconds: &dur, EndReason: "local_hangup", EndState: "ended"},
	}
	for i, rec := range calls {
		if err := st.UpsertCall(ctx, rec); err != nil {
			t.Fatalf("seed call %d: %v", i, err)
		}
	}
}

func decodeHistoryJSON(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, rec.Body.String())
	}
}

func TestHistoryAPIListCallsAndSMS(t *testing.T) {
	st := resetHistoryStore(t)
	seedHistory(t, st)
	a := &app{}

	t.Run("calls paged desc", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/history/calls?limit=2&offset=0", nil)
		rec := httptest.NewRecorder()
		a.listHistoryCalls(rec, req)
		var resp struct {
			Items  []historyCallJSON `json:"items"`
			Total  int64             `json:"total"`
			Limit  int               `json:"limit"`
			Offset int               `json:"offset"`
		}
		decodeHistoryJSON(t, rec, &resp)
		if resp.Total != 3 || len(resp.Items) != 2 || resp.Limit != 2 {
			t.Fatalf("calls page = total %d items %d limit %d", resp.Total, len(resp.Items), resp.Limit)
		}
		if resp.Items[0].ID != "call-3" {
			t.Fatalf("first call = %q, want call-3 (newest first)", resp.Items[0].ID)
		}
		if resp.Items[0].EndReasonLabel != "本机挂断" {
			t.Fatalf("end_reason_label = %q", resp.Items[0].EndReasonLabel)
		}
	})

	t.Run("calls missed filter", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/history/calls?missed=true", nil)
		rec := httptest.NewRecorder()
		a.listHistoryCalls(rec, req)
		var resp struct {
			Items []historyCallJSON `json:"items"`
			Total int64             `json:"total"`
		}
		decodeHistoryJSON(t, rec, &resp)
		if resp.Total != 1 || resp.Items[0].ID != "call-2" || !resp.Items[0].Missed {
			t.Fatalf("missed filter = %+v", resp)
		}
	})

	t.Run("calls subscription filter", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/history/calls?subscription_id=iccid:BBBBBBBBBBBBBBBBBBBB0002", nil)
		rec := httptest.NewRecorder()
		a.listHistoryCalls(rec, req)
		var resp struct {
			Items []historyCallJSON `json:"items"`
			Total int64             `json:"total"`
		}
		decodeHistoryJSON(t, rec, &resp)
		if resp.Total != 1 || resp.Items[0].ID != "call-2" {
			t.Fatalf("subscription filter = %+v", resp)
		}
	})

	t.Run("sms paged and peer filter", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/history/sms?limit=2", nil)
		rec := httptest.NewRecorder()
		a.listHistorySMS(rec, req)
		var resp struct {
			Items []historySMSJSON `json:"items"`
			Total int64            `json:"total"`
		}
		decodeHistoryJSON(t, rec, &resp)
		if resp.Total != 3 || len(resp.Items) != 2 {
			t.Fatalf("sms page = total %d items %d", resp.Total, len(resp.Items))
		}
		if resp.Items[0].Body != "third" {
			t.Fatalf("first sms = %q, want third", resp.Items[0].Body)
		}
		req = httptest.NewRequest("GET", "/api/history/sms?peer=10086", nil)
		rec = httptest.NewRecorder()
		a.listHistorySMS(rec, req)
		decodeHistoryJSON(t, rec, &resp)
		if resp.Total != 2 {
			t.Fatalf("peer filter total = %d, want 2", resp.Total)
		}
	})

	t.Run("bad pagination rejected", func(t *testing.T) {
		for _, url := range []string{
			"/api/history/calls?limit=0",
			"/api/history/calls?limit=101",
			"/api/history/calls?limit=abc",
			"/api/history/calls?offset=-1",
			"/api/history/sms?direction=sideways",
		} {
			req := httptest.NewRequest("GET", url, nil)
			rec := httptest.NewRecorder()
			if strings.HasPrefix(url, "/api/history/calls") {
				a.listHistoryCalls(rec, req)
			} else {
				a.listHistorySMS(rec, req)
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("GET %s = %d, want 400", url, rec.Code)
			}
		}
	})
}

func TestHistoryStatusHandler(t *testing.T) {
	st := resetHistoryStore(t)
	seedHistory(t, st)
	a := &app{sip: sip.New(sip.DefaultConfig(), nil)}
	if _, err := a.historyStore(); err != nil {
		t.Fatalf("historyStore() = %v", err)
	}
	req := httptest.NewRequest("GET", "/api/history/status", nil)
	rec := httptest.NewRecorder()
	a.historyStatus(rec, req)
	var resp struct {
		Available     bool `json:"available"`
		SchemaVersion int  `json:"schema_version"`
		Counts        struct {
			SMS    int64 `json:"sms"`
			Calls  int64 `json:"calls"`
			Outbox int64 `json:"outbox"`
		} `json:"counts"`
		HookInstalled bool   `json:"hook_installed"`
		LastError     string `json:"last_error"`
	}
	decodeHistoryJSON(t, rec, &resp)
	if !resp.Available || resp.SchemaVersion != history.CurrentSchemaVersion {
		t.Fatalf("status = %+v", resp)
	}
	if resp.Counts.SMS != 3 || resp.Counts.Calls != 3 {
		t.Fatalf("counts = %+v", resp.Counts)
	}
	if !resp.HookInstalled {
		t.Fatal("hook_installed = false, want true after historyStore with sip server")
	}
	if resp.LastError != "" {
		t.Fatalf("last_error = %q, want empty", resp.LastError)
	}
}

func TestMergeSMSPersistsAndReportsFailure(t *testing.T) {
	st := resetHistoryStore(t)
	a := &app{demo: true} // demo 跳过通知；存储走测试覆盖的临时库。
	now := time.Now()
	newCount, total, persistErr := a.mergeSMS([]receivedSMS{
		{Sender: "10086", Content: "hello", Timestamp: now},
		{Sender: "10010", Content: "world", Timestamp: now.Add(time.Minute)},
	})
	if persistErr != nil {
		t.Fatalf("mergeSMS persistErr = %v", persistErr)
	}
	if newCount != 2 || total != 2 {
		t.Fatalf("mergeSMS = (%d, %d), want (2, 2)", newCount, total)
	}
	rows, totalCount, err := st.ListSMS(context.Background(), history.SMSQuery{Limit: 10})
	if err != nil || totalCount != 2 || len(rows) != 2 {
		t.Fatalf("stored SMS = %d rows, total %d, %v", len(rows), totalCount, err)
	}
	// 重复合并：内存去重 + 存储去重，行数不变。
	if _, _, persistErr := a.mergeSMS([]receivedSMS{
		{Sender: "10086", Content: "hello", Timestamp: now},
	}); persistErr != nil {
		t.Fatalf("mergeSMS repeat persistErr = %v", persistErr)
	}
	if _, totalCount, _ := st.ListSMS(context.Background(), history.SMSQuery{Limit: 10}); totalCount != 2 {
		t.Fatalf("stored SMS after repeat = %d, want 2", totalCount)
	}
	// 存储关闭后：落盘失败必须返回错误（调用方据此阻止模块清理），
	// 内存缓存不受影响。把路径指到一个文件占位之下，强制打开失败。
	historyStoreMu.Lock()
	if historyStoreInstance != nil {
		_ = historyStoreInstance.Close()
		historyStoreInstance = nil
	}
	historyStoreMu.Unlock()
	badDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(badDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write placeholder: %v", err)
	}
	historyPathOverride = filepath.Join(badDir, "history.sqlite")
	_, _, persistErr = a.mergeSMS([]receivedSMS{
		{Sender: "10086", Content: "later", Timestamp: now.Add(2 * time.Minute)},
	})
	if persistErr == nil {
		t.Fatal("mergeSMS with broken store: persistErr = nil, want error (cleanup must be blocked)")
	}
	if historyLastErrorString() == "" {
		t.Fatal("historyLastErrorString() empty after persist failure")
	}
}

func TestRecordOutboxAttemptStatuses(t *testing.T) {
	st := resetHistoryStore(t)
	a := &app{demo: true}
	// 成功：已提交。
	a.recordOutboxAttempt("10086", "hello", 1, []int{7}, nil)
	// 失败：通道不报告阶段，一律记结果不确定，不自动补发。
	a.recordOutboxAttempt("10086", "again", 1, []int{8}, errors.New("send timeout"))
	sms, calls, outbox, err := st.Counts(context.Background())
	if err != nil || sms != 2 || outbox != 2 || calls != 0 {
		t.Fatalf("Counts() = (%d, %d, %d, %v)", sms, calls, outbox, err)
	}
	rows, _, err := st.ListSMS(context.Background(), history.SMSQuery{Direction: "out", Limit: 10})
	if err != nil || len(rows) != 2 {
		t.Fatalf("out SMS rows = %d, %v", len(rows), err)
	}
	// 送达报告可按引用把已提交的尝试翻成已送达。
	if ok, err := st.MarkOutboxDeliveredByRef(context.Background(), 7); err != nil || !ok {
		t.Fatalf("MarkOutboxDeliveredByRef(7) = (%v, %v)", ok, err)
	}
}

func TestRecordCallHistoryViaSIPHook(t *testing.T) {
	resetHistoryStore(t)
	a := &app{}
	a.sip = sip.New(sip.DefaultConfig(), nil)
	if _, err := a.historyStore(); err != nil {
		t.Fatalf("historyStore() = %v", err)
	}
	if a.sip.HistorySink == nil {
		t.Fatal("HistorySink not installed after historyStore()")
	}
	// 无 modem：拨号失败，不触发真实硬件；失败事件应写入历史。
	if _, err := a.sip.DialFromAPI("10086"); !errors.Is(err, sip.ErrNoModem) {
		t.Fatalf("DialFromAPI() = %v, want ErrNoModem", err)
	}
	st, err := a.historyStore()
	if err != nil {
		t.Fatalf("historyStore() = %v", err)
	}
	rows, total, err := st.ListCalls(context.Background(), history.CallQuery{Limit: 10})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("ListCalls() = %d rows, total %d, %v", len(rows), total, err)
	}
	rec := rows[0]
	if rec.Direction != "outbound" || rec.NumberRaw != "10086" {
		t.Fatalf("call = %+v", rec)
	}
	if rec.EndState != "failed" || rec.EndReason != "no_network" || rec.Status != history.CallStatusEnded {
		t.Fatalf("call end = state %q reason %q status %q", rec.EndState, rec.EndReason, rec.Status)
	}
	if rec.DurationSeconds != nil {
		t.Fatalf("failed call duration = %v, want nil", rec.DurationSeconds)
	}
	if rec.Snapshot.SubscriptionID != history.UnknownSubscriptionID {
		t.Fatalf("snapshot = %q, want unknown (no subscription cached)", rec.Snapshot.SubscriptionID)
	}
}

func TestParseHistoryPagination(t *testing.T) {
	for _, tc := range []struct {
		url       string
		wantLimit int
		wantOff   int
		wantErr   bool
	}{
		{"/x", 50, 0, false},
		{"/x?limit=10&offset=20", 10, 20, false},
		{"/x?limit=100", 100, 0, false},
		{"/x?limit=101", 0, 0, true},
		{"/x?limit=0", 0, 0, true},
		{"/x?offset=-1", 0, 0, true},
		{"/x?limit=abc", 0, 0, true},
	} {
		req := httptest.NewRequest("GET", tc.url, nil)
		limit, offset, err := parseHistoryPagination(req)
		if tc.wantErr && err == nil {
			t.Fatalf("GET %s: err = nil, want error", tc.url)
		}
		if !tc.wantErr && (err != nil || limit != tc.wantLimit || offset != tc.wantOff) {
			t.Fatalf("GET %s = (%d, %d, %v), want (%d, %d, nil)",
				tc.url, limit, offset, err, tc.wantLimit, tc.wantOff)
		}
	}
}

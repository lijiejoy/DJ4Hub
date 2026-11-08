package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/WongLoki/DJ4Hub/internal/history"
)

// 交付点 4 handler 测试：复用 history_handlers_test.go 的
// resetHistoryStore / seedHistory（10086/10010 占位号），
// 只用模拟数据，不发真实短信、不改设备配置。

func TestConversationsAPIListAndThread(t *testing.T) {
	st := resetHistoryStore(t)
	seedHistory(t, st)
	a := &app{}

	// 会话列表：10086（2 条）排在 10010（1 条）前面（最后一条更新）。
	req := httptest.NewRequest("GET", "/api/conversations?limit=50", nil)
	rec := httptest.NewRecorder()
	a.listConversations(rec, req)
	var list struct {
		Items []conversationJSON `json:"items"`
		Total int64              `json:"total"`
	}
	decodeHistoryJSON(t, rec, &list)
	if list.Total != 2 || len(list.Items) != 2 {
		t.Fatalf("conversations = %+v", list)
	}
	if list.Items[0].PeerNormalized != "10086" || list.Items[0].TotalCount != 2 || list.Items[0].UnreadCount != 2 {
		t.Fatalf("first conversation = %+v", list.Items[0])
	}
	if list.Items[1].PeerNormalized != "10010" || list.Items[1].LastBody != "second" {
		t.Fatalf("second conversation = %+v", list.Items[1])
	}

	// 会话内消息流：按 peer 取，倒序。
	req = httptest.NewRequest("GET", "/api/conversations/thread?peer=10086&limit=50", nil)
	rec = httptest.NewRecorder()
	a.conversationThread(rec, req)
	var thread struct {
		Items []threadSMSJSON `json:"items"`
		Total int64           `json:"total"`
		Peer  string          `json:"peer"`
	}
	decodeHistoryJSON(t, rec, &thread)
	if thread.Total != 2 || len(thread.Items) != 2 {
		t.Fatalf("thread = %+v", thread)
	}
	if thread.Items[0].Body != "third" || thread.Items[1].Body != "first" {
		t.Fatalf("thread order = %q, %q", thread.Items[0].Body, thread.Items[1].Body)
	}
	// peer 缺失返回 400。
	req = httptest.NewRequest("GET", "/api/conversations/thread", nil)
	rec = httptest.NewRecorder()
	a.conversationThread(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("thread without peer status = %d, want 400", rec.Code)
	}
}

func TestConversationsAPISearchAndUnread(t *testing.T) {
	st := resetHistoryStore(t)
	seedHistory(t, st)
	a := &app{}

	// 按正文搜索。
	req := httptest.NewRequest("GET", "/api/conversations/search?q=third&limit=50", nil)
	rec := httptest.NewRecorder()
	a.searchConversations(rec, req)
	var resp struct {
		Items []historySMSJSON `json:"items"`
		Total int64            `json:"total"`
	}
	decodeHistoryJSON(t, rec, &resp)
	if resp.Total != 1 || len(resp.Items) != 1 || resp.Items[0].Body != "third" {
		t.Fatalf("search body = %+v", resp)
	}
	// 按号码搜索。
	req = httptest.NewRequest("GET", "/api/conversations/search?q=10010&limit=50", nil)
	rec = httptest.NewRecorder()
	a.searchConversations(rec, req)
	decodeHistoryJSON(t, rec, &resp)
	if resp.Total != 1 || resp.Items[0].PeerNormalized != "10010" {
		t.Fatalf("search peer = %+v", resp)
	}
	// q 为空返回 400。
	req = httptest.NewRequest("GET", "/api/conversations/search", nil)
	rec = httptest.NewRecorder()
	a.searchConversations(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("search without q status = %d, want 400", rec.Code)
	}

	// 未读标记：先把 10086 会话标已读，再查未读会话只剩 10010。
	body, _ := json.Marshal(map[string]string{"peer": "10086"})
	req = httptest.NewRequest("POST", "/api/conversations/read", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	a.markConversationRead(rec, req)
	var marked struct {
		Marked int64 `json:"marked"`
	}
	decodeHistoryJSON(t, rec, &marked)
	if marked.Marked != 2 {
		t.Fatalf("marked = %d, want 2", marked.Marked)
	}
	req = httptest.NewRequest("GET", "/api/conversations?unread_only=true&limit=50", nil)
	rec = httptest.NewRecorder()
	a.listConversations(rec, req)
	var list struct {
		Items []conversationJSON `json:"items"`
		Total int64              `json:"total"`
	}
	decodeHistoryJSON(t, rec, &list)
	if list.Total != 1 || list.Items[0].PeerNormalized != "10010" {
		t.Fatalf("unread-only conversations = %+v", list)
	}
	// peer 与 ids 都不给返回 400。
	req = httptest.NewRequest("POST", "/api/conversations/read", strings.NewReader("{}"))
	rec = httptest.NewRecorder()
	a.markConversationRead(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("read without peer/ids status = %d, want 400", rec.Code)
	}
}

func TestConversationsAPIOutboxStatus(t *testing.T) {
	st := resetHistoryStore(t)
	seedHistory(t, st)
	a := &app{}

	// 用发件箱写一条已提交的尝试，再发一条不确定的尝试。
	a.recordOutboxAttempt("10086", "seeded out", 1, []int{11}, nil)
	a.recordOutboxAttempt("10086", "uncertain out", 2, []int{12}, errors.New("simulated send failure"))

	// 会话内消息流：发出的短信带发件箱状态（与发件箱一致）。
	req := httptest.NewRequest("GET", "/api/conversations/thread?peer=10086&limit=50", nil)
	rec := httptest.NewRecorder()
	a.conversationThread(rec, req)
	var thread struct {
		Items []threadSMSJSON `json:"items"`
		Total int64           `json:"total"`
	}
	decodeHistoryJSON(t, rec, &thread)
	var seenSubmitted, seenUncertain bool
	for _, item := range thread.Items {
		if item.Direction != "out" {
			continue
		}
		switch item.Body {
		case "seeded out":
			seenSubmitted = item.OutboxStatus == history.OutboxSubmitted && item.OutboxStatusLabel == "已提交"
		case "uncertain out":
			seenUncertain = item.OutboxStatus == history.OutboxUncertain && item.OutboxStatusLabel == "结果不确定"
		}
	}
	if !seenSubmitted || !seenUncertain {
		t.Fatalf("thread outbox statuses = %+v (want submitted + uncertain)", thread.Items)
	}

	// 发件箱接口：按会话筛选，按状态筛选。
	req = httptest.NewRequest("GET", "/api/conversations/outbox?peer=10086&limit=50", nil)
	rec = httptest.NewRecorder()
	a.listConversationOutbox(rec, req)
	var outbox struct {
		Items []outboxAttemptJSON `json:"items"`
		Total int64               `json:"total"`
	}
	decodeHistoryJSON(t, rec, &outbox)
	if outbox.Total != 2 {
		t.Fatalf("outbox total = %d, want 2", outbox.Total)
	}
	req = httptest.NewRequest("GET", "/api/conversations/outbox?peer=10086&status=uncertain&limit=50", nil)
	rec = httptest.NewRecorder()
	a.listConversationOutbox(rec, req)
	decodeHistoryJSON(t, rec, &outbox)
	if outbox.Total != 1 || outbox.Items[0].Status != history.OutboxUncertain {
		t.Fatalf("outbox uncertain = %+v", outbox)
	}
	// 非法状态返回 400。
	req = httptest.NewRequest("GET", "/api/conversations/outbox?status=nope", nil)
	rec = httptest.NewRecorder()
	a.listConversationOutbox(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("outbox bad status = %d, want 400", rec.Code)
	}
}

func TestConversationsAPIDrafts(t *testing.T) {
	_ = resetHistoryStore(t)
	a := &app{}

	// 保存草稿 → 列表可见 → 覆盖 → 空正文删除 → DELETE 丢弃。
	body, _ := json.Marshal(map[string]string{"peer": "10086", "body": "草稿一"})
	req := httptest.NewRequest("PUT", "/api/conversations/drafts", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	a.saveDraft(rec, req)
	var draft draftJSON
	decodeHistoryJSON(t, rec, &draft)
	if draft.Body != "草稿一" || draft.PeerNormalized != "10086" {
		t.Fatalf("saved draft = %+v", draft)
	}

	body, _ = json.Marshal(map[string]string{"peer": "10086", "body": "草稿二"})
	req = httptest.NewRequest("PUT", "/api/conversations/drafts", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	a.saveDraft(rec, req)
	decodeHistoryJSON(t, rec, &draft)
	if draft.Body != "草稿二" {
		t.Fatalf("overwritten draft = %+v", draft)
	}

	req = httptest.NewRequest("GET", "/api/conversations/drafts", nil)
	rec = httptest.NewRecorder()
	a.listDrafts(rec, req)
	var list struct {
		Items []draftJSON `json:"items"`
		Total int         `json:"total"`
	}
	decodeHistoryJSON(t, rec, &list)
	if list.Total != 1 || list.Items[0].Body != "草稿二" {
		t.Fatalf("draft list = %+v", list)
	}

	// 空正文 PUT 等同删除。
	body, _ = json.Marshal(map[string]string{"peer": "10086", "body": "   "})
	req = httptest.NewRequest("PUT", "/api/conversations/drafts", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	a.saveDraft(rec, req)
	var deleted struct {
		Deleted bool `json:"deleted"`
	}
	decodeHistoryJSON(t, rec, &deleted)
	if !deleted.Deleted {
		t.Fatalf("empty-body save = %+v, want deleted", deleted)
	}

	// DELETE 丢弃不存在的草稿也不报错；缺 peer 返回 400。
	req = httptest.NewRequest("DELETE", "/api/conversations/drafts?peer=10086", nil)
	rec = httptest.NewRecorder()
	a.deleteDraft(rec, req)
	decodeHistoryJSON(t, rec, &deleted)
	if !deleted.Deleted {
		t.Fatalf("delete draft = %+v", deleted)
	}
	req = httptest.NewRequest("DELETE", "/api/conversations/drafts", nil)
	rec = httptest.NewRecorder()
	a.deleteDraft(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete without peer = %d, want 400", rec.Code)
	}
	// 超长正文拒绝。
	body, _ = json.Marshal(map[string]string{"peer": "10086", "body": strings.Repeat("x", 1001)})
	req = httptest.NewRequest("PUT", "/api/conversations/drafts", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	a.saveDraft(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversize draft = %d, want 400", rec.Code)
	}
}

func TestConversationsAPIExport(t *testing.T) {
	st := resetHistoryStore(t)
	seedHistory(t, st)
	a := &app{}

	// JSON 导出：可下载头 + 结构完整 + 含全部 3 条。
	req := httptest.NewRequest("GET", "/api/conversations/export?format=json", nil)
	rec := httptest.NewRecorder()
	a.exportConversations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export json status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(ct, "attachment;") || !strings.Contains(ct, ".json") {
		t.Fatalf("export json Content-Disposition = %q", ct)
	}
	var exported struct {
		Items     []threadSMSJSON `json:"items"`
		Total     int64           `json:"total"`
		Truncated bool            `json:"truncated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &exported); err != nil {
		t.Fatalf("export json decode: %v (body %.200s)", err, rec.Body.String())
	}
	if exported.Total != 3 || len(exported.Items) != 3 || exported.Truncated {
		t.Fatalf("export json = %+v", exported)
	}

	// CSV 导出：表头 + 3 行数据。
	req = httptest.NewRequest("GET", "/api/conversations/export?format=csv", nil)
	rec = httptest.NewRecorder()
	a.exportConversations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export csv status = %d", rec.Code)
	}
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("export csv lines = %d, want 4 (header + 3 rows): %q", len(lines), rec.Body.String())
	}
	if !strings.HasPrefix(lines[0], "id,direction,peer,") {
		t.Fatalf("export csv header = %q", lines[0])
	}

	// 只导一个会话。
	req = httptest.NewRequest("GET", "/api/conversations/export?format=json&peer=10010", nil)
	rec = httptest.NewRecorder()
	a.exportConversations(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &exported); err != nil {
		t.Fatalf("export peer json decode: %v", err)
	}
	if exported.Total != 1 || len(exported.Items) != 1 || exported.Items[0].PeerNormalized != "10010" {
		t.Fatalf("export peer = %+v", exported)
	}

	// 非法格式返回 400。
	req = httptest.NewRequest("GET", "/api/conversations/export?format=xml", nil)
	rec = httptest.NewRecorder()
	a.exportConversations(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("export bad format = %d, want 400", rec.Code)
	}
	_ = st
}

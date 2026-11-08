package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// 交付点 3：最近通话动作接口（回拨 / 发短信 / 保存联系人 / 未接计数）。
// 只用内存/临时目录数据库与占位号（10086/10010）；不做真实拨号、
// 不发真实短信：回拨在无 SIP 服务时应返回 503，短信成功路径走演示模式。
func TestCallsMissedCountAndSeen(t *testing.T) {
	st := resetHistoryStore(t)
	seedHistory(t, st)
	a := &app{}

	get := func(url string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest("GET", url, nil)
		rec := httptest.NewRecorder()
		a.callsMissedCount(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}
	if code, resp := get("/api/calls/missed-count"); code != http.StatusOK || resp["missed_unseen"] != float64(1) || resp["missed_total"] != float64(1) {
		t.Fatalf("missed-count = %d %+v, want unseen 1 total 1", code, resp)
	}
	if code, resp := get("/api/calls/missed-count?subscription_id=iccid:AAAAAAAAAAAAAAAAAAAA0001"); code != http.StatusOK || resp["missed_total"] != float64(0) {
		t.Fatalf("missed-count(card A) = %d %+v, want total 0", code, resp)
	}

	mark := func(body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/calls/missed/seen", strings.NewReader(body))
		rec := httptest.NewRecorder()
		a.callsMarkMissedSeen(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}
	if code, resp := mark(`{}`); code != http.StatusOK || resp["marked"] != float64(1) {
		t.Fatalf("missed/seen = %d %+v, want marked 1", code, resp)
	}
	if code, resp := get("/api/calls/missed-count"); code != http.StatusOK || resp["missed_unseen"] != float64(0) || resp["missed_total"] != float64(1) {
		t.Fatalf("missed-count after seen = %d %+v, want unseen 0 total 1", code, resp)
	}
	if code, resp := mark(``); code != http.StatusOK || resp["marked"] != float64(0) {
		t.Fatalf("missed/seen empty body = %d %+v, want marked 0", code, resp)
	}
}

func TestCallsRedialDelegatesToSIPCall(t *testing.T) {
	st := resetHistoryStore(t)
	seedHistory(t, st)
	a := &app{} // sip == nil：无真实拨号链路，只能 503。

	post := func(url, body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest("POST", url, strings.NewReader(body))
		rec := httptest.NewRecorder()
		a.callsRedial(rec, req)
		return rec.Code, rec.Body.String()
	}
	// 未知 call_id → 404，不触及拨号。
	if code, _ := post("/api/calls/redial", `{"call_id":"call-nope"}`); code != http.StatusNotFound {
		t.Fatalf("redial unknown call = %d, want 404", code)
	}
	// 既无 call_id 又无号码 → 400。
	if code, _ := post("/api/calls/redial", `{}`); code != http.StatusBadRequest {
		t.Fatalf("redial empty = %d, want 400", code)
	}
	// 已知记录回拨：查到号码后转调 /api/sip/call，无 SIP 服务 → 503，
	// 绝不伪造拨号成功。
	if code, body := post("/api/calls/redial", `{"call_id":"call-2"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("redial call-2 without SIP = %d %s, want 503", code, body)
	}
	if code, _ := post("/api/calls/redial", `{"number":"10086"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("redial by number without SIP = %d, want 503", code)
	}
}

func TestCallsSMSDelegatesToSendSMS(t *testing.T) {
	st := resetHistoryStore(t)
	seedHistory(t, st)
	a := &app{demo: true} // 演示模式：sendSMS 不碰模块，只记发件箱。

	post := func(body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/calls/sms", strings.NewReader(body))
		rec := httptest.NewRecorder()
		a.callsSMS(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}
	if code, _ := post(`{"call_id":"call-2"}`); code != http.StatusBadRequest {
		t.Fatalf("calls/sms without message = %d, want 400", code)
	}
	if code, _ := post(`{"call_id":"call-nope","message":"hi"}`); code != http.StatusNotFound {
		t.Fatalf("calls/sms unknown call = %d, want 404", code)
	}
	if code, resp := post(`{"call_id":"call-2","message":"test"}`); code != http.StatusOK || resp["sent"] != true {
		t.Fatalf("calls/sms demo = %d %+v, want sent true", code, resp)
	}
	// 发件箱已落盘：经演示模式的 sendSMS 路径记录了一条尝试。
	sms, calls, outbox, err := st.Counts(context.Background())
	if err != nil || outbox != 1 {
		t.Fatalf("counts after demo sms = sms %d calls %d outbox %d, %v", sms, calls, outbox, err)
	}
}

func TestCallsSaveContactAndContactsCRUD(t *testing.T) {
	st := resetHistoryStore(t)
	seedHistory(t, st)
	a := &app{}

	postJSON := func(handler func(http.ResponseWriter, *http.Request), url, body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest("POST", url, strings.NewReader(body))
		rec := httptest.NewRecorder()
		handler(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}

	// 从未接记录保存联系人（call-2 的号码是 10010）。
	code, resp := postJSON(a.callsSaveContact, "/api/calls/save-contact", `{"call_id":"call-2","name":"测试联系人"}`)
	if code != http.StatusOK || resp["name"] != "测试联系人" {
		t.Fatalf("save-contact = %d %+v", code, resp)
	}
	contactID := resp["id"].(float64)
	numbers, _ := resp["numbers"].([]any)
	if len(numbers) != 1 || numbers[0] != "10010" {
		t.Fatalf("save-contact numbers = %v, want [10010]", numbers)
	}
	// 同号再保存只改名，id 不变。
	if code, resp := postJSON(a.callsSaveContact, "/api/calls/save-contact", `{"call_id":"call-2","name":"改名后"}`); code != http.StatusOK || resp["id"] != contactID || resp["name"] != "改名后" {
		t.Fatalf("save-contact rename = %d %+v, want same id %v", code, resp, contactID)
	}
	if code, _ := postJSON(a.callsSaveContact, "/api/calls/save-contact", `{"call_id":"call-nope","name":"x"}`); code != http.StatusNotFound {
		t.Fatalf("save-contact unknown call = %d, want 404", code)
	}
	if code, _ := postJSON(a.callsSaveContact, "/api/calls/save-contact", `{"call_id":"call-2","name":" "}`); code != http.StatusBadRequest {
		t.Fatalf("save-contact empty name = %d, want 400", code)
	}

	// 联系人列表：1 位。
	req := httptest.NewRequest("GET", "/api/contacts", nil)
	rec := httptest.NewRecorder()
	a.listContacts(rec, req)
	var list struct {
		Items []contactJSON `json:"items"`
	}
	decodeHistoryJSON(t, rec, &list)
	if len(list.Items) != 1 || list.Items[0].Name != "改名后" {
		t.Fatalf("list contacts = %+v", list.Items)
	}

	// 追加号码（多号码）：同一位联系人再加 10086。
	code, resp = postJSON(a.saveContact, "/api/contacts", `{"id":`+strconv.Itoa(int(contactID))+`,"number":"10086"}`)
	if code != http.StatusOK {
		t.Fatalf("add contact number = %d %+v", code, resp)
	}
	numbers, _ = resp["numbers"].([]any)
	if len(numbers) != 2 {
		t.Fatalf("add contact number numbers = %v, want 2", numbers)
	}
	// 收藏开关。
	code, resp = postJSON(a.saveContact, "/api/contacts", `{"id":`+strconv.Itoa(int(contactID))+`,"favorite":true}`)
	if code != http.StatusOK || resp["favorite"] != true {
		t.Fatalf("favorite contact = %d %+v", code, resp)
	}
	// 同号保存只改名，不产生新联系人。
	if code, _ := postJSON(a.saveContact, "/api/contacts", `{"name":"另一位","number":"10010"}`); code != http.StatusOK {
		t.Fatalf("second contact same number unexpectedly = %d", code)
	}
	// 另建一位联系人，再追加已被占用的号码应被拒绝。
	code, resp = postJSON(a.saveContact, "/api/contacts", `{"name":"第三位","number":"10000"}`)
	if code != http.StatusOK {
		t.Fatalf("third contact = %d, want 200", code)
	}
	thirdID := int(resp["id"].(float64))
	if code, _ := postJSON(a.saveContact, "/api/contacts", `{"id":`+strconv.Itoa(thirdID)+`,"number":"10010"}`); code != http.StatusBadRequest {
		t.Fatalf("add number owned by another contact = %d, want 400", code)
	}

	// 删除。
	delReq := httptest.NewRequest("DELETE", "/api/contacts", strings.NewReader(`{"id":`+strconv.Itoa(thirdID)+`}`))
	delRec := httptest.NewRecorder()
	a.deleteContact(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("delete contact = %d %s", delRec.Code, delRec.Body.String())
	}
	delReq = httptest.NewRequest("DELETE", "/api/contacts", strings.NewReader(`{"id":`+strconv.Itoa(thirdID)+`}`))
	delRec = httptest.NewRecorder()
	a.deleteContact(delRec, delReq)
	if delRec.Code != http.StatusNotFound {
		t.Fatalf("delete missing contact = %d, want 404", delRec.Code)
	}
}

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSignWebhookDeterministic(t *testing.T) {
	body := []byte(`{"event":"sms"}`)
	a := signWebhook("secret", body)
	b := signWebhook("secret", body)
	if a != b || len(a) != 64 {
		t.Fatalf("signature not deterministic hex: %q", a)
	}
	if c := signWebhook("other", body); c == a {
		t.Fatalf("different secret gave same signature")
	}
}

func TestSendTelegramAgainstLocalServer(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	cfg := telegramNotifySettings{BotToken: "TOK", ChatID: 123}
	// BaseURL with %s placeholders mirrors the upstream config pattern.
	cfg.BaseURL = srv.URL + "/bot%s/%s"
	ctx := context.Background()
	if err := sendTelegram(ctx, cfg, "hello"); err != nil {
		t.Fatalf("sendTelegram: %v", err)
	}
	if gotPath != "/botTOK/sendMessage" {
		t.Fatalf("unexpected path %q", gotPath)
	}
	if gotBody["chat_id"] != float64(123) || gotBody["text"] != "hello" {
		t.Fatalf("unexpected body %v", gotBody)
	}
}

func TestSendFeishuAgainstLocalServer(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"StatusCode":0}`))
	}))
	defer srv.Close()
	if err := sendFeishu(context.Background(), feishuNotifySettings{WebhookURL: srv.URL}, "hi"); err != nil {
		t.Fatalf("sendFeishu: %v", err)
	}
	if got["msg_type"] != "text" {
		t.Fatalf("unexpected feishu body %v", got)
	}
}

func TestSendBarkAgainstLocalServer(t *testing.T) {
	var gotPath string
	var gotGroup string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotGroup = r.URL.Query().Get("group")
		w.Write([]byte(`{"code":200}`))
	}))
	defer srv.Close()
	cfg := barkNotifySettings{URLs: []string{srv.URL + "/KEY"}, Group: "DJ 4G Hub"}
	if err := sendBark(context.Background(), cfg, "标题", "内容"); err != nil {
		t.Fatalf("sendBark: %v", err)
	}
	if !strings.HasPrefix(gotPath, "/KEY/") {
		t.Fatalf("unexpected bark path %q", gotPath)
	}
	if gotGroup != "DJ 4G Hub" {
		t.Fatalf("unexpected bark group %q", gotGroup)
	}
}

func TestSendPushplusAgainstLocalServer(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"code":200,"msg":"ok"}`))
	}))
	defer srv.Close()
	old := pushplusEndpoint
	pushplusEndpoint = srv.URL
	defer func() { pushplusEndpoint = old }()
	if err := sendPushplus(context.Background(), pushplusNotifySettings{Token: "T"}, "标题", "内容"); err != nil {
		t.Fatalf("sendPushplus: %v", err)
	}
	if got["token"] != "T" || got["title"] != "标题" {
		t.Fatalf("unexpected pushplus body %v", got)
	}
}

func TestSendWebhookSignatureAndRetry(t *testing.T) {
	var sigs []string
	var bodies []string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		sigs = append(sigs, r.Header.Get("X-DJ4Hub-Signature"))
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		bodies = append(bodies, string(buf[:n]))
		if calls == 1 {
			w.WriteHeader(500)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	cfg := webhookNotifySettings{URLs: []string{srv.URL}, Secret: "s3cr3t", TimeoutMs: 5000, RetryMax: 2}
	ev := notifyEvent{Kind: "sms", Title: "t", Body: "b", Code: "123456", Sender: "10086", Time: time.Now()}
	if err := sendWebhook(context.Background(), cfg, ev); err != nil {
		t.Fatalf("sendWebhook: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 1 retry (2 calls), got %d", calls)
	}
	want := signWebhook("s3cr3t", []byte(bodies[1]))
	if sigs[1] != want {
		t.Fatalf("signature mismatch: got %q want %q", sigs[1], want)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(bodies[1]), &decoded); err != nil {
		t.Fatalf("webhook body not JSON: %v", err)
	}
	if decoded["code"] != "123456" || decoded["sender"] != "10086" {
		t.Fatalf("unexpected webhook payload %v", decoded)
	}
}

func TestValidateNotifySettings(t *testing.T) {
	cfg := defaultNotifySettings()
	cfg.Telegram.Enabled = true
	if err := validateNotifySettings(cfg); err == nil {
		t.Fatalf("expected error for telegram enabled without token/chat")
	}
	cfg.Telegram.BotToken = "x"
	cfg.Telegram.ChatID = 1
	if err := validateNotifySettings(cfg); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	cfg.Webhook.Enabled = true
	cfg.Webhook.URLs = []string{"not-a-url"}
	if err := validateNotifySettings(cfg); err == nil {
		t.Fatalf("expected error for invalid webhook URL")
	}
}

func TestNotifySettingsNeverReturnsSecrets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	a := &app{}
	saveBody := `{"telegram":{"enabled":true,"bot_token":"TOK-SECRET-123","chat_id":111},"pushplus":{"enabled":true,"token":"PP-SECRET-456"},"notify_sms":true,"notify_calls":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/notify/settings", strings.NewReader(saveBody))
	w := httptest.NewRecorder()
	a.notifySettingsSave(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("save status %d: %s", w.Code, w.Body.String())
	}
	// Second save with empty secrets must keep the stored ones.
	req2 := httptest.NewRequest(http.MethodPost, "/api/notify/settings", strings.NewReader(`{"telegram":{"enabled":true,"chat_id":111},"notify_sms":true,"notify_calls":true}`))
	w2 := httptest.NewRecorder()
	a.notifySettingsSave(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("second save status %d: %s", w2.Code, w2.Body.String())
	}
	req3 := httptest.NewRequest(http.MethodGet, "/api/notify/settings", nil)
	w3 := httptest.NewRecorder()
	a.notifySettingsGet(w3, req3)
	view := w3.Body.String()
	if strings.Contains(view, "TOK-SECRET-123") || strings.Contains(view, "PP-SECRET-456") {
		t.Fatalf("settings view leaks a secret: %s", view)
	}
	if !strings.Contains(view, `"configured":true`) {
		t.Fatalf("expected configured=true in view: %s", view)
	}
	// The stored values survived the empty-secret save: test endpoint for
	// telegram must not report "not configured".
	a.notifyMu.Lock()
	a.notify.Telegram.BaseURL = srv.URL + "/bot%s/%s"
	a.notifyMu.Unlock()
	req4 := httptest.NewRequest(http.MethodPost, "/api/notify/test", strings.NewReader(`{"channel":"telegram"}`))
	w4 := httptest.NewRecorder()
	a.notifyTest(w4, req4)
	if w4.Code == http.StatusUnprocessableEntity {
		t.Fatalf("token was wiped by empty-secret save: %s", w4.Body.String())
	}
}

func TestNotifyTestUnconfigured(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	a := &app{}
	req := httptest.NewRequest(http.MethodPost, "/api/notify/test", strings.NewReader(`{"channel":"bark"}`))
	w := httptest.NewRecorder()
	a.notifyTest(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "未配置") {
		t.Fatalf("expected clear not-configured error: %s", w.Body.String())
	}
}

func TestNotifyIncomingCallDedup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	a := &app{}
	a.notifyIncomingCall("13800000000")
	a.notifyIncomingCall("13800000000")
	a.notifyIncomingCall("13800000001")
	deadline := time.Now().Add(3 * time.Second)
	for {
		a.notifyLogMu.Lock()
		n := len(a.notifyLog)
		a.notifyLogMu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	a.notifyLogMu.Lock()
	defer a.notifyLogMu.Unlock()
	if len(a.notifyLog) != 2 {
		t.Fatalf("expected 2 call log entries (deduped), got %d: %+v", len(a.notifyLog), a.notifyLog)
	}
}

func TestNotifyNewSMSRecencyGuard(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	a := &app{}
	// An old stored message (startup cache fill) must not notify.
	a.notifyNewSMS([]receivedSMS{{Sender: "10086", Content: "old", Timestamp: time.Now().Add(-2 * time.Hour)}})
	time.Sleep(200 * time.Millisecond)
	a.notifyLogMu.Lock()
	n := len(a.notifyLog)
	a.notifyLogMu.Unlock()
	if n != 0 {
		t.Fatalf("old SMS should not notify, got %d log entries", n)
	}
	// A fresh message notifies (logged as no-channel-configured).
	a.notifyNewSMS([]receivedSMS{{Sender: "10086", Content: "验证码 123456", Code: "123456", Timestamp: time.Now()}})
	deadline := time.Now().Add(3 * time.Second)
	for {
		a.notifyLogMu.Lock()
		n = len(a.notifyLog)
		a.notifyLogMu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	a.notifyLogMu.Lock()
	defer a.notifyLogMu.Unlock()
	if len(a.notifyLog) != 1 || a.notifyLog[0].Kind != "sms" {
		t.Fatalf("expected 1 sms log entry, got %+v", a.notifyLog)
	}
}

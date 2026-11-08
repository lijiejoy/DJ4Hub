package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTelegramPollFailureTracker(t *testing.T) {
	var tr telegramPollFailureTracker
	for i := 1; i <= 2; i++ {
		if logNow, _ := tr.noteFailure(); logNow {
			t.Fatalf("failure %d: want silent, got log", i)
		}
	}
	if logNow, n := tr.noteFailure(); !logNow || n != 3 {
		t.Fatalf("failure 3: want log with count 3, got logNow=%v count=%d", logNow, n)
	}
	for i := 4; i <= 9; i++ {
		if logNow, _ := tr.noteFailure(); logNow {
			t.Fatalf("failure %d: want silent, got log", i)
		}
	}
	if logNow, n := tr.noteFailure(); !logNow || n != 10 {
		t.Fatalf("failure 10: want log with count 10, got logNow=%v count=%d", logNow, n)
	}
	tr.noteSuccess()
	if logNow, _ := tr.noteFailure(); logNow {
		t.Fatal("after success, first failure should be silent")
	}
}

func TestTelegramPollOnceConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":409,"description":"Conflict"}`))
	}))
	defer server.Close()
	a := &app{}
	cfg := &notifySettings{Telegram: telegramNotifySettings{BotToken: "test-token", BaseURL: server.URL + "/bot%s/%s"}}
	err := a.telegramPollOnce(&http.Client{Timeout: 5 * time.Second}, cfg)
	if err == nil {
		t.Fatal("want an error for HTTP 409")
	}
	if !strings.Contains(err.Error(), "已有其他轮询会话") {
		t.Fatalf("want a duplicate-session message, got %v", err)
	}
}

func TestTelegramPollOnceSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer server.Close()
	a := &app{}
	cfg := &notifySettings{Telegram: telegramNotifySettings{BotToken: "test-token", BaseURL: server.URL + "/bot%s/%s"}}
	if err := a.telegramPollOnce(&http.Client{Timeout: 5 * time.Second}, cfg); err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
}

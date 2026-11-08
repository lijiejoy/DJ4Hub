package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/subscription"
)

// resetSubscriptionTestState 为单测隔离包级订阅单例与备注持久化。
func resetSubscriptionTestState(t *testing.T) {
	t.Helper()
	sharedSubscriptionStore = subscription.NewStore()
	subscriptionRemarksMu.Lock()
	subscriptionRemarksLoaded = false
	subscriptionRemarksMu.Unlock()
	subscriptionRemarksPathOverride = t.TempDir() + "/subscription-remarks.json"
	subscriptionHookMu.Lock()
	subscriptionHookICCID = ""
	subscriptionHookFingerprint = ""
	subscriptionHookMu.Unlock()
}

func TestSubscriptionHandlersDemo(t *testing.T) {
	resetSubscriptionTestState(t)
	a := &app{demo: true}

	// 首次查询：演示身份，未读到号码记 unknown。
	req := httptest.NewRequest(http.MethodGet, "/api/subscription", nil)
	rec := httptest.NewRecorder()
	a.subscriptionStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/subscription status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got subscriptionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.SubscriptionID != "iccid:89860123456789012345" {
		t.Fatalf("SubscriptionID=%q", got.SubscriptionID)
	}
	if got.Number != "" || got.NumberSource != subscription.NumberSourceUnknown {
		t.Fatalf("demo number=%q source=%q want empty/unknown", got.Number, got.NumberSource)
	}
	if got.ICCIDMasked == "" || got.ICCIDMasked == got.ICCID {
		t.Fatalf("ICCIDMasked=%q should be masked", got.ICCIDMasked)
	}

	// 设置备注：号码规范化、来源为 remark，且不显示成运营商已验证。
	req = httptest.NewRequest(http.MethodPost, "/api/subscription/remark", strings.NewReader(`{"number":" 138-0000-1111 "}`))
	rec = httptest.NewRecorder()
	a.subscriptionRemark(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST remark status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Number != "13800001111" || got.NumberSource != subscription.NumberSourceRemark {
		t.Fatalf("remarked number=%q source=%q", got.Number, got.NumberSource)
	}
	if got.NumberLabel != "用户备注" {
		t.Fatalf("NumberLabel=%q want 用户备注", got.NumberLabel)
	}

	// 非法备注拒绝。
	req = httptest.NewRequest(http.MethodPost, "/api/subscription/remark", strings.NewReader(`{"number":"abc"}`))
	rec = httptest.NewRecorder()
	a.subscriptionRemark(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid remark status=%d want 400", rec.Code)
	}

	// 清除备注：回落到 unknown（演示模式无自动读数）。
	req = httptest.NewRequest(http.MethodPost, "/api/subscription/remark", strings.NewReader(`{"number":""}`))
	rec = httptest.NewRecorder()
	a.subscriptionRemark(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear remark status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Number != "" || got.NumberSource != subscription.NumberSourceUnknown {
		t.Fatalf("cleared number=%q source=%q want empty/unknown", got.Number, got.NumberSource)
	}
}

func TestSubscriptionRemarkPersistsAcrossStoreReload(t *testing.T) {
	resetSubscriptionTestState(t)
	a := &app{demo: true}
	req := httptest.NewRequest(http.MethodPost, "/api/subscription/remark", strings.NewReader(`{"number":"13900000001"}`))
	rec := httptest.NewRecorder()
	a.subscriptionRemark(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST remark status=%d body=%s", rec.Code, rec.Body.String())
	}
	// 模拟重启：换一个全新 Store 并重新载入磁盘备注。
	sharedSubscriptionStore = subscription.NewStore()
	subscriptionRemarksMu.Lock()
	subscriptionRemarksLoaded = false
	subscriptionRemarksMu.Unlock()
	ensureSubscriptionRemarksLoaded()
	req = httptest.NewRequest(http.MethodGet, "/api/subscription", nil)
	rec = httptest.NewRecorder()
	a.subscriptionStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/subscription status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got subscriptionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Number != "13900000001" || got.NumberSource != subscription.NumberSourceRemark {
		t.Fatalf("remark did not survive reload, got %q %q", got.Number, got.NumberSource)
	}
}

func TestSubscriptionStatusNoDevice(t *testing.T) {
	resetSubscriptionTestState(t)
	a := &app{} // 无 modem、无 usbAT、无 demo：读不到身份应报 503，不伪造
	req := httptest.NewRequest(http.MethodGet, "/api/subscription", nil)
	rec := httptest.NewRecorder()
	a.subscriptionStatus(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/subscription status=%d want 503", rec.Code)
	}
}

func TestNoteDeviceStatusInvalidatesOnSIMChange(t *testing.T) {
	resetSubscriptionTestState(t)
	a := &app{demo: true}
	// 首次见到 A 卡：触发后台刷新（演示 Reader 立即返回），等缓存建立。
	a.noteDeviceStatusForSubscription("89860123456789012345", "2ca3:0125:0x14400000")
	waitForSubscriptionCache(t, 5*time.Second)
	// 手动清一次缓存，再同卡重复上报：不重复触发刷新，缓存保持为空。
	sharedSubscriptionStore.Invalidate()
	a.noteDeviceStatusForSubscription("89860123456789012345", "2ca3:0125:0x14400000")
	time.Sleep(200 * time.Millisecond)
	if _, ok := sharedSubscriptionStore.Current(); ok {
		t.Fatalf("same ICCID must not trigger a refresh by itself")
	}
	// 换 B 卡：失效 + 后台刷新应重建身份（演示 Reader 固定返回 A 卡读数，
	// 这里只验证“换卡触发了刷新动作”，即缓存被重建）。
	a.noteDeviceStatusForSubscription("8944100000000000001", "2ca3:0125:0x14400000")
	waitForSubscriptionCache(t, 5*time.Second)
}

// waitForSubscriptionCache 等待后台刷新重建缓存，超时则失败。
func waitForSubscriptionCache(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, ok := sharedSubscriptionStore.Current(); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("background refresh did not rebuild the cache")
}

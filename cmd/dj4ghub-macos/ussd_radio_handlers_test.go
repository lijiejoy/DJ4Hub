package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/modem"
)

func doJSON(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func offlineTestApp() *app {
	// Prevent lazy discovery from claiming a connected modem in no-modem tests.
	return &app{usbATBackoffUntil: time.Now().Add(time.Hour), usbATBackoffErr: "test: modem disabled"}
}

func TestUSSDStartNoModem503(t *testing.T) {
	a := offlineTestApp()
	rec := doJSON(t, a.routes(), "POST", "/api/ussd", `{"code":"*100#"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/ussd code = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "modem unavailable") {
		t.Fatalf("body = %s, want it to mention modem unavailable", rec.Body.String())
	}
}

func TestUSSDStartValidation400(t *testing.T) {
	a := offlineTestApp()
	for _, body := range []string{
		`{"code":""}`,
		`{"code":"1\"2"}`,
		`{"code":"*100#","via":"bogus"}`,
	} {
		rec := doJSON(t, a.routes(), "POST", "/api/ussd", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST /api/ussd %s code = %d, want 400", body, rec.Code)
		}
	}
	long := strings.Repeat("1", 183)
	rec := doJSON(t, a.routes(), "POST", "/api/ussd", `{"code":"`+long+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/ussd long code = %d, want 400", rec.Code)
	}
}

func TestUSSDStatusIdleAndSessionGuards(t *testing.T) {
	a := offlineTestApp()
	h := a.routes()

	rec := doJSON(t, h, "GET", "/api/ussd/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/ussd/status code = %d, want 200", rec.Code)
	}
	var status map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if status["state"] != "idle" {
		t.Fatalf("state = %v, want idle", status["state"])
	}

	// Continue / cancel with no session at all.
	rec = doJSON(t, h, "POST", "/api/ussd/continue", `{"response":"1"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("continue without session code = %d, want 409", rec.Code)
	}
	rec = doJSON(t, h, "POST", "/api/ussd/cancel", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cancel without session code = %d, want 400", rec.Code)
	}

	// An active session blocks a new start (409) and a continue with no
	// modem fails 503 without dropping the session.
	a.storeUSSDSession(&ussdSession{
		ID: "ussd-test", Code: "*100#", State: ussdStateActive, Path: "at-cusd",
		StartedAt: time.Now(), UpdatedAt: time.Now(),
	})
	rec = doJSON(t, h, "POST", "/api/ussd", `{"code":"*100#"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("start with active session code = %d, want 409", rec.Code)
	}
	rec = doJSON(t, h, "POST", "/api/ussd/continue", `{"response":"1"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("continue with active session but no modem code = %d, want 503", rec.Code)
	}
	if s := a.currentUSSDSession(); s == nil || s.State != ussdStateActive {
		t.Fatalf("session after failed continue = %+v, want still active", s)
	}

	// Cancel marks the session cancelled locally even with no modem, and
	// never resubmits anything.
	rec = doJSON(t, h, "POST", "/api/ussd/cancel", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel active session code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if s := a.currentUSSDSession(); s == nil || s.State != ussdStateCancelled {
		t.Fatalf("session after cancel = %+v, want cancelled", s)
	}
	// Cancelling again is idempotent.
	rec = doJSON(t, h, "POST", "/api/ussd/cancel", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("second cancel code = %d, want 200", rec.Code)
	}
}

func TestUSSDIMSNoModem503(t *testing.T) {
	a := offlineTestApp()
	rec := doJSON(t, a.routes(), "GET", "/api/ussd/ims", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/ussd/ims code = %d, want 503", rec.Code)
	}
}

func TestUSSDDemo503(t *testing.T) {
	a := newDemoApp()
	rec := doJSON(t, a.routes(), "POST", "/api/ussd", `{"code":"*100#"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("demo POST /api/ussd code = %d, want 503", rec.Code)
	}
	rec = doJSON(t, a.routes(), "GET", "/api/ussd/ims", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("demo GET /api/ussd/ims code = %d, want 200", rec.Code)
	}
	var ims map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &ims); err != nil {
		t.Fatalf("unmarshal ims: %v", err)
	}
	if ims["ims_ussi_supported"] != false {
		t.Fatalf("demo ims_ussi_supported = %v, want false", ims["ims_ussi_supported"])
	}
}

func TestDeliveryReports(t *testing.T) {
	a := offlineTestApp()
	h := a.routes()

	rec := doJSON(t, h, "GET", "/api/sms/delivery", "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("GET /api/sms/delivery = (%d, %s), want (200, [])", rec.Code, rec.Body.String())
	}

	a.recordSentSMS("13800138000", []int{42})
	a.recordDeliveryReport(modem.SMSDeliveryReport{
		MessageRef: 42, Recipient: "13800138000", Status: 0,
		StatusText: "delivered", State: "delivered",
	})
	// A report for an MR this process never sent still creates a record.
	a.recordDeliveryReport(modem.SMSDeliveryReport{
		MessageRef: 99, Recipient: "+8613800138001", Status: 2,
		StatusText: "failed (status 2)", State: "failed",
	})

	rec = doJSON(t, h, "GET", "/api/sms/delivery", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/sms/delivery code = %d, want 200", rec.Code)
	}
	var items []smsDeliveryRecord
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("unmarshal delivery: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(delivery) = %d, want 2: %s", len(items), rec.Body.String())
	}
	byRef := map[int]smsDeliveryRecord{}
	for _, it := range items {
		byRef[it.MessageRef] = it
	}
	if byRef[42].State != "delivered" {
		t.Fatalf("MR 42 state = %q, want delivered", byRef[42].State)
	}
	if byRef[99].State != "failed" || byRef[99].To != "+8613800138001" {
		t.Fatalf("MR 99 = %+v, want failed to +8613800138001", byRef[99])
	}
}

func TestRadioCellLockValidationAndNoModem(t *testing.T) {
	a := offlineTestApp()
	h := a.routes()

	for _, body := range []string{
		`{}`,
		`{"earfcn":3740}`,
		`{"earfcn":-1,"pci":132}`,
		`{"earfcn":262144,"pci":132}`,
		`{"earfcn":3740,"pci":-1}`,
		`{"earfcn":3740,"pci":504}`,
	} {
		rec := doJSON(t, h, "POST", "/api/radio/cell-lock", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST /api/radio/cell-lock %s code = %d, want 400", body, rec.Code)
		}
	}
	rec := doJSON(t, h, "POST", "/api/radio/cell-lock", `{"earfcn":3740,"pci":132}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/radio/cell-lock valid but no modem code = %d, want 503", rec.Code)
	}
	rec = doJSON(t, h, "POST", "/api/radio/cell-unlock", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/radio/cell-unlock code = %d, want 503", rec.Code)
	}
	rec = doJSON(t, h, "GET", "/api/radio/cell-lock", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/radio/cell-lock code = %d, want 503", rec.Code)
	}
	rec = doJSON(t, h, "GET", "/api/radio/cell", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/radio/cell code = %d, want 503", rec.Code)
	}
	rec = doJSON(t, h, "GET", "/api/radio/operators", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"idle"`) {
		t.Fatalf("GET /api/radio/operators must return cache without modem: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, "POST", "/api/radio/operators/scan", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/radio/operators/scan code = %d, want 503", rec.Code)
	}
}

func TestRadioOperatorValidationAndNoModem(t *testing.T) {
	a := offlineTestApp()
	h := a.routes()

	for _, body := range []string{
		`{"mode":"bogus"}`,
		`{"mode":"manual"}`,
		`{"mode":"manual","plmn":"4600"}`,
		`{"mode":"manual","plmn":"46a00"}`,
	} {
		rec := doJSON(t, h, "POST", "/api/radio/operator", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST /api/radio/operator %s code = %d, want 400", body, rec.Code)
		}
	}
	rec := doJSON(t, h, "POST", "/api/radio/operator", `{"mode":"auto"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/radio/operator auto no modem code = %d, want 503", rec.Code)
	}
	rec = doJSON(t, h, "POST", "/api/radio/operator", `{"mode":"manual","plmn":"46000"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/radio/operator manual no modem code = %d, want 503", rec.Code)
	}
	rec = doJSON(t, h, "GET", "/api/radio/operator", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/radio/operator code = %d, want 503", rec.Code)
	}
}

func TestRadioPreferencesAlways200(t *testing.T) {
	a := offlineTestApp()
	rec := doJSON(t, a.routes(), "GET", "/api/radio/preferences", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/radio/preferences code = %d, want 200", rec.Code)
	}
}

func TestRadioDemoCell(t *testing.T) {
	a := newDemoApp()
	rec := doJSON(t, a.routes(), "GET", "/api/radio/cell", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("demo GET /api/radio/cell code = %d, want 200", rec.Code)
	}
	var cell map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &cell); err != nil {
		t.Fatalf("unmarshal cell: %v", err)
	}
	if cell["earfcn"] != float64(3740) || cell["pci"] != float64(132) {
		t.Fatalf("demo cell = %v, want earfcn 3740 pci 132", cell)
	}
}

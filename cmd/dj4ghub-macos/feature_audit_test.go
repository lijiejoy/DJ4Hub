package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuditTaskValidationAndScheduling(t *testing.T) {
	for _, tt := range []struct {
		name, kind, code, to, text, daily string
		interval                          int
		valid                             bool
	}{
		{"ussd", "ussd", "*100#", "", "", "", 5, true},
		{"sms", "sms", "", "+8613800000000", "test", "", 5, true},
		{"daily reboot", "reboot", "", "", "", "08:30", 0, true},
		{"missing target", "sms", "", "", "test", "", 5, false},
		{"missing text", "sms", "", "12345", "", "", 5, false},
		{"no schedule", "reboot", "", "", "", "", 0, false},
		{"too frequent", "reboot", "", "", "", "", 4, false},
		{"two schedules", "reboot", "", "", "", "08:30", 5, false},
		{"invalid clock", "reboot", "", "", "", "25:60", 0, false},
		{"unknown kind", "other", "", "", "", "", 5, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTaskBody(tt.kind, tt.code, tt.to, tt.text, tt.interval, tt.daily)
			if (err == nil) != tt.valid {
				t.Fatalf("validation: %v", err)
			}
		})
	}
	now := time.Date(2026, 11, 7, 8, 30, 0, 0, time.FixedZone("CST", 8*3600))
	if next := nextRunAfter(now, 0, "08:30"); !next.Equal(now.Add(24 * time.Hour)) {
		t.Fatalf("daily schedule repeated current slot: %v", next)
	}
	if next := nextRunAfter(now, 5, ""); !next.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("interval schedule: %v", next)
	}
}

func TestAuditGNSSFixParsing(t *testing.T) {
	status, ok := parseQGPSLOC("+QGPSLOC: 085959.0,3113.3937N,12121.2687E,1.0,12.0,3,0,0.0,0.0,071126,8\r\nOK")
	if !ok || !status.Fix || status.Latitude == nil || status.Longitude == nil ||
		*status.Latitude < 31.22 || *status.Latitude > 31.23 || *status.Longitude < 121.35 || *status.Longitude > 121.36 {
		t.Fatalf("valid GNSS fix: %+v", status)
	}
	if status, ok := parseQGPSLOC("+CME ERROR: 516"); ok || status != nil {
		t.Fatal("no-fix response produced coordinates")
	}
}

func TestAuditMCPAuthenticationAndMutationGuards(t *testing.T) {
	mcpTokenMu.Lock()
	previous := mcpToken
	mcpToken = "local-test-token"
	mcpTokenMu.Unlock()
	defer func() { mcpTokenMu.Lock(); mcpToken = previous; mcpTokenMu.Unlock() }()
	a := offlineTestApp()
	a.tasks = []*scheduledTask{}
	handler := a.routes()
	rec := doJSON(t, handler, http.MethodPost, "/api/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated MCP: %d", rec.Code)
	}
	for _, operation := range []string{"sms_send", "sip_call", "ussd_send", "reboot_module", "tasks_create"} {
		// Exercise the authenticated protocol without executing device actions.
		req := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+operation+`"}}`))
		req.Header.Set("Authorization", "Bearer local-test-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		var response mcpResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Error == nil || response.Error.Code != -32601 {
			t.Fatalf("mutation %s was not refused: %s", operation, rec.Body.String())
		}
	}
}

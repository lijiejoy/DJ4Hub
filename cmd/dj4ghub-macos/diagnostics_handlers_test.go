package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func healthyInput() diagnosticsInput {
	return diagnosticsInput{
		HasDevice:            true,
		SimKnown:             true,
		SimInserted:          true,
		RegKnown:             true,
		RegStatus:            1,
		MicrophonePermission: "authorized",
		AudioKnown:           true,
		AudioAvailable:       true,
		AudioDetail:          "USB audio ready",
		AudioInput:           "DJI Module Input",
		AudioOutput:          "DJI Module Output",
	}
}

func checkByID(t *testing.T, resp initDiagnosticsResponse, id string) diagnosticCheck {
	t.Helper()
	for _, c := range resp.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("check %q missing in %#v", id, resp.Checks)
	return diagnosticCheck{}
}

func TestBuildInitDiagnosticsAllPass(t *testing.T) {
	resp := buildInitDiagnostics(healthyInput())
	if resp.Summary != "ok" {
		t.Fatalf("Summary=%q want ok", resp.Summary)
	}
	if resp.CallActive || resp.RepairsBlocked {
		t.Fatalf("no call must mean repairs not blocked")
	}
	if len(resp.Checks) != 6 {
		t.Fatalf("len(checks)=%d want 6", len(resp.Checks))
	}
	for _, c := range resp.Checks {
		if c.Status != "pass" {
			t.Fatalf("check %s status=%q want pass (%s)", c.ID, c.Status, c.Detail)
		}
	}
	if resp.Note == "" {
		t.Fatalf("diagnostics must state they never auto-repair")
	}
}

func TestBuildInitDiagnosticsSimMissing(t *testing.T) {
	in := healthyInput()
	in.SimInserted = false
	resp := buildInitDiagnostics(in)
	if resp.Summary != "error" {
		t.Fatalf("Summary=%q want error", resp.Summary)
	}
	if c := checkByID(t, resp, "sim_ready"); c.Status != "fail" || c.Suggestion == "" {
		t.Fatalf("sim_ready=%#v", c)
	}
	if c := checkByID(t, resp, "recoverable_cause"); c.Status != "fail" {
		t.Fatalf("recoverable_cause=%#v want fail", c)
	}
}

func TestBuildInitDiagnosticsRegistrationDenied(t *testing.T) {
	in := healthyInput()
	in.RegStatus = 3
	resp := buildInitDiagnostics(in)
	if c := checkByID(t, resp, "registration"); c.Status != "fail" {
		t.Fatalf("registration=%#v want fail", c)
	}
	if resp.Summary != "error" {
		t.Fatalf("Summary=%q want error", resp.Summary)
	}
}

func TestBuildInitDiagnosticsSearchingIsWarnNotFail(t *testing.T) {
	in := healthyInput()
	in.RegStatus = 2
	resp := buildInitDiagnostics(in)
	if c := checkByID(t, resp, "registration"); c.Status != "warn" {
		t.Fatalf("registration=%#v want warn", c)
	}
	if resp.Summary != "attention" {
		t.Fatalf("Summary=%q want attention", resp.Summary)
	}
	if c := checkByID(t, resp, "recoverable_cause"); c.Status != "warn" {
		t.Fatalf("recoverable_cause=%#v want warn", c)
	}
}

func TestBuildInitDiagnosticsMicDenied(t *testing.T) {
	in := healthyInput()
	in.MicrophonePermission = "denied"
	resp := buildInitDiagnostics(in)
	if c := checkByID(t, resp, "microphone_permission"); c.Status != "fail" {
		t.Fatalf("microphone_permission=%#v want fail", c)
	}
	// 麦克风未授权时语音路由应为 warn（模块音频就绪但拾音不可用），不是 pass。
	if c := checkByID(t, resp, "voice_route"); c.Status != "warn" {
		t.Fatalf("voice_route=%#v want warn", c)
	}
}

func TestBuildInitDiagnosticsAudioUnavailable(t *testing.T) {
	in := healthyInput()
	in.AudioAvailable = false
	in.AudioDetail = "USB audio unavailable: no module device"
	resp := buildInitDiagnostics(in)
	if c := checkByID(t, resp, "audio_devices"); c.Status != "fail" || c.Suggestion == "" {
		t.Fatalf("audio_devices=%#v", c)
	}
	if c := checkByID(t, resp, "voice_route"); c.Status != "fail" {
		t.Fatalf("voice_route=%#v want fail", c)
	}
}

func TestBuildInitDiagnosticsNoDevice(t *testing.T) {
	resp := buildInitDiagnostics(diagnosticsInput{})
	if resp.Summary != "unknown" {
		t.Fatalf("Summary=%q want unknown", resp.Summary)
	}
	if c := checkByID(t, resp, "sim_ready"); c.Status != "unknown" {
		t.Fatalf("sim_ready=%#v want unknown", c)
	}
	if c := checkByID(t, resp, "recoverable_cause"); c.Status != "unknown" {
		t.Fatalf("recoverable_cause=%#v want unknown", c)
	}
}

func TestBuildInitDiagnosticsCallBlocksRepairs(t *testing.T) {
	in := healthyInput()
	in.CallActive = true
	resp := buildInitDiagnostics(in)
	if !resp.CallActive || !resp.RepairsBlocked {
		t.Fatalf("active call must block repairs: %#v", resp)
	}
	if resp.Summary != "ok" {
		t.Fatalf("Summary=%q want ok (call alone is not a failure)", resp.Summary)
	}
}

func TestInitDiagnosticsHandlerDemo(t *testing.T) {
	a := &app{demo: true} // 无 SIP：音频/权限项应为 unknown，不伪造通过
	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/init", nil)
	rec := httptest.NewRecorder()
	a.initDiagnostics(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/diagnostics/init status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp initDiagnosticsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if c := checkByID(t, resp, "sim_ready"); c.Status != "pass" {
		t.Fatalf("sim_ready=%#v want pass in demo", c)
	}
	if c := checkByID(t, resp, "registration"); c.Status != "pass" {
		t.Fatalf("registration=%#v want pass in demo", c)
	}
	if c := checkByID(t, resp, "microphone_permission"); c.Status != "unknown" {
		t.Fatalf("microphone_permission=%#v want unknown without SIP", c)
	}
	if resp.RepairsBlocked {
		t.Fatalf("demo has no call, repairs must not be blocked")
	}
}

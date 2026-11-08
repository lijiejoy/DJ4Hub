package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/WongLoki/DJ4Hub/internal/sip"
)

// newActiveCallServer builds a server with one active outbound call, the
// same shape the DTMF tests need. No real dial happens: the modem hooks
// are fakes.
func newActiveCallServer(t *testing.T) (*sip.Server, string) {
	t.Helper()
	server := sip.New(sip.DefaultConfig(), nil)
	server.Dial = func(string) error { return nil }
	server.ModemCallState = func() (sip.ModemCallState, error) {
		return sip.ModemCallState{CPAS: 4, CLCC: []sip.CLCCEntry{{Number: "12025550142", State: 0}}}, nil
	}
	id, err := server.DialFromAPI("12025550142")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.ReconcileWithModem(); err != nil {
		t.Fatal(err)
	}
	if got := server.Status().Calls[0]; got.State != sip.CallStateActive {
		t.Fatalf("expected active call, got %+v", got)
	}
	return server, id
}

func doSipJSON(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestSipDTMFHandler(t *testing.T) {
	server, id := newActiveCallServer(t)
	var digits string
	server.DTMF = func(digit string) error { digits += digit; return nil }
	instance := &app{sip: server}
	routes := instance.routes()

	w := doSipJSON(t, routes, http.MethodPost, "/api/sip/dtmf", `{"call_id":"`+id+`","digit":"5"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("DTMF 5: status %d body %s", w.Code, w.Body.String())
	}
	if digits != "5" {
		t.Fatalf("digit did not reach modem callback: %q", digits)
	}
	for _, bad := range []string{
		`{"call_id":"` + id + `","digit":"A"}`,
		`{"call_id":"` + id + `","digit":"12"}`,
		`{"call_id":"no-such-call","digit":"5"}`,
		`{"call_id":"","digit":"5"}`,
	} {
		if w := doSipJSON(t, routes, http.MethodPost, "/api/sip/dtmf", bad); w.Code != http.StatusBadRequest {
			t.Fatalf("bad DTMF %s: status %d, want 400", bad, w.Code)
		}
	}

	// Without a SIP server the endpoint is unavailable, never a fake success.
	empty := &app{}
	if w := doSipJSON(t, empty.routes(), http.MethodPost, "/api/sip/dtmf", `{"call_id":"x","digit":"5"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("DTMF without SIP: status %d, want 503", w.Code)
	}
}

func TestSipAudioControlsHandler(t *testing.T) {
	empty := &app{}
	if w := doSipJSON(t, empty.routes(), http.MethodPost, "/api/sip/audio-controls", `{"call_id":"x","microphone_muted":true,"speaker_muted":false}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("audio controls without bridge: status %d, want 503", w.Code)
	}
	bridge := sip.NewCallAudioBridge(t.TempDir())
	defer bridge.Close()
	instance := &app{sipAudio: bridge}
	// No such live call: the mute must be refused, not applied to nothing.
	if w := doSipJSON(t, instance.routes(), http.MethodPost, "/api/sip/audio-controls", `{"call_id":"old","microphone_muted":true,"speaker_muted":false}`); w.Code != http.StatusBadRequest {
		t.Fatalf("stale audio controls: status %d, want 400", w.Code)
	}
}

func TestSipAudioDevicesHandler(t *testing.T) {
	empty := &app{}
	if w := doSipJSON(t, empty.routes(), http.MethodGet, "/api/sip/audio-devices", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("audio devices without bridge: status %d, want 503", w.Code)
	}
	bridge := sip.NewCallAudioBridge(t.TempDir())
	defer bridge.Close()
	instance := &app{sipAudio: bridge}
	routes := instance.routes()

	w := doSipJSON(t, routes, http.MethodGet, "/api/sip/audio-devices", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET audio devices: status %d body %s", w.Code, w.Body.String())
	}
	var listed struct {
		Devices []sip.AudioDevice `json:"devices"`
		Input   string            `json:"input"`
		Output  string            `json:"output"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Devices) == 0 {
		t.Fatal("expected at least one listed audio device on this Mac")
	}
	for _, d := range listed.Devices {
		if d.Name == "" || d.UID == "" {
			t.Fatalf("malformed listed device: %+v", d)
		}
	}

	// An unlisted device is rejected; an empty selection resets to default.
	if w := doSipJSON(t, routes, http.MethodPost, "/api/sip/audio-devices", `{"input":"no-such-device","output":""}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unlisted device: status %d, want 400", w.Code)
	}
	var input, output string
	for _, d := range listed.Devices {
		if d.HasInput {
			input = d.UID
		}
		if d.HasOutput {
			output = d.UID
		}
	}
	if input == "" || output == "" {
		t.Skip("no device with both directions listed")
	}
	if w := doSipJSON(t, routes, http.MethodPost, "/api/sip/audio-devices", `{"input":"`+input+`","output":"`+output+`"}`); w.Code != http.StatusOK {
		t.Fatalf("save devices: status %d body %s", w.Code, w.Body.String())
	}
	w = doSipJSON(t, routes, http.MethodGet, "/api/sip/audio-devices", "")
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Input != input || listed.Output != output {
		t.Fatalf("saved selection not reported: got %q/%q want %q/%q", listed.Input, listed.Output, input, output)
	}
}

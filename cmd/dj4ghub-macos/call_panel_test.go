package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/sip"
)

func TestNativeDialerReusesWindowAndStopsWithService(t *testing.T) {
	var launches atomic.Int32
	started, focused := make(chan struct{}), make(chan struct{})
	panel := &nativeCallPanel{run: func(ctx context.Context, focus <-chan struct{}) error {
		launches.Add(1)
		close(started)
		select {
		case <-focus:
			close(focused)
		case <-ctx.Done():
		}
		<-ctx.Done()
		return nil
	}}
	if err := panel.Show(); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := panel.Show(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-focused:
	case <-time.After(time.Second):
		t.Fatal("reopening did not focus existing window")
	}
	panel.Close()
	if launches.Load() != 1 {
		t.Fatal("duplicate native dialers")
	}
	if err := panel.Show(); err == nil {
		t.Fatal("closed service reopened a dialer")
	}
}

func TestNativeRingingReleasesOwnershipWhenPanelExits(t *testing.T) {
	if !nativeCallWindowAvailable {
		t.Skip("native panel is unavailable")
	}
	exit := make(chan struct{})
	panel := &nativeCallPanel{run: func(context.Context, <-chan struct{}) error {
		<-exit
		return nil
	}}
	defer panel.Close()
	alerts := newLocalCallAlerts(nil, panel)
	defer alerts.Close()
	alerts.sound = func(ctx context.Context) { <-ctx.Done() }
	alerts.OnCallState(sip.PublicCall{ID: "closed-panel", Direction: "inbound", State: sip.CallStateRinging})
	deadline := time.Now().Add(time.Second)
	for !panel.IsOpen() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	open := panel.IsOpen()
	close(exit)
	if !open {
		t.Fatal("native ringing did not open panel")
	}
	deadline = time.Now().Add(time.Second)
	for alerts.RingingCallID() != "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if alerts.RingingCallID() != "" {
		t.Fatal("closed native panel retained ringing ownership")
	}
}

func TestCallPanelRejectsStaleActionsAndRoutesDTMF(t *testing.T) {
	server := sip.New(sip.DefaultConfig(), nil)
	server.Dial = func(string) error { return nil }
	id, err := server.DialFromAPI("12345")
	if err != nil {
		t.Fatal(err)
	}
	server.ModemCallState = func() (sip.ModemCallState, error) {
		return sip.ModemCallState{CPAS: 4, CLCC: []sip.CLCCEntry{{Number: "12345", State: 0}}}, nil
	}
	if _, err := server.ReconcileWithModem(); err != nil {
		t.Fatal(err)
	}
	var digits string
	server.DTMF = func(digit string) error { digits += digit; return nil }
	instance := &app{sip: server}
	if err := instance.performCallPanelAction(callPanelAction{Action: "dtmf", CallID: id, Digit: "#"}); err != nil {
		t.Fatal(err)
	}
	if digits != "#" {
		t.Fatal("keypad did not reach modem callback")
	}
	if err := instance.performCallPanelAction(callPanelAction{Action: "hangup", CallID: "old"}); err == nil {
		t.Fatal("stale panel action accepted")
	}
	if err := instance.performCallPanelAction(callPanelAction{Action: "hangup"}); err == nil {
		t.Fatal("missing call ID accepted")
	}
}

func TestCallPanelDialRejectsCommandCharacters(t *testing.T) {
	server := sip.New(sip.DefaultConfig(), nil)
	dials := 0
	server.Dial = func(string) error { dials++; return nil }
	instance := &app{sip: server}
	for _, number := range []string{"123;ATH", "123\nATH", "12+345", "1 234", "123\""} {
		if err := instance.performCallPanelAction(callPanelAction{Action: "dial", Number: number}); err == nil {
			t.Fatalf("invalid dial string %q accepted", number)
		}
	}
	if dials != 0 {
		t.Fatal("invalid dial string reached modem")
	}
}

func TestCallPanelHTTPEntryUnavailableAndOpen(t *testing.T) {
	instance := &app{}
	w := httptest.NewRecorder()
	instance.sipShowPanel(w, httptest.NewRequest(http.MethodPost, "/api/sip/panel", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal("unsupported platform reported success")
	}
	started := make(chan struct{})
	instance.sipPanel = &nativeCallPanel{run: func(ctx context.Context, _ <-chan struct{}) error {
		close(started)
		<-ctx.Done()
		return nil
	}}
	defer instance.sipPanel.Close()
	w = httptest.NewRecorder()
	instance.sipShowPanel(w, httptest.NewRequest(http.MethodPost, "/api/sip/panel", nil))
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("menu request did not open panel")
	}
}

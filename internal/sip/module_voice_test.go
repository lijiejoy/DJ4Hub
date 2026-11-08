package sip

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModuleVoiceSessionReusesAndClosesPrivateServer(t *testing.T) {
	dir := t.TempDir()
	adb := filepath.Join(dir, "adb")
	logPath := filepath.Join(dir, "commands")
	t.Setenv("DJ4HUB_ADB_TEST_LOG", logPath)
	if err := os.WriteFile(adb, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$DJ4HUB_ADB_TEST_LOG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	session := &moduleVoiceSession{adb: adb}
	defer session.Close()
	if err := session.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	port := session.port
	if err := session.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if session.port != port {
		t.Error("next call replaced the private server")
	}
	session.Close()
	session.Close()
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), "start-server") != 1 || strings.Count(string(log), "kill-server") != 1 {
		t.Fatalf("expected one server lifetime, got %s", log)
	}
	if err := session.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	session.Close()
	log, _ = os.ReadFile(logPath)
	if strings.Count(string(log), "start-server") != 2 {
		t.Fatal("closed session could not restart")
	}
}

func TestModuleTransportPinsPhysicalUSBLocation(t *testing.T) {
	uid := "AppleUSBAudioEngine:BAIWANG:Baiwang:14100000:7"
	list := "List of devices attached\nphone device usb:123X transport_id:2\n(no serial number) device usb:336592896X transport_id:3\n"
	got, err := moduleTransport(list, uid)
	if err != nil || got != "3" {
		t.Fatalf("transport=%q err=%v", got, err)
	}
	for _, list := range []string{
		"phone device usb:123X transport_id:2",
		"module unauthorized usb:336592896X transport_id:3",
		"module device usb:336592896X transport_id:3\nother device usb:336592896X transport_id:4",
		"module device usb:336592896X transport_id:3;reboot",
	} {
		if _, err := moduleTransport(list, uid); err == nil {
			t.Fatalf("unsafe list accepted: %s", list)
		}
	}
}

func TestCallStateHookDistinguishesLocalCallsAndRejectsLateActivation(t *testing.T) {
	var events []PublicCall
	s := New(DefaultConfig(), nil)
	s.OnCallState = func(call PublicCall) { events = append(events, call) }
	s.mu.Lock()
	local := s.newCallLocked("outbound", "12345")
	remote := s.newCallLocked("outbound", "12346")
	s.mu.Unlock()
	remote.sipLeg = true
	local.setState(CallStateActive, "")
	local.setState(CallStateActive, "")
	local.setState(CallStateEnded, "")
	local.setState(CallStateActive, "")
	remote.setState(CallStateActive, "")
	if len(events) != 3 || !events[0].LocalAudio || events[1].State != CallStateEnded || events[2].LocalAudio {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestIncomingCallWithoutSIPClientCanBeAnsweredLocally(t *testing.T) {
	s := New(DefaultConfig(), nil)
	s.Answer = func() error { return nil }
	s.IncomingCall("12345")
	calls := s.Calls()
	if len(calls) != 1 || calls[0].State != CallStateRinging {
		t.Fatalf("incoming state: %+v", calls)
	}
	if err := s.AnswerCall(calls[0].ID); err != nil {
		t.Fatal(err)
	}
	if calls = s.Calls(); calls[0].State != CallStateActive || !calls[0].LocalAudio {
		t.Fatalf("answered state: %+v", calls)
	}
	s.HangupCall(calls[0].ID)
}

func TestWebDialPreparesAudioBeforeDialCommand(t *testing.T) {
	s := New(DefaultConfig(), nil)
	prepared := false
	s.OnCallState = func(call PublicCall) {
		if call.State == CallStateDialing && call.LocalAudio {
			prepared = true
		}
	}
	s.Dial = func(string) error {
		if !prepared {
			t.Fatal("audio preparation did not begin before dialing")
		}
		return nil
	}
	id, err := s.DialFromAPI("12345")
	if err != nil {
		t.Fatal(err)
	}
	s.HangupCall(id)
}

func TestWithheldIncomingCallReconcilesCancellation(t *testing.T) {
	s := New(DefaultConfig(), nil)
	s.IncomingCall("")
	s.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 3}, nil
	}
	if _, err := s.ReconcileWithModem(); err != nil {
		t.Fatal(err)
	}
	if s.Calls()[0].State != CallStateRinging {
		t.Fatal("ringing without caller ID was lost")
	}
	s.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 0}, nil
	}
	if _, err := s.ReconcileWithModem(); err != nil {
		t.Fatal(err)
	}
	if s.Calls()[0].State != CallStateEnded {
		t.Fatal("cancelled incoming call kept ringing")
	}
}

func TestIncomingCallerIDPublishesUpdatedSnapshot(t *testing.T) {
	s := New(DefaultConfig(), nil)
	var events []PublicCall
	s.OnCallState = func(call PublicCall) { events = append(events, call) }
	s.IncomingCall("")
	s.IncomingCall("12345")
	s.IncomingCall("12345")
	if len(events) != 2 || events[1].Number != "12345" || events[0].ID != events[1].ID {
		t.Fatalf("caller ID update snapshots: %+v", events)
	}
	s.HangupCall(events[0].ID)
}

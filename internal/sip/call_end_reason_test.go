package sip

import (
	"testing"
	"time"
)

func TestEndReasonMappings(t *testing.T) {
	if got, ok := MapSIPRejectReason(486); !ok || got != EndReasonBusy {
		t.Fatalf("486 = %v/%v, want busy/true", got, ok)
	}
	if got, ok := MapSIPRejectReason(487); !ok || got != EndReasonRemoteCancel {
		t.Fatalf("487 = %v/%v, want remote_cancel/true", got, ok)
	}
	if got, ok := MapSIPRejectReason(408); !ok || got != EndReasonNoAnswer {
		t.Fatalf("408 = %v/%v, want no_answer/true", got, ok)
	}
	if _, ok := MapSIPRejectReason(404); ok {
		t.Fatal("404 must not map to a reason")
	}
	if got, ok := MapReleaseURC("BUSY", false); !ok || got != EndReasonBusy {
		t.Fatalf("BUSY = %v/%v, want busy/true", got, ok)
	}
	if got, ok := MapReleaseURC("NO ANSWER", false); !ok || got != EndReasonNoAnswer {
		t.Fatalf("NO ANSWER = %v/%v, want no_answer/true", got, ok)
	}
	if got, ok := MapReleaseURC("NO CARRIER", true); !ok || got != EndReasonRemoteHangup {
		t.Fatalf("NO CARRIER active = %v/%v, want remote_hangup/true", got, ok)
	}
	if got, ok := MapReleaseURC("NO CARRIER", false); !ok || got != EndReasonRemoteCancel {
		t.Fatalf("NO CARRIER ringing = %v/%v, want remote_cancel/true", got, ok)
	}
	if got, ok := MapReleaseURC("NO DIALTONE", false); !ok || got != EndReasonNoNetwork {
		t.Fatalf("NO DIALTONE = %v/%v, want no_network/true", got, ok)
	}
	if _, ok := MapReleaseURC("something else", false); ok {
		t.Fatal("an unrecognised URC must not map to a reason")
	}
	for _, reason := range []CallEndReason{
		EndReasonUnknown, EndReasonLocalHangup, EndReasonRemoteHangup,
		EndReasonLocalReject, EndReasonRemoteCancel, EndReasonNoAnswer,
		EndReasonNoNetwork, EndReasonBusy, EndReasonMediaFailed,
		EndReasonDeviceDisconnected, EndReasonLocalCancel,
	} {
		if EndReasonLabel(reason) == "" {
			t.Fatalf("no label for reason %q", reason)
		}
	}
}

func findCallByNumber(t *testing.T, srv *Server, number string) PublicCall {
	t.Helper()
	for _, c := range srv.Status().Calls {
		if c.Number == number {
			return c
		}
	}
	t.Fatalf("no call record for %s", number)
	return PublicCall{}
}

func TestEndReasonIsRecordedPerTransition(t *testing.T) {
	srv, _ := startTestServer(t)
	srv.Dial = func(string) error { return nil }

	// Outbound dial cancelled locally while ringing: local_cancel.
	id, err := srv.DialFromAPI("12025550142")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.HangupCall(id); err != nil {
		t.Fatal(err)
	}
	call := findCallByNumber(t, srv, "12025550142")
	if call.State != CallStateEnded || call.EndReason != EndReasonLocalCancel {
		t.Fatalf("cancelled dial: state=%s reason=%s", call.State, call.EndReason)
	}

	// Inbound ringing rejected locally: local_reject, same stable ID.
	srv.IncomingCall("12025550143")
	inbound := findCallByNumber(t, srv, "12025550143")
	if inbound.State != CallStateRinging {
		t.Fatalf("inbound should be ringing, got %+v", inbound)
	}
	if err := srv.HangupCall(inbound.ID); err != nil {
		t.Fatal(err)
	}
	after := findCallByNumber(t, srv, "12025550143")
	if after.ID != inbound.ID {
		t.Fatalf("hangup created a new record: %s -> %s", inbound.ID, after.ID)
	}
	if after.State != CallStateEnded || after.EndReason != EndReasonLocalReject {
		t.Fatalf("rejected inbound: state=%s reason=%s", after.State, after.EndReason)
	}

	// Inbound ringing cancelled remotely (NO CARRIER): remote_cancel.
	srv.IncomingCall("12025550144")
	ringing := findCallByNumber(t, srv, "12025550144")
	srv.ModemHungUp()
	after = findCallByNumber(t, srv, "12025550144")
	if after.ID != ringing.ID || after.State != CallStateEnded || after.EndReason != EndReasonRemoteCancel {
		t.Fatalf("remote cancel: id=%s state=%s reason=%s", after.ID, after.State, after.EndReason)
	}

	// Outbound answered, then remote hangup: remote_hangup.
	srv.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 4, CLCC: []CLCCEntry{{Index: 1, Dir: 1, State: 0, Number: "12025550145"}}}, nil
	}
	id, err = srv.DialFromAPI("12025550145")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ReconcileWithModem(); err != nil {
		t.Fatal(err)
	}
	calls := srv.Status().Calls
	var active *PublicCall
	for i := range calls {
		if calls[i].ID == id {
			active = &calls[i]
		}
	}
	if active == nil || active.State != CallStateActive {
		t.Fatalf("expected active call, got %+v", calls)
	}
	if !active.ModuleConnected {
		t.Fatal("active call backed by a real +CLCC entry must report module connected")
	}
	srv.ModemHungUp()
	calls = srv.Status().Calls
	for i := range calls {
		if calls[i].ID == id {
			if calls[i].State != CallStateEnded || calls[i].EndReason != EndReasonRemoteHangup {
				t.Fatalf("remote hangup: state=%s reason=%s", calls[i].State, calls[i].EndReason)
			}
		}
	}
}

func TestEndReasonNoModemAndBusyAndTimeout(t *testing.T) {
	// No modem at all: dial fails as no_network, never as a generic miss.
	srv, _ := startTestServer(t)
	id, err := srv.DialFromAPI("12025550142")
	if err == nil {
		t.Fatal("dial without a modem should fail")
	}
	call := srv.Status().Calls[0]
	if call.ID != id || call.State != CallStateFailed || call.EndReason != EndReasonNoNetwork {
		t.Fatalf("no-modem dial: id=%s state=%s reason=%s", call.ID, call.State, call.EndReason)
	}

	// Modem releases an unestablished dial with BUSY: busy, not missed.
	srv2, _ := startTestServer(t)
	srv2.Dial = func(string) error { return nil }
	srv2.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 0, CLCC: nil, ReleaseReason: "BUSY"}, nil
	}
	if _, err := srv2.DialFromAPI("12025550142"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv2.ReconcileWithModem(); err != nil {
		t.Fatal(err)
	}
	call = srv2.Status().Calls[0]
	if call.State != CallStateFailed || call.EndReason != EndReasonBusy {
		t.Fatalf("BUSY release: state=%s reason=%s", call.State, call.EndReason)
	}

	// A dial that never connects fails the 60 s timeout as no_answer.
	srv3, _ := startTestServer(t)
	srv3.Dial = func(string) error { return nil }
	srv3.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 0, CLCC: nil}, nil
	}
	if _, err := srv3.DialFromAPI("12025550142"); err != nil {
		t.Fatal(err)
	}
	srv3.mu.Lock()
	for _, c := range srv3.calls {
		c.StartedAt = c.StartedAt.Add(-61 * time.Second)
	}
	srv3.mu.Unlock()
	if _, err := srv3.ReconcileWithModem(); err != nil {
		t.Fatal(err)
	}
	call = srv3.Status().Calls[0]
	if call.State != CallStateFailed || call.EndReason != EndReasonNoAnswer {
		t.Fatalf("dial timeout: state=%s reason=%s", call.State, call.EndReason)
	}
}

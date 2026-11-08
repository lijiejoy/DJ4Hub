package sip

import (
	"net"
	"testing"
	"time"
)

// Duration counts only the active segment: ring wait is excluded, and a
// call that never connected reports zero.
func TestCallDurationCountsOnlyActiveSegment(t *testing.T) {
	srv, _ := startTestServer(t)
	srv.Dial = func(string) error { return nil }
	id, err := srv.DialFromAPI("12025550142")
	if err != nil {
		t.Fatal(err)
	}
	ringing := findCallByNumber(t, srv, "12025550142")
	if ringing.DurationSeconds != 0 {
		t.Fatalf("ringing call must report 0 duration, got %d", ringing.DurationSeconds)
	}
	if ringing.Timings == nil || ringing.Timings.DialToRingingMs == nil {
		t.Fatalf("ringing call must record dial-to-ringing timing, got %+v", ringing.Timings)
	}
	// Peer answers, then hangs up 90 s later (simulated by backdating
	// ConnectedAt: the ring wait before it must not leak into duration).
	srv.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 4, CLCC: []CLCCEntry{{Index: 1, Dir: 1, State: 0, Number: "12025550142"}}}, nil
	}
	if _, err := srv.ReconcileWithModem(); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	for _, c := range srv.calls {
		if c.ID == id {
			c.ConnectedAt = c.ConnectedAt.Add(-90 * time.Second)
			c.ringAt = c.StartedAt.Add(2 * time.Second)
		}
	}
	srv.mu.Unlock()
	active := findCallByNumber(t, srv, "12025550142")
	if active.State != CallStateActive {
		t.Fatalf("expected active call, got %+v", active)
	}
	if active.DurationSeconds < 89 || active.DurationSeconds > 91 {
		t.Fatalf("live active duration = %d, want ~90", active.DurationSeconds)
	}
	if active.Timings == nil || active.Timings.RingToAnswerMs == nil || active.Timings.DialToConnectedMs == nil {
		t.Fatalf("active call must record ring-to-answer and dial-to-connected timings, got %+v", active.Timings)
	}
	srv.ModemHungUp()
	ended := findCallByNumber(t, srv, "12025550142")
	if ended.DurationSeconds < 89 || ended.DurationSeconds > 91 {
		t.Fatalf("ended duration = %d, want ~90", ended.DurationSeconds)
	}
}

// A late CLIP must update the same ringing record, not create a second
// call or a second alert.
func TestLateCallerIDUpdatesSameCall(t *testing.T) {
	srv, _ := startTestServer(t)
	srv.NoteRing()
	time.Sleep(clipWait + 100*time.Millisecond)
	calls := srv.Status().Calls
	if len(calls) != 1 {
		t.Fatalf("RING without CLIP must create exactly one call, got %d", len(calls))
	}
	if calls[0].Number != "unknown" || calls[0].State != CallStateRinging {
		t.Fatalf("unexpected first record: %+v", calls[0])
	}
	firstID := calls[0].ID
	srv.NoteClip("12025550142")
	calls = srv.Status().Calls
	if len(calls) != 1 {
		t.Fatalf("late CLIP must not create a second call, got %d", len(calls))
	}
	if calls[0].ID != firstID || calls[0].Number != "12025550142" {
		t.Fatalf("late CLIP must update the same record: %+v", calls[0])
	}
}

// MediaReady is set only once RTP frames actually move, and the
// answer-to-first-media segment is recorded with it.
func TestMediaReadyRequiresFlowingRTP(t *testing.T) {
	srv, _ := startTestServer(t)
	srv.mu.Lock()
	call := srv.newCallLocked("outbound", "12025550142")
	srv.mu.Unlock()
	if got := call.public(); got.MediaReady {
		t.Fatal("a call with no media session must not report media ready")
	}
	rtp, err := NewRTPSession(20000, 20100, &QPCMVBridge{})
	if err != nil {
		t.Fatal(err)
	}
	defer rtp.Stop()
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if err := rtp.Start(peer.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	call.mu.Lock()
	call.rtp = rtp
	call.mu.Unlock()
	call.setState(CallStateActive, "")
	deadline := time.Now().Add(2 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if got := call.public(); got.MediaReady {
			ready = true
			if got.MediaReadyAt.IsZero() {
				t.Fatal("media ready without a timestamp")
			}
			if got.Timings == nil || got.Timings.AnswerToFirstMedia == nil {
				t.Fatalf("answer-to-first-media timing missing: %+v", got.Timings)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("RTP frames flowing for 2 s did not mark media ready")
	}
}

// The local bridge reports media flowing only after PCM was forwarded in
// either direction — "running" alone is not enough.
func TestLocalMediaFlowingRequiresForwardedPCM(t *testing.T) {
	b, _, _ := testLocalBridge()
	defer b.Close()
	if b.LocalMediaFlowing("one") {
		t.Fatal("no local call must not report media flowing")
	}
	b.OnCallState(PublicCall{ID: "one", State: CallStateActive, LocalAudio: true})
	waitLocalTest(t, func() bool { return b.LocalMediaFlowing("one") })
	if b.LocalMediaFlowing("two") {
		t.Fatal("a different call ID must not report media flowing")
	}
	b.OnCallState(PublicCall{ID: "one", State: CallStateEnded, LocalAudio: true})
	waitLocalTest(t, func() bool { return b.AudioSnapshot().Local.State == "idle" })
	if b.LocalMediaFlowing("one") {
		t.Fatal("an ended call must not report media flowing")
	}
}

// Device selection round-trips through the persisted file and rejects
// names/UIDs that are not listed, so a stale choice can never silently
// fall back to another device.
func TestAudioDeviceSelectionRoundtrip(t *testing.T) {
	devices, err := ListLocalAudioDevices()
	if err != nil {
		t.Skipf("audio device listing unavailable in this environment: %v", err)
	}
	if len(devices) == 0 {
		t.Skip("no local audio devices listed")
	}
	for _, d := range devices {
		if d.Name == "" || d.UID == "" || (!d.HasInput && !d.HasOutput) {
			t.Fatalf("malformed listed device: %+v", d)
		}
	}
	dir := t.TempDir()
	bridge := NewCallAudioBridge(dir)
	defer bridge.Close()
	if err := bridge.SetAudioDevices("no-such-device", ""); err == nil {
		t.Fatal("an unlisted input device was accepted")
	}
	if err := bridge.SetAudioDevices("", "no-such-device"); err == nil {
		t.Fatal("an unlisted output device was accepted")
	}
	var input, output string
	for _, d := range devices {
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
	if err := bridge.SetAudioDevices(input, output); err != nil {
		t.Fatal(err)
	}
	gotIn, gotOut := bridge.AudioDeviceSelection()
	if gotIn != input || gotOut != output {
		t.Fatalf("selection roundtrip: got %q/%q want %q/%q", gotIn, gotOut, input, output)
	}
	// A fresh bridge over the same directory reads the same selection.
	bridge2 := NewCallAudioBridge(dir)
	defer bridge2.Close()
	gotIn, gotOut = bridge2.AudioDeviceSelection()
	if gotIn != input || gotOut != output {
		t.Fatalf("persisted selection: got %q/%q want %q/%q", gotIn, gotOut, input, output)
	}
	if err := bridge2.SetAudioDevices("", ""); err != nil {
		t.Fatal(err)
	}
	gotIn, gotOut = bridge2.AudioDeviceSelection()
	if gotIn != "" || gotOut != "" {
		t.Fatalf("reset did not clear the selection: %q/%q", gotIn, gotOut)
	}
}

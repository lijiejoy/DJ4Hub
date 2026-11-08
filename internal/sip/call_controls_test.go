package sip

import "testing"

func TestDTMFRequiresConnectedExactCallAndOneValidDigit(t *testing.T) {
	server := New(DefaultConfig(), nil)
	server.Dial = func(string) error { return nil }
	id, err := server.DialFromAPI("12345")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server.DTMF = func(string) error { calls++; return nil }
	if err := server.SendDTMF(id, "1"); err == nil {
		t.Fatal("DTMF accepted before answer")
	}
	server.ModemCallState = func() (ModemCallState, error) {
		return ModemCallState{CPAS: 4, CLCC: []CLCCEntry{{Number: "12345", State: 0}}}, nil
	}
	if _, err := server.ReconcileWithModem(); err != nil {
		t.Fatal(err)
	}
	for _, digit := range []string{"", "12", "A", `1";ATH`, "\n"} {
		if err := server.SendDTMF(id, digit); err == nil {
			t.Fatalf("invalid digit %q accepted", digit)
		}
	}
	if err := server.SendDTMF("", "1"); err == nil {
		t.Fatal("empty ID accepted")
	}
	for _, digit := range []string{"0", "9", "*", "#"} {
		if err := server.SendDTMF(id, digit); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 4 {
		t.Fatalf("unexpected modem commands: %d", calls)
	}
	server.HangupCall(id)
	if err := server.SendDTMF(id, "1"); err == nil {
		t.Fatal("DTMF accepted after hangup")
	}
}

func TestLocalMuteControlsSilenceOnlySelectedDirectionAndReset(t *testing.T) {
	b, _, _ := testLocalBridge()
	defer b.Close()
	b.OnCallState(PublicCall{ID: "one", State: CallStateActive, LocalAudio: true})
	waitLocalTest(t, func() bool { return b.AudioSnapshot().Local.State == "running" })
	if err := b.SetLocalControls("old", true, true); err == nil {
		t.Fatal("stale call altered microphone")
	}
	if err := b.SetLocalControls("one", true, false); err != nil {
		t.Fatal(err)
	}
	status := b.AudioSnapshot().Local
	if !status.MicrophoneMuted || status.SpeakerMuted {
		t.Fatal("mute directions mixed up")
	}
	read := func(int) ([]int16, error) { return []int16{100, -200, 300}, nil }
	for _, muted := range []bool{false, true} {
		var written []int16
		_, err := forwardControlledCallPCM(read, func(p []int16) error {
			written = append(written, p...)
			return nil
		}, muted)
		if err != nil || len(written) != 3 {
			t.Fatal("muting interrupted device clock")
		}
		for i, sample := range written {
			if (sample == 0) != muted {
				t.Fatalf("sample %d mute=%v value=%d", i, muted, sample)
			}
		}
	}
	b.OnCallState(PublicCall{ID: "one", State: CallStateEnded, LocalAudio: true})
	b.OnCallState(PublicCall{ID: "two", State: CallStateActive, LocalAudio: true})
	waitLocalTest(t, func() bool { return b.AudioSnapshot().Local.State == "running" })
	if b.AudioSnapshot().Local.MicrophoneMuted {
		t.Fatal("next call inherited microphone mute")
	}
}

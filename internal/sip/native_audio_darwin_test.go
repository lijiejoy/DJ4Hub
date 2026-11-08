//go:build darwin && cgo

package sip

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeCallPreparationLatency(t *testing.T) {
	if os.Getenv("DJ4HUB_TEST_CALL_PREPARE") != "1" {
		t.Skip("set DJ4HUB_TEST_CALL_PREPARE=1 with no live call to measure preparation without microphone capture")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	bridge := NewCallAudioBridge(filepath.Join(dir, "DJ 4G Hub"))
	defer bridge.Close()
	warm := bridge.warmModule
	warmed := make(chan error, 1)
	bridge.warmModule = func(ctx context.Context) error {
		start := time.Now()
		err := warm(ctx)
		t.Logf("background module warmup: %s", time.Since(start).Round(time.Millisecond))
		warmed <- err
		return err
	}
	bridge.Warmup()
	if err := <-warmed; err != nil {
		t.Fatal(err)
	}
	if status := bridge.AudioSnapshot(); status.Running || status.Local.State != "idle" {
		t.Fatal("background warmup opened host audio")
	}
	prepared := make(chan pcmEndpoint, 1)
	moduleOpen, localOpen := bridge.usbAudioBridge.open, bridge.openLocal
	bridge.usbAudioBridge.open = func() (pcmEndpoint, audioDevices, error) {
		start := time.Now()
		endpoint, devices, err := moduleOpen()
		t.Logf("module route and USB startup: %s", time.Since(start).Round(time.Millisecond))
		return endpoint, devices, err
	}
	bridge.openLocal = func() (pcmEndpoint, audioDevices, error) {
		start := time.Now()
		endpoint, devices, err := localOpen()
		t.Logf("stopped Mac audio preparation: %s", time.Since(start).Round(time.Millisecond))
		if err == nil {
			prepared <- endpoint
		}
		return endpoint, devices, err
	}
	previousRoute := ""
	for _, id := range []string{"cold", "warm"} {
		if !t.Run(id, func(t *testing.T) {
			start := time.Now()
			bridge.OnCallState(PublicCall{ID: id, State: CallStateRinging, LocalAudio: true})
			t.Cleanup(func() {
				bridge.OnCallState(PublicCall{ID: id, State: CallStateEnded, LocalAudio: true})
				waitLocalTest(t, func() bool { return bridge.AudioSnapshot().Local.State == "idle" })
			})
			deadline := time.NewTimer(60 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-deadline.C:
					t.Fatal("call preparation timed out")
				case <-ticker.C:
					status := bridge.AudioSnapshot()
					if status.Local.State == "failed" {
						t.Fatal(status.Local.Error)
					}
					if status.Local.State == "ready" {
						counts := (<-prepared).Counters()
						if counts.Captured != 0 || counts.Played != 0 {
							t.Fatal("prepared Mac audio captured or played before answer")
						}
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						list, err := bridge.voice.command(ctx, "devices", "-l")
						if err != nil {
							t.Fatal(err)
						}
						transport, err := moduleTransport(list, status.InputUID)
						if err != nil {
							t.Fatal(err)
						}
						route, err := bridge.voice.command(ctx, "-t", transport, "shell", "cat /run/dj4hub-voice-route.pid")
						if err != nil || strings.TrimSpace(route) == "" {
							t.Fatalf("cannot identify prepared route: %v", err)
						}
						if previousRoute == route {
							t.Fatal("next call reused the previous call's DSP route")
						}
						previousRoute = route
						t.Logf("prepared both endpoints in %s without starting Mac microphone", time.Since(start).Round(time.Millisecond))
						return
					}
				}
			}
		}) {
			break
		}
	}
}

// Explicit opt-in keeps the normal test suite from touching USB audio.
func TestNativeModuleAudio(t *testing.T) {
	if os.Getenv("DJ4HUB_TEST_USB_AUDIO") != "1" {
		t.Skip("set DJ4HUB_TEST_USB_AUDIO=1 for attached module audio validation")
	}
	bridge := NewUSBAudioBridge().(*usbAudioBridge)
	original, err := inspectNativeAudio()
	if err != nil {
		t.Fatal(err)
	}
	before := bridge.AudioSnapshot()
	t.Logf("module audio: %+v", before)
	if !before.Available {
		t.Fatal(before.Detail)
	}
	if err := bridge.StartPCM(); err != nil {
		t.Fatal(err)
	}
	defer bridge.StopPCM()
	during, err := inspectNativeAudio()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("hardware frames input=%d output=%d (before %d/%d)",
		during.Input.BufferFrames, during.Output.BufferFrames, original.Input.BufferFrames, original.Output.BufferFrames)
	for i := 0; i < 100; i++ {
		if err := bridge.WritePCM(make([]int16, 160)); err != nil {
			t.Fatal(err)
		}
		if _, err := bridge.ReadPCM(160); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	status := bridge.AudioSnapshot()
	t.Logf("active module audio: %+v", status)
	if status.CapturedSamples == 0 || status.PlayedSamples == 0 {
		t.Fatal("CoreAudio callbacks did not transfer samples; check microphone permission")
	}
	bridge.StopPCM()
	if bridge.AudioSnapshot().Running {
		t.Fatal("audio did not stop")
	}
	restored, err := inspectNativeAudio()
	if err != nil {
		t.Fatal(err)
	}
	if restored.Input.BufferFrames != original.Input.BufferFrames || restored.Output.BufferFrames != original.Output.BufferFrames {
		t.Fatalf("hardware buffer not restored: before=%d/%d after=%d/%d",
			original.Input.BufferFrames, original.Output.BufferFrames, restored.Input.BufferFrames, restored.Output.BufferFrames)
	}
}

func TestNativeLocalVoiceAudioBoundsStalledCapture(t *testing.T) {
	if os.Getenv("DJ4HUB_TEST_LOCAL_AUDIO") != "1" {
		t.Skip("set DJ4HUB_TEST_LOCAL_AUDIO=1 for Mac voice-processing validation")
	}
	endpoint, devices, err := openLocalAudio("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	t.Logf("voice processing devices: %+v", devices)
	// Reproduce a stalled consumer: capture must recover current audio instead
	// of leaving several hundred milliseconds of stale speech in the ring.
	time.Sleep(700 * time.Millisecond)
	before := endpoint.Counters()
	if _, err := endpoint.ReadPCM(80); err != nil {
		t.Fatal(err)
	}
	after := endpoint.Counters()
	if before.Captured == 0 || before.Played == 0 {
		t.Fatal("voice-processing callbacks did not run")
	}
	if after.Dropped <= before.Dropped {
		t.Fatal("stale capture was not discarded")
	}
	// Allow a simultaneous hardware callback after ReadPCM.
	if after.CaptureQueued > 800 {
		t.Fatalf("capture backlog remains above 100ms: %+v", after)
	}
	t.Logf("capture backlog before=%d after=%d discarded=%d", before.CaptureQueued, after.CaptureQueued, after.Dropped-before.Dropped)
}

func TestNativeLocalPreparedAudioDoesNotCaptureBeforeAnswer(t *testing.T) {
	if os.Getenv("DJ4HUB_TEST_LOCAL_AUDIO") != "1" {
		t.Skip("set DJ4HUB_TEST_LOCAL_AUDIO=1 for Mac voice-processing validation")
	}
	endpoint, _, err := prepareLocalAudio("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	time.Sleep(300 * time.Millisecond)
	if counts := endpoint.Counters(); counts.Captured != 0 || counts.Played != 0 {
		t.Fatalf("prepared device started callbacks before answer: %+v", counts)
	}
	start := time.Now()
	if err := endpoint.(*localPCM).Start(); err != nil {
		t.Fatal(err)
	}
	for time.Since(start) < time.Second {
		if counts := endpoint.Counters(); counts.Captured > 0 && counts.Played > 0 {
			t.Logf("prepared audio callbacks started in %s", time.Since(start))
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("prepared audio did not start within one second")
}

func TestNativeLocalDuplexContinuity(t *testing.T) {
	if os.Getenv("DJ4HUB_TEST_CALL_AUDIO") != "1" {
		t.Skip("set DJ4HUB_TEST_CALL_AUDIO=1 for Mac/module continuity validation without dialing")
	}
	module := NewUSBAudioBridge().(*usbAudioBridge)
	if err := module.StartPCM(); err != nil {
		t.Fatal(err)
	}
	defer module.StopPCM()
	local, _, err := openLocalAudio("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	var beforeLocal pcmCounters
	var beforeModule AudioStatus
	for i := 0; i < 800; i++ {
		<-ticker.C
		if _, err := forwardCallPCM(local.ReadPCM, module.WritePCM); err != nil {
			t.Fatal(err)
		}
		if _, err := forwardCallPCM(module.ReadPCM, local.WritePCM); err != nil {
			t.Fatal(err)
		}
		if i == 399 {
			beforeLocal, beforeModule = local.Counters(), module.AudioSnapshot()
		}
	}
	afterLocal, afterModule := local.Counters(), module.AudioSnapshot()
	mPlayed := afterModule.PlayedSamples - beforeModule.PlayedSamples
	mMissing := afterModule.UnderrunSamples - beforeModule.UnderrunSamples
	mDropped := afterModule.DroppedSamples - beforeModule.DroppedSamples
	lPlayed := afterLocal.Played - beforeLocal.Played
	lMissing := afterLocal.Underruns - beforeLocal.Underruns
	lDropped := afterLocal.Dropped - beforeLocal.Dropped
	t.Logf("steady module played=%d missing=%d dropped=%d; Mac played=%d missing=%d dropped=%d",
		mPlayed, mMissing, mDropped, lPlayed, lMissing, lDropped)
	if mPlayed == 0 || lPlayed == 0 {
		t.Fatal("duplex callbacks stopped")
	}
	if mMissing*100 > mPlayed*3 || lMissing*100 > lPlayed*3 ||
		mDropped*100 > mPlayed*3 || lDropped*100 > lPlayed*3 {
		t.Fatal("steady audio interruption exceeds 3%; inspect callback batching")
	}
}

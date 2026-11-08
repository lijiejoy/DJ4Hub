package sip

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestModuleWarmupDoesNotOpenHostAudioAndStopsOnClose(t *testing.T) {
	b, module, local := testLocalBridge()
	entered, stopped := make(chan struct{}), make(chan struct{})
	permissions := 0
	b.permission = func() error {
		permissions++
		return nil
	}
	b.warmModule = func(ctx context.Context) error {
		if permissions != 1 {
			t.Error("startup did not request permission before warming module")
		}
		close(entered)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}
	b.Warmup()
	b.Warmup()
	<-entered
	if state := b.AudioSnapshot().Local.State; state != "idle" {
		t.Fatalf("warmup started a call: %s", state)
	}
	b.Close()
	select {
	case <-stopped:
	default:
		t.Fatal("warmup did not stop")
	}
	b.Warmup()
	if n, closed := module.snapshot(); n != 0 || closed != 0 {
		t.Fatal("route warmup opened module host streams")
	}
	if n, closed := local.snapshot(); n != 0 || closed != 0 {
		t.Fatal("route warmup opened Mac microphone/speaker")
	}
}

type localTestPCM struct {
	mu                     sync.Mutex
	written, reads, closed int
	fail                   bool
	starts                 int
}

func (p *localTestPCM) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.starts++
	return nil
}

func (p *localTestPCM) ReadPCM(n int) ([]int16, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads++
	if p.fail {
		return nil, errors.New("device disconnected")
	}
	pcm := make([]int16, n)
	for i := range pcm {
		pcm[i] = 1500
	}
	return pcm, nil
}
func (p *localTestPCM) WritePCM(pcm []int16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, v := range pcm {
		if v != 0 {
			p.written++
		}
	}
	return nil
}
func (p *localTestPCM) Close() { p.mu.Lock(); p.closed++; p.mu.Unlock() }
func (p *localTestPCM) Counters() pcmCounters {
	p.mu.Lock()
	defer p.mu.Unlock()
	return pcmCounters{Captured: uint64(p.reads * 160), Played: uint64(p.written), InputNonzero: uint64(p.reads * 160), OutputNonzero: uint64(p.written)}
}
func (p *localTestPCM) snapshot() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.written, p.closed
}
func waitLocalTest(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("local audio condition timed out")
}
func testLocalBridge() (*CallAudioBridge, *localTestPCM, *localTestPCM) {
	module, local := &localTestPCM{}, &localTestPCM{}
	devices := audioDevices{InputUID: "module-input", OutputUID: "module-output"}
	b := &CallAudioBridge{
		usbAudioBridge: &usbAudioBridge{
			probe: func() (audioDevices, error) { return devices, nil },
			open:  func() (pcmEndpoint, audioDevices, error) { return module, devices, nil },
		},
		openLocal: func() (pcmEndpoint, audioDevices, error) {
			return local, audioDevices{InputName: "mic", OutputName: "speaker"}, nil
		},
		local: LocalAudioStatus{State: "idle"},
	}
	return b, module, local
}
func TestLocalCallTransfersBothDirectionsAndStopsOnce(t *testing.T) {
	b, module, local := testLocalBridge()
	defer b.Close()
	call := PublicCall{ID: "local-1", LocalAudio: true, State: CallStateActive}
	b.OnCallState(call)
	b.OnCallState(call)
	waitLocalTest(t, func() bool { m, _ := module.snapshot(); l, _ := local.snapshot(); return m > 0 && l > 0 })
	call.State = CallStateEnded
	b.OnCallState(call)
	b.OnCallState(call)
	b.Close()
	if _, closed := module.snapshot(); closed != 1 {
		t.Fatalf("module closed %d times", closed)
	}
	if _, closed := local.snapshot(); closed != 1 {
		t.Fatalf("local closed %d times", closed)
	}
	if status := b.AudioSnapshot(); status.Running || status.Local.State != "idle" {
		t.Fatalf("still running: %+v", status)
	}
}

func TestConsecutiveLocalCallsTransferAudioAndReleasePreviousDevices(t *testing.T) {
	b, _, _ := testLocalBridge()
	defer b.Close()
	modules := []*localTestPCM{{}, {}, {}}
	locals := []*localTestPCM{{}, {}, {}}
	moduleIndex, localIndex := 0, 0
	b.usbAudioBridge.open = func() (pcmEndpoint, audioDevices, error) {
		endpoint := modules[moduleIndex]
		moduleIndex++
		return endpoint, audioDevices{}, nil
	}
	b.openLocal = func() (pcmEndpoint, audioDevices, error) {
		endpoint := locals[localIndex]
		localIndex++
		return endpoint, audioDevices{}, nil
	}
	for i, id := range []string{"first", "second", "third"} {
		call := PublicCall{ID: id, Direction: "inbound", State: CallStateRinging, LocalAudio: true}
		b.OnCallState(call)
		waitLocalTest(t, func() bool { return b.AudioSnapshot().Local.State == "ready" })
		for previous := 0; previous < i; previous++ {
			_, moduleClosed := modules[previous].snapshot()
			_, localClosed := locals[previous].snapshot()
			if moduleClosed != 1 || localClosed != 1 {
				t.Fatal("next call started before previous endpoints were released")
			}
		}
		call.State = CallStateActive
		b.OnCallState(call)
		waitLocalTest(t, func() bool {
			moduleSamples, _ := modules[i].snapshot()
			localSamples, _ := locals[i].snapshot()
			return moduleSamples > 0 && localSamples > 0
		})
		call.State = CallStateEnded
		b.OnCallState(call)
		// Start the next call immediately, while previous cleanup may still run.
	}
	b.Close()
	for i := range modules {
		_, moduleClosed := modules[i].snapshot()
		_, localClosed := locals[i].snapshot()
		if moduleClosed != 1 || localClosed != 1 {
			t.Fatalf("call %d cleanup counts module=%d local=%d", i+1, moduleClosed, localClosed)
		}
	}
}
func TestSIPCallDoesNotOpenLocalMicrophone(t *testing.T) {
	b, module, local := testLocalBridge()
	b.OnCallState(PublicCall{ID: "sip", State: CallStateActive, LocalAudio: false})
	b.Close()
	if written, closed := module.snapshot(); written != 0 || closed != 0 {
		t.Fatal("SIP call opened local module worker")
	}
	if written, closed := local.snapshot(); written != 0 || closed != 0 {
		t.Fatal("SIP call touched Mac devices")
	}
}

func TestLateActiveNotificationDoesNotCaptureAfterHangup(t *testing.T) {
	b, module, local := testLocalBridge()
	b.OnCallState(PublicCall{ID: "local", State: CallStateEnded, LocalAudio: true})
	b.OnCallState(PublicCall{ID: "local", State: CallStateActive, LocalAudio: true})
	b.Close()
	if _, closed := module.snapshot(); closed != 0 {
		t.Fatal("late event opened module")
	}
	if _, closed := local.snapshot(); closed != 0 {
		t.Fatal("late event opened microphone")
	}
	if state := b.AudioSnapshot().Local.State; state != "idle" {
		t.Fatalf("late event changed state: %s", state)
	}
}
func TestLocalStartupReleasedAfterHangup(t *testing.T) {
	b, module, local := testLocalBridge()
	entered, release := make(chan struct{}), make(chan struct{})
	b.openLocal = func() (pcmEndpoint, audioDevices, error) {
		close(entered)
		<-release
		return local, audioDevices{}, nil
	}
	b.OnCallState(PublicCall{ID: "local", State: CallStateActive, LocalAudio: true})
	<-entered
	b.OnCallState(PublicCall{ID: "local", State: CallStateEnded, LocalAudio: true})
	close(release)
	b.Close()
	if _, closed := module.snapshot(); closed != 1 {
		t.Fatal("late startup leaked module")
	}
	if _, closed := local.snapshot(); closed != 1 {
		t.Fatal("late startup leaked microphone")
	}
}

func TestLocalDisconnectReleasesBothEndpoints(t *testing.T) {
	b, module, local := testLocalBridge()
	defer b.Close()
	b.OnCallState(PublicCall{ID: "local", State: CallStateActive, LocalAudio: true})
	waitLocalTest(t, func() bool { n, _ := module.snapshot(); return n > 0 })
	local.mu.Lock()
	local.fail = true
	local.mu.Unlock()
	waitLocalTest(t, func() bool { return b.AudioSnapshot().Local.State == "failed" })
	b.Close()
	if _, closed := module.snapshot(); closed != 1 {
		t.Fatal("disconnect leaked module")
	}
	if _, closed := local.snapshot(); closed != 1 {
		t.Fatal("disconnect leaked local endpoint")
	}
}

func TestLocalCallPreparesBeforeAnswerWithoutOpeningMicrophone(t *testing.T) {
	b, module, local := testLocalBridge()
	defer b.Close()
	call := PublicCall{ID: "incoming", Direction: "inbound", State: CallStateRinging, LocalAudio: true}
	b.OnCallState(call)
	waitLocalTest(t, func() bool { return b.AudioSnapshot().Local.State == "ready" })
	local.mu.Lock()
	starts, reads := local.starts, local.reads
	local.mu.Unlock()
	if starts != 0 || reads != 0 {
		t.Fatal("microphone used before answer")
	}
	if n, _ := module.snapshot(); n != 0 {
		t.Fatal("speech sent before answer")
	}
	call.State = CallStateActive
	b.OnCallState(call)
	b.OnCallState(call)
	waitLocalTest(t, func() bool { return b.AudioSnapshot().Local.State == "running" })
	local.mu.Lock()
	starts = local.starts
	local.mu.Unlock()
	if starts != 1 {
		t.Fatalf("prepared device started %d times", starts)
	}
}

func TestDeclinedCallReleasesPreparedDevicesWithoutCapture(t *testing.T) {
	b, module, local := testLocalBridge()
	call := PublicCall{ID: "incoming", State: CallStateRinging, LocalAudio: true}
	b.OnCallState(call)
	waitLocalTest(t, func() bool { return b.AudioSnapshot().Local.State == "ready" })
	call.State = CallStateEnded
	b.OnCallState(call)
	b.Close()
	local.mu.Lock()
	starts, reads := local.starts, local.reads
	local.mu.Unlock()
	if starts != 0 || reads != 0 {
		t.Fatal("declined call started microphone")
	}
	if _, n := module.snapshot(); n != 1 {
		t.Fatal("prepared module leaked")
	}
	if _, n := local.snapshot(); n != 1 {
		t.Fatal("prepared local device leaked")
	}
}

func TestForwardCallPCMDoesNotInsertSilenceBetweenDeviceBatches(t *testing.T) {
	frames := [][]int16{nil, {100, -100, 32767, -32768}, nil, {200, -200}}
	var received []int16
	write := func(pcm []int16) error { received = append(received, pcm...); return nil }
	var clipped uint64
	for _, frame := range frames {
		level, err := forwardCallPCM(func(n int) ([]int16, error) {
			if n != 320 {
				t.Fatal("unexpected batch limit")
			}
			return frame, nil
		}, write)
		if err != nil {
			t.Fatal(err)
		}
		clipped += level.clipped
	}
	want := []int16{100, -100, 32767, -32768, 200, -200}
	if len(received) != len(want) {
		t.Fatalf("inserted or lost samples: %v", received)
	}
	for i, v := range want {
		if received[i] != v {
			t.Fatalf("sample changed at %d", i)
		}
	}
	if clipped != 2 {
		t.Fatalf("clipped=%d", clipped)
	}
}

func TestForwardCallPCMPropagatesDeviceFailures(t *testing.T) {
	disconnected := errors.New("disconnected")
	if _, err := forwardCallPCM(func(int) ([]int16, error) { return nil, disconnected }, func([]int16) error {
		t.Fatal("wrote after capture failed")
		return nil
	}); !errors.Is(err, disconnected) {
		t.Fatal(err)
	}
	if _, err := forwardCallPCM(func(int) ([]int16, error) { return []int16{1}, nil }, func([]int16) error {
		return disconnected
	}); !errors.Is(err, disconnected) {
		t.Fatal(err)
	}
}

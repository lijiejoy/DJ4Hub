package sip

import (
	"errors"
	"testing"
	"time"
)

func TestUSBAudioStopCancelsPendingStartup(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	endpoint := &testPCMEndpoint{}
	bridge := &usbAudioBridge{
		probe: func() (audioDevices, error) { return audioDevices{}, nil },
		open: func() (pcmEndpoint, audioDevices, error) {
			close(entered)
			<-release
			return endpoint, audioDevices{}, nil
		},
	}
	done := make(chan error, 1)
	go func() { done <- bridge.StartPCM() }()
	<-entered
	stopped := make(chan struct{})
	go func() { bridge.StopPCM(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		close(release)
		<-done
		<-stopped
		t.Fatal("hangup blocked on pending device startup")
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("cancelled audio startup must report cancellation")
	}
	if endpoint.closed != 1 {
		t.Fatal("late audio endpoint was not released")
	}
}

type testPCMEndpoint struct {
	closed    int
	readError error
	written   []int16
}

func (p *testPCMEndpoint) ReadPCM(n int) ([]int16, error) {
	return []int16{1200, -1200}, p.readError
}
func (p *testPCMEndpoint) WritePCM(pcm []int16) error {
	p.written = append(p.written, pcm...)
	return nil
}
func (p *testPCMEndpoint) Counters() pcmCounters { return pcmCounters{Captured: 320, Played: 160} }
func (p *testPCMEndpoint) Close()                { p.closed++ }

func TestUSBAudioOwnsStreamsAndReleasesOnDisconnect(t *testing.T) {
	devices := audioDevices{InputName: "module input", OutputName: "module output", InputUID: "usb-in", OutputUID: "usb-out"}
	endpoint := &testPCMEndpoint{}
	opens := 0
	bridge := &usbAudioBridge{
		probe: func() (audioDevices, error) { return devices, nil },
		open: func() (pcmEndpoint, audioDevices, error) {
			opens++
			return endpoint, devices, nil
		},
	}
	status := bridge.AudioSnapshot()
	if opens != 0 || status.Running || !status.Available || !status.PCMWired {
		t.Fatalf("idle status must discover devices without starting capture: %+v", status)
	}
	if err := bridge.StartPCM(); err != nil {
		t.Fatal(err)
	}
	if err := bridge.StartPCM(); err == nil {
		t.Fatal("second session must not share an active endpoint")
	}
	if err := bridge.WritePCM([]int16{100, -100}); err != nil {
		t.Fatal(err)
	}
	if len(endpoint.written) != 2 || endpoint.written[1] != -100 {
		t.Fatal("playback PCM did not reach endpoint")
	}
	pcm, err := bridge.ReadPCM(160)
	if err != nil || len(pcm) != 2 || pcm[0] != 1200 {
		t.Fatalf("capture PCM=%v err=%v", pcm, err)
	}
	status = bridge.AudioSnapshot()
	if !status.Running || status.CapturedSamples != 320 || status.PlayedSamples != 160 {
		t.Fatalf("running counters: %+v", status)
	}
	endpoint.readError = errors.New("device removed")
	if _, err := bridge.ReadPCM(160); err == nil {
		t.Fatal("disconnect must be reported")
	}
	if endpoint.closed != 1 {
		t.Fatalf("endpoint closed %d times", endpoint.closed)
	}
	bridge.StopPCM()
	if endpoint.closed != 1 {
		t.Fatal("stop after disconnect closed endpoint twice")
	}
	status = bridge.AudioSnapshot()
	if status.Running || status.LastError != "device removed" {
		t.Fatalf("disconnect status: %+v", status)
	}
}

func TestUSBAudioReportsMissingHardware(t *testing.T) {
	bridge := &usbAudioBridge{
		probe: func() (audioDevices, error) { return audioDevices{}, errors.New("no module") },
		open: func() (pcmEndpoint, audioDevices, error) {
			return nil, audioDevices{}, errors.New("capture permission denied")
		},
	}
	if bridge.Available() {
		t.Fatal("missing module reported available")
	}
	if err := bridge.StartPCM(); err == nil {
		t.Fatal("startup failure must be reported")
	}
	status := bridge.AudioSnapshot()
	if status.Running || status.LastError != "capture permission denied" {
		t.Fatalf("failure status: %+v", status)
	}
}

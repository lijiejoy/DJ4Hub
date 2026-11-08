package sip

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

type audioDevices struct {
	InputName  string
	OutputName string
	InputUID   string
	OutputUID  string
}

type pcmCounters struct {
	Captured       uint64
	Played         uint64
	InputNonzero   uint64
	OutputNonzero  uint64
	Dropped        uint64
	Underruns      uint64
	CaptureQueued  uint64
	PlaybackQueued uint64
}

type pcmEndpoint interface {
	ReadPCM(int) ([]int16, error)
	WritePCM([]int16) error
	Counters() pcmCounters
	Close()
}

// usbAudioBridge owns the two module streams for exactly one media session.
// Neither construction nor status inspection starts microphone capture.
type usbAudioBridge struct {
	mu         sync.Mutex
	probe      func() (audioDevices, error)
	open       func() (pcmEndpoint, audioDevices, error)
	endpoint   pcmEndpoint
	devices    audioDevices
	counts     pcmCounters
	lastError  string
	probeAt    time.Time
	probeError error
	starting   bool
	generation uint64
}

// NewUSBAudioBridge selects only verified module USB audio devices.
// It never falls back to the computer's default microphone or speakers.
func NewUSBAudioBridge() AudioBridge {
	return &usbAudioBridge{probe: probeNativeAudio, open: openNativeAudio}
}

func (b *usbAudioBridge) AudioSnapshot() AudioStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.endpoint == nil && (b.probeAt.IsZero() || time.Since(b.probeAt) > 2*time.Second) {
		b.devices, b.probeError = b.probe()
		b.probeAt = time.Now()
	}
	counts := b.counts
	if b.endpoint != nil {
		counts = b.endpoint.Counters()
	}
	st := AudioStatus{
		Mode: "coreaudio", PCMWired: true,
		Available: b.probeError == nil && b.devices.InputUID != "" && b.devices.OutputUID != "",
		Running:   b.endpoint != nil, SampleRate: 8000,
		InputDevice: b.devices.InputName, OutputDevice: b.devices.OutputName,
		InputUID: b.devices.InputUID, OutputUID: b.devices.OutputUID,
		CapturedSamples: counts.Captured, PlayedSamples: counts.Played,
		InputNonzeroSamples: counts.InputNonzero, OutputNonzeroSamples: counts.OutputNonzero,
		DroppedSamples: counts.Dropped, UnderrunSamples: counts.Underruns,
		CaptureQueuedSamples: counts.CaptureQueued, PlaybackQueuedSamples: counts.PlaybackQueued,
		LastError:            b.lastError,
		MicrophonePermission: nativeAudioAuthorization(),
	}
	switch {
	case b.endpoint != nil:
		st.Detail = fmt.Sprintf("USB audio running: %s -> RTP -> %s (8000 Hz mono)", st.InputDevice, st.OutputDevice)
	case b.probeError != nil:
		st.Detail = "USB audio unavailable: " + b.probeError.Error()
	case b.lastError != "":
		st.Detail = "USB audio stopped: " + b.lastError
	case st.MicrophonePermission != "authorized":
		st.Detail = "USB audio detected; microphone permission " + st.MicrophonePermission + " (System Settings > Privacy & Security > Microphone)"
	default:
		st.Detail = fmt.Sprintf("USB audio ready: %s / %s (8000 Hz mono)", st.InputDevice, st.OutputDevice)
	}
	return st
}

func (b *usbAudioBridge) Available() bool    { return b.AudioSnapshot().Available }
func (b *usbAudioBridge) Status() string     { return b.AudioSnapshot().Detail }
func (b *usbAudioBridge) QPCMVEnabled() bool { return false }
func (b *usbAudioBridge) Enable() error      { return b.StartPCM() }
func (b *usbAudioBridge) Disable() error     { b.StopPCM(); return nil }

func (b *usbAudioBridge) StartPCM() error {
	b.mu.Lock()
	if b.endpoint != nil || b.starting {
		b.mu.Unlock()
		return errors.New("USB audio is already in use")
	}
	b.starting = true
	b.generation++
	generation := b.generation
	b.mu.Unlock()
	endpoint, devices, err := b.open()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.starting = false
	if b.generation != generation {
		if endpoint != nil {
			endpoint.Close()
		}
		return errors.New("USB audio startup cancelled")
	}
	b.probeAt = time.Time{}
	if err != nil {
		b.lastError = err.Error()
		return err
	}
	b.endpoint, b.devices = endpoint, devices
	b.probeError, b.lastError = nil, ""
	b.counts = pcmCounters{}
	return nil
}

func (b *usbAudioBridge) stopLocked() {
	if b.endpoint != nil {
		b.counts = b.endpoint.Counters()
		b.endpoint.Close()
		b.endpoint = nil
	}
}

func (b *usbAudioBridge) StopPCM() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.generation++
	b.stopLocked()
}

func (b *usbAudioBridge) ReadPCM(n int) ([]int16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.endpoint == nil {
		return nil, errors.New("USB audio capture is not running")
	}
	pcm, err := b.endpoint.ReadPCM(n)
	if err != nil {
		b.lastError = err.Error()
		b.stopLocked()
	}
	return pcm, err
}

func (b *usbAudioBridge) WritePCM(pcm []int16) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.endpoint == nil {
		return errors.New("USB audio playback is not running")
	}
	err := b.endpoint.WritePCM(pcm)
	if err != nil {
		b.lastError = err.Error()
		b.stopLocked()
	}
	return err
}

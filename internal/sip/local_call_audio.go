package sip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type LocalAudioStatus struct {
	State            string `json:"state"`
	Input            string `json:"input_device,omitempty"`
	Output           string `json:"output_device,omitempty"`
	MicSamples       uint64 `json:"microphone_samples"`
	MicNonzero       uint64 `json:"microphone_nonzero_samples"`
	SpeakerSamples   uint64 `json:"speaker_samples"`
	SpeakerNonzero   uint64 `json:"speaker_nonzero_samples"`
	EchoCancellation bool   `json:"echo_cancellation"`
	MicrophoneMuted  bool   `json:"microphone_muted"`
	SpeakerMuted     bool   `json:"speaker_muted"`
	CaptureQueued    uint64 `json:"capture_queued_samples"`
	PlaybackQueued   uint64 `json:"playback_queued_samples"`
	Dropped          uint64 `json:"dropped_samples"`
	Underruns        uint64 `json:"underrun_samples"`
	MicForwarded     uint64 `json:"microphone_forwarded_samples"`
	RemoteForwarded  uint64 `json:"remote_forwarded_samples"`
	MicClipped       uint64 `json:"microphone_clipped_samples"`
	RemoteClipped    uint64 `json:"remote_clipped_samples"`
	MicPeak          int    `json:"microphone_peak"`
	RemotePeak       int    `json:"remote_peak"`
	Error            string `json:"error,omitempty"`
}

// CallAudioBridge keeps local web calls and SIP-client media mutually exclusive.
// Each local worker owns all its handles, including late startup cleanup.
type CallAudioBridge struct {
	*usbAudioBridge
	mu         sync.Mutex
	workerMu   sync.Mutex
	wg         sync.WaitGroup
	callID     string
	ended      map[string]bool
	cancel     context.CancelFunc
	active     chan struct{}
	activated  bool
	local      LocalAudioStatus
	openLocal  func() (pcmEndpoint, audioDevices, error)
	voice      *moduleVoiceSession
	warmModule func(context.Context) error
	permission func() error
	warmCancel context.CancelFunc
	closed     bool
	// configDir holds call-audio.json, the persisted local audio device
	// selection. It is read at each call start, so a selection applies
	// to the next call, never to a running one.
	configDir string
}

// LocalMediaFlowing reports whether the local audio pump for call id is
// actually moving PCM in either direction. "running" alone only means the
// pump started; forwarded samples prove audio flowed.
func (b *CallAudioBridge) LocalMediaFlowing(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if id == "" || id != b.callID || b.local.State != "running" {
		return false
	}
	return b.local.MicForwarded+b.local.RemoteForwarded > 0
}

// AudioDeviceSelection returns the persisted local input/output device
// selection (UID or name as stored in call-audio.json). Empty means
// "built-in default", which is what the audio layer picks when nothing
// is configured.
func (b *CallAudioBridge) AudioDeviceSelection() (input, output string) {
	data, err := os.ReadFile(filepath.Join(b.configDir, "call-audio.json"))
	if err != nil {
		return "", ""
	}
	var config struct {
		Input  string `json:"input_device"`
		Output string `json:"output_device"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return "", ""
	}
	return config.Input, config.Output
}

// SetAudioDevices persists the local input/output device choice for the
// next call. Values are a device UID or name as listed by
// ListLocalAudioDevices; empty resets that side to the built-in default.
// A non-empty value that matches no listed device is rejected, so a
// stale selection can never silently fall back to another device. The
// choice never touches a running call.
func (b *CallAudioBridge) SetAudioDevices(input, output string) error {
	if b.configDir == "" {
		return errors.New("no config directory for audio device selection")
	}
	input = strings.TrimSpace(input)
	output = strings.TrimSpace(output)
	if input != "" || output != "" {
		devices, err := ListLocalAudioDevices()
		if err != nil {
			return fmt.Errorf("cannot list audio devices: %w", err)
		}
		if input != "" && !audioDeviceMatches(devices, input, true) {
			return fmt.Errorf("unknown input device %q", input)
		}
		if output != "" && !audioDeviceMatches(devices, output, false) {
			return fmt.Errorf("unknown output device %q", output)
		}
	}
	var existing struct {
		Enabled *bool  `json:"enabled,omitempty"`
		Input   string `json:"input_device,omitempty"`
		Output  string `json:"output_device,omitempty"`
	}
	data, err := os.ReadFile(filepath.Join(b.configDir, "call-audio.json"))
	if err == nil {
		_ = json.Unmarshal(data, &existing)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	existing.Input, existing.Output = input, output
	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(b.configDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(b.configDir, "call-audio.json"), out, 0o600)
}

func audioDeviceMatches(devices []AudioDevice, wanted string, wantInput bool) bool {
	for _, d := range devices {
		if wantInput && !d.HasInput || !wantInput && !d.HasOutput {
			continue
		}
		if d.UID == wanted || d.Name == wanted {
			return true
		}
	}
	return false
}

func NewCallAudioBridge(configDir string) *CallAudioBridge {
	module := NewUSBAudioBridge().(*usbAudioBridge)
	voice := &moduleVoiceSession{}
	open := module.open
	module.open = func() (pcmEndpoint, audioDevices, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := voice.prepare(ctx, filepath.Join(configDir, "module-voice"), true); err != nil {
			return nil, audioDevices{}, err
		}
		return open()
	}
	bridge := &CallAudioBridge{usbAudioBridge: module, voice: voice, local: LocalAudioStatus{State: "idle"}, configDir: configDir}
	bridge.permission = ensureMicrophonePermission
	bridge.warmModule = func(ctx context.Context) error {
		return voice.prepare(ctx, filepath.Join(configDir, "module-voice"), false)
	}
	bridge.openLocal = func() (pcmEndpoint, audioDevices, error) {
		var config struct {
			Enabled *bool  `json:"enabled"`
			Input   string `json:"input_device"`
			Output  string `json:"output_device"`
		}
		data, err := os.ReadFile(filepath.Join(configDir, "call-audio.json"))
		if err != nil && !os.IsNotExist(err) {
			return nil, audioDevices{}, err
		}
		if err == nil {
			if err := json.Unmarshal(data, &config); err != nil {
				return nil, audioDevices{}, err
			}
		}
		if config.Enabled != nil && !*config.Enabled {
			return nil, audioDevices{}, errors.New("local call audio is disabled")
		}
		return prepareLocalAudio(config.Input, config.Output)
	}
	return bridge
}

// Warmup requests permission and prepares the module without opening host audio.
func (b *CallAudioBridge) Warmup() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.warmCancel != nil || b.warmModule == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	b.warmCancel = cancel
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer cancel()
		if b.permission != nil {
			if err := b.permission(); err != nil {
				log.Printf("[sip] microphone authorization: %v", err)
			}
		}
		start := time.Now()
		if err := b.warmModule(ctx); err != nil {
			if ctx.Err() == nil {
				log.Printf("[sip] module voice warmup failed; next call will retry")
			}
			return
		}
		log.Printf("[sip] module voice warmup ready in %s; host microphone remains stopped", time.Since(start).Round(time.Millisecond))
	}()
}

func (b *CallAudioBridge) OnCallState(call PublicCall) {
	if !call.LocalAudio {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	live := call.State == CallStateActive || call.State == CallStateDialing || call.State == CallStateRinging
	if !live {
		if call.State == CallStateEnded || call.State == CallStateFailed || call.State == CallStateMissed {
			if b.ended == nil {
				b.ended = make(map[string]bool)
			}
			b.ended[call.ID] = true
		}
		if call.ID == b.callID && b.cancel != nil {
			b.cancel()
			b.local.State = "stopping"
		}
		return
	}
	if b.ended[call.ID] {
		return
	}
	if call.ID == b.callID {
		if call.State == CallStateActive && !b.activated {
			b.activated = true
			close(b.active)
		}
		return
	}
	if b.cancel != nil {
		b.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.callID, b.cancel = call.ID, cancel
	b.active, b.activated = make(chan struct{}), call.State == CallStateActive
	if b.activated {
		close(b.active)
	}
	b.local = LocalAudioStatus{State: "starting"}
	b.wg.Add(1)
	go b.runLocal(ctx, call.ID, b.active)
}

func (b *CallAudioBridge) runLocal(ctx context.Context, id string, active <-chan struct{}) {
	defer b.wg.Done()
	b.workerMu.Lock()
	defer b.workerMu.Unlock()
	var local pcmEndpoint
	var devices audioDevices
	var failure error
	started := false
	defer func() {
		var counts pcmCounters
		if local != nil {
			counts = local.Counters()
			local.Close()
		}
		if started {
			b.usbAudioBridge.StopPCM()
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.callID != id {
			return
		}
		b.setLocalCounts(counts)
		b.local.State = "idle"
		if failure != nil && ctx.Err() == nil {
			b.local.State, b.local.Error = "failed", failure.Error()
			log.Printf("[sip] local audio startup/stream failed: %v", failure)
		}
	}()
	if ctx.Err() != nil {
		return
	}
	if failure = b.usbAudioBridge.StartPCM(); failure != nil {
		return
	}
	started = true
	if ctx.Err() != nil {
		return
	}
	local, devices, failure = b.openLocal()
	if failure != nil {
		return
	}
	if ctx.Err() != nil {
		return
	}
	b.mu.Lock()
	if b.callID == id {
		b.local.State, b.local.Input, b.local.Output = "ready", devices.InputName, devices.OutputName
	}
	b.mu.Unlock()
	select {
	case <-ctx.Done():
		return
	case <-active:
	}
	if ctx.Err() != nil {
		return
	}
	// Discard pre-answer module audio. Prepared Mac units have not started
	// callbacks and cannot capture the microphone before answer.
	if _, failure = b.usbAudioBridge.ReadPCM(4096); failure != nil {
		return
	}
	if endpoint, ok := local.(interface{ Start() error }); ok {
		if failure = endpoint.Start(); failure != nil {
			return
		}
	}
	b.mu.Lock()
	if b.callID == id {
		b.local.State, b.local.Input, b.local.Output = "running", devices.InputName, devices.OutputName
		b.local.EchoCancellation = true
	}
	b.mu.Unlock()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		b.mu.Lock()
		microphoneMuted, speakerMuted := b.local.MicrophoneMuted, b.local.SpeakerMuted
		b.mu.Unlock()
		mic, err := forwardControlledCallPCM(local.ReadPCM, b.usbAudioBridge.WritePCM, microphoneMuted)
		failure = err
		if failure != nil {
			return
		}
		remote, err := forwardControlledCallPCM(b.usbAudioBridge.ReadPCM, local.WritePCM, speakerMuted)
		failure = err
		if failure != nil {
			return
		}
		counts := local.Counters()
		b.mu.Lock()
		if b.callID == id {
			b.setLocalCounts(counts)
			b.local.MicForwarded += mic.samples
			b.local.RemoteForwarded += remote.samples
			b.local.MicClipped += mic.clipped
			b.local.RemoteClipped += remote.clipped
			b.local.MicPeak, b.local.RemotePeak = mic.peak, remote.peak
		}
		b.mu.Unlock()
	}
}

type callPCMLevel struct {
	samples, clipped uint64
	peak             int
}

// Device callbacks own the clocks. Drain their actual batches instead of
// synthesizing silence on an independent host clock between hardware bursts.
func forwardCallPCM(read func(int) ([]int16, error), write func([]int16) error) (callPCMLevel, error) {
	return forwardControlledCallPCM(read, write, false)
}

func forwardControlledCallPCM(read func(int) ([]int16, error), write func([]int16) error, muted bool) (callPCMLevel, error) {
	var level callPCMLevel
	pcm, err := read(320)
	if err != nil || len(pcm) == 0 {
		return level, err
	}
	if muted {
		clear(pcm)
	}
	level.samples = uint64(len(pcm))
	for _, sample := range pcm {
		v := int(sample)
		if v < 0 {
			v = -v
		}
		if v > level.peak {
			level.peak = v
		}
		if v >= 32760 {
			level.clipped++
		}
	}
	return level, write(pcm)
}

func (b *CallAudioBridge) setLocalCounts(c pcmCounters) {
	b.local.MicSamples, b.local.MicNonzero = c.Captured, c.InputNonzero
	b.local.SpeakerSamples, b.local.SpeakerNonzero = c.Played, c.OutputNonzero
	b.local.CaptureQueued, b.local.PlaybackQueued = c.CaptureQueued, c.PlaybackQueued
	b.local.Dropped, b.local.Underruns = c.Dropped, c.Underruns
}

func (b *CallAudioBridge) Close() {
	b.mu.Lock()
	b.closed = true
	if b.warmCancel != nil {
		b.warmCancel()
	}
	if b.cancel != nil {
		b.cancel()
	}
	b.mu.Unlock()
	b.wg.Wait()
	if b.voice != nil {
		b.voice.Close()
	}
}

func (b *CallAudioBridge) AudioSnapshot() AudioStatus {
	status := b.usbAudioBridge.AudioSnapshot()
	b.mu.Lock()
	local := b.local
	b.mu.Unlock()
	status.Local = &local
	if local.State == "running" {
		status.Mode = "local-coreaudio"
		status.Detail = fmt.Sprintf("本机通话音频运行中：%s → 电话 → %s", local.Input, local.Output)
	} else if local.State == "starting" {
		status.Detail = "正在准备通话音频"
	} else if local.State == "ready" {
		status.Detail = "通话音频已就绪，等待接通"
	} else if local.State == "failed" {
		status.Detail = "本机通话音频失败：" + local.Error
		status.LastError = local.Error
	}
	return status
}

func (b *CallAudioBridge) Status() string { return b.AudioSnapshot().Detail }

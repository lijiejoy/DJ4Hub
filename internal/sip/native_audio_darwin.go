//go:build darwin && cgo

package sip

/*
#cgo CFLAGS: -std=c11
#cgo LDFLAGS: -framework CoreAudio -framework AudioToolbox -framework AudioUnit -framework CoreFoundation -framework IOKit -framework AVFoundation -framework Foundation -Wl,-sectcreate,__TEXT,__info_plist,${SRCDIR}/audio-info.plist
#include "native_audio.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"
)

func nativeDevices(info *C.dj_audio_devices) audioDevices {
	return audioDevices{
		InputName: C.GoString(&info.input_name[0]), OutputName: C.GoString(&info.output_name[0]),
		InputUID: C.GoString(&info.input_uid[0]), OutputUID: C.GoString(&info.output_uid[0]),
	}
}

// AudioDevice is one host audio device a local call may use. UID is the
// stable selector stored in call-audio.json; Name is for display only.
// The module's own USB audio engine is never listed.
type AudioDevice struct {
	Name      string `json:"name"`
	UID       string `json:"uid"`
	HasInput  bool   `json:"has_input"`
	HasOutput bool   `json:"has_output"`
	Transport int    `json:"transport,omitempty"`
}

// ListLocalAudioDevices enumerates host microphones/speakers via CoreAudio.
// It reads device properties only: no stream is opened and no permission
// prompt is triggered.
func ListLocalAudioDevices() ([]AudioDevice, error) {
	const capacity = 32
	devices := make([]C.dj_local_audio_device, capacity)
	var count C.int
	var message [512]C.char
	if C.dj_local_audio_list(&devices[0], C.int(capacity), &count, &message[0], C.int(len(message))) != 0 {
		return nil, errors.New(C.GoString(&message[0]))
	}
	out := make([]AudioDevice, 0, int(count))
	for i := 0; i < int(count); i++ {
		out = append(out, AudioDevice{
			Name:      C.GoString(&devices[i].name[0]),
			UID:       C.GoString(&devices[i].uid[0]),
			HasInput:  devices[i].has_input != 0,
			HasOutput: devices[i].has_output != 0,
			Transport: int(devices[i].transport),
		})
	}
	return out, nil
}

func probeNativeAudio() (audioDevices, error) {
	var info C.dj_audio_devices
	var message [512]C.char
	if C.dj_audio_probe(&info, &message[0], C.int(len(message))) != 0 {
		return audioDevices{}, errors.New(C.GoString(&message[0]))
	}
	return nativeDevices(&info), nil
}

func nativeControls(name, uid string, controls *C.dj_audio_controls) audioDeviceInspection {
	device := audioDeviceInspection{
		Name: name, UID: uid, SampleRate: float64(controls.sample_rate),
		Channels: uint32(controls.channels), Bits: uint32(controls.bits),
		BytesPerFrame: uint32(controls.bytes_per_frame), FormatFlags: uint32(controls.format_flags),
		BufferFrames: uint32(controls.buffer_frames), BufferStatus: int32(controls.buffer_status),
	}
	// Channel zero is the master control; channel one is the mono channel.
	for i := 0; i < 2; i++ {
		channel := audioChannelControl{
			Channel: i, MuteStatus: int32(controls.mute_status[i]),
			VolumeStatus: int32(controls.volume_status[i]),
		}
		if channel.MuteStatus == 0 {
			mute := controls.mute[i] != 0
			channel.Mute = &mute
		}
		if channel.VolumeStatus == 0 {
			volume := float32(controls.volume[i])
			channel.Volume = &volume
		}
		device.Controls = append(device.Controls, channel)
	}
	return device
}

func inspectNativeAudio() (audioInspection, error) {
	var info C.dj_audio_devices
	var input, output C.dj_audio_controls
	var message [512]C.char
	if C.dj_audio_inspect(&info, &input, &output, &message[0], C.int(len(message))) != 0 {
		return audioInspection{}, errors.New(C.GoString(&message[0]))
	}
	devices := nativeDevices(&info)
	return audioInspection{
		Input:                nativeControls(devices.InputName, devices.InputUID, &input),
		Output:               nativeControls(devices.OutputName, devices.OutputUID, &output),
		MicrophonePermission: nativeAudioAuthorization(),
	}, nil
}

type nativePCM struct {
	handle    *C.dj_audio
	checkedAt time.Time
}

func nativeAudioAuthorization() string {
	switch int(C.dj_audio_authorization()) {
	case 0:
		return "not_determined"
	case 1:
		return "restricted"
	case 2:
		return "denied"
	case 3:
		return "authorized"
	default:
		return "unknown"
	}
}

var nativeOpenMu sync.Mutex

func nativeRequestAudioPermission() error {
	var message [512]C.char
	if C.dj_audio_permission(&message[0], C.int(len(message))) != 0 {
		return errors.New(C.GoString(&message[0]))
	}
	return nil
}

func openNativeAudio() (pcmEndpoint, audioDevices, error) {
	if err := ensureMicrophonePermission(); err != nil {
		return nil, audioDevices{}, err
	}
	if !nativeOpenMu.TryLock() {
		return nil, audioDevices{}, errors.New("a previous CoreAudio stream startup is still pending")
	}
	type result struct {
		endpoint pcmEndpoint
		devices  audioDevices
		err      error
	}
	done := make(chan result)
	cancel := make(chan struct{})
	go func() {
		defer nativeOpenMu.Unlock()
		var info C.dj_audio_devices
		var detail [512]C.char
		handle := C.dj_audio_open(&info, &detail[0], C.int(len(detail)))
		out := result{devices: nativeDevices(&info)}
		if handle == nil {
			out.err = errors.New(C.GoString(&detail[0]))
		} else {
			out.endpoint = &nativePCM{handle: handle}
		}
		select {
		case done <- out:
		case <-cancel:
			if out.endpoint != nil {
				out.endpoint.Close()
			}
		}
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case out := <-done:
		return out.endpoint, out.devices, out.err
	case <-timer.C:
		close(cancel)
		return nil, audioDevices{}, errors.New("CoreAudio stream startup timed out; check microphone permission and reconnect the module before retrying")
	}
}

func (p *nativePCM) checkAlive() error {
	if p.checkedAt.IsZero() || time.Since(p.checkedAt) >= time.Second {
		p.checkedAt = time.Now()
		if C.dj_audio_alive(p.handle) == 0 {
			return errors.New("module USB audio disconnected or stream format changed")
		}
	}
	return nil
}

func (p *nativePCM) ReadPCM(n int) ([]int16, error) {
	if err := p.checkAlive(); err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, nil
	}
	pcm := make([]int16, n)
	read := int(C.dj_audio_read(p.handle, (*C.int16_t)(unsafe.Pointer(&pcm[0])), C.int(n)))
	return pcm[:read], nil
}

func (p *nativePCM) WritePCM(pcm []int16) error {
	if err := p.checkAlive(); err != nil {
		return err
	}
	if len(pcm) > 0 {
		C.dj_audio_write(p.handle, (*C.int16_t)(unsafe.Pointer(&pcm[0])), C.int(len(pcm)))
	}
	return nil
}

func (p *nativePCM) Counters() pcmCounters {
	var counts C.dj_audio_counts
	C.dj_audio_counters(p.handle, &counts)
	return pcmCounters{
		Captured: uint64(counts.captured), Played: uint64(counts.played),
		InputNonzero: uint64(counts.input_nonzero), OutputNonzero: uint64(counts.output_nonzero),
		Dropped: uint64(counts.dropped), Underruns: uint64(counts.underruns),
		CaptureQueued: uint64(counts.capture_queued), PlaybackQueued: uint64(counts.playback_queued),
	}
}

func (p *nativePCM) Close() {
	if p.handle != nil {
		C.dj_audio_close(p.handle)
		p.handle = nil
	}
}

type localPCM struct{ handle *C.dj_local_audio }

var localOpenMu sync.Mutex

func openLocalAudio(input, output string) (pcmEndpoint, audioDevices, error) {
	return openLocalAudioMode(input, output, true)
}

func prepareLocalAudio(input, output string) (pcmEndpoint, audioDevices, error) {
	return openLocalAudioMode(input, output, false)
}

func openLocalAudioMode(input, output string, start bool) (pcmEndpoint, audioDevices, error) {
	if err := ensureMicrophonePermission(); err != nil {
		return nil, audioDevices{}, err
	}
	if !localOpenMu.TryLock() {
		return nil, audioDevices{}, errors.New("a previous Mac audio startup is still pending")
	}
	type result struct {
		endpoint pcmEndpoint
		devices  audioDevices
		err      error
	}
	done, cancelled := make(chan result), make(chan struct{})
	go func() {
		defer localOpenMu.Unlock()
		endpoint, devices, err := openLocalAudioUnbounded(input, output, start)
		select {
		case done <- result{endpoint, devices, err}:
		case <-cancelled:
			if endpoint != nil {
				endpoint.Close()
			}
		}
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.endpoint, r.devices, r.err
	case <-timer.C:
		close(cancelled)
		return nil, audioDevices{}, errors.New("Mac microphone/speaker startup timed out")
	}
}

func openLocalAudioUnbounded(input, output string, start bool) (pcmEndpoint, audioDevices, error) {
	var message [512]C.char
	in, out := C.CString(input), C.CString(output)
	defer C.free(unsafe.Pointer(in))
	defer C.free(unsafe.Pointer(out))
	var info C.dj_audio_devices
	startNow := C.int(0)
	if start {
		startNow = 1
	}
	handle := C.dj_local_audio_open(in, out, startNow, &info, &message[0], C.int(len(message)))
	if handle == nil {
		return nil, audioDevices{}, errors.New(C.GoString(&message[0]))
	}
	return &localPCM{handle}, nativeDevices(&info), nil
}

func (p *localPCM) Start() error {
	if status := C.dj_local_audio_start(p.handle); status != 0 {
		return fmt.Errorf("start prepared Mac voice audio: OSStatus %d", int(status))
	}
	return nil
}

func (p *localPCM) ReadPCM(n int) ([]int16, error) {
	if C.dj_local_audio_alive(p.handle) == 0 {
		return nil, errors.New("Mac audio device disconnected or queue failed")
	}
	if n <= 0 {
		return nil, nil
	}
	pcm := make([]int16, n)
	count := int(C.dj_local_audio_read(p.handle, (*C.int16_t)(unsafe.Pointer(&pcm[0])), C.int(n)))
	return pcm[:count], nil
}
func (p *localPCM) WritePCM(pcm []int16) error {
	if len(pcm) > 0 {
		C.dj_local_audio_write(p.handle, (*C.int16_t)(unsafe.Pointer(&pcm[0])), C.int(len(pcm)))
	}
	return nil
}
func (p *localPCM) Counters() pcmCounters {
	var c C.dj_audio_counts
	C.dj_local_audio_counters(p.handle, &c)
	return pcmCounters{
		Captured: uint64(c.captured), Played: uint64(c.played),
		InputNonzero: uint64(c.input_nonzero), OutputNonzero: uint64(c.output_nonzero),
		Dropped: uint64(c.dropped), Underruns: uint64(c.underruns),
		CaptureQueued: uint64(c.capture_queued), PlaybackQueued: uint64(c.playback_queued),
	}
}
func (p *localPCM) Close() {
	if p.handle != nil {
		C.dj_local_audio_close(p.handle)
		p.handle = nil
	}
}

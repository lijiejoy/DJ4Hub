//go:build !darwin || !cgo

package sip

import "errors"

// AudioDevice mirrors the darwin+cgo definition in native_audio_darwin.go.
// It is duplicated here (rather than shared) because this file builds when
// cgo is off, where the CoreAudio-backed definition is unavailable. The
// fields must stay in sync: ListLocalAudioDevices returns it on both paths.
type AudioDevice struct {
	Name      string `json:"name"`
	UID       string `json:"uid"`
	HasInput  bool   `json:"has_input"`
	HasOutput bool   `json:"has_output"`
	Transport int    `json:"transport,omitempty"`
}

func nativeAudioAuthorization() string { return "unsupported" }

func nativeRequestAudioPermission() error {
	return errors.New("microphone authorization requires macOS with cgo enabled")
}

func ListLocalAudioDevices() ([]AudioDevice, error) {
	return nil, errors.New("listing local audio devices requires macOS with cgo enabled")
}

func probeNativeAudio() (audioDevices, error) {
	return audioDevices{}, errors.New("module USB audio requires macOS with cgo enabled")
}

func inspectNativeAudio() (audioInspection, error) {
	_, err := probeNativeAudio()
	return audioInspection{}, err
}

func openNativeAudio() (pcmEndpoint, audioDevices, error) {
	devices, err := probeNativeAudio()
	return nil, devices, err
}

func openLocalAudio(input, output string) (pcmEndpoint, audioDevices, error) {
	return openNativeAudio()
}

func prepareLocalAudio(input, output string) (pcmEndpoint, audioDevices, error) {
	return openNativeAudio()
}

//go:build !darwin || !cgo

package sip

import "errors"

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

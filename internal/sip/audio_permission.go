package sip

import (
	"fmt"
	"sync"
)

// The OS owns the persistent grant. Only prompt attempts are cached here.
type audioPermissionGate struct {
	mu        sync.Mutex
	attempted bool
}

func (g *audioPermissionGate) ensure(status func() string, request func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	state := status()
	if state == "authorized" {
		return nil
	}
	if state == "not_determined" && !g.attempted {
		g.attempted = true
		if err := request(); err != nil {
			return err
		}
		state = status()
		if state == "authorized" {
			return nil
		}
	}
	return fmt.Errorf("microphone permission is %s; allow DJ 4G Hub or its launching app in System Settings > Privacy & Security > Microphone", state)
}

var microphonePermission audioPermissionGate

func ensureMicrophonePermission() error {
	return microphonePermission.ensure(nativeAudioAuthorization, nativeRequestAudioPermission)
}

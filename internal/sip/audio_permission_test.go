package sip

import (
	"errors"
	"sync"
	"testing"
)

func TestMicrophonePermissionRemembersPromptButChecksRevocation(t *testing.T) {
	gate := &audioPermissionGate{}
	state, prompts := "not_determined", 0
	status := func() string { return state }
	request := func() error { prompts++; state = "authorized"; return nil }
	for i := 0; i < 3; i++ {
		if err := gate.ensure(status, request); err != nil {
			t.Fatal(err)
		}
	}
	if prompts != 1 {
		t.Fatalf("permission requested %d times", prompts)
	}
	state = "denied"
	if err := gate.ensure(status, request); err == nil || prompts != 1 {
		t.Fatal("revoked permission was cached or requested again")
	}
	state = "authorized"
	if err := gate.ensure(status, request); err != nil || prompts != 1 {
		t.Fatal("system settings grant was not respected")
	}
}

func TestMicrophonePermissionConcurrentRequestsPromptOnce(t *testing.T) {
	gate := &audioPermissionGate{}
	state, prompts := "not_determined", 0
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := gate.ensure(func() string { return state }, func() error {
				prompts++
				state = "authorized"
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if prompts != 1 {
		t.Fatalf("concurrent startup requested permission %d times", prompts)
	}
}

func TestMicrophonePermissionTimeoutDoesNotPromptOnEveryCall(t *testing.T) {
	gate := &audioPermissionGate{}
	prompts := 0
	for i := 0; i < 3; i++ {
		err := gate.ensure(func() string { return "not_determined" }, func() error {
			prompts++
			return errors.New("permission request timed out")
		})
		if err == nil {
			t.Fatal("unanswered permission treated as granted")
		}
	}
	if prompts != 1 {
		t.Fatalf("unanswered permission prompted %d times", prompts)
	}
}

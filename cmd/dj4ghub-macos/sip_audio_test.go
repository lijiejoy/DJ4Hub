package main

import (
	"strings"
	"testing"

	"github.com/WongLoki/DJ4Hub/internal/sip"
)

func TestSIPAudioTracksUSBReconnect(t *testing.T) {
	instance := &app{}
	bridge := &sip.QPCMVBridge{Modem: &appAudio{a: instance}}
	if bridge.Available() {
		t.Fatal("audio adapter must not report an attached modem before USB discovery")
	}
	if !strings.Contains(bridge.Status(), "no modem attached") {
		t.Fatal("disconnected audio status must report the missing modem")
	}

	// An empty USB handle represents discovery only; no device I/O is used.
	instance.usbAT = &usbAT{}
	if !bridge.Available() {
		t.Fatal("audio adapter did not notice a modem connected after startup")
	}
	instance.usbAT = nil
	if bridge.Available() {
		t.Fatal("audio adapter did not notice the modem disconnecting")
	}
}

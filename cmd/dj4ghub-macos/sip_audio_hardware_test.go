//go:build darwin && cgo

package main

import (
	"os"
	"testing"
	"time"
)

// Opt-in diagnostics only: the normal suite never claims USB interfaces.
func TestAlternateATVoiceCapability(t *testing.T) {
	if os.Getenv("DJ4HUB_TEST_ALT_AT_AUDIO") != "1" {
		t.Skip("set DJ4HUB_TEST_ALT_AT_AUDIO=1 to inspect an available alternate USB AT interface")
	}
	device, err := openDJIUSBAT()
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()
	t.Log(device.Description())
	for _, command := range []string{"AT+QPCMV=?", "AT+QPCMV?", "AT+QAUDMOD?", "AT+QDAI?"} {
		response, err := device.Command(command, 2*time.Second)
		t.Logf("%s: response=%q err=%v", command, response, err)
		if err != nil {
			t.Fatal(err)
		}
	}
}

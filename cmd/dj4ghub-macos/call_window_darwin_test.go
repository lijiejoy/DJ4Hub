//go:build darwin && cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeCallPanelTransitions(t *testing.T) {
	if os.Getenv("DJ4HUB_TEST_NATIVE_PANEL") != "1" {
		t.Skip("set DJ4HUB_TEST_NATIVE_PANEL=1 to open a native test window without dialing")
	}
	binary := filepath.Join(t.TempDir(), "call-panel-test")
	build := exec.Command("clang", "-fobjc-arc", "-framework", "AppKit", "-framework", "QuartzCore",
		"-o", binary, "testdata/call_window_harness.m")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("native harness build: %v\n%s", err, output)
	}
	output, err := exec.Command(binary).CombinedOutput()
	if err != nil {
		t.Fatalf("native transitions: %v\n%s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "{") {
			var action callPanelAction
			if err := json.Unmarshal([]byte(line), &action); err != nil {
				t.Fatalf("native action cannot be read by service: %v\n%s", err, line)
			}
		}
	}
	t.Log(string(output))
}

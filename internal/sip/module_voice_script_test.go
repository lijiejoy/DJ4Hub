package sip

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestModuleRouteHealthCheckRejectsStaleOrUnhealthyRoute(t *testing.T) {
	for _, scenario := range []string{"ready", "stale process", "stopped PCM", "USB disabled", "missing calibration"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			write := func(path, value string, mode os.FileMode) {
				t.Helper()
				path = filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(value), mode); err != nil {
					t.Fatal(err)
				}
			}
			write("bin/id", "#!/bin/sh\nprintf '0\\n'\n", 0700)
			write("bin/uname", "#!/bin/sh\ncase \"$1\" in -r) echo 3.18.44;; -m) echo armv7l;; *) exit 1;; esac\n", 0700)
			hashScript := "#!/bin/sh\ncase \"$1\" in\n"
			for _, asset := range voiceArtifacts {
				hashScript += "*" + asset.name + ") echo '" + asset.hash + " file';;\n"
			}
			write("bin/sha256sum", hashScript+"*) exit 1;;\nesac\n", 0700)
			for _, command := range []string{"chmod", "insmod", "nohup"} {
				write("bin/"+command, "#!/bin/sh\necho unexpected-side-effect >&2\nexit 99\n", 0700)
			}
			write("run/dj4hub-voice-route.pid", "123 500\n", 0600)
			write("run/dj4hub-voice-calibration.pid", "124 600\n", 0600)
			write("proc/123/stat", strings.Repeat("0 ", 21)+"500\n", 0600)
			write("proc/124/stat", strings.Repeat("0 ", 21)+"600\n", 0600)
			write("proc/123/cmdline", filepath.Join(root, "runtime/mavo-pcm-bridge.armv7")+"\x00--voice-route-session\x00", 0600)
			write("proc/124/cmdline", "/usr/bin/alsaucm_test\x00", 0600)
			write("run/dj4hub-voice-route.log", "VoLTE route session active on hw:0,4\n", 0600)
			write("run/dj4hub-voice-calibration.log", "ACDB -> Sent VocProc Cal!\n", 0600)
			write("sys/class/android_usb/android0/idVendor", "2ca3\n", 0600)
			write("sys/class/android_usb/android0/idProduct", "4006\n", 0600)
			write("sys/class/android_usb/f_audio/audio_enable", "1\n", 0600)
			write("proc/asound/card0/pcm4p/sub0/status", "state: RUNNING\n", 0600)
			write("proc/asound/card0/pcm4c/sub0/status", "state: RUNNING\n", 0600)
			switch scenario {
			case "stale process":
				write("proc/123/stat", strings.Repeat("0 ", 21)+"999\n", 0600)
			case "stopped PCM":
				write("proc/asound/card0/pcm4c/sub0/status", "state: SETUP\n", 0600)
			case "USB disabled":
				write("sys/class/android_usb/f_audio/audio_enable", "0\n", 0600)
			case "missing calibration":
				write("run/dj4hub-voice-calibration.log", "", 0600)
			}
			script := strings.NewReplacer(
				"/tmp/dj4hub-voice", filepath.Join(root, "runtime"),
				"/proc/", filepath.Join(root, "proc")+"/",
				"/run/", filepath.Join(root, "run")+"/",
				"/sys/", filepath.Join(root, "sys")+"/",
			).Replace(string(voicePrepareScript))
			modes := []string{"--check-ready"}
			if scenario != "ready" {
				modes = append(modes, "--rotate-ready")
			}
			for _, mode := range modes {
				cmd := exec.Command("sh", "-s", "--", mode)
				cmd.Stdin = strings.NewReader(script)
				cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"))
				output, err := cmd.CombinedOutput()
				ready := err == nil && strings.Contains(string(output), "DJ4HUB_VOICE_ROUTE_READY")
				if ready != (scenario == "ready") || strings.Contains(string(output), "unexpected-side-effect") {
					t.Fatalf("mode=%s ready=%v err=%v output=%s", mode, ready, err, output)
				}
			}
		})
	}
}

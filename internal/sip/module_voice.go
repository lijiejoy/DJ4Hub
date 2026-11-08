package sip

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const voiceSource = "https://raw.githubusercontent.com/moluncn/mavo/0443dfdaf8aec086fd76ba2ee9152fd908114524/Resources/ModuleVoice/"

var voiceArtifacts = []struct{ name, hash string }{
	{"qdc507_aprv3.ko", "3d82d3dec4f1e323201bba87156df9d41438e08314097353f2607f9117211d4a"},
	{"qdc507_voice.ko", "ed3821682d5309969a01c764192c83feff9669c61ef237c69475cd1619cf296c"},
	{"mavo-pcm-bridge.armv7", "88d47c15e61d1428a59c821fed804c2e6490e82859a085062f21966b58d167fc"},
}

//go:embed module_voice_prepare.sh
var voicePrepareScript []byte

// InstallModuleVoice downloads only pinned, hash-verified runtime artifacts.
// They stay in the user's private cache and are not bundled with DJ4Hub.
func InstallModuleVoice(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	for _, asset := range voiceArtifacts {
		path := filepath.Join(dir, asset.name)
		if data, err := os.ReadFile(path); err == nil && fmt.Sprintf("%x", sha256.Sum256(data)) == asset.hash {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, voiceSource+asset.name, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
		resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if resp.StatusCode != http.StatusOK || fmt.Sprintf("%x", sha256.Sum256(data)) != asset.hash {
			return fmt.Errorf("module runtime %s failed download/hash verification", asset.name)
		}
		file, err := os.CreateTemp(dir, ".runtime-")
		if err != nil {
			return err
		}
		name := file.Name()
		_, err = file.Write(data)
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(name, path)
		}
		if err != nil {
			os.Remove(name)
			return err
		}
	}
	return nil
}

// A serial-less ADB device must match the USB location of the verified audio
// endpoint, rather than simply being the first attached Android device.
func moduleTransport(list, uid string) (string, error) {
	parts := strings.Split(uid, ":")
	if len(parts) < 3 || parts[0] != "AppleUSBAudioEngine" {
		return "", errors.New("invalid verified module audio UID")
	}
	location, err := strconv.ParseUint(parts[len(parts)-2], 16, 32)
	if err != nil {
		return "", err
	}
	want := "usb:" + strconv.FormatUint(location, 10) + "X"
	found := ""
	for _, line := range strings.Split(list, "\n") {
		ready, match, id := false, false, ""
		for _, field := range strings.Fields(line) {
			ready = ready || field == "device"
			match = match || field == want
			if strings.HasPrefix(field, "transport_id:") {
				id = strings.TrimPrefix(field, "transport_id:")
			}
		}
		if !ready || !match {
			continue
		}
		if n, err := strconv.ParseUint(id, 10, 32); err != nil || n == 0 || found != "" {
			return "", errors.New("invalid or ambiguous module ADB transport")
		}
		found = id
	}
	if found == "" {
		return "", errors.New("no ready ADB device matches the verified module USB location")
	}
	return found, nil
}

var voiceRuntimeMu sync.Mutex

type moduleVoiceSession struct {
	adb, port string
	env       []string
}

func (s *moduleVoiceSession) command(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, s.adb, append([]string{"-P", s.port}, args...)...)
	cmd.Env = s.env
	cmd.WaitDelay = time.Second
	data, err := cmd.CombinedOutput()
	if err != nil {
		return string(data), fmt.Errorf("module voice command: %w (%s)", err, strings.TrimSpace(string(data)))
	}
	return string(data), nil
}

func (s *moduleVoiceSession) start(ctx context.Context) error {
	if s.port != "" {
		return nil
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.port = strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	listener.Close()
	s.env = nil
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "ADB_SERVER_SOCKET=") && !strings.HasPrefix(item, "ANDROID_SERIAL=") &&
			!strings.HasPrefix(item, "ADB_LIBUSB=") {
			s.env = append(s.env, item)
		}
	}
	s.env = append(s.env, "ADB_LIBUSB=0")
	_, err = s.command(ctx, "start-server")
	return err
}

func (s *moduleVoiceSession) closeLocked() {
	if s.port == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s.command(ctx, "kill-server")
	s.port = ""
}

func (s *moduleVoiceSession) Close() {
	voiceRuntimeMu.Lock()
	defer voiceRuntimeMu.Unlock()
	s.closeLocked()
}

// PrepareModuleVoice creates a private legacy-USB ADB server, pins it to the
// physical module, and prepares a temporary module-side audio route. It never
// unlocks debug access, changes USB layout, writes firmware, or hot-unloads.
func PrepareModuleVoice(ctx context.Context, dir string, rotate bool) error {
	session := &moduleVoiceSession{}
	defer session.Close()
	return session.prepare(ctx, dir, rotate)
}

func (session *moduleVoiceSession) prepare(ctx context.Context, dir string, rotate bool) (failure error) {
	voiceRuntimeMu.Lock()
	defer voiceRuntimeMu.Unlock()
	defer func() {
		if failure != nil {
			session.closeLocked()
		}
	}()
	devices, err := probeNativeAudio()
	if err != nil {
		return err
	}
	paths := make([]string, 0, 3)
	for _, asset := range voiceArtifacts {
		path := filepath.Join(dir, asset.name)
		data, err := os.ReadFile(path)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != asset.hash {
			return fmt.Errorf("missing or unverified module voice runtime %s; run dj4hub-call-test -prepare-module", asset.name)
		}
		paths = append(paths, path)
	}
	adb, err := exec.LookPath("adb")
	if err != nil {
		for _, path := range []string{"/usr/local/bin/adb", "/opt/homebrew/bin/adb"} {
			if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
				adb, err = path, nil
				break
			}
		}
		if err != nil {
			return errors.New("ADB is required to prepare the module voice route")
		}
	}
	session.adb = adb
	if err := session.start(ctx); err != nil {
		return err
	}
	run := session.command
	list, err := run(ctx, "devices", "-l")
	if err != nil {
		return err
	}
	transport, err := moduleTransport(list, devices.InputUID)
	if err != nil {
		return err
	}
	shell := func(command string) (string, error) {
		result, err := run(ctx, "-t", transport, "shell", command)
		if err != nil {
			return result, err
		}
		if !strings.HasSuffix(strings.TrimSpace(result), "DJ4HUB_EXIT=0") {
			return result, fmt.Errorf("module voice preparation failed: %s", strings.TrimSpace(result))
		}
		return result, nil
	}
	scriptHash := fmt.Sprintf("%x", sha256.Sum256(voicePrepareScript))
	mode := "--check-ready"
	if rotate {
		mode = "--rotate-ready"
	}
	check := `test "$(sha256sum /tmp/dj4hub-voice/prepare.sh | cut -d ' ' -f 1)" = ` + scriptHash +
		` && timeout -t 12 -s KILL sh /tmp/dj4hub-voice/prepare.sh ` + mode + `; result=$?; printf '\nDJ4HUB_EXIT=%s\n' "$result"`
	if result, err := shell(check); err == nil && strings.Contains(result, "DJ4HUB_VOICE_ROUTE_READY") {
		return nil
	}
	// Check identity before copying anything to the target. Legacy ADB shell
	// does not propagate exit status, so require our explicit success marker.
	preflight := `test "$(id -u)" = 0 && test "$(uname -r)" = 3.18.44 && test "$(uname -m)" = armv7l && test "$(cat /sys/class/android_usb/android0/idVendor)" = 2ca3 && test "$(cat /sys/class/android_usb/android0/idProduct)" = 4006 && mkdir -p /tmp/dj4hub-voice && chmod 700 /tmp/dj4hub-voice; result=$?; printf '\nDJ4HUB_EXIT=%s\n' "$result"`
	if _, err := shell(preflight); err != nil {
		return err
	}
	args := append([]string{"-t", transport, "push"}, paths...)
	args = append(args, "/tmp/dj4hub-voice/")
	if _, err := run(ctx, args...); err != nil {
		return err
	}
	file, err := os.CreateTemp("", "dj4hub-voice-*.sh")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(voicePrepareScript)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if _, err := run(ctx, "-t", transport, "push", file.Name(), "/tmp/dj4hub-voice/prepare.sh"); err != nil {
		return err
	}
	// A healthy route returned above. Full recovery rotates only our owned process.
	result, err := shell("timeout -t 40 -s KILL sh /tmp/dj4hub-voice/prepare.sh --rotate" + `; result=$?; printf '\nDJ4HUB_EXIT=%s\n' "$result"`)
	if err == nil && !strings.Contains(result, "DJ4HUB_VOICE_ROUTE_READY") {
		err = errors.New("module voice script did not verify the route")
	}
	return err
}

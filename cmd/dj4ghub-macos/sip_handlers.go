package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/sip"
)

// SIP voice bridge wiring (Item 1).
//
// The SIP server lives in internal/sip and speaks UDP 5060 + RTP PCMU/8000
// on loopback. This file only adapts it to the app: config dir + first-run
// password, AT call-control closures (modem.Manager when present, USB-AT
// otherwise, honest error when neither), modem URC callbacks (RING/CLIP/
// NO CARRIER/CONNECT), and the /api/sip/* HTTP handlers in the existing
// main.go route style.

// appAudio adapts the app's modem/USB-AT paths to sip.ModemAudio.
type appAudio struct {
	a *app

	mu           sync.Mutex
	qpcmvAt      time.Time
	qpcmvEnabled bool
	qpcmvMode    int
	qpcmvErr     error
}

func (aa *appAudio) Available() bool {
	return !aa.a.demo && (aa.a.modem != nil || aa.a.usbAT != nil)
}

func (aa *appAudio) EnableUSBAudio() error {
	aa.invalidateQPCMVCache()
	if aa.a.modem != nil {
		return aa.a.modem.EnableUSBAudio()
	}
	_, err := aa.a.runATCommand("AT+QPCMV=1,2", 2*time.Second)
	return err
}

func (aa *appAudio) DisableUSBAudio() error {
	aa.invalidateQPCMVCache()
	if aa.a.modem != nil {
		return aa.a.modem.DisableUSBAudio()
	}
	_, err := aa.a.runATCommand("AT+QPCMV=0", 2*time.Second)
	return err
}

func (aa *appAudio) invalidateQPCMVCache() {
	aa.mu.Lock()
	aa.qpcmvAt = time.Time{}
	aa.mu.Unlock()
}

func (aa *appAudio) QueryUSBAudioMode() (bool, int, error) {
	if aa.a.modem != nil {
		return aa.a.modem.QueryUSBAudioMode()
	}
	aa.mu.Lock()
	if !aa.qpcmvAt.IsZero() && time.Since(aa.qpcmvAt) < 30*time.Second {
		enabled, mode, err := aa.qpcmvEnabled, aa.qpcmvMode, aa.qpcmvErr
		aa.mu.Unlock()
		return enabled, mode, err
	}
	aa.mu.Unlock()
	store := func(enabled bool, mode int, err error) (bool, int, error) {
		aa.mu.Lock()
		aa.qpcmvEnabled, aa.qpcmvMode, aa.qpcmvErr = enabled, mode, err
		aa.qpcmvAt = time.Now()
		aa.mu.Unlock()
		return enabled, mode, err
	}
	resp, err := aa.a.runATCommand("AT+QPCMV?", 2*time.Second)
	if err != nil {
		return store(false, 0, err)
	}
	idx := strings.Index(resp, "+QPCMV:")
	if idx == -1 {
		if strings.Contains(resp, "ERROR") {
			return store(false, 0, errors.New("AT+QPCMV? returned ERROR; current QPCMV state is unknown (this does not establish command support or USB audio availability)"))
		}
		return store(false, 0, errors.New("response did not contain +QPCMV"))
	}
	parts := strings.Split(strings.TrimSpace(resp[idx+7:]), ",")
	enabled := strings.TrimSpace(parts[0]) == "1"
	mode := 0
	if len(parts) > 1 {
		fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &mode)
	}
	return store(enabled, mode, nil)
}

// sipModemCallState reports AT+CPAS + AT+CLCC for the SIP reconciler.
// CPAS is the authority for "is there any call at all"; CLCC entries with
// an empty number are this firmware's phantom (idx 2 survives ATH while
// CPAS reads 0) and are dropped here as well as in the reconciler.
func (a *app) sipModemCallState() (sip.ModemCallState, error) {
	if a.demo {
		return sip.ModemCallState{CPAS: -1}, nil
	}
	cpasResp, err := a.runATCommand("AT+CPAS", 5*time.Second)
	if err != nil {
		return sip.ModemCallState{CPAS: -1}, err
	}
	ms := sip.ModemCallState{CPAS: -1}
	for _, line := range strings.Split(cpasResp, "\n") {
		line = strings.TrimSpace(strings.Trim(line, "\r"))
		if strings.HasPrefix(line, "+CPAS:") {
			fmt.Sscanf(strings.TrimSpace(line[len("+CPAS:"):]), "%d", &ms.CPAS)
		}
	}
	if ms.CPAS == -1 {
		return ms, fmt.Errorf("could not parse +CPAS from %q", cpasResp)
	}
	// The release reason is the modem's own word for why a call ended:
	// the most recent call-release URC captured while draining the bulk
	// interface (NO CARRIER / BUSY / NO ANSWER / CME ERROR). Without it
	// a released call can only be reported as a generic failure.
	if a.usbAT != nil {
		for _, u := range a.usbAT.RecentURCs() {
			upper := strings.ToUpper(u)
			if strings.Contains(upper, "NO CARRIER") || strings.Contains(upper, "BUSY") ||
				strings.Contains(upper, "NO ANSWER") || strings.Contains(upper, "NO DIALTONE") ||
				strings.Contains(upper, "CME ERROR") || strings.Contains(upper, "CMS ERROR") {
				ms.ReleaseReason = strings.TrimSpace(u)
			}
		}
	}
	clccResp, err := a.runATCommand("AT+CLCC", 5*time.Second)
	if err != nil {
		// CPAS alone is enough to clear a stale record; report what we have.
		return ms, nil
	}
	log.Printf("[sip] modem state: CPAS=%d (%q) CLCC=%q release=%q", ms.CPAS, truncForLog(cpasResp, 80), truncForLog(clccResp, 160), ms.ReleaseReason)
	for _, line := range strings.Split(clccResp, "\n") {
		line = strings.TrimSpace(strings.Trim(line, "\r"))
		if !strings.HasPrefix(line, "+CLCC:") {
			continue
		}
		fields := strings.Split(line[len("+CLCC:"):], ",")
		if len(fields) < 6 {
			continue
		}
		var e sip.CLCCEntry
		fmt.Sscanf(strings.TrimSpace(fields[0]), "%d", &e.Index)
		fmt.Sscanf(strings.TrimSpace(fields[1]), "%d", &e.Dir)
		fmt.Sscanf(strings.TrimSpace(fields[2]), "%d", &e.State)
		e.Number = strings.Trim(strings.TrimSpace(fields[5]), "\"")
		if e.Number == "" {
			continue // phantom entry: no number, never a real call
		}
		ms.CLCC = append(ms.CLCC, e)
	}
	return ms, nil
}

// truncForLog shortens a raw AT response for one log line.
func truncForLog(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\r", "")
	s = strings.ReplaceAll(s, "\n", " | ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func (aa *appAudio) GetQPCMVChan() <-chan int {
	if aa.a.modem != nil {
		return aa.a.modem.GetQPCMVChan()
	}
	return nil
}

// sipConfigDir is ~/Library/Application Support/DJ 4G Hub (os.UserConfigDir).
func (a *app) sipConfigDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate SIP config directory: %w", err)
	}
	return filepath.Join(configDir, "DJ 4G Hub"), nil
}

// startSIP loads (or first-run generates) the SIP config, builds the server
// with AT call-control hooks, registers the modem voice callbacks, and
// starts listening. A failure to bind (e.g. port taken) is logged and
// reported via /api/sip/status; it never takes the HTTP server down.
func (a *app) startSIP() {
	dir, err := a.sipConfigDir()
	if err != nil {
		log.Printf("[sip] unavailable: %v", err)
		return
	}
	cfg, generated, err := sip.LoadOrCreate(dir)
	if err != nil {
		log.Printf("[sip] unavailable: %v", err)
		return
	}
	if generated {
		// First-run pattern (Item 6): print once, stored 0600 in sip-password.
		log.Printf("[sip] first run: generated password for SIP user %q (stored 0600 in %s)",
			cfg.Username, filepath.Join(dir, "sip-password"))
	}
	a.sipDir = dir
	// Host audio starts only with a media session.
	var bridge sip.AudioBridge
	if a.demo {
		bridge = &sip.QPCMVBridge{}
	} else {
		a.sipAudio = sip.NewCallAudioBridge(dir)
		bridge = a.sipAudio
	}
	server := sip.New(cfg, bridge)
	server.HistorySink = a.recordCallHistory
	a.sip = server
	if !a.demo {
		a.sipPanel = a.newCallPanel()
		a.sipAlerts = newLocalCallAlerts(server, a.sipPanel)
	}
	server.OnCallState = func(call sip.PublicCall) {
		if a.sipAlerts != nil {
			a.sipAlerts.OnCallState(call)
		}
		if a.sipAudio != nil {
			a.sipAudio.OnCallState(call)
		}
		if a.sipPanel != nil && call.LocalAudio && call.Direction == "outbound" && call.State == sip.CallStateDialing {
			_ = a.sipPanel.Show()
		}
	}
	server.Dial = a.dialViaModem
	server.Answer = a.answerViaModem
	server.Hangup = a.hangupViaModem
	server.DTMF = func(digit string) error {
		if a.demo {
			return errors.New("演示模式无法发送电话按键")
		}
		_, err := a.runATOK(`AT+VTS="`+digit+`"`, 3*time.Second)
		return err
	}
	// CPAS/CLCC reconciliation: the USB-AT path has no URC callbacks, so
	// the server must ask the modem whether a call is really up. Without
	// this, a stale record blocks every later dial.
	server.ModemCallState = a.sipModemCallState
	if a.modem != nil {
		// Batch 1: wrap the ring/CLIP callbacks so an incoming call also
		// notifies (deduped per ringing episode); SIP still gets the event.
		a.modem.SetRingCallback(func() {
			server.NoteRing()
			a.notifyIncomingCall("")
		})
		a.modem.SetClipCallback(func(number string) {
			server.NoteClip(number)
			a.notifyIncomingCall(number)
		})
		a.modem.SetHangupCallback(server.ModemHungUp)
		a.modem.SetConnectCallback(server.ModemConnected)
	}
	a.sip = server
	if err := server.Start(); err != nil {
		log.Printf("[sip] listener failed (%v); HTTP /api/sip/status reports the error", err)
	} else if cfg.Enabled && a.sipAudio != nil {
		a.sipAudio.Warmup()
	}
	if !a.demo && a.modem == nil {
		ctx, cancel := context.WithCancel(context.Background())
		a.sipCallPollCancel = cancel
		go a.pollIncomingCalls(ctx, server)
		go a.pollUSBCallEvents(ctx)
	}
}

// Incoming calls must not wait behind a complete SMS scan. Once a call is
// tracked, the SIP reconciler owns the faster live-call modem polling.
func (a *app) pollIncomingCalls(ctx context.Context, server *sip.Server) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var callerIDUSB *usbAT
	var callerIDAttempt time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !server.Config().Enabled {
			continue
		}
		if a.usbAT != nil && a.usbAT != callerIDUSB && time.Since(callerIDAttempt) > 30*time.Second {
			callerIDAttempt = time.Now()
			device := a.usbAT
			if _, err := a.runATOK("AT+CLIP=1", 3*time.Second); err != nil {
				log.Printf("[sip] caller ID reporting setup failed: %v", err)
			} else {
				callerIDUSB = device
				log.Printf("[sip] caller ID reporting enabled")
			}
		}
		live := false
		for _, call := range server.Status().Calls {
			if call.State == sip.CallStateActive || call.State == sip.CallStateRinging || call.State == sip.CallStateDialing {
				live = true
				break
			}
		}
		// Continue caller-ID polling while ringing: RING frequently precedes
		// CLIP, and some firmware only exposes the number through CLCC.
		ringing := false
		for _, call := range server.Calls() {
			if call.Direction == "inbound" && call.State == sip.CallStateRinging {
				ringing = true
			}
		}
		if !live || ringing {
			a.checkIncomingCallUSBAT()
		}
	}
}

func (a *app) stopSIP() {
	if a.sipCallPollCancel != nil {
		a.sipCallPollCancel()
	}
	if a.sipAlerts != nil {
		a.sipAlerts.Close()
	}
	if a.sipPanel != nil {
		a.sipPanel.Close()
	}
	if a.sip != nil {
		a.sip.Stop()
	}
	if a.sipAudio != nil {
		a.sipAudio.Close()
	}
}

// dialViaModem dials via the modem (ATD<number>;) or the USB-AT bridge.
func (a *app) dialViaModem(number string) error {
	if a.demo {
		return errors.New("demo mode: no modem to dial with")
	}
	if a.modem != nil {
		return a.modem.DialCall(number)
	}
	if _, err := a.runATOK("ATD"+number+";", 60*time.Second); err != nil {
		return err
	}
	return nil
}

// answerViaModem answers via the modem (ATA) or the USB-AT bridge.
func (a *app) answerViaModem() error {
	if a.demo {
		return errors.New("demo mode: no modem to answer with")
	}
	if a.modem != nil {
		return a.modem.AnswerCall()
	}
	if _, err := a.runATOK("ATA", 5*time.Second); err != nil {
		return err
	}
	return nil
}

// hangupViaModem hangs up via the modem (ATH) or the USB-AT bridge.
func (a *app) hangupViaModem() error {
	if a.demo {
		return errors.New("demo mode: no modem to hang up")
	}
	if a.modem != nil {
		return a.modem.HangupCall()
	}
	if _, err := a.runATOK("ATH", 3*time.Second); err != nil {
		return err
	}
	return nil
}

// --- HTTP handlers ------------------------------------------------------

// sipStatus returns the SIP server snapshot: listener, registrations, calls,
// RTP range and the (stub) audio bridge state.
func (a *app) sipStatus(w http.ResponseWriter, _ *http.Request) {
	if a.sip == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"enabled": false, "listening": false,
			"listen_error":  "SIP server not started",
			"registrations": []sip.Registration{}, "calls": []sip.PublicCall{},
		})
		return
	}
	writeJSON(w, http.StatusOK, struct {
		sip.Status
		NativeAlertCallID string `json:"native_alert_call_id"`
	}{a.sip.Status(), a.sipAlerts.RingingCallID()})
}

// sipCall dials a number via the modem. No real call is possible without a
// module: that path returns 503 with the reason, never a fake success.
func (a *app) sipCall(w http.ResponseWriter, r *http.Request) {
	if a.sip == nil {
		writeError(w, http.StatusServiceUnavailable, "SIP server not started")
		return
	}
	var body struct {
		Number string `json:"number"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	callID, err := a.sip.DialFromAPI(body.Number)
	if err != nil {
		// Validation problems (bad number, line busy) are 400; a dial that
		// cannot reach any modem is 503, never a fake success.
		if strings.HasPrefix(err.Error(), "invalid phone number") || strings.HasPrefix(err.Error(), "another call") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "call_id": callID})
}

// sipHangup ends a call (BYE to the SIP peer + ATH to the modem).
func (a *app) sipHangup(w http.ResponseWriter, r *http.Request) {
	if a.sip == nil {
		writeError(w, http.StatusServiceUnavailable, "SIP server not started")
		return
	}
	var body struct {
		CallID string `json:"call_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := a.sip.HangupCall(body.CallID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sipAnswer answers the current inbound call (ATA to the modem).
func (a *app) sipAnswer(w http.ResponseWriter, r *http.Request) {
	if a.sip == nil {
		writeError(w, http.StatusServiceUnavailable, "SIP server not started")
		return
	}
	var body struct {
		CallID string `json:"call_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := a.sip.AnswerCall(body.CallID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sipDTMF sends one DTMF digit on a connected call (AT+VTS via the
// modem). The native panel already had this path; the web UI had none.
func (a *app) sipDTMF(w http.ResponseWriter, r *http.Request) {
	if a.sip == nil {
		writeError(w, http.StatusServiceUnavailable, "SIP server not started")
		return
	}
	var body struct {
		CallID string `json:"call_id"`
		Digit  string `json:"digit"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := a.sip.SendDTMF(body.CallID, body.Digit); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sipAudioControls sets the local microphone/speaker mute for one call.
// Same control the native panel sends; now reachable from the web UI.
func (a *app) sipAudioControls(w http.ResponseWriter, r *http.Request) {
	if a.sipAudio == nil {
		writeError(w, http.StatusServiceUnavailable, "local call audio unavailable")
		return
	}
	var body struct {
		CallID          string `json:"call_id"`
		MicrophoneMuted bool   `json:"microphone_muted"`
		SpeakerMuted    bool   `json:"speaker_muted"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := a.sipAudio.SetLocalControls(body.CallID, body.MicrophoneMuted, body.SpeakerMuted); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sipAudioDevices lists host audio devices and persists the selection for
// the next call. GET returns the list plus the current selection; POST
// saves {input, output} (device UID or name; empty resets that side to
// the built-in default). A selection never touches a running call.
func (a *app) sipAudioDevices(w http.ResponseWriter, r *http.Request) {
	if a.sipAudio == nil {
		writeError(w, http.StatusServiceUnavailable, "local call audio unavailable")
		return
	}
	if r.Method == http.MethodPost {
		var body struct {
			Input  string `json:"input"`
			Output string `json:"output"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		if err := a.sipAudio.SetAudioDevices(body.Input, body.Output); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	devices, err := sip.ListLocalAudioDevices()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	input, output := a.sipAudio.AudioDeviceSelection()
	writeJSON(w, http.StatusOK, map[string]any{
		"devices": devices, "input": input, "output": output,
	})
}

// sipSettingsPublic is the settings view returned to the UI. The password
// is never returned; only whether one exists.
type sipSettingsPublic struct {
	Enabled     bool   `json:"enabled"`
	Listen      string `json:"listen"`
	Realm       string `json:"realm"`
	Username    string `json:"username"`
	HasPassword bool   `json:"has_password"`
	RTPMin      int    `json:"rtp_min"`
	RTPMax      int    `json:"rtp_max"`
}

func (a *app) sipSettingsView() sipSettingsPublic {
	cfg := a.sip.Config()
	return sipSettingsPublic{
		Enabled: cfg.Enabled, Listen: cfg.Listen, Realm: cfg.Realm,
		Username: cfg.Username, HasPassword: cfg.Password != "",
		RTPMin: cfg.RTPMin, RTPMax: cfg.RTPMax,
	}
}

// sipSettings handles GET/POST /api/sip/settings. POST applies partial
// updates; a new password is stored 0600 and never echoed back. Changing
// the listen address or enabled flag restarts the SIP listener.
func (a *app) sipSettings(w http.ResponseWriter, r *http.Request) {
	if a.sip == nil {
		writeError(w, http.StatusServiceUnavailable, "SIP server not started")
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, a.sipSettingsView())
		return
	}
	var body struct {
		Enabled  *bool   `json:"enabled"`
		Listen   *string `json:"listen"`
		Realm    *string `json:"realm"`
		Username *string `json:"username"`
		Password *string `json:"password"`
		RTPMin   *int    `json:"rtp_min"`
		RTPMax   *int    `json:"rtp_max"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	cfg := a.sip.Config()
	if body.Enabled != nil {
		cfg.Enabled = *body.Enabled
	}
	if body.Listen != nil {
		cfg.Listen = strings.TrimSpace(*body.Listen)
	}
	if body.Realm != nil {
		cfg.Realm = strings.TrimSpace(*body.Realm)
	}
	if body.Username != nil {
		cfg.Username = strings.TrimSpace(*body.Username)
	}
	if body.RTPMin != nil {
		cfg.RTPMin = *body.RTPMin
	}
	if body.RTPMax != nil {
		cfg.RTPMax = *body.RTPMax
	}
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Password != nil && strings.TrimSpace(*body.Password) != "" {
		if err := sip.SavePassword(a.sipDir, *body.Password); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		cfg.Password = strings.TrimSpace(*body.Password)
	}
	if err := cfg.Save(a.sipDir); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := a.sip.UpdateConfig(cfg); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.sipSettingsView())
}

package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/modem"
)

// USSD + IMS USSI + SMS delivery reports (Item 2).
//
// USSD rides the existing backends: the serial-AT Manager path reuses
// Manager.ExecuteUSSD/ContinueUSSD/CancelUSSD (the +CUSD URC channel),
// and the USB-AT bridge path sends AT+CUSD and then reads the interface
// directly for the +CUSD line (usbAT.WaitForCUSD) because the bridge has
// no background URC reader.
//
// Session safety (VoCat rule): a cancelled or errored session is terminal
// and is never auto-resubmitted over another channel. A new session while
// one is active is rejected with 409 — cancel first. A session that ends
// (status 0/2) or errors stays visible until the next explicit start.
//
// IMS USSI (TS 24.390): this build has no IMS/VoWiFi stack — no SIP
// REGISTER to an IMS core, no IKEv2/ePDG — so USSI cannot be routed over
// IMS on AT-mode. The IMS path is therefore detected (AT+QIMS?) and
// answered with an explicit 501, never silently fallen back to. The
// default "auto" path always uses AT+CUSD and reports which path was used.

const (
	ussdStateActive    = "active"
	ussdStateEnded     = "ended"
	ussdStateCancelled = "cancelled"
	ussdStateError     = "error"
)

const ussdSessionTimeoutDefault = 30 * time.Second

type ussdSession struct {
	ID            string    `json:"id"`
	Code          string    `json:"code"`
	State         string    `json:"state"`
	Path          string    `json:"path"`
	IMSRegistered bool      `json:"ims_registered"`
	Status        int       `json:"status"`
	Text          string    `json:"text"`
	RawText       string    `json:"raw_text,omitempty"`
	DCS           int       `json:"dcs"`
	StartedAt     time.Time `json:"started_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Error         string    `json:"error,omitempty"`
}

type smsDeliveryRecord struct {
	ID            string     `json:"id"`
	MessageRef    int        `json:"message_ref"`
	To            string     `json:"to"`
	State         string     `json:"state"`
	Status        int        `json:"status,omitempty"`
	StatusText    string     `json:"status_text,omitempty"`
	SentAt        time.Time  `json:"sent_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	DischargeTime *time.Time `json:"discharge_time,omitempty"`
}

func ussdStateForStatus(status int) string {
	if status == 1 {
		return ussdStateActive
	}
	return ussdStateEnded
}

// requireModemTransport ensures an AT path exists (serial Manager or
// USB-AT bridge, opening the bridge on demand). It returns an error when
// neither is available; handlers turn that into 503.
func (a *app) requireModemTransport() error {
	if a.demo {
		return errors.New("demo mode: no modem")
	}
	if a.modem != nil {
		return nil
	}
	if err := a.ensureUSBAT(); err != nil {
		return err
	}
	if a.usbAT == nil {
		return errors.New("AT serial port is unavailable")
	}
	return nil
}

// queryIMS reports IMS (VoLTE) registration from AT+QIMS?. The raw value
// is surfaced because its exact meaning is firmware-defined (1 =
// registered on Quectel). An error means the modem did not answer.
func (a *app) queryIMS() (registered bool, raw int, err error) {
	if a.modem != nil {
		raw, err = a.modem.QueryIMSStatus()
		if err != nil {
			return false, 0, err
		}
		return raw == 1, raw, nil
	}
	resp, err := a.runATCommand("AT+QIMS?", 3*time.Second)
	if err != nil {
		return false, 0, err
	}
	raw, ok := modem.ParseQIMSStatus(resp)
	if !ok {
		return false, 0, fmt.Errorf("modem did not report +QIMS")
	}
	return raw == 1, raw, nil
}

// execUSSD sends one AT+CUSD and waits for the +CUSD result.
func (a *app) execUSSD(code string, timeout time.Duration) (*modem.USSDResult, error) {
	if a.modem != nil {
		return a.modem.ExecuteUSSD(code, timeout)
	}
	cmd, err := modem.BuildCUSDCommand(code)
	if err != nil {
		return nil, err
	}
	resp, err := a.runATCommand(cmd, 5*time.Second)
	if err != nil {
		return nil, err
	}
	if atResponseIsError(resp) {
		return nil, fmt.Errorf("modem rejected USSD: %s", strings.TrimSpace(resp))
	}
	return a.waitUSSDResult(timeout)
}

// continueUSSDExec replies to an in-progress USSD session.
func (a *app) continueUSSDExec(input string, timeout time.Duration) (*modem.USSDResult, error) {
	if a.modem != nil {
		return a.modem.ContinueUSSD(input, timeout)
	}
	cmd, err := modem.BuildCUSDCommand(input)
	if err != nil {
		return nil, err
	}
	resp, err := a.runATCommand(cmd, 5*time.Second)
	if err != nil {
		return nil, err
	}
	if atResponseIsError(resp) {
		return nil, fmt.Errorf("modem rejected USSD reply: %s", strings.TrimSpace(resp))
	}
	return a.waitUSSDResult(timeout)
}

// waitUSSDResult reads the USB-AT interface for the +CUSD URC line.
func (a *app) waitUSSDResult(timeout time.Duration) (*modem.USSDResult, error) {
	line, err := a.usbAT.WaitForCUSD(timeout)
	if err != nil {
		return nil, err
	}
	status, dcs, text, ok := modem.ParseCUSDLine(line)
	if !ok {
		return nil, fmt.Errorf("unparsable +CUSD line: %s", line)
	}
	return &modem.USSDResult{
		Status:  status,
		Text:    modem.DecodeUSSDText(text, dcs),
		RawText: text,
		DCS:     dcs,
	}, nil
}

// cancelUSSDExec sends AT+CUSD=2 on whichever transport exists.
func (a *app) cancelUSSDExec() error {
	if a.modem != nil {
		a.modem.CancelUSSD()
		return nil
	}
	resp, err := a.runATCommand("AT+CUSD=2", 3*time.Second)
	if err != nil {
		return err
	}
	if atResponseIsError(resp) {
		return fmt.Errorf("modem rejected USSD cancel: %s", strings.TrimSpace(resp))
	}
	return nil
}

func (a *app) currentUSSDSession() *ussdSession {
	a.ussdMu.Lock()
	defer a.ussdMu.Unlock()
	if a.ussdSession == nil {
		return nil
	}
	cp := *a.ussdSession
	return &cp
}

func (a *app) storeUSSDSession(s *ussdSession) {
	a.ussdMu.Lock()
	defer a.ussdMu.Unlock()
	a.ussdSession = s
}

func clampUSSDTimeout(seconds *int) time.Duration {
	timeout := ussdSessionTimeoutDefault
	if seconds != nil {
		timeout = time.Duration(*seconds) * time.Second
		if timeout < 5*time.Second {
			timeout = 5 * time.Second
		}
		if timeout > 120*time.Second {
			timeout = 120 * time.Second
		}
	}
	return timeout
}

// startUSSD starts a USSD session. via=auto (default) uses AT+CUSD and
// reports the path; via=ims is answered 501 (no IMS USSI stack, see the
// file header); via=at forces AT+CUSD.
func (a *app) startUSSD(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code     string `json:"code"`
		Via      string `json:"via"`
		TimeoutS *int   `json:"timeout_s"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	code := strings.TrimSpace(body.Code)
	if _, err := modem.BuildCUSDCommand(code); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	via := strings.ToLower(strings.TrimSpace(body.Via))
	if via == "" {
		via = "auto"
	}
	if via != "auto" && via != "at" && via != "ims" {
		writeError(w, http.StatusBadRequest, "via must be auto, at or ims")
		return
	}
	timeout := clampUSSDTimeout(body.TimeoutS)

	a.ussdMu.Lock()
	active := a.ussdSession != nil && a.ussdSession.State == ussdStateActive
	a.ussdMu.Unlock()
	if active {
		writeError(w, http.StatusConflict, "a USSD session is already active; cancel it first")
		return
	}
	if err := a.requireModemTransport(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "modem unavailable: "+err.Error())
		return
	}
	if via == "ims" {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":              "IMS USSI (TS 24.390) is not supported on this build: no IMS/VoWiFi stack, so USSD cannot be routed over IMS; use via=auto (AT+CUSD)",
			"ims_ussi_supported": false,
			"path":               "ims-ussi",
		})
		return
	}
	registered, _, imsErr := a.queryIMS()
	result, err := a.execUSSD(code, timeout)
	now := time.Now()
	if err != nil {
		session := &ussdSession{
			ID: "ussd-" + now.Format("20060102150405"), Code: code,
			State: ussdStateError, Path: "at-cusd", IMSRegistered: registered,
			StartedAt: now, UpdatedAt: now, Error: err.Error(),
		}
		a.storeUSSDSession(session)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	session := &ussdSession{
		ID: "ussd-" + now.Format("20060102150405"), Code: code,
		State: ussdStateForStatus(result.Status), Path: "at-cusd",
		IMSRegistered: registered, Status: result.Status, Text: result.Text,
		RawText: result.RawText, DCS: result.DCS,
		StartedAt: now, UpdatedAt: now,
	}
	a.storeUSSDSession(session)
	resp := map[string]any{
		"session":            session,
		"path":               "at-cusd",
		"ims_registered":     registered,
		"ims_ussi_supported": false,
	}
	if imsErr != nil {
		resp["ims_error"] = imsErr.Error()
	}
	if registered {
		resp["ims_note"] = "IMS is registered, but IMS USSI is not supported on this build; the session ran over AT+CUSD"
	}
	writeJSON(w, http.StatusOK, resp)
}

// continueUSSD replies to an active USSD session (status 1).
func (a *app) continueUSSD(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Response string `json:"response"`
		TimeoutS *int   `json:"timeout_s"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if _, err := modem.BuildCUSDCommand(body.Response); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session := a.currentUSSDSession()
	if session == nil || session.State != ussdStateActive {
		writeError(w, http.StatusConflict, "no active USSD session to reply to")
		return
	}
	timeout := clampUSSDTimeout(body.TimeoutS)
	if err := a.requireModemTransport(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "modem unavailable: "+err.Error())
		return
	}
	result, err := a.continueUSSDExec(body.Response, timeout)
	now := time.Now()
	if err != nil {
		session.State = ussdStateError
		session.Error = err.Error()
		session.UpdatedAt = now
		a.storeUSSDSession(session)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	session.State = ussdStateForStatus(result.Status)
	session.Status = result.Status
	session.Text = result.Text
	session.RawText = result.RawText
	session.DCS = result.DCS
	session.Error = ""
	session.UpdatedAt = now
	a.storeUSSDSession(session)
	writeJSON(w, http.StatusOK, map[string]any{"session": session, "path": "at-cusd"})
}

// cancelUSSD cancels the current session. Cancelling is terminal: the
// session is never auto-resubmitted. If the modem is gone, the session is
// still marked cancelled locally (the network side times out on its own).
func (a *app) cancelUSSD(w http.ResponseWriter, _ *http.Request) {
	session := a.currentUSSDSession()
	if session == nil {
		writeError(w, http.StatusBadRequest, "no USSD session")
		return
	}
	if session.State != ussdStateActive {
		writeJSON(w, http.StatusOK, map[string]any{"session": session, "modem_cancelled": false, "note": "session was already " + session.State})
		return
	}
	cancelErr := a.cancelUSSDExec()
	session.State = ussdStateCancelled
	session.UpdatedAt = time.Now()
	a.storeUSSDSession(session)
	resp := map[string]any{"session": session, "modem_cancelled": cancelErr == nil}
	if cancelErr != nil {
		resp["modem_cancel_error"] = cancelErr.Error()
		resp["note"] = "session marked cancelled locally; the network side times out on its own"
	}
	writeJSON(w, http.StatusOK, resp)
}

// ussdStatus returns the current session, or idle when there is none.
func (a *app) ussdStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.ussdSnapshot())
}

func (a *app) ussdSnapshot() map[string]any {
	session := a.currentUSSDSession()
	if session == nil {
		return map[string]any{"state": "idle", "session": nil}
	}
	return map[string]any{"state": session.State, "session": session}
}

// ussdIMS reports IMS registration and the (unsupported) IMS USSI path.
func (a *app) ussdIMS(w http.ResponseWriter, _ *http.Request) {
	if a.demo {
		writeJSON(w, http.StatusOK, map[string]any{
			"ims_registered": false, "ims_ussi_supported": false, "demo": true,
			"ims_state": "unknown",
			"note":      "IMS USSI (TS 24.390) is not supported on this build: no IMS/VoWiFi stack",
		})
		return
	}
	if err := a.requireModemTransport(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "modem unavailable: "+err.Error())
		return
	}
	registered, raw, err := a.queryIMS()
	resp := map[string]any{
		"ims_registered":     registered,
		"ims_status_raw":     raw,
		"ims_ussi_supported": false,
		"note":               "IMS USSI (TS 24.390) is not supported on this build: no IMS/VoWiFi stack, so USSD always uses AT+CUSD",
	}
	resp["ims_state"] = "unregistered"
	if registered {
		resp["ims_state"] = "registered"
	}
	if err != nil {
		resp["ims_state"] = "unknown"
		resp["ims_error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- SMS delivery reports -----------------------------------------------

// recordSentSMS records freshly submitted segments as "sent", keyed by the
// +CMGS message reference so a later +CDS can match them. MR -1 means the
// modem did not report a reference.
func (a *app) recordSentSMS(to string, mrs []int) {
	a.deliveryMu.Lock()
	defer a.deliveryMu.Unlock()
	now := time.Now()
	for _, mr := range mrs {
		a.deliverySeq++
		a.delivery = append([]smsDeliveryRecord{{
			ID:         fmt.Sprintf("sms-%d-%d", now.UnixMilli(), a.deliverySeq),
			MessageRef: mr,
			To:         to,
			State:      "sent",
			SentAt:     now,
			UpdatedAt:  now,
		}}, a.delivery...)
	}
	if len(a.delivery) > 500 {
		a.delivery = a.delivery[:500]
	}
}

// recordDeliveryReport applies one decoded status report: it updates the
// matching sent record (by MR), or creates a record when the send was not
// recorded here (e.g. sent before a restart).
func (a *app) recordDeliveryReport(rep modem.SMSDeliveryReport) {
	a.deliveryMu.Lock()
	defer a.deliveryMu.Unlock()
	now := time.Now()
	for i := range a.delivery {
		if rep.MessageRef >= 0 && a.delivery[i].MessageRef == rep.MessageRef {
			rec := &a.delivery[i]
			rec.State = rep.State
			rec.Status = rep.Status
			rec.StatusText = rep.StatusText
			rec.UpdatedAt = now
			if !rep.DischargeTime.IsZero() {
				dt := rep.DischargeTime
				rec.DischargeTime = &dt
			}
			return
		}
	}
	a.deliverySeq++
	dt := rep.DischargeTime
	rec := smsDeliveryRecord{
		ID:         fmt.Sprintf("sms-%d-%d", now.UnixMilli(), a.deliverySeq),
		MessageRef: rep.MessageRef,
		To:         rep.Recipient,
		State:      rep.State,
		Status:     rep.Status,
		StatusText: rep.StatusText,
		SentAt:     now,
		UpdatedAt:  now,
	}
	if !rep.DischargeTime.IsZero() {
		rec.DischargeTime = &dt
	}
	a.delivery = append([]smsDeliveryRecord{rec}, a.delivery...)
	if len(a.delivery) > 500 {
		a.delivery = a.delivery[:500]
	}
	log.Printf("SMS delivery report: MR=%d to=%s state=%s", rep.MessageRef, rep.Recipient, rep.State)
}

// listDeliveryReports returns the per-message delivery states, newest first.
func (a *app) listDeliveryReports(w http.ResponseWriter, _ *http.Request) {
	a.deliveryMu.RLock()
	items := append([]smsDeliveryRecord(nil), a.delivery...)
	a.deliveryMu.RUnlock()
	if items == nil {
		items = []smsDeliveryRecord{}
	}
	writeJSON(w, http.StatusOK, items)
}

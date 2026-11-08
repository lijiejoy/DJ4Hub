package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/backend"
	"github.com/WongLoki/DJ4Hub/internal/modem"
	"github.com/WongLoki/DJ4Hub/internal/sip"
)

// Serving cell, operator scan/select and cell lock (Item 3).
//
// Serving cell comes from AT+QENG="servingcell" (EARFCN/PCI/RSRP/RSRQ/
// SINR/band/channel). Operator scan/select reuse the existing backend
// (AT+COPS=? / AT+COPS=0 / AT+COPS=1,2,"<PLMN>"[,<act>]). Cell lock is
// new: AT+QNWLOCK="common/lte",2,<EARFCN>,<PCI> to lock,
// AT+QNWLOCK="common/lte",0 to unlock (Teltonika/Quectel EG/EC syntax;
// the lock is not persistent across modem reboot).
//
// Operator scan takes 30-120 s on real hardware, so it runs in a
// background goroutine: GET returns the current scan state immediately
// (202 while scanning) and never blocks the HTTP handler.
//
// Lock + operator preferences are persisted per ICCID in
// radio-prefs.json next to the SIP config (0600, same pattern as
// internal/sip's LoadOrCreate). The persisted preference is a *preference*:
// the modem is the source of truth for the live state, and the lock is
// re-applied only when the user asks — never silently on startup.

type radioPrefsStore struct {
	Version int                   `json:"version"`
	ByICCID map[string]radioPrefs `json:"by_iccid"`
}

type radioPrefs struct {
	CellLock  *cellLockPref `json:"cell_lock,omitempty"`
	Operator  *operatorPref `json:"operator,omitempty"`
	UpdatedAt time.Time     `json:"updated_at"`
}

type cellLockPref struct {
	Locked    bool      `json:"locked"`
	EARFCN    int       `json:"earfcn,omitempty"`
	PCI       int       `json:"pci,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type operatorPref struct {
	Mode      string    `json:"mode"`
	PLMN      string    `json:"plmn,omitempty"`
	RAT       string    `json:"rat,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type operatorScanState struct {
	State      string                      `json:"state"` // scanning | done | error
	StartedAt  time.Time                   `json:"started_at"`
	FinishedAt *time.Time                  `json:"finished_at,omitempty"`
	Operators  []backend.OperatorCandidate `json:"operators"`
	Error      string                      `json:"error,omitempty"`
}

const radioPrefsFileName = "radio-prefs.json"

// --- persisted preferences ----------------------------------------------

// normalizeICCID trims quotes/space and the trailing filler F so the same
// SIM keys the same preference on both transports.
func normalizeICCID(raw string) string {
	s := strings.TrimSpace(strings.Trim(raw, `"`))
	s = strings.TrimRight(s, "Ff")
	return s
}

func (a *app) radioPrefsPath() string {
	dir, err := a.sipConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, radioPrefsFileName)
}

func (a *app) loadRadioPrefsLocked() *radioPrefsStore {
	store := &radioPrefsStore{Version: 1, ByICCID: map[string]radioPrefs{}}
	path := a.radioPrefsPath()
	if path == "" {
		return store
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return store
	}
	var loaded radioPrefsStore
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return store
	}
	if loaded.ByICCID == nil {
		loaded.ByICCID = map[string]radioPrefs{}
	}
	return &loaded
}

func (a *app) saveRadioPrefsLocked(store *radioPrefsStore) error {
	path := a.radioPrefsPath()
	if path == "" {
		return errors.New("no config directory for radio preferences")
	}
	raw, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// currentICCID returns the serving SIM's ICCID without opening anything
// new. It returns ok=false when no transport is up.
func (a *app) currentICCID() (string, bool) {
	if a.modem != nil {
		iccid, err := a.modem.QueryICCID()
		if err != nil {
			return "", false
		}
		return normalizeICCID(iccid), true
	}
	if a.usbAT == nil {
		return "", false
	}
	resp, err := a.runATCommand("AT+QCCID", 3*time.Second)
	if err != nil {
		return "", false
	}
	iccid := normalizeICCID(parseUSBATPrefixed(resp, "+QCCID:"))
	if iccid == "" {
		return "", false
	}
	return iccid, true
}

// saveRadioPreference persists one preference under the current ICCID.
// It returns false when there is no ICCID to key on or the write failed.
func (a *app) saveRadioPreference(mutate func(*radioPrefs)) bool {
	iccid, ok := a.currentICCID()
	if !ok || iccid == "" {
		return false
	}
	a.radioPrefsMu.Lock()
	defer a.radioPrefsMu.Unlock()
	store := a.loadRadioPrefsLocked()
	pref := store.ByICCID[iccid]
	mutate(&pref)
	pref.UpdatedAt = time.Now()
	store.ByICCID[iccid] = pref
	return a.saveRadioPrefsLocked(store) == nil
}

func (a *app) loadRadioPreference() (string, *radioPrefs) {
	iccid, ok := a.currentICCID()
	if !ok || iccid == "" {
		return "", nil
	}
	a.radioPrefsMu.Lock()
	defer a.radioPrefsMu.Unlock()
	store := a.loadRadioPrefsLocked()
	pref, found := store.ByICCID[iccid]
	if !found {
		return iccid, nil
	}
	return iccid, &pref
}

// --- serving cell --------------------------------------------------------

func servingCellJSON(info modem.ServingCellLTEInfo) map[string]any {
	return map[string]any{
		"rat":     "LTE",
		"earfcn":  info.EARFCN,
		"pci":     info.PCI,
		"cell_id": info.CellID,
		"rsrp":    info.RSRP,
		"rsrq":    info.RSRQ,
		"sinr":    info.SINR,
		"duplex":  info.Duplex,
		"band":    info.Band,
		"channel": info.Channel,
	}
}

// servingCell returns the serving LTE cell via AT+QENG="servingcell".
func (a *app) servingCell(w http.ResponseWriter, _ *http.Request) {
	if a.demo {
		writeJSON(w, http.StatusOK, map[string]any{
			"rat": "LTE", "earfcn": 3740, "pci": 132, "cell_id": "8401A29",
			"rsrp": -75, "rsrq": -8, "sinr": 11, "duplex": "FDD",
			"band": "LTE BAND 8", "channel": 3740, "demo": true,
		})
		return
	}
	if err := a.requireModemTransport(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "modem unavailable: "+err.Error())
		return
	}
	if a.modem != nil {
		info, err := a.modem.QueryServingCellLTEInfo()
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, servingCellJSON(info))
		return
	}
	resp, err := a.runATCommand(`AT+QENG="servingcell"`, 5*time.Second)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	info, ok := modem.ParseServingCellLTEInfo(resp)
	if !ok {
		writeError(w, http.StatusBadGateway, "serving cell not reported (non-LTE or no service)")
		return
	}
	writeJSON(w, http.StatusOK, servingCellJSON(info))
}

// --- operator scan / select ----------------------------------------------

// runOperatorScan executes the long AT+COPS=? scan in the background and
// records the outcome in a.operatorScan.
func (a *app) runOperatorScan() {
	started := time.Now()
	var candidates []backend.OperatorCandidate
	var err error
	if a.modem != nil {
		candidates, err = backend.NewATBackend(a.modem).ScanOperators(a.backgroundContext())
	} else {
		var resp string
		resp, err = a.runATCommand("AT+COPS=?", 120*time.Second)
		if err == nil {
			if atResponseIsError(resp) {
				err = fmt.Errorf("operator scan rejected: %s", strings.TrimSpace(resp))
			} else {
				candidates = backend.MapOperatorScanEntries(modem.ParseCOPSScan(resp))
			}
		}
	}
	finished := time.Now()
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if err != nil {
		a.operatorScan = &operatorScanState{State: "error", StartedAt: started, FinishedAt: &finished, Operators: []backend.OperatorCandidate{}, Error: err.Error()}
		return
	}
	if candidates == nil {
		candidates = []backend.OperatorCandidate{}
	}
	a.operatorScan = &operatorScanState{State: "done", StartedAt: started, FinishedAt: &finished, Operators: candidates}
}

// Reading status must never enqueue a long command on the call-control channel.
func (a *app) listOperators(w http.ResponseWriter, _ *http.Request) {
	if a.demo {
		writeJSON(w, http.StatusOK, map[string]any{
			"state": "done", "demo": true,
			"operators": []backend.OperatorCandidate{
				{PLMN: "46000", MCC: "460", MNC: "00", OperatorName: "China Mobile", Status: "current", RATs: []backend.OperatorAccessTechnology{backend.OperatorRATLTE}},
				{PLMN: "46001", MCC: "460", MNC: "01", OperatorName: "China Unicom", Status: "available", RATs: []backend.OperatorAccessTechnology{backend.OperatorRATLTE}},
			},
		})
		return
	}
	a.scanMu.Lock()
	st := operatorScanState{State: "idle", Operators: []backend.OperatorCandidate{}}
	if a.operatorScan != nil {
		st = *a.operatorScan
	}
	a.scanMu.Unlock()
	code := http.StatusOK
	if st.State == "scanning" {
		code = http.StatusAccepted
	}
	writeJSON(w, code, st)
}

// forceOperatorScan restarts the scan even when a recent result exists.
func (a *app) forceOperatorScan(w http.ResponseWriter, _ *http.Request) {
	if a.demo {
		writeError(w, http.StatusServiceUnavailable, "demo mode: no modem to scan with")
		return
	}
	if a.hasLiveCall() {
		writeError(w, http.StatusConflict, "正在通话或有来电，暂时无法扫描运营商")
		return
	}
	if err := a.requireModemTransport(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "modem unavailable: "+err.Error())
		return
	}
	a.scanMu.Lock()
	if a.operatorScan != nil && a.operatorScan.State == "scanning" {
		a.scanMu.Unlock()
		writeError(w, http.StatusConflict, "operator scan already running")
		return
	}
	a.operatorScan = &operatorScanState{State: "scanning", StartedAt: time.Now(), Operators: []backend.OperatorCandidate{}}
	a.scanMu.Unlock()
	go a.runOperatorScan()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"state": "scanning",
		"note":  "operator scan takes 30-120s; poll GET /api/radio/operators for the result",
	})
}

func (a *app) hasLiveCall() bool {
	if a.sip != nil {
		for _, call := range a.sip.Calls() {
			if call.State == sip.CallStateDialing || call.State == sip.CallStateRinging || call.State == sip.CallStateActive {
				return true
			}
		}
	}
	return false
}

// getOperator returns the current operator selection plus the saved
// preference for this ICCID.
func (a *app) getOperator(w http.ResponseWriter, _ *http.Request) {
	if a.demo {
		writeJSON(w, http.StatusOK, map[string]any{
			"current": backend.OperatorSelection{Mode: backend.OperatorSelectionAutomatic, OperatorName: "China Mobile"},
			"demo":    true,
		})
		return
	}
	if err := a.requireModemTransport(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "modem unavailable: "+err.Error())
		return
	}
	var sel backend.OperatorSelection
	if a.modem != nil {
		var err error
		sel, err = backend.NewATBackend(a.modem).GetOperatorSelection(context.Background())
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
	} else {
		resp, err := a.runATCommand("AT+COPS?", 5*time.Second)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		state, ok := modem.ParseCOPSSelection(resp)
		if !ok {
			writeError(w, http.StatusBadGateway, "could not parse +COPS response")
			return
		}
		sel = operatorSelectionFromModemState(state)
	}
	iccid, saved := a.loadRadioPreference()
	writeJSON(w, http.StatusOK, map[string]any{
		"current": sel, "iccid": iccid, "saved": saved,
	})
}

func operatorSelectionFromModemState(state modem.OperatorSelectionState) backend.OperatorSelection {
	sel := backend.OperatorSelection{}
	if state.Mode == 0 {
		sel.Mode = backend.OperatorSelectionAutomatic
		return sel
	}
	sel.Mode = backend.OperatorSelectionManual
	sel.PLMN = state.PLMN
	sel.RAT = backend.ATActToRAT(state.Act)
	if norm, err := backend.NormalizePLMNSelection(state.PLMN); err == nil {
		sel.MCC = norm.MCC
		sel.MNC = norm.MNC
		sel.MNCLength = norm.MNCLength
		sel.IncludesPCSDigit = norm.IncludesPCSDigit
	}
	return sel
}

// setOperator switches automatic/manual selection. Manual selection
// re-registers the modem and can take up to a minute.
func (a *app) setOperator(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode     string `json:"mode"`
		Operator string `json:"operator"`
		PLMN     string `json:"plmn"`
		RAT      string `json:"rat"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	plmn := strings.TrimSpace(body.PLMN)
	if plmn == "" {
		plmn = strings.TrimSpace(body.Operator)
	}
	mode := strings.ToLower(strings.TrimSpace(body.Mode))
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" && mode != "manual" {
		writeError(w, http.StatusBadRequest, "mode must be auto or manual")
		return
	}
	var reqMode backend.OperatorSelectionMode
	if mode == "auto" {
		reqMode = backend.OperatorSelectionAutomatic
	} else {
		reqMode = backend.OperatorSelectionManual
	}
	req := backend.SetOperatorSelectionRequest{
		Mode: reqMode, PLMN: plmn,
		RAT: backend.OperatorAccessTechnology(strings.ToLower(strings.TrimSpace(body.RAT))),
	}
	sel, err := backend.NormalizeSetOperatorSelectionRequest(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if a.demo {
		writeError(w, http.StatusServiceUnavailable, "demo mode: no modem to select an operator with")
		return
	}
	if err := a.requireModemTransport(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "modem unavailable: "+err.Error())
		return
	}
	if a.modem != nil {
		sel, err = backend.NewATBackend(a.modem).SetOperatorSelection(context.Background(), req)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
	} else {
		cmd := "AT+COPS=0"
		if reqMode == backend.OperatorSelectionManual {
			cmd = fmt.Sprintf(`AT+COPS=1,2,"%s"`, sel.PLMN)
			if sel.RAT != backend.OperatorRATUnknown {
				cmd = fmt.Sprintf(`AT+COPS=1,2,"%s",%d`, sel.PLMN, backend.RATToATAct(sel.RAT))
			}
		}
		resp, err := a.runATCommand(cmd, 60*time.Second)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		if atResponseIsError(resp) {
			writeError(w, http.StatusBadGateway, "modem rejected operator selection: "+strings.TrimSpace(resp))
			return
		}
	}
	persisted := a.saveRadioPreference(func(pref *radioPrefs) {
		pref.Operator = &operatorPref{Mode: string(sel.Mode), PLMN: sel.PLMN, RAT: string(sel.RAT), UpdatedAt: time.Now()}
	})
	writeJSON(w, http.StatusOK, map[string]any{"selection": sel, "persisted": persisted})
}

// --- cell lock -----------------------------------------------------------

// lockCell locks the modem to one LTE cell. The modem's OK is required:
// a rejected command returns 502 and nothing is persisted.
func (a *app) lockCell(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EARFCN *int `json:"earfcn"`
		PCI    *int `json:"pci"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.EARFCN == nil || body.PCI == nil {
		writeError(w, http.StatusBadRequest, "earfcn and pci are required")
		return
	}
	cmd, err := modem.BuildQNWLockCommand(*body.EARFCN, *body.PCI)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if a.demo {
		writeError(w, http.StatusServiceUnavailable, "demo mode: no modem to lock a cell with")
		return
	}
	if err := a.requireModemTransport(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "modem unavailable: "+err.Error())
		return
	}
	verified := false
	if a.modem != nil {
		verified, err = a.modem.LockCell(*body.EARFCN, *body.PCI)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
	} else {
		resp, err := a.runATCommand(cmd, 10*time.Second)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		if atResponseIsError(resp) {
			writeError(w, http.StatusBadGateway, "modem rejected cell lock: "+strings.TrimSpace(resp))
			return
		}
		if state, _, qerr := a.queryCellLockUSBAT(); qerr == nil && state.Locked &&
			state.EARFCN == *body.EARFCN && state.PCI == *body.PCI {
			verified = true
		}
	}
	persisted := a.saveRadioPreference(func(pref *radioPrefs) {
		pref.CellLock = &cellLockPref{Locked: true, EARFCN: *body.EARFCN, PCI: *body.PCI, UpdatedAt: time.Now()}
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"locked": true, "earfcn": *body.EARFCN, "pci": *body.PCI,
		"verified": verified, "persisted": persisted,
		"note": "cell lock is not persistent across modem reboot; re-apply after a restart",
	})
}

// unlockCell releases any cell lock.
func (a *app) unlockCell(w http.ResponseWriter, _ *http.Request) {
	if a.demo {
		writeError(w, http.StatusServiceUnavailable, "demo mode: no modem to unlock")
		return
	}
	if err := a.requireModemTransport(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "modem unavailable: "+err.Error())
		return
	}
	if a.modem != nil {
		if err := a.modem.UnlockCell(); err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
	} else {
		resp, err := a.runATCommand(modem.BuildQNWUnlockCommand(), 10*time.Second)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		if atResponseIsError(resp) {
			writeError(w, http.StatusBadGateway, "modem rejected cell unlock: "+strings.TrimSpace(resp))
			return
		}
	}
	persisted := a.saveRadioPreference(func(pref *radioPrefs) {
		pref.CellLock = &cellLockPref{Locked: false, UpdatedAt: time.Now()}
	})
	writeJSON(w, http.StatusOK, map[string]any{"locked": false, "persisted": persisted})
}

// queryCellLockUSBAT reads the lock state over USB-AT. The second return
// value reports "query unsupported": the QDC507 answers AT+QNWLOCK? with
// ERROR (its read-back is not implemented) while the SET command is
// accepted, so callers must not treat that as a transport failure.
func (a *app) queryCellLockUSBAT() (modem.CellLockState, bool, error) {
	resp, err := a.runATCommand("AT+QNWLOCK?", 5*time.Second)
	if err != nil {
		return modem.CellLockState{}, false, err
	}
	state, found := modem.ParseQNWLockResponse(resp)
	if !found {
		if atResponseIsError(resp) {
			return modem.CellLockState{Supported: false}, true, nil
		}
		return modem.CellLockState{}, false, fmt.Errorf("modem does not report a +QNWLOCK line (unsupported firmware?)")
	}
	return state, false, nil
}

// cellLockStatus reports the live lock state from the modem plus the saved
// preference. A modem that rejects AT+QNWLOCK? (the QDC507 answers the
// query with ERROR while accepting the SET) returns 200 with
// supported=false — an unsupported query is never reported as locked, and
// never as a transport failure either.
func (a *app) cellLockStatus(w http.ResponseWriter, _ *http.Request) {
	if a.demo {
		writeJSON(w, http.StatusOK, map[string]any{"locked": false, "supported": true, "demo": true})
		return
	}
	if err := a.requireModemTransport(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "modem unavailable: "+err.Error())
		return
	}
	var state modem.CellLockState
	if a.modem != nil {
		var err error
		state, err = a.modem.QueryCellLock()
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
	} else {
		var err error
		var unsupported bool
		state, unsupported, err = a.queryCellLockUSBAT()
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		if unsupported {
			iccid, saved := a.loadRadioPreference()
			writeJSON(w, http.StatusOK, map[string]any{
				"locked": false, "supported": false, "iccid": iccid, "saved": saved,
				"note": "this modem rejects AT+QNWLOCK? (query unsupported on this firmware); the lock SET command is accepted but its state cannot be read back — lock/unlock replies carry verified=false",
			})
			return
		}
	}
	iccid, saved := a.loadRadioPreference()
	resp := map[string]any{
		"locked": state.Locked, "earfcn": state.EARFCN, "pci": state.PCI,
		"supported": state.Supported, "iccid": iccid, "saved": saved,
	}
	if state.Raw != "" {
		resp["raw"] = state.Raw
	}
	writeJSON(w, http.StatusOK, resp)
}

// radioPreferences returns the persisted lock/operator preferences for
// the current ICCID (if any). Always 200 — it is a local read.
func (a *app) radioPreferences(w http.ResponseWriter, _ *http.Request) {
	iccid, saved := a.loadRadioPreference()
	writeJSON(w, http.StatusOK, map[string]any{"iccid": iccid, "saved": saved})
}

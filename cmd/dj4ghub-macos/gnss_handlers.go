package main

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// GNSS + per-IMEI multi-module (Item 6).
//
// GNSS rides AT+QGPS (Quectel): AT+QGPS=1 powers the receiver, AT+QGPSLOC?
// returns the fix. The receiver is off by default; enabling it is
// explicit (POST /api/gnss) and the state is reported honestly: without a
// satellite fix the module answers +CME ERROR: 516 and the endpoint says
// fix=false — it never invents a position. A fix needs a GPS antenna and
// sky view; indoors on the MacBook the QDC507 reports 516 until it has
// one.
//
// Multi-module: GET /api/modules enumerates every attached 2ca3 device
// (ioreg, grouped by locationID). Each module is keyed by its IMEI
// (AT+CGSN) when it is the active one; a second attached module is listed
// with its USB identity and active=false — its IMEI/ICCID are only known
// after opening it, and this build drives one module at a time.

type gnssStatus struct {
	IMEI       string   `json:"imei,omitempty"`
	Enabled    bool     `json:"enabled"`
	Fix        bool     `json:"fix"`
	Latitude   *float64 `json:"latitude,omitempty"`
	Longitude  *float64 `json:"longitude,omitempty"`
	AltitudeM  *float64 `json:"altitude_m,omitempty"`
	HDOP       *float64 `json:"hdop,omitempty"`
	SpeedKmh   *float64 `json:"speed_kmh,omitempty"`
	Satellites *int     `json:"satellites,omitempty"`
	UTCTime    string   `json:"utc_time,omitempty"`
	Date       string   `json:"date,omitempty"`
	Raw        string   `json:"raw,omitempty"`
	Note       string   `json:"note,omitempty"`
}

func (a *app) queryIMEI() string {
	if a.modem != nil {
		if imei, err := a.modem.QueryIMEI(); err == nil {
			return strings.TrimSpace(imei)
		}
		return ""
	}
	resp, err := a.runATCommand("AT+CGSN", 3*time.Second)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "AT") || line == "OK" || line == "ERROR" {
			continue
		}
		return line
	}
	return ""
}

// parseQGPSLOC parses Quectel +QGPSLOC:
// <UTC>,<lat>,<lon>,<hdop>,<alt>,<fix>,<cog>,<spkm>,<spkn>,<date>,<nsat>
// with lat like 3113.3937N and lon like 12121.2687E (ddmm.mmmm + hemisphere).
func parseQGPSLOC(resp string) (*gnssStatus, bool) {
	st := &gnssStatus{}
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "+QGPSLOC:") {
			st.Raw = strings.TrimSpace(strings.TrimPrefix(line, "+QGPSLOC:"))
			break
		}
	}
	if st.Raw == "" {
		return nil, false
	}
	fields := strings.Split(st.Raw, ",")
	if len(fields) < 11 {
		return st, true // unparsable shape: surface raw, no fix claimed
	}
	st.UTCTime = strings.TrimSpace(fields[0])
	latText, lonText := strings.TrimSpace(fields[1]), strings.TrimSpace(fields[2])
	if lat, err := parseNMEACoord(latText); err == nil && (strings.HasSuffix(latText, "N") || strings.HasSuffix(latText, "S")) {
		st.Latitude = &lat
	}
	if lon, err := parseNMEACoord(lonText); err == nil && (strings.HasSuffix(lonText, "E") || strings.HasSuffix(lonText, "W")) {
		st.Longitude = &lon
	}
	if v, err := parseFiniteFloat(fields[3]); err == nil && v >= 0 {
		st.HDOP = &v
	}
	if v, err := parseFiniteFloat(fields[4]); err == nil {
		st.AltitudeM = &v
	}
	fix, _ := strconv.Atoi(strings.TrimSpace(fields[5]))
	st.Fix = fix >= 2 && st.Latitude != nil && st.Longitude != nil
	if v, err := parseFiniteFloat(fields[7]); err == nil && v >= 0 {
		st.SpeedKmh = &v
	}
	st.Date = strings.TrimSpace(fields[9])
	if n, err := strconv.Atoi(strings.TrimSpace(fields[10])); err == nil && n >= 0 {
		st.Satellites = &n
	}
	return st, true
}

func parseFiniteFloat(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("invalid finite number")
	}
	return v, nil
}

// parseNMEACoord converts ddmm.mmmmN/S or dddmm.mmmmE/W to decimal degrees.
func parseNMEACoord(s string) (float64, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("short coordinate %q", s)
	}
	hemi := s[len(s)-1]
	digits := s[:len(s)-1]
	v, err := parseFiniteFloat(digits)
	if err != nil {
		return 0, err
	}
	deg := float64(int(v / 100))
	min := v - deg*100
	limit := 180.0
	if hemi == 'N' || hemi == 'S' {
		limit = 90
	}
	if v < 0 || min < 0 || min >= 60 || deg > limit || (deg == limit && min != 0) {
		return 0, fmt.Errorf("coordinate outside valid range")
	}
	dec := deg + min/60
	if hemi == 'S' || hemi == 'W' {
		dec = -dec
	} else if hemi != 'N' && hemi != 'E' {
		return 0, fmt.Errorf("unknown hemisphere %q", s)
	}
	return dec, nil
}

func (a *app) gnssStatus(w http.ResponseWriter, _ *http.Request) {
	if a.demo {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": false, "fix": false, "demo": true,
			"note": "demo mode: no GNSS receiver",
		})
		return
	}
	st, err := a.queryGNSS()
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// queryGNSS reads the receiver state + last fix. Shared by the HTTP
// handler and the MCP gnss_status tool.
func (a *app) queryGNSS() (*gnssStatus, error) {
	if err := a.requireModemTransport(); err != nil {
		return nil, err
	}
	st := &gnssStatus{IMEI: a.queryIMEI()}
	resp, err := a.runATCommand("AT+QGPS?", 3*time.Second)
	if err != nil {
		return nil, err
	}
	if strings.Contains(resp, "+QGPS: 1") {
		st.Enabled = true
	}
	locResp, err := a.runATCommand("AT+QGPSLOC?", 5*time.Second)
	if err != nil {
		return nil, err
	}
	if parsed, ok := parseQGPSLOC(locResp); ok {
		st.Raw = parsed.Raw
		st.UTCTime, st.Date = parsed.UTCTime, parsed.Date
		st.Latitude, st.Longitude = parsed.Latitude, parsed.Longitude
		st.AltitudeM, st.HDOP, st.SpeedKmh = parsed.AltitudeM, parsed.HDOP, parsed.SpeedKmh
		st.Satellites = parsed.Satellites
		st.Fix = parsed.Fix
	} else if strings.Contains(locResp, "516") || strings.Contains(locResp, "ERROR") {
		st.Fix = false
		st.Note = "no satellite fix yet (module reports CME ERROR 516): needs a GPS antenna and sky view; receiver state is reported, no position is invented"
	} else {
		st.Fix = false
		st.Raw = strings.TrimSpace(locResp)
	}
	if st.Enabled && !st.Fix && st.Note == "" {
		st.Note = "receiver on, no fix yet"
	}
	return st, nil
}

func (a *app) setGNSS(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	if a.demo {
		writeError(w, http.StatusServiceUnavailable, "demo mode: no GNSS receiver")
		return
	}
	if err := a.requireModemTransport(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "modem unavailable: "+err.Error())
		return
	}
	cmd := "AT+QGPS=0"
	if *body.Enabled {
		cmd = "AT+QGPS=1"
	}
	resp, err := a.runATCommand(cmd, 5*time.Second)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if atResponseIsError(resp) {
		writeError(w, http.StatusBadGateway, "modem rejected GNSS switch: "+strings.TrimSpace(resp))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": *body.Enabled, "imei": a.queryIMEI()})
}

// --- multi-module ---------------------------------------------------------

type moduleEntry struct {
	LocationID string               `json:"location_id"`
	VendorID   string               `json:"vendor_id"`
	ProductID  string               `json:"product_id"`
	Product    string               `json:"product"`
	Vendor     string               `json:"vendor"`
	Speed      string               `json:"speed,omitempty"`
	Mode       string               `json:"mode,omitempty"`
	Interfaces []usbInterfaceStatus `json:"interfaces,omitempty"`
	Active     bool                 `json:"active"`
	IMEI       string               `json:"imei,omitempty"`
	ICCID      string               `json:"iccid,omitempty"`
	Note       string               `json:"note,omitempty"`
}

func (a *app) listModules(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.queryModules())
}

// queryModules enumerates attached modules. Shared by the HTTP handler
// and the MCP modules tool.
func (a *app) queryModules() map[string]any {
	devices := discoverAllDJIUSBDevices()
	entries := []moduleEntry{}
	activeLoc := ""
	if a.usbDevice != nil {
		activeLoc = a.usbDevice.LocationID
	}
	for _, d := range devices {
		e := moduleEntry{
			LocationID: d.LocationID, VendorID: d.VendorID, ProductID: d.ProductID,
			Product: d.Product, Vendor: d.Vendor, Speed: d.Speed, Mode: d.Mode,
			Interfaces: d.Interfaces, Active: d.LocationID != "" && d.LocationID == activeLoc,
		}
		if e.Active {
			e.IMEI = a.queryIMEI()
			if iccid, ok := a.currentICCID(); ok {
				e.ICCID = iccid
			}
		} else {
			e.Note = "attached but not active: IMEI/ICCID are only known after opening the module; this build drives one module at a time"
		}
		entries = append(entries, e)
	}
	return map[string]any{
		"count": len(entries), "modules": entries,
		"note": "one active module at a time; per-IMEI identity (IMEI/ICCID) is reported for the active module, per-ICCID radio preferences live under /api/radio/preferences",
	}
}

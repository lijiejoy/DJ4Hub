package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
)

//go:embed web/*
var webAssets embed.FS

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", a.health)
	mux.HandleFunc("GET /api/status", a.status)
	a.registerSubscriptionRoutes(mux)
	a.registerHistoryRoutes(mux)
	a.registerCallsRoutes(mux)
	a.registerConversationsRoutes(mux)
	a.registerDiagnosticsRoutes(mux)
	mux.HandleFunc("GET /api/sms", a.listSMS)
	mux.HandleFunc("GET /api/sms/status", a.smsStatus)
	mux.HandleFunc("POST /api/sms/send", a.sendSMS)
	mux.HandleFunc("POST /api/sms/refresh", a.refreshSMS)
	mux.HandleFunc("POST /api/sms/clear-module", a.clearModuleSMS)
	mux.HandleFunc("POST /api/at", a.executeAT)
	mux.HandleFunc("GET /api/network", a.networkDiagnostic)
	mux.HandleFunc("GET /api/network/local", a.localNetworkConnection)
	mux.HandleFunc("GET /api/network/activity", a.networkActivity)
	mux.HandleFunc("GET /api/network/traffic", a.networkTraffic)
	mux.HandleFunc("POST /api/network/check-4g", a.check4GRoute)
	mux.HandleFunc("POST /api/network/check-proxy", a.checkProxyRoute)
	mux.HandleFunc("POST /api/network/usbnet", a.setUSBNetMode)
	mux.HandleFunc("POST /api/network/reboot-module", a.rebootModule)
	mux.HandleFunc("GET /api/esim", a.esimOverview)
	mux.HandleFunc("GET /api/esim/notes", a.listESIMNotes)
	mux.HandleFunc("PUT /api/esim/notes", a.saveESIMNote)
	mux.HandleFunc("GET /api/esim/module-notes", a.listModuleESIMNotes)
	mux.HandleFunc("PUT /api/esim/module-notes", a.saveModuleESIMNote)
	mux.HandleFunc("GET /api/esim/health", a.esimHealth)
	mux.HandleFunc("POST /api/esim/phonebook/probe", a.probeESIMPhonebook)
	mux.HandleFunc("POST /api/esim/switch", a.switchESIM)
	mux.HandleFunc("PATCH /api/esim/profile", a.renameESIMProfile)
	mux.HandleFunc("DELETE /api/esim/profile", a.deleteESIMProfile)
	mux.HandleFunc("POST /api/esim/download", a.downloadESIMProfile)
	mux.HandleFunc("GET /api/sip/status", a.sipStatus)
	mux.HandleFunc("POST /api/sip/call", a.sipCall)
	mux.HandleFunc("POST /api/sip/hangup", a.sipHangup)
	mux.HandleFunc("POST /api/sip/answer", a.sipAnswer)
	mux.HandleFunc("POST /api/sip/dtmf", a.sipDTMF)
	mux.HandleFunc("POST /api/sip/audio-controls", a.sipAudioControls)
	mux.HandleFunc("GET /api/sip/audio-devices", a.sipAudioDevices)
	mux.HandleFunc("POST /api/sip/audio-devices", a.sipAudioDevices)
	mux.HandleFunc("POST /api/sip/panel", a.sipShowPanel)
	mux.HandleFunc("GET /api/sip/settings", a.sipSettings)
	mux.HandleFunc("POST /api/sip/settings", a.sipSettings)
	mux.HandleFunc("POST /api/ussd", a.startUSSD)
	mux.HandleFunc("POST /api/ussd/continue", a.continueUSSD)
	mux.HandleFunc("POST /api/ussd/cancel", a.cancelUSSD)
	mux.HandleFunc("GET /api/ussd/status", a.ussdStatus)
	mux.HandleFunc("GET /api/ussd/ims", a.ussdIMS)
	mux.HandleFunc("GET /api/sms/delivery", a.listDeliveryReports)
	mux.HandleFunc("GET /api/radio/cell", a.servingCell)
	mux.HandleFunc("GET /api/radio/operators", a.listOperators)
	mux.HandleFunc("POST /api/radio/operators/scan", a.forceOperatorScan)
	mux.HandleFunc("GET /api/radio/operator", a.getOperator)
	mux.HandleFunc("POST /api/radio/operator", a.setOperator)
	mux.HandleFunc("POST /api/radio/cell-lock", a.lockCell)
	mux.HandleFunc("GET /api/radio/cell-lock", a.cellLockStatus)
	mux.HandleFunc("POST /api/radio/cell-unlock", a.unlockCell)
	mux.HandleFunc("GET /api/radio/preferences", a.radioPreferences)
	mux.HandleFunc("GET /api/tasks", a.listTasks)
	mux.HandleFunc("POST /api/tasks", a.createTask)
	mux.HandleFunc("POST /api/tasks/enable", a.setTaskEnabled)
	mux.HandleFunc("POST /api/tasks/delete", a.deleteTask)
	mux.HandleFunc("POST /api/tasks/run-now", a.runTaskNow)
	mux.HandleFunc("GET /api/gnss", a.gnssStatus)
	mux.HandleFunc("POST /api/gnss", a.setGNSS)
	mux.HandleFunc("GET /api/modules", a.listModules)
	mux.HandleFunc("POST /api/mcp", a.handleMCP)
	mux.HandleFunc("GET /api/mcp/status", a.mcpStatus)
	mux.HandleFunc("GET /api/notify/settings", a.notifySettingsGet)
	mux.HandleFunc("POST /api/notify/settings", a.notifySettingsSave)
	mux.HandleFunc("POST /api/notify/test", a.notifyTest)
	mux.HandleFunc("GET /api/notify/log", a.notifyLogHandler)
	content, _ := fs.Sub(webAssets, "web")
	mux.Handle("/", http.FileServer(http.FS(content)))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (a *app) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.healthSnapshot())
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

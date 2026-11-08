package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/esim"
	"github.com/damonto/euicc-go/driver"
)

func (a *app) initUSBATESIMManager() {
	if manager, _ := a.currentESIMManager(); manager != nil {
		return
	}
	esimManager, err := esim.NewManager(esim.ManagerOptions{
		DeviceID:  "mac-usbat",
		Transport: "custom",
		SmartCardChannelFactory: func() (driver.SmartCardChannel, error) {
			return newUSBATESIMChannel(a.runATCommand), nil
		},
	})
	if err != nil {
		log.Printf("eSIM manager unavailable over USB AT: %v", err)
		return
	}
	if a.installESIMManager(esimManager, true) {
		log.Printf("eSIM manager is available over USB AT with profile switching enabled")
	}
}

func (a *app) currentESIMManager() (*esim.Manager, bool) {
	a.esimMu.RLock()
	defer a.esimMu.RUnlock()
	return a.esim, a.esimSwitchAllowed
}

func (a *app) installESIMManager(manager *esim.Manager, switchAllowed bool) bool {
	if manager == nil {
		return false
	}
	a.esimMu.Lock()
	defer a.esimMu.Unlock()
	if a.esim != nil {
		return false
	}
	a.esim = manager
	a.esimSwitchAllowed = switchAllowed
	return true
}

func (a *app) esimOverview(w http.ResponseWriter, _ *http.Request) {
	if a.demo {
		writeJSON(w, http.StatusOK, map[string]any{
			"chip_info": map[string]any{
				"sku_name":      "eUICC Demo Card",
				"serial_number": "DEMO-001",
				"firmware":      "1.0.0",
				"eids": []map[string]any{{
					"aid": "A0000005591010FFFFFFFF8900000100",
					"eid": "89049032000000000000000000000001",
				}},
			},
			"profiles": []map[string]any{{
				"eid":     "89049032000000000000000000000001",
				"aid_hex": "A0000005591010FFFFFFFF8900000100",
				"profiles": []map[string]any{
					{
						"iccid": "89860123456789012345", "name": "中国移动",
						"service_provider_name": "China Mobile", "state": 1, "state_text": "enabled",
					},
					{
						"iccid": "8944100000000000001", "name": "英国旅行卡",
						"service_provider_name": "giffgaff UK", "state": 0, "state_text": "disabled",
					},
					{
						"iccid": "8949020000000000002", "name": "欧洲数据卡",
						"service_provider_name": "Travel Europe", "state": 0, "state_text": "disabled",
					},
				},
			}},
		})
		return
	}
	esimManager, _ := a.currentESIMManager()
	if esimManager == nil {
		writeError(w, http.StatusServiceUnavailable, "eSIM manager is unavailable")
		return
	}
	overview, err := esimManager.GetEsimOverview()
	if err != nil {
		if isPhysicalSIMESIMProbeError(err) {
			writeJSON(w, http.StatusOK, map[string]any{
				"card_type": "physical_sim",
				"message":   "当前卡片为实体卡，非 eSIM 卡片",
			})
			return
		}
		log.Printf("eSIM overview failed: %v", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

// A normal physical SIM cannot open the GSMA eUICC management AIDs. The
// manager reports that as no eUICC discovered with an AT+CCHO ERROR; expose it
// as a neutral card type instead of leaking an implementation error to the UI.
func isPhysicalSIMESIMProbeError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "未发现任何 euicc") &&
		strings.Contains(message, "at+ccho") &&
		strings.Contains(message, "error")
}

func (a *app) esimHealth(w http.ResponseWriter, _ *http.Request) {
	esimManager, _ := a.currentESIMManager()
	if esimManager == nil {
		writeError(w, http.StatusServiceUnavailable, "eSIM manager is unavailable")
		return
	}
	overview, err := esimManager.GetEsimOverview()
	if err != nil {
		if isPhysicalSIMESIMProbeError(err) {
			writeJSON(w, http.StatusOK, map[string]any{"card_type": "physical_sim"})
			return
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	var active *esim.ProfileItem
	for _, group := range overview.Profiles {
		for index := range group.Profiles {
			if group.Profiles[index].State == 1 {
				active = &group.Profiles[index]
				break
			}
		}
		if active != nil {
			break
		}
	}
	if active == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "eSIM 卡片已识别，但没有已启用的 Profile"})
		return
	}

	if err := a.ensureUSBAT(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	status, err := a.usbATStatus()
	if err != nil {
		a.resetUSBATIfGone(err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	registered := status.RegStatus == 1 || status.RegStatus == 5
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             status.SimInserted && registered,
		"active_profile": active,
		"module_iccid":   status.ICCID,
		"imsi":           status.IMSI,
		"operator":       status.Operator,
		"registration":   status.RegStatusText,
		"registered":     registered,
		"signal_dbm":     status.SignalDBM,
		"network_mode":   status.NetworkMode,
	})
}

func (a *app) switchESIM(w http.ResponseWriter, r *http.Request) {
	esimManager, switchAllowed := a.currentESIMManager()
	if !a.demo && esimManager == nil {
		writeError(w, http.StatusServiceUnavailable, "eSIM manager is unavailable")
		return
	}
	var body struct {
		ICCID string `json:"iccid"`
		AID   string `json:"aid"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.ICCID) == "" {
		writeError(w, http.StatusBadRequest, "iccid is required")
		return
	}
	if a.demo {
		writeJSON(w, http.StatusOK, map[string]any{
			"switch_accepted": true,
			"phase":           "done",
			"target_iccid":    body.ICCID,
		})
		return
	}
	if !switchAllowed && a.modem == nil {
		writeError(w, http.StatusServiceUnavailable, "USB AT eSIM/卡片当前暂不允许切换 Profile")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	result, err := esimManager.SwitchProfileWithResult(ctx, body.ICCID, body.AID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	// Enabling a Profile resets the eUICC, but the DJI modem can keep the
	// previous SIM session (and therefore its CNUM) until its own firmware is
	// restarted.  Reload it here so the newly enabled Profile actually becomes
	// the modem's active subscriber identity.
	rebootResponse, rebootErr := a.runATCommand("AT+CFUN=1,1", 3*time.Second)
	rebootRequested := rebootErr == nil
	rebootWarning := ""
	if rebootErr != nil {
		// A USB disconnect immediately after CFUN is expected on some firmware;
		// the command may already have been accepted before the bridge drops.
		upper := strings.ToUpper(rebootErr.Error())
		if strings.Contains(upper, "NO_DEVICE") || strings.Contains(upper, "NOT_FOUND") {
			rebootRequested = true
		} else {
			rebootWarning = rebootErr.Error()
			log.Printf("eSIM profile switched but module restart was not confirmed: %v", rebootErr)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"switch_accepted":         result.SwitchAccepted,
		"phase":                   result.Phase,
		"target_iccid":            result.TargetICCID,
		"recovery_pending":        result.RecoveryPending,
		"module_reboot_requested": rebootRequested,
		"module_reboot_response":  rebootResponse,
		"module_reboot_warning":   rebootWarning,
		"reconnect_wait_seconds":  10,
	})
}

func (a *app) renameESIMProfile(w http.ResponseWriter, r *http.Request) {
	esimManager, _ := a.currentESIMManager()
	if !a.demo && esimManager == nil {
		writeError(w, http.StatusServiceUnavailable, "eSIM manager is unavailable")
		return
	}
	var body struct {
		ICCID string `json:"iccid"`
		AID   string `json:"aid"`
		Name  string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.ICCID = strings.TrimSpace(body.ICCID)
	body.Name = strings.TrimSpace(body.Name)
	if body.ICCID == "" || body.Name == "" {
		writeError(w, http.StatusBadRequest, "iccid and name are required")
		return
	}
	if a.demo {
		writeJSON(w, http.StatusOK, map[string]string{"message": "Profile 名称修改成功"})
		return
	}
	if err := esimManager.RenameProfile(body.ICCID, body.Name, body.AID); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("修改 Profile 名称失败: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Profile 名称修改成功"})
}

func (a *app) deleteESIMProfile(w http.ResponseWriter, r *http.Request) {
	esimManager, _ := a.currentESIMManager()
	if !a.demo && esimManager == nil {
		writeError(w, http.StatusServiceUnavailable, "eSIM manager is unavailable")
		return
	}
	var body struct {
		ICCID string `json:"iccid"`
		AID   string `json:"aid"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.ICCID = strings.TrimSpace(body.ICCID)
	if body.ICCID == "" {
		writeError(w, http.StatusBadRequest, "iccid is required")
		return
	}
	if a.demo {
		writeJSON(w, http.StatusOK, map[string]string{"message": "Profile 已删除"})
		return
	}
	result, err := esimManager.DeleteProfile(body.ICCID, body.AID)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("删除 Profile 失败: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *app) downloadESIMProfile(w http.ResponseWriter, r *http.Request) {
	esimManager, _ := a.currentESIMManager()
	if !a.demo && esimManager == nil {
		writeError(w, http.StatusServiceUnavailable, "eSIM manager is unavailable")
		return
	}
	var body struct {
		SMDP             string `json:"smdp"`
		MatchingID       string `json:"matching_id"`
		ConfirmationCode string `json:"confirmation_code"`
		AID              string `json:"aid"`
		IMEI             string `json:"imei"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.SMDP = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(body.SMDP, "https://"), "http://"))
	if body.SMDP == "" {
		writeError(w, http.StatusBadRequest, "smdp is required")
		return
	}
	if strings.TrimSpace(body.IMEI) == "" {
		writeError(w, http.StatusBadRequest, "imei is required for USB AT eSIM download")
		return
	}
	if a.demo {
		writeJSON(w, http.StatusOK, map[string]string{"message": "演示：Profile 下载完成"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	result, err := esimManager.DownloadProfile(ctx, body.AID, body.SMDP, body.MatchingID, body.ConfirmationCode, body.IMEI, func(event esim.DownloadProgressEvent) {
		log.Printf("eSIM download %d%% %s", event.Pct, event.Msg)
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("下载 Profile 失败: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, result)
}

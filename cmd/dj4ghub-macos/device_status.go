package main

import (
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/modem"
)

func (a *app) status(w http.ResponseWriter, _ *http.Request) {
	if a.demo {
		writeJSON(w, http.StatusOK, modem.DeviceStatus{
			IMEI:          "867400000000001",
			Firmware:      "EG25GGBR07A08M2G",
			ICCID:         "89860123456789012345",
			IMSI:          "460001234567890",
			Operator:      "China Mobile",
			SimInserted:   true,
			SignalDBM:     -73,
			SignalRSRP:    -96,
			SignalRSRQ:    -9,
			RegStatus:     1,
			RegStatusText: "已注册",
			NetworkMode:   "LTE",
			NetworkDuplex: "FDD",
			RadioBand:     "B3",
			USBNetMode:    0,
		})
		return
	}
	if a.modem == nil {
		// A libusb handle may survive a physical unplug. Refresh the macOS USB
		// inventory before using it so the UI never reports a stale connection.
		if a.usbAT != nil && a.currentUSBDevice() == nil {
			a.markUSBATDetached("DJI USB device disconnected")
		}
		if err := a.ensureUSBAT(); err != nil {
			log.Printf("USB AT retry failed: %v", err)
		}
		if a.usbAT != nil {
			status, err := a.usbATStatus()
			if err == nil {
				writeJSON(w, http.StatusOK, status)
				return
			}
			a.resetUSBATIfGone(err)
			log.Printf("USB AT status failed: %v", err)
		}
		usbDevice := a.currentUSBDevice()
		summary := "未发现 AT 串口"
		operator := "未连接"
		network := "不可用"
		if usbDevice != nil {
			summary = fmt.Sprintf("%s %s (%s:%s)", usbDevice.Vendor, usbDevice.Product, usbDevice.VendorID, usbDevice.ProductID)
			operator = "已检测到 USB 设备"
			network = usbDevice.Mode
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"operator":        operator,
			"signal_dbm":      nil,
			"network_mode":    network,
			"sim_inserted":    false,
			"hardware_status": summary,
			"discovery_error": a.discoveryError,
			"usb_device":      usbDevice,
		})
		return
	}
	writeJSON(w, http.StatusOK, a.modem.GetFullStatus())
}

func (a *app) currentUSBDevice() *usbDeviceStatus {
	if a.modem != nil || a.demo {
		return a.usbDevice
	}
	usbDevice := discoverDJIUSBDevice()
	// Never retain the last successful scan: that is stale after an unplug.
	a.usbDevice = usbDevice
	return usbDevice
}

func (a *app) usbATStatus() (modem.DeviceStatus, error) {
	firmwareResp, _ := a.usbAT.Command("ATI", 3*time.Second)
	cpinResp, cpinErr := a.usbAT.Command("AT+CPIN?", 3*time.Second)
	csqResp, _ := a.usbAT.Command("AT+CSQ", 3*time.Second)
	ceregResp, _ := a.usbAT.Command("AT+CEREG?", 3*time.Second)
	cregResp, _ := a.usbAT.Command("AT+CREG?", 3*time.Second)
	_, _ = a.usbAT.Command("AT+COPS=3,2", 3*time.Second)
	copsResp, _ := a.usbAT.Command("AT+COPS?", 3*time.Second)
	qccidResp, _ := a.usbAT.Command("AT+QCCID", 3*time.Second)
	cimiResp, _ := a.usbAT.Command("AT+CIMI", 3*time.Second)
	qnwinfoResp, _ := a.usbAT.Command("AT+QNWINFO", 3*time.Second)
	usbnetResp, _ := a.usbAT.Command(`AT+QCFG="usbnet"`, 3*time.Second)

	if cpinErr != nil {
		return modem.DeviceStatus{}, cpinErr
	}

	regStatus := firstNonZeroRegistration(ceregResp, cregResp)
	mode, duplex, band, channel := parseUSBATQNWInfo(qnwinfoResp)
	usbnetMode := -1
	if parsedMode, err := strconv.Atoi(parseUSBNetMode(usbnetResp)); err == nil {
		usbnetMode = parsedMode
	}
	status := modem.DeviceStatus{
		Firmware:      parseUSBATFirmware(firmwareResp),
		ICCID:         parseUSBATPrefixed(qccidResp, "+QCCID:"),
		IMSI:          parseUSBATIMSI(cimiResp),
		Operator:      parseUSBATOperator(copsResp),
		SimInserted:   strings.Contains(strings.ToUpper(cpinResp), "READY"),
		SignalDBM:     parseUSBATCSQDBM(csqResp),
		RegStatus:     regStatus,
		RegStatusText: registrationText(regStatus),
		NetworkMode:   mode,
		NetworkDuplex: duplex,
		RadioBand:     band,
		RadioChannel:  channel,
		USBNetMode:    usbnetMode,
	}
	if status.Operator == "" && strings.Contains(copsResp, "CHN-UNICOM") {
		status.Operator = "CHN-UNICOM"
	}
	return status, nil
}

func parseUSBATFirmware(resp string) string {
	lines := splitATLines(resp)
	var useful []string
	for _, line := range lines {
		up := strings.ToUpper(line)
		if strings.HasPrefix(up, "ATI") || up == "OK" {
			continue
		}
		useful = append(useful, line)
	}
	return strings.Join(useful, " · ")
}

func splitATLines(resp string) []string {
	resp = strings.ReplaceAll(resp, "\r", "\n")
	raw := strings.Split(resp, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func parseUSBATPrefixed(resp, prefix string) string {
	for _, line := range splitATLines(resp) {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func parseUSBATIMSI(resp string) string {
	for _, line := range splitATLines(resp) {
		up := strings.ToUpper(line)
		if up == "OK" || strings.HasPrefix(up, "AT") {
			continue
		}
		if _, err := strconv.ParseUint(line, 10, 64); err == nil && len(line) >= 5 {
			return line
		}
	}
	return ""
}

func parseUSBATCSQDBM(resp string) int {
	re := regexp.MustCompile(`\+CSQ:\s*(\d+),`)
	match := re.FindStringSubmatch(resp)
	if len(match) != 2 {
		return 0
	}
	rssi, err := strconv.Atoi(match[1])
	if err != nil || rssi == 99 {
		return 0
	}
	return -113 + 2*rssi
}

func parseUSBATOperator(resp string) string {
	re := regexp.MustCompile(`\+COPS:\s*\d+,\d+,"([^"]*)"`)
	match := re.FindStringSubmatch(resp)
	if len(match) != 2 {
		return ""
	}
	return modem.ResolveServingOperatorNameFromPLMN(match[1])
}

func firstNonZeroRegistration(responses ...string) int {
	for _, resp := range responses {
		re := regexp.MustCompile(`\+(?:CE)?REG:\s*\d+,(\d+)`)
		match := re.FindStringSubmatch(resp)
		if len(match) != 2 {
			continue
		}
		status, err := strconv.Atoi(match[1])
		if err == nil && status != 0 {
			return status
		}
	}
	return 0
}

func registrationText(status int) string {
	switch status {
	case 1:
		return "已注册"
	case 5:
		return "漫游注册"
	case 2:
		return "搜索中"
	case 3:
		return "注册被拒绝"
	default:
		return "未注册"
	}
}

func parseUSBATQNWInfo(resp string) (mode, duplex, band string, channel uint32) {
	re := regexp.MustCompile(`\+QNWINFO:\s*"([^"]*)","[^"]*","([^"]*)",(\d+)`)
	match := re.FindStringSubmatch(resp)
	if len(match) != 4 {
		return "", "", "", 0
	}
	mode = match[1]
	radio := match[2]
	if strings.Contains(strings.ToUpper(mode), "FDD") {
		duplex = "FDD"
		mode = strings.TrimSpace(strings.TrimPrefix(mode, "FDD"))
	}
	if strings.Contains(strings.ToUpper(mode), "TDD") {
		duplex = "TDD"
		mode = strings.TrimSpace(strings.TrimPrefix(mode, "TDD"))
	}
	band = strings.TrimPrefix(radio, "LTE ")
	if value, err := strconv.ParseUint(match[3], 10, 32); err == nil {
		channel = uint32(value)
	}
	return mode, duplex, band, channel
}

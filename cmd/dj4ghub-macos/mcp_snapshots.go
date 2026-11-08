package main

import (
	"time"

	"github.com/WongLoki/DJ4Hub/internal/modem"
)

// Read-only snapshots shared by the MCP tools and the HTTP handlers they
// mirror (Item 7). Each returns the same data its HTTP endpoint serves,
// so MCP can never show a different state from the console.

func (a *app) healthSnapshot() map[string]any {
	usbDevice := a.currentUSBDevice()
	esimManager, _ := a.currentESIMManager()
	return map[string]any{
		"ok": true, "port": a.port, "esim_available": a.demo || esimManager != nil, "demo": a.demo,
		"usb_device": usbDevice, "discovery_error": a.discoveryError,
		"service": serviceIdentity,
	}
}

func (a *app) servingCellSnapshot() (any, *mcpError) {
	if a.demo {
		return map[string]any{
			"rat": "LTE", "earfcn": 3740, "pci": 132, "cell_id": "8401A29",
			"rsrp": -75, "rsrq": -8, "sinr": 11, "duplex": "FDD",
			"band": "LTE BAND 8", "channel": 3740, "demo": true,
		}, nil
	}
	if err := a.requireModemTransport(); err != nil {
		return nil, &mcpError{Code: -32002, Message: "modem unavailable: " + err.Error()}
	}
	if a.modem != nil {
		info, err := a.modem.QueryServingCellLTEInfo()
		if err != nil {
			return nil, &mcpError{Code: -32002, Message: err.Error()}
		}
		return servingCellJSON(info), nil
	}
	resp, err := a.runATCommand(`AT+QENG="servingcell"`, 5*time.Second)
	if err != nil {
		return nil, &mcpError{Code: -32002, Message: err.Error()}
	}
	info, ok := modem.ParseServingCellLTEInfo(resp)
	if !ok {
		return nil, &mcpError{Code: -32002, Message: "serving cell not reported (non-LTE or no service)"}
	}
	return servingCellJSON(info), nil
}

func (a *app) gnssSnapshot() (any, *mcpError) {
	st, err := a.queryGNSS()
	if err != nil {
		return nil, &mcpError{Code: -32002, Message: err.Error()}
	}
	return st, nil
}

func (a *app) modulesSnapshot() (any, *mcpError) {
	return a.queryModules(), nil
}

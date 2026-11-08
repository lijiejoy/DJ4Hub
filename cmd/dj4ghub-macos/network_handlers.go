package main

import (
	"encoding/csv"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type networkDiagnostic struct {
	USBNetMode        string            `json:"usbnet_mode"`
	USBCfg            string            `json:"usbcfg"`
	PDPContexts       []pdpContext      `json:"pdp_contexts"`
	ActiveContexts    []int             `json:"active_contexts"`
	PDPAddresses      []string          `json:"pdp_addresses"`
	MacInterfaces     []macNetInterface `json:"mac_interfaces"`
	DefaultRoute      macDefaultRoute   `json:"default_route"`
	USBNetworkPresent bool              `json:"usb_network_present"`
	USBDevice         *usbDeviceStatus  `json:"usb_device,omitempty"`
	Raw               map[string]string `json:"raw,omitempty"`
	Errors            map[string]string `json:"errors,omitempty"`
}

type pdpContext struct {
	ID  int    `json:"id"`
	PDN string `json:"pdn"`
	APN string `json:"apn"`
}

type macNetInterface struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	IPv4   string `json:"ipv4"`
	Kind   string `json:"kind"`
}

type macDefaultRoute struct {
	Interface string `json:"interface"`
	Gateway   string `json:"gateway"`
}

type localNetworkConnection struct {
	Interface string `json:"interface"`
	IPv4      string `json:"ipv4"`
	IsDefault bool   `json:"is_default"`
}

type networkActivitySnapshot struct {
	Available         bool                    `json:"available"`
	PhysicalInterface string                  `json:"physical_interface,omitempty"`
	PhysicalIPv4      string                  `json:"physical_ipv4,omitempty"`
	TunnelInterface   string                  `json:"tunnel_interface,omitempty"`
	PhysicalActive    bool                    `json:"physical_active"`
	SampledAtMS       int64                   `json:"sampled_at_ms"`
	Connections       []networkActivityRecord `json:"connections"`
}

type networkActivityRecord struct {
	Process   string `json:"process"`
	Host      string `json:"host,omitempty"`
	IP        string `json:"ip"`
	Port      string `json:"port,omitempty"`
	Protocol  string `json:"protocol"`
	Interface string `json:"interface"`
	State     string `json:"state,omitempty"`
	RXBytes   uint64 `json:"rx_bytes"`
	TXBytes   uint64 `json:"tx_bytes"`
}

type networkByteCounters struct {
	RX uint64
	TX uint64
}

type networkTrafficSnapshot struct {
	Available    bool   `json:"available"`
	Interface    string `json:"interface,omitempty"`
	RXBytes      uint64 `json:"rx_bytes"`
	TXBytes      uint64 `json:"tx_bytes"`
	SessionRX    uint64 `json:"session_rx_bytes"`
	SessionTX    uint64 `json:"session_tx_bytes"`
	SessionTotal uint64 `json:"session_total_bytes"`
	SampledAtMS  int64  `json:"sampled_at_ms"`
	Error        string `json:"error,omitempty"`
}

type networkCheckResult struct {
	OK      bool   `json:"ok"`
	Summary string `json:"summary"`
	Detail  string `json:"detail"`
}

func (a *app) networkDiagnostic(w http.ResponseWriter, _ *http.Request) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("network diagnostic panic: %v", recovered)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("network diagnostic failed: %v", recovered))
		}
	}()
	raw := make(map[string]string)
	errs := make(map[string]string)
	diag := networkDiagnostic{
		USBDevice:     a.currentUSBDevice(),
		MacInterfaces: discoverMacNetworkInterfaces(),
		DefaultRoute:  discoverMacDefaultRoute(),
		Raw:           raw,
		Errors:        errs,
	}
	diag.USBNetworkPresent = hasLikelyUSBNetworkInterface(diag.MacInterfaces)

	commands := map[string]string{
		"usbnet":  `AT+QCFG="usbnet"`,
		"usbcfg":  `AT+QCFG="usbcfg"`,
		"cgdcont": `AT+CGDCONT?`,
		"cgact":   `AT+CGACT?`,
		"cgpaddr": `AT+CGPADDR=1`,
	}
	for key, command := range commands {
		resp, err := a.runATCommand(command, 8*time.Second)
		if err != nil {
			errs[key] = err.Error()
			continue
		}
		raw[key] = resp
	}

	diag.USBNetMode = parseUSBNetMode(raw["usbnet"])
	diag.USBCfg = parseUSBATPrefixed(raw["usbcfg"], "+QCFG:")
	diag.PDPContexts = parsePDPContexts(raw["cgdcont"])
	diag.ActiveContexts = parseActivePDPContexts(raw["cgact"])
	diag.PDPAddresses = parsePDPAddresses(raw["cgpaddr"])
	if len(errs) == 0 {
		diag.Errors = nil
	}
	writeJSON(w, http.StatusOK, diag)
}

func (a *app) localNetworkConnection(w http.ResponseWriter, _ *http.Request) {
	if discoverDJIUSBDevice() == nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	interfaces := discoverMacNetworkInterfaces()
	route := discoverMacDefaultRoute()
	name := selectUSBTrafficInterface(interfaces, route)
	if name == "" {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	for _, item := range interfaces {
		if item.Name == name {
			writeJSON(w, http.StatusOK, localNetworkConnection{
				Interface: name,
				IPv4:      item.IPv4,
				IsDefault: name == route.Interface,
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, nil)
}

func (a *app) networkActivity(w http.ResponseWriter, _ *http.Request) {
	snapshot := networkActivitySnapshot{SampledAtMS: time.Now().UnixMilli()}
	if discoverDJIUSBDevice() == nil {
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	interfaces := discoverMacNetworkInterfaces()
	route := discoverMacDefaultRoute()
	physical := selectUSBTrafficInterface(interfaces, route)
	if physical == "" {
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	snapshot.Available = true
	snapshot.PhysicalInterface = physical
	for _, item := range interfaces {
		if item.Name == physical {
			snapshot.PhysicalIPv4 = item.IPv4
			break
		}
	}
	tunnel := route.Interface
	if tunnel == "" {
		tunnel = physical
	}
	snapshot.TunnelInterface = tunnel

	type sampleResult struct {
		records []networkActivityRecord
	}
	results := make(chan sampleResult, 2)
	for _, mode := range []string{"tcp", "udp"} {
		go func(mode string) {
			out, err := exec.Command("nettop", "-L", "1", "-x", "-m", mode, "-t", "wired").Output()
			if err != nil {
				results <- sampleResult{}
				return
			}
			results <- sampleResult{records: parseNettopActivity(string(out))}
		}(mode)
	}
	var all []networkActivityRecord
	for range 2 {
		all = append(all, (<-results).records...)
	}
	activityInterface := tunnel
	for _, item := range all {
		if item.Interface == physical {
			snapshot.PhysicalActive = true
		}
		if item.Interface == activityInterface {
			snapshot.Connections = append(snapshot.Connections, item)
		}
	}
	sort.Slice(snapshot.Connections, func(i, j int) bool {
		left := snapshot.Connections[i].RXBytes + snapshot.Connections[i].TXBytes
		right := snapshot.Connections[j].RXBytes + snapshot.Connections[j].TXBytes
		return left > right
	})
	writeJSON(w, http.StatusOK, snapshot)
}

func parseNettopActivity(out string) []networkActivityRecord {
	reader := csv.NewReader(strings.NewReader(out))
	reader.FieldsPerRecord = -1
	var records []networkActivityRecord
	process := "系统"
	for {
		row, err := reader.Read()
		if err != nil {
			break
		}
		if len(row) < 6 || row[1] == "" {
			continue
		}
		description := strings.TrimSpace(row[1])
		iface := strings.TrimSpace(row[2])
		if !strings.Contains(description, "<->") {
			process = regexp.MustCompile(`\.\d+$`).ReplaceAllString(description, "")
			continue
		}
		protocol, host, port := parseNettopFlow(description)
		if host == "" || iface == "" || host == "127.0.0.1" || host == "::1" {
			continue
		}
		rx, _ := strconv.ParseUint(strings.TrimSpace(row[4]), 10, 64)
		tx, _ := strconv.ParseUint(strings.TrimSpace(row[5]), 10, 64)
		state := ""
		if strings.HasPrefix(protocol, "tcp") && len(row) > 3 {
			state = strings.TrimSpace(row[3])
		}
		record := networkActivityRecord{
			Process: process, Port: port, Protocol: protocol,
			Interface: iface, State: state, RXBytes: rx, TXBytes: tx,
		}
		if net.ParseIP(host) == nil {
			record.Host = host
		} else {
			record.IP = host
		}
		records = append(records, record)
	}
	return records
}

func parseNettopFlow(description string) (protocol, host, port string) {
	fields := strings.Fields(description)
	if len(fields) < 2 {
		return "", "", ""
	}
	protocol = fields[0]
	flow := fields[len(fields)-1]
	parts := strings.SplitN(flow, "<->", 2)
	if len(parts) != 2 {
		return protocol, "", ""
	}
	remote := parts[1]
	if strings.Count(remote, ":") > 1 {
		if index := strings.LastIndex(remote, "."); index > 0 {
			return protocol, strings.Trim(remote[:index], "[]"), remote[index+1:]
		}
		return protocol, strings.Trim(remote, "[]"), ""
	}
	if index := strings.LastIndex(remote, ":"); index > 0 {
		return protocol, strings.Trim(remote[:index], "[]"), remote[index+1:]
	}
	return protocol, strings.Trim(remote, "[]"), ""
}

func (a *app) networkTraffic(w http.ResponseWriter, _ *http.Request) {
	snapshot := networkTrafficSnapshot{
		SampledAtMS: time.Now().UnixMilli(),
	}

	interfaces := discoverMacNetworkInterfaces()
	name := selectUSBTrafficInterface(interfaces, discoverMacDefaultRoute())
	if name == "" {
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	counters, err := discoverMacInterfaceCounters()
	if err != nil {
		snapshot.Interface = name
		snapshot.Error = err.Error()
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	current, ok := counters[name]
	if !ok {
		snapshot.Interface = name
		snapshot.Error = "未读取到网卡计数"
		writeJSON(w, http.StatusOK, snapshot)
		return
	}

	a.trafficMu.Lock()
	if a.trafficBaselines == nil {
		a.trafficBaselines = make(map[string]networkByteCounters)
	}
	baseline, exists := a.trafficBaselines[name]
	if !exists || current.RX < baseline.RX || current.TX < baseline.TX {
		baseline = current
		a.trafficBaselines[name] = baseline
	}
	a.trafficMu.Unlock()

	snapshot.Available = true
	snapshot.Interface = name
	snapshot.RXBytes = current.RX
	snapshot.TXBytes = current.TX
	snapshot.SessionRX, snapshot.SessionTX, snapshot.SessionTotal = sessionTrafficFromCounters(current, baseline)
	writeJSON(w, http.StatusOK, snapshot)
}

func sessionTrafficFromCounters(current, baseline networkByteCounters) (rx, tx, total uint64) {
	rx = current.RX - baseline.RX
	tx = current.TX - baseline.TX
	return rx, tx, rx + tx
}

func (a *app) check4GRoute(w http.ResponseWriter, _ *http.Request) {
	route := discoverMacDefaultRoute()
	interfaces := discoverMacNetworkInterfaces()
	var active *macNetInterface
	for i := range interfaces {
		if interfaces[i].Name == route.Interface {
			active = &interfaces[i]
			break
		}
	}
	if route.Interface == "" {
		writeJSON(w, http.StatusOK, networkCheckResult{
			OK:      false,
			Summary: "未读取到默认出口",
			Detail:  "macOS 没有返回 default route",
		})
		return
	}
	if active != nil && active.Name != "en0" && active.Kind == "ethernet" && active.Status == "active" {
		writeJSON(w, http.StatusOK, networkCheckResult{
			OK:      true,
			Summary: "当前正在走 4G 模块",
			Detail:  fmt.Sprintf("默认出口 %s -> %s，IP %s", route.Interface, route.Gateway, active.IPv4),
		})
		return
	}
	detail := fmt.Sprintf("默认出口 %s -> %s", route.Interface, route.Gateway)
	if active != nil && active.IPv4 != "" {
		detail += "，IP " + active.IPv4
	}
	writeJSON(w, http.StatusOK, networkCheckResult{
		OK:      false,
		Summary: "当前没有优先走 4G 模块",
		Detail:  detail,
	})
}

func (a *app) checkProxyRoute(w http.ResponseWriter, _ *http.Request) {
	proxyURL, _ := url.Parse("http://127.0.0.1:7890")
	client := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}
	req, err := http.NewRequest(http.MethodHead, "https://www.google.com/generate_204", nil)
	if err != nil {
		writeJSON(w, http.StatusOK, networkCheckResult{OK: false, Summary: "代理检测请求创建失败", Detail: err.Error()})
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, networkCheckResult{
			OK:      false,
			Summary: "代理未打通",
			Detail:  "127.0.0.1:7890 代理访问失败：" + err.Error(),
		})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || (resp.StatusCode >= 200 && resp.StatusCode < 400) {
		writeJSON(w, http.StatusOK, networkCheckResult{
			OK:      true,
			Summary: "代理已打通",
			Detail:  fmt.Sprintf("127.0.0.1:7890 -> google generate_204 返回 %s", resp.Status),
		})
		return
	}
	writeJSON(w, http.StatusOK, networkCheckResult{
		OK:      false,
		Summary: "代理响应异常",
		Detail:  fmt.Sprintf("127.0.0.1:7890 返回 %s", resp.Status),
	})
}

func (a *app) setUSBNetMode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode int `json:"mode"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Mode < 0 || body.Mode > 3 {
		writeError(w, http.StatusBadRequest, "only usbnet mode 0, 1, 2 or 3 is allowed")
		return
	}
	command := fmt.Sprintf(`AT+QCFG="usbnet",%d`, body.Mode)
	response, err := a.runATCommand(command, 8*time.Second)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":         body.Mode,
		"response":     response,
		"needs_reboot": true,
	})
}

func (a *app) rebootModule(w http.ResponseWriter, _ *http.Request) {
	response, err := a.runATCommand("AT+CFUN=1,1", 3*time.Second)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true,
		"response": response,
	})
}

func parseUSBNetMode(resp string) string {
	re := regexp.MustCompile(`\+QCFG:\s*"usbnet",(\d+)`)
	match := re.FindStringSubmatch(resp)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func parsePDPContexts(resp string) []pdpContext {
	re := regexp.MustCompile(`\+CGDCONT:\s*(\d+),"([^"]*)","([^"]*)"`)
	var contexts []pdpContext
	for _, match := range re.FindAllStringSubmatch(resp, -1) {
		id, _ := strconv.Atoi(match[1])
		contexts = append(contexts, pdpContext{ID: id, PDN: match[2], APN: match[3]})
	}
	return contexts
}

func parseActivePDPContexts(resp string) []int {
	re := regexp.MustCompile(`\+CGACT:\s*(\d+),1`)
	var contexts []int
	for _, match := range re.FindAllStringSubmatch(resp, -1) {
		id, _ := strconv.Atoi(match[1])
		contexts = append(contexts, id)
	}
	return contexts
}

func parsePDPAddresses(resp string) []string {
	re := regexp.MustCompile(`\+CGPADDR:\s*\d+,"([^"]*)"`)
	match := re.FindStringSubmatch(resp)
	if len(match) != 2 {
		return nil
	}
	var out []string
	for _, part := range strings.Split(match[1], ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func discoverMacNetworkInterfaces() []macNetInterface {
	out, err := exec.Command("ifconfig").Output()
	if err != nil {
		return nil
	}
	var interfaces []macNetInterface
	for _, block := range splitIfconfigBlocks(string(out)) {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		lines := strings.Split(block, "\n")
		if len(lines) == 0 {
			continue
		}
		fields := strings.Fields(lines[0])
		if len(fields) == 0 {
			continue
		}
		name := strings.TrimSuffix(fields[0], ":")
		if name == "" || strings.HasPrefix(name, "lo") || strings.HasPrefix(name, "utun") {
			continue
		}
		item := macNetInterface{Name: name, Status: "unknown", Kind: classifyMacInterfaceName(name)}
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "status:") {
				item.Status = strings.TrimSpace(strings.TrimPrefix(line, "status:"))
			}
			if strings.HasPrefix(line, "inet ") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					item.IPv4 = fields[1]
				}
			}
		}
		interfaces = append(interfaces, item)
	}
	return interfaces
}

func discoverMacDefaultRoute() macDefaultRoute {
	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err != nil {
		return macDefaultRoute{}
	}
	var route macDefaultRoute
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gateway:") {
			route.Gateway = strings.TrimSpace(strings.TrimPrefix(line, "gateway:"))
		}
		if strings.HasPrefix(line, "interface:") {
			route.Interface = strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
		}
	}
	return route
}

func splitIfconfigBlocks(out string) []string {
	var blocks []string
	var current []string
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if line[0] != '\t' && line[0] != ' ' && strings.Contains(line, ":") {
			if len(current) > 0 {
				blocks = append(blocks, strings.Join(current, "\n"))
			}
			current = []string{line}
			continue
		}
		if len(current) > 0 {
			current = append(current, line)
		}
	}
	if len(current) > 0 {
		blocks = append(blocks, strings.Join(current, "\n"))
	}
	return blocks
}

func classifyMacInterfaceName(name string) string {
	switch {
	case strings.HasPrefix(name, "en"):
		return "ethernet"
	case strings.HasPrefix(name, "bridge"):
		return "bridge"
	case strings.HasPrefix(name, "awdl") || strings.HasPrefix(name, "llw") || strings.HasPrefix(name, "ap"):
		return "apple-wireless"
	default:
		return "other"
	}
}

func hasLikelyUSBNetworkInterface(interfaces []macNetInterface) bool {
	for _, item := range interfaces {
		if item.Kind == "ethernet" && item.Name != "en0" && item.Status == "active" {
			return true
		}
	}
	return false
}

func selectUSBTrafficInterface(interfaces []macNetInterface, route macDefaultRoute) string {
	for _, item := range interfaces {
		if item.Name == route.Interface && item.Kind == "ethernet" && item.Name != "en0" && item.Status == "active" {
			return item.Name
		}
	}
	for _, item := range interfaces {
		if item.Kind == "ethernet" && item.Name != "en0" && item.Status == "active" {
			return item.Name
		}
	}
	return ""
}

func discoverMacInterfaceCounters() (map[string]networkByteCounters, error) {
	out, err := exec.Command("netstat", "-ibn").Output()
	if err != nil {
		return nil, err
	}
	return parseMacInterfaceCounters(string(out)), nil
}

func parseMacInterfaceCounters(out string) map[string]networkByteCounters {
	counters := make(map[string]networkByteCounters)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || !strings.HasPrefix(fields[2], "<Link#") {
			continue
		}
		name := strings.TrimSuffix(fields[0], "*")
		rx, rxErr := strconv.ParseUint(fields[6], 10, 64)
		tx, txErr := strconv.ParseUint(fields[9], 10, 64)
		if name == "" || rxErr != nil || txErr != nil {
			continue
		}
		counters[name] = networkByteCounters{RX: rx, TX: tx}
	}
	return counters
}

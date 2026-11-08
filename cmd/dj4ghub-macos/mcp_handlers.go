package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// MCP / API token (Item 7, last).
//
// A minimal MCP server over HTTP (JSON-RPC 2.0, POST /api/mcp) for agents
// that need to read the hub's state. Authentication is a first-run random
// bearer token: generated once, stored 0600 in mcp-token next to the SIP
// config, printed once to the log, and never returned by any API — the
// same pattern as the SIP first-run password (Item 5).
//
// Only read-only tools are exposed. Every sensitive operation — SMS send,
// SIP call/answer/hangup, USSD send, operator select, cell lock/unlock,
// module reboot, settings changes, task create/enable/delete/run — is
// excluded by design: tools/call for one of those names answers with a
// 501-style JSON-RPC error (-32601, "not implemented"), never by executing
// it. There is no way to perform a mutation through MCP.

const mcpTokenFileName = "mcp-token"

var (
	mcpTokenMu sync.Mutex
	mcpToken   string
)

// startMCP loads (or first-run generates) the MCP bearer token. Called
// from serve() so every launch path gets it.
func (a *app) startMCP() {
	dir, err := a.sipConfigDir()
	if err != nil {
		log.Printf("[mcp] unavailable: %v", err)
		return
	}
	mcpTokenMu.Lock()
	defer mcpTokenMu.Unlock()
	if mcpToken != "" {
		return
	}
	path := filepath.Join(dir, mcpTokenFileName)
	if raw, err := os.ReadFile(path); err == nil {
		if tok := strings.TrimSpace(string(raw)); tok != "" {
			mcpToken = tok
			return
		}
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		log.Printf("[mcp] unavailable: cannot generate token: %v", err)
		return
	}
	tok := hex.EncodeToString(buf)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[mcp] unavailable: %v", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(tok+"\n"), 0o600); err != nil {
		log.Printf("[mcp] unavailable: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("[mcp] unavailable: %v", err)
		return
	}
	mcpToken = tok
	log.Printf("[mcp] first run: generated API token (stored 0600 in %s, never returned by any API)", path)
}

func mcpAuthOK(r *http.Request) bool {
	mcpTokenMu.Lock()
	tok := mcpToken
	mcpTokenMu.Unlock()
	if tok == "" {
		return false
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))), []byte(tok)) == 1
}

// --- JSON-RPC plumbing ----------------------------------------------------

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeMCP(w http.ResponseWriter, id json.RawMessage, result any, rpcErr *mcpError) {
	if id == nil {
		id = json.RawMessage("null")
	}
	code := http.StatusOK
	if rpcErr != nil && rpcErr.Code == -32001 {
		code = http.StatusUnauthorized
	}
	writeJSON(w, code, mcpResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr})
}

// --- tools -----------------------------------------------------------------

type mcpTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// mcpReadTools is the complete read-only tool list. Anything not in this
// map is refused with a 501-style error (see handleMCPCall).
var mcpReadTools = map[string]mcpTool{
	"health":           {Name: "health", Description: "Hub health + attached module identity (read-only)."},
	"sip_status":       {Name: "sip_status", Description: "SIP server status: listener, registrations, calls, audio bridge state (read-only)."},
	"serving_cell":     {Name: "serving_cell", Description: "Current LTE serving cell: EARFCN/PCI/RSRP/RSRQ/SINR/band (read-only)."},
	"ussd_status":      {Name: "ussd_status", Description: "Current USSD session state (read-only; sending USSD is not available over MCP)."},
	"sms_list":         {Name: "sms_list", Description: "Recently received SMS (read-only)."},
	"delivery_reports": {Name: "delivery_reports", Description: "Per-message SMS delivery states (read-only)."},
	"gnss_status":      {Name: "gnss_status", Description: "GNSS receiver state and last fix, if any (read-only)."},
	"modules":          {Name: "modules", Description: "Attached modules, keyed by IMEI for the active one (read-only)."},
	"tasks_list":       {Name: "tasks_list", Description: "Scheduled tasks and their last results (read-only; task management is not available over MCP)."},
}

// mcpExcludedOps names the sensitive operations MCP refuses, so the 501
// error can say exactly what was asked for and why it is refused.
var mcpExcludedOps = []string{
	"sms_send", "sip_call", "sip_answer", "sip_hangup", "ussd_send",
	"operator_select", "cell_lock", "cell_unlock", "reboot_module",
	"settings_update", "task_create", "task_enable", "task_delete", "task_run",
}

func (a *app) mcpCallReadTool(name string) (any, *mcpError) {
	switch name {
	case "health":
		return a.healthSnapshot(), nil
	case "sip_status":
		if a.sip == nil {
			return map[string]any{"enabled": false, "listening": false}, nil
		}
		return a.sip.Status(), nil
	case "serving_cell":
		return a.servingCellSnapshot()
	case "ussd_status":
		return a.ussdSnapshot(), nil
	case "sms_list":
		a.smsMu.RLock()
		items := append([]receivedSMS(nil), a.sms...)
		a.smsMu.RUnlock()
		if items == nil {
			items = []receivedSMS{}
		}
		return items, nil
	case "delivery_reports":
		a.deliveryMu.RLock()
		items := append([]smsDeliveryRecord(nil), a.delivery...)
		a.deliveryMu.RUnlock()
		if items == nil {
			items = []smsDeliveryRecord{}
		}
		return items, nil
	case "gnss_status":
		return a.gnssSnapshot()
	case "modules":
		return a.modulesSnapshot()
	case "tasks_list":
		tasks := a.getTasks()
		if tasks == nil {
			tasks = []*scheduledTask{}
		}
		return tasks, nil
	}
	return nil, &mcpError{Code: -32601, Message: fmt.Sprintf("tool %q is not implemented", name)}
}

func (a *app) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if !mcpAuthOK(r) {
		writeJSON(w, http.StatusUnauthorized, mcpResponse{
			JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &mcpError{Code: -32001, Message: "missing or invalid Bearer token (see mcp-token, 0600, in the DJ 4G Hub config dir)"},
		})
		return
	}
	var req mcpRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	switch req.Method {
	case "initialize":
		writeMCP(w, req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]any{"name": "dj4hub", "version": "1.0"},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		}, nil)
	case "tools/list":
		tools := make([]mcpTool, 0, len(mcpReadTools))
		for _, t := range mcpReadTools {
			tools = append(tools, t)
		}
		writeMCP(w, req.ID, map[string]any{"tools": tools}, nil)
	case "tools/call":
		var params struct {
			Name string `json:"name"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeMCP(w, req.ID, nil, &mcpError{Code: -32602, Message: "invalid params: " + err.Error()})
				return
			}
		}
		for _, ex := range mcpExcludedOps {
			if params.Name == ex {
				writeMCP(w, req.ID, nil, &mcpError{Code: -32601, Message: fmt.Sprintf("501: %q is a sensitive operation and is excluded from MCP by design; use the local console instead", ex)})
				return
			}
		}
		result, rpcErr := a.mcpCallReadTool(params.Name)
		if rpcErr != nil {
			writeMCP(w, req.ID, nil, rpcErr)
			return
		}
		writeMCP(w, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": mustJSON(result)}},
		}, nil)
	default:
		writeMCP(w, req.ID, nil, &mcpError{Code: -32601, Message: fmt.Sprintf("method %q is not implemented", req.Method)})
	}
}

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(raw)
}

// mcpStatus reports whether MCP is configured. The token value is never
// returned.
func (a *app) mcpStatus(w http.ResponseWriter, _ *http.Request) {
	mcpTokenMu.Lock()
	configured := mcpToken != ""
	mcpTokenMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": configured, "token_configured": configured,
		"endpoint": "/api/mcp",
		"note":     "Bearer token required; first-run token is stored 0600 in mcp-token and logged once at startup. Read-only tools only — sensitive operations are excluded.",
	})
}

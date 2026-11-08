package modem

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cell lock (Item 3) via Quectel AT+QNWLOCK.
//
// Syntax (Teltonika "Locking Devices to a Mobile Cell", Quectel EG/EC
// modems; QNWLOCK is not in the public AT manual):
//
//	AT+QNWLOCK=?                       -> +QNWLOCK: "common/lte"[,<action>[,<EARFCN>,<PCI>[,<status>]]]
//	AT+QNWLOCK="common/lte",2,<EARFCN>,<PCI>   lock to one LTE cell (action 2)
//	AT+QNWLOCK="common/lte",0                  unlock, back to automatic (action 0)
//
// The lock is NOT persistent across reboot (Quectel forum, 2025-03-24) and
// it forces LTE-only camping; background scanning continues, the modem
// just will not attach to another frequency. EARFCN 0-262143, PCI 0-503.
// The module may reject the command entirely (firmware without QuecCell
// support) — callers must surface that error, never claim a lock succeeded
// without the modem's OK.

const (
	maxEARFCN = 262143
	maxPCI    = 503
)

// CellLockState is the modem-reported cell-lock state.
type CellLockState struct {
	Locked    bool   `json:"locked"`
	EARFCN    int    `json:"earfcn,omitempty"`
	PCI       int    `json:"pci,omitempty"`
	Raw       string `json:"raw,omitempty"`
	Supported bool   `json:"supported"` // false when the modem has no +QNWLOCK line at all
}

// BuildQNWLockCommand builds the AT command that locks to one LTE cell.
// It validates the ranges before anything is sent to the modem.
func BuildQNWLockCommand(earfcn, pci int) (string, error) {
	if earfcn < 0 || earfcn > maxEARFCN {
		return "", fmt.Errorf("earfcn %d out of range 0-%d", earfcn, maxEARFCN)
	}
	if pci < 0 || pci > maxPCI {
		return "", fmt.Errorf("pci %d out of range 0-%d", pci, maxPCI)
	}
	return fmt.Sprintf(`AT+QNWLOCK="common/lte",2,%d,%d`, earfcn, pci), nil
}

// BuildQNWUnlockCommand builds the AT command that releases a cell lock.
func BuildQNWUnlockCommand() string {
	return `AT+QNWLOCK="common/lte",0`
}

// ParseQNWLockResponse parses an AT+QNWLOCK? response. It returns
// found=false when the response has no +QNWLOCK line (command unsupported
// or unparsable); the caller decides how to report that.
func ParseQNWLockResponse(resp string) (CellLockState, bool) {
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "+QNWLOCK:") {
			continue
		}
		state := CellLockState{Supported: true, Raw: line}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "+QNWLOCK:"))
		// Format: "common/lte"[,<action>[,<earfcn>,<pci>[,<status>]]]
		fields := strings.Split(rest, ",")
		if len(fields) < 2 {
			// Bare "common/lte" with no action: no lock active.
			return state, true
		}
		action, err := strconv.Atoi(strings.TrimSpace(fields[1]))
		if err != nil {
			return CellLockState{}, false
		}
		if action != 2 {
			return state, true
		}
		state.Locked = true
		if len(fields) >= 3 {
			if earfcn, err := strconv.Atoi(strings.TrimSpace(fields[2])); err == nil {
				state.EARFCN = earfcn
			}
		}
		if len(fields) >= 4 {
			if pci, err := strconv.Atoi(strings.TrimSpace(fields[3])); err == nil {
				state.PCI = pci
			}
		}
		return state, true
	}
	return CellLockState{}, false
}

// LockCell locks the modem to one LTE cell. It returns verified=true only
// when a follow-up AT+QNWLOCK? read-back shows the same EARFCN/PCI.
// verified=false with err=nil means the modem answered OK to the lock but
// refused or did not answer the read-back — the lock was accepted, not
// confirmed. An error means the modem rejected the lock command.
func (m *Manager) LockCell(earfcn, pci int) (verified bool, err error) {
	cmd, err := BuildQNWLockCommand(earfcn, pci)
	if err != nil {
		return false, err
	}
	if _, err := m.ExecuteAT(cmd, 10*time.Second); err != nil {
		return false, fmt.Errorf("cell lock rejected by modem: %w", err)
	}
	state, qerr := m.QueryCellLock()
	if qerr != nil {
		return false, nil
	}
	if state.Locked && state.EARFCN == earfcn && state.PCI == pci {
		return true, nil
	}
	return false, nil
}

// UnlockCell releases any cell lock and returns to automatic selection.
func (m *Manager) UnlockCell() error {
	if _, err := m.ExecuteAT(BuildQNWUnlockCommand(), 10*time.Second); err != nil {
		return fmt.Errorf("cell unlock rejected by modem: %w", err)
	}
	return nil
}

// QueryCellLock reads the current lock state via AT+QNWLOCK?.
func (m *Manager) QueryCellLock() (CellLockState, error) {
	resp, err := m.ExecuteATSilent("AT+QNWLOCK?", 5*time.Second)
	if err != nil {
		return CellLockState{}, fmt.Errorf("query cell lock: %w", err)
	}
	state, found := ParseQNWLockResponse(resp)
	if !found {
		return CellLockState{}, fmt.Errorf("modem does not report a +QNWLOCK line (unsupported firmware?)")
	}
	return state, nil
}

package smscodec

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	smspdu "github.com/warthog618/sms"
	"github.com/warthog618/sms/encoding/tpdu"
)

// SMS-STATUS-REPORT (3GPP TS 23.040 §9.2.2.3) decoding.
//
// A status report is the network's answer to a SUBMIT that requested one
// (TP-SRR). Its TPDU first octet has MTI = 0b10. It carries the message
// reference (TP-MR) of the original SUBMIT, so a report can be matched to
// the sent message without any new storage: the caller keeps the MR from
// the +CMGS response and matches it here.

// StatusReport is a decoded SMS-STATUS-REPORT TPDU.
type StatusReport struct {
	MessageRef    int       `json:"message_ref"`
	Recipient     string    `json:"recipient"`
	SCTS          time.Time `json:"scts"`
	DischargeTime time.Time `json:"discharge_time"`
	Status        int       `json:"status"`
	StatusText    string    `json:"status_text"`
	State         string    `json:"state"` // delivered | pending | failed | unknown
}

// DeliveryState maps a TP-ST status octet to a coarse delivery state.
// TP-ST (TS 23.040 §9.2.3.15): 0 = delivered, 1 = pending (still trying),
// 2 = failed; 32-47 = SC-specific pending, 48-63 = SC-specific failures
// (treated as failed — the SC gave up), 64-95 = permanent errors,
// 96-127 = temporary errors (still trying).
func DeliveryState(status int) string {
	switch {
	case status == 0:
		return "delivered"
	case status == 1:
		return "pending"
	case status == 2:
		return "failed"
	case status >= 32 && status <= 47:
		return "pending"
	case status >= 48 && status <= 63:
		return "failed"
	case status >= 64 && status <= 95:
		return "failed"
	case status >= 96 && status <= 127:
		return "pending"
	default:
		return "unknown"
	}
}

// StatusText renders a TP-ST octet as a short human-readable string.
func StatusText(status int) string {
	switch DeliveryState(status) {
	case "delivered":
		return "delivered"
	case "pending":
		return fmt.Sprintf("pending (status %d)", status)
	case "failed":
		return fmt.Sprintf("failed (status %d)", status)
	default:
		return fmt.Sprintf("unknown status %d", status)
	}
}

// IsStatusReportTPDU reports whether tpduBytes starts a SMS-STATUS-REPORT
// (first octet MTI bits = 0b10). It never panics on short input.
func IsStatusReportTPDU(tpduBytes []byte) bool {
	if len(tpduBytes) == 0 {
		return false
	}
	return tpduBytes[0]&0x03 == 0x02
}

// IsStatusReportFullPDU reports whether a full PDU (SMSC + TPDU, hex) is a
// status report. Malformed input returns false, never an error.
func IsStatusReportFullPDU(pduHex string) bool {
	full, err := hex.DecodeString(strings.TrimSpace(pduHex))
	if err != nil || len(full) < 2 {
		return false
	}
	smscLen := int(full[0])
	offset := 1 + smscLen
	if offset >= len(full) {
		return false
	}
	return IsStatusReportTPDU(full[offset:])
}

// DecodeStatusReportTPDU decodes a SMS-STATUS-REPORT TPDU (SMSC already
// stripped). It returns an error for any other TPDU type or truncated data.
func DecodeStatusReportTPDU(tpduBytes []byte) (StatusReport, error) {
	if !IsStatusReportTPDU(tpduBytes) {
		return StatusReport{}, errors.New("not a SMS-STATUS-REPORT TPDU")
	}
	t, err := smspdu.Unmarshal(tpduBytes)
	if err != nil {
		return StatusReport{}, err
	}
	if t.SmsType() != tpdu.SmsStatusReport {
		return StatusReport{}, fmt.Errorf("unexpected TPDU type %v", t.SmsType())
	}
	rep := StatusReport{
		MessageRef:    int(t.MR),
		Recipient:     t.RA.Number(),
		SCTS:          t.SCTS.Time,
		DischargeTime: t.DT.Time,
		Status:        int(t.ST),
	}
	rep.State = DeliveryState(rep.Status)
	rep.StatusText = StatusText(rep.Status)
	return rep, nil
}

// DecodeStatusReportFullPDU decodes a full PDU hex string (SMSC + TPDU) that
// is a SMS-STATUS-REPORT, e.g. the PDU line following a +CDS URC or a
// stored report read via +CMGL/+CMGR.
func DecodeStatusReportFullPDU(pduHex string) (StatusReport, error) {
	full, err := hex.DecodeString(strings.TrimSpace(pduHex))
	if err != nil {
		return StatusReport{}, fmt.Errorf("PDU hex decode: %w", err)
	}
	if len(full) < 2 {
		return StatusReport{}, errors.New("PDU too short")
	}
	smscLen := int(full[0])
	offset := 1 + smscLen
	if offset >= len(full) {
		return StatusReport{}, errors.New("PDU has invalid SMSC length")
	}
	rep, err := DecodeStatusReportTPDU(full[offset:])
	if err != nil {
		return StatusReport{}, err
	}
	return rep, nil
}

// ParseCDSHeader parses a "+CDS: <length>" URC header line. The PDU itself
// arrives on the following line.
func ParseCDSHeader(line string) (length int, ok bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "+CDS:") {
		return 0, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(s, "+CDS:"))
	if _, err := fmt.Sscanf(rest, "%d", &length); err != nil {
		return 0, false
	}
	return length, true
}

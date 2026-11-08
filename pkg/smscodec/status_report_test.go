package smscodec

import (
	"encoding/hex"
	"strings"
	"testing"
)

// Hand-built SMS-STATUS-REPORT (TS 23.040 §9.2.2.3):
// first octet 0x02 (MTI=10), MR 0x2A (42), RA 0B 91 + "8613800138000",
// SCTS 2026-11-06 12:34:56 UTC, DT one second later, ST 0x00 delivered.
const statusReportTPDUHex = "022A0D91683108108300F0621160214365006211602143660000"

func TestDecodeStatusReportTPDU(t *testing.T) {
	tpduBytes := mustHex(t, statusReportTPDUHex)
	rep, err := DecodeStatusReportTPDU(tpduBytes)
	if err != nil {
		t.Fatalf("DecodeStatusReportTPDU() error = %v", err)
	}
	if rep.MessageRef != 42 {
		t.Fatalf("MessageRef = %d, want 42", rep.MessageRef)
	}
	if !strings.Contains(rep.Recipient, "8613800138000") {
		t.Fatalf("Recipient = %q, want it to contain 8613800138000", rep.Recipient)
	}
	if rep.Status != 0 || rep.State != "delivered" {
		t.Fatalf("Status/State = %d/%q, want 0/delivered", rep.Status, rep.State)
	}
	if rep.SCTS.Year() != 2026 || int(rep.SCTS.Month()) != 11 || rep.SCTS.Day() != 6 ||
		rep.SCTS.Hour() != 12 || rep.SCTS.Minute() != 34 || rep.SCTS.Second() != 56 {
		t.Fatalf("SCTS = %v, want 2026-11-06 12:34:56", rep.SCTS)
	}
	if rep.DischargeTime.IsZero() {
		t.Fatal("DischargeTime is zero")
	}
}

func TestDecodeStatusReportFullPDU(t *testing.T) {
	rep, err := DecodeStatusReportFullPDU("00" + statusReportTPDUHex)
	if err != nil {
		t.Fatalf("DecodeStatusReportFullPDU() error = %v", err)
	}
	if rep.MessageRef != 42 || rep.State != "delivered" {
		t.Fatalf("got %+v, want MR 42 delivered", rep)
	}
	if !IsStatusReportFullPDU("00" + statusReportTPDUHex) {
		t.Fatal("IsStatusReportFullPDU() = false, want true")
	}
	// A DELIVER PDU must not be mistaken for a status report.
	if IsStatusReportFullPDU("079144872000302320048102020000625061028204401AD9775D0E72D7DBE2B21C949E8360B75A4E7683D16AB71B") {
		t.Fatal("IsStatusReportFullPDU() = true for a DELIVER PDU")
	}
}

func TestDeliveryStateMapping(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{0, "delivered"},
		{1, "pending"},
		{2, "failed"},
		{32, "pending"},
		{48, "failed"},
		{64, "failed"},
		{96, "pending"},
		{127, "pending"},
		{255, "unknown"},
	}
	for _, tc := range cases {
		if got := DeliveryState(tc.status); got != tc.want {
			t.Fatalf("DeliveryState(%d) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestDecodeStatusReportMalformed(t *testing.T) {
	bad := [][]byte{
		{},
		{0x02},
		{0x02, 0x2A},
		{0x00, 0x2A, 0x0B},             // wrong MTI
		{0x02, 0x2A, 0x0B, 0x91, 0x68}, // truncated address
	}
	for i, b := range bad {
		if _, err := DecodeStatusReportTPDU(b); err == nil {
			t.Fatalf("case %d: DecodeStatusReportTPDU() error = nil, want error", i)
		}
	}
	for _, hexStr := range []string{"", "0", "ZZ", "00"} {
		if _, err := DecodeStatusReportFullPDU(hexStr); err == nil {
			t.Fatalf("DecodeStatusReportFullPDU(%q) error = nil, want error", hexStr)
		}
		if IsStatusReportFullPDU(hexStr) {
			t.Fatalf("IsStatusReportFullPDU(%q) = true, want false", hexStr)
		}
	}
}

func TestParseCDSHeader(t *testing.T) {
	cases := []struct {
		line   string
		length int
		ok     bool
	}{
		{"+CDS: 24", 24, true},
		{"+CDS: 0", 0, true},
		{"+CDS: abc", 0, false},
		{"+CDS:", 0, false},
		{"+CMTI: \"SM\",3", 0, false},
		{"", 0, false},
		{"OK", 0, false},
	}
	for _, tc := range cases {
		length, ok := ParseCDSHeader(tc.line)
		if ok != tc.ok || length != tc.length {
			t.Fatalf("ParseCDSHeader(%q) = (%d, %v), want (%d, %v)", tc.line, length, ok, tc.length, tc.ok)
		}
	}
}

func TestBuildSubmitTPDUStatusReportBit(t *testing.T) {
	withSRR, _, err := BuildSubmitTPDUsWithOptions("13800138000", "hi", SubmitOptions{RequestStatusReport: true})
	if err != nil {
		t.Fatalf("BuildSubmitTPDUsWithOptions() error = %v", err)
	}
	if len(withSRR) == 0 || len(withSRR[0]) == 0 {
		t.Fatal("empty TPDU")
	}
	if withSRR[0][0]&0x20 == 0 {
		t.Fatalf("SRR bit not set: first octet = 0x%02x", withSRR[0][0])
	}
	withoutSRR, _, err := BuildSubmitTPDUsWithOptions("13800138000", "hi", SubmitOptions{})
	if err != nil {
		t.Fatalf("BuildSubmitTPDUsWithOptions() error = %v", err)
	}
	if withoutSRR[0][0]&0x20 != 0 {
		t.Fatalf("SRR bit unexpectedly set: first octet = 0x%02x", withoutSRR[0][0])
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex decode: %v", err)
	}
	return b
}

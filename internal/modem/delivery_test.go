package modem

import (
	"testing"
)

// Same status-report PDU as pkg/smscodec/status_report_test.go:
// MR 42, recipient 8613800138000, ST 0 delivered.
const testCDSPDUHex = "00022A0D91683108108300F0621160214365006211602143660000"

func TestPendingCDSConsumesPDULine(t *testing.T) {
	m := &Manager{}
	var got []SMSDeliveryReport
	m.SetDeliveryReportCallback(func(rep SMSDeliveryReport) { got = append(got, rep) })

	// No pending state: a hex line is not consumed.
	if _, ok := m.takePendingCDS(testCDSPDUHex); ok {
		t.Fatal("takePendingCDS() = true without a +CDS header")
	}

	// Header arms it; the next hex line completes it.
	m.noteCDSHeader(25)
	rep, ok := m.takePendingCDS(testCDSPDUHex)
	if !ok {
		t.Fatal("takePendingCDS() = false, want true")
	}
	if rep.MessageRef != 42 || rep.State != "delivered" {
		t.Fatalf("report = %+v, want MR 42 delivered", rep)
	}
	// Consumed: a second PDU line is not a report.
	if _, ok := m.takePendingCDS(testCDSPDUHex); ok {
		t.Fatal("takePendingCDS() = true after consumption")
	}

	// The callback path dispatches through the same state machine.
	m.handleURC("+CDS: 25")
	rep, ok = m.takePendingCDS(testCDSPDUHex)
	if !ok {
		t.Fatal("takePendingCDS() after handleURC(+CDS) = false")
	}
	m.dispatchDeliveryReport(rep)
	if len(got) != 1 || got[0].MessageRef != 42 {
		t.Fatalf("callback got %+v, want one MR-42 report", got)
	}
}

func TestPendingCDSMalformedInputNeverCrashes(t *testing.T) {
	m := &Manager{}
	m.SetDeliveryReportCallback(func(SMSDeliveryReport) { t.Fatal("callback must not fire for malformed input") })

	// Non-hex line clears the pending state instead of swallowing traffic.
	m.noteCDSHeader(25)
	if _, ok := m.takePendingCDS("OK"); ok {
		t.Fatal("takePendingCDS(OK) = true")
	}
	if _, ok := m.takePendingCDS(testCDSPDUHex); ok {
		t.Fatal("takePendingCDS() = true after the pending state was cleared")
	}

	// Hex but not a status report (a DELIVER PDU) is rejected.
	m.noteCDSHeader(25)
	deliverPDU := "079144872000302320048102020000625061028204401AD9775D0E72D7DBE2B21C949E8360B75A4E7683D16AB71B"
	if _, ok := m.takePendingCDS(deliverPDU); ok {
		t.Fatal("takePendingCDS(DELIVER PDU) = true")
	}

	// A +CDS header with a bad length still arms the state machine; the
	// PDU line decides. A garbage header must not arm it.
	m.handleURC("+CDS: abc")
	if _, ok := m.takePendingCDS(testCDSPDUHex); ok {
		t.Fatal("takePendingCDS() = true after a garbage +CDS header")
	}

	// Nil callback is safe.
	m2 := &Manager{}
	m2.noteCDSHeader(25)
	rep, ok := m2.takePendingCDS(testCDSPDUHex)
	if !ok {
		t.Fatal("takePendingCDS() = false")
	}
	m2.dispatchDeliveryReport(rep)
}

func TestFormatURCCDS(t *testing.T) {
	m := &Manager{}
	out := m.formatURC("+CDS: 25")
	if out.Key != "+CDS" {
		t.Fatalf("formatURC(+CDS) key = %q, want +CDS", out.Key)
	}
}

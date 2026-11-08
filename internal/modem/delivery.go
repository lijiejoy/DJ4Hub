package modem

import (
	"time"

	"github.com/WongLoki/DJ4Hub/pkg/smscodec"
)

// SMS delivery reports (Item 2).
//
// The modem reports delivery with a +CDS URC (3GPP TS 27.005) when
// AT+CNMI <ds>=1 is set (see initModem): the URC header "+CDS: <len>" is
// followed on the next line by the status-report PDU in hex. Because the
// serial reader splits input into lines, the Manager keeps a small
// pending-CDS state: the header arms it, the next hex line completes it.
// A pending state older than 10 s, or a non-hex line, clears it without
// touching anything else, so a malformed +CDS can never swallow later
// traffic or crash the reader. Status reports stored on the module are
// also caught in decodePDU (stored-report MTI check) and routed here.
//
// The callback is invoked synchronously from the reader path; it must be
// fast (record state, return) and must never block on AT commands.

// SMSDeliveryReport is one decoded SMS-STATUS-REPORT.
type SMSDeliveryReport struct {
	MessageRef    int       `json:"message_ref"`
	Recipient     string    `json:"recipient"`
	Status        int       `json:"status"`
	StatusText    string    `json:"status_text"`
	State         string    `json:"state"` // delivered | pending | failed | unknown
	SCTS          time.Time `json:"scts"`
	DischargeTime time.Time `json:"discharge_time"`
}

// DeliveryReportCallback receives decoded status reports.
type DeliveryReportCallback func(SMSDeliveryReport)

const cdsPendingTTL = 10 * time.Second

// SetDeliveryReportCallback registers the delivery-report sink. A nil
// callback disables delivery (reports are still parsed and logged).
func (m *Manager) SetDeliveryReportCallback(cb DeliveryReportCallback) {
	m.cdsMu.Lock()
	defer m.cdsMu.Unlock()
	m.deliveryCallback = cb
}

func (m *Manager) dispatchDeliveryReport(rep SMSDeliveryReport) {
	m.cdsMu.Lock()
	cb := m.deliveryCallback
	m.cdsMu.Unlock()
	if cb != nil {
		cb(rep)
	}
}

// noteCDSHeader arms the pending-CDS state from a "+CDS: <len>" URC line.
func (m *Manager) noteCDSHeader(length int) {
	m.cdsMu.Lock()
	defer m.cdsMu.Unlock()
	m.awaitingCDS = true
	m.cdsLen = length
	m.cdsSince = time.Now()
}

// takePendingCDS consumes one input line as a +CDS PDU when a report is
// pending. It returns the decoded report and true on success; otherwise
// false (and the pending state is cleared, so the line is processed
// normally by the caller).
func (m *Manager) takePendingCDS(line string) (SMSDeliveryReport, bool) {
	m.cdsMu.Lock()
	pending := m.awaitingCDS
	fresh := time.Since(m.cdsSince) < cdsPendingTTL
	m.cdsMu.Unlock()
	if !pending || !fresh {
		if pending {
			m.cdsMu.Lock()
			m.awaitingCDS = false
			m.cdsMu.Unlock()
		}
		return SMSDeliveryReport{}, false
	}
	if !smscodec.IsHexString(line) {
		m.cdsMu.Lock()
		m.awaitingCDS = false
		m.cdsMu.Unlock()
		return SMSDeliveryReport{}, false
	}
	rep, err := smscodec.DecodeStatusReportFullPDU(line)
	m.cdsMu.Lock()
	m.awaitingCDS = false
	m.cdsMu.Unlock()
	if err != nil {
		return SMSDeliveryReport{}, false
	}
	return smsDeliveryReportFromCodec(rep), true
}

func smsDeliveryReportFromCodec(rep smscodec.StatusReport) SMSDeliveryReport {
	return SMSDeliveryReport{
		MessageRef:    rep.MessageRef,
		Recipient:     rep.Recipient,
		Status:        rep.Status,
		StatusText:    rep.StatusText,
		State:         rep.State,
		SCTS:          rep.SCTS,
		DischargeTime: rep.DischargeTime,
	}
}

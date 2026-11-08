package main

import (
	"encoding/csv"
	"strings"
	"sync"
	"time"
)

type modemCallEvent struct {
	Kind, Number string
	At           time.Time
}

// USB responses and unsolicited indications share one byte stream. Assemble
// complete lines before dispatch so RING/CLIP are not lost inside a response.
type modemCallEvents struct {
	mu      sync.Mutex
	partial string
	events  []modemCallEvent
}

func atFields(value string) []string {
	reader := csv.NewReader(strings.NewReader(value))
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1
	fields, err := reader.Read()
	if err != nil {
		return nil
	}
	for i := range fields {
		fields[i] = strings.TrimSpace(fields[i])
	}
	return fields
}

func (m *modemCallEvents) feedCallBytes(chunk string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.partial += chunk
	for {
		i := strings.IndexAny(m.partial, "\r\n")
		if i < 0 {
			break
		}
		line := strings.TrimSpace(m.partial[:i])
		m.partial = m.partial[i+1:]
		upper := strings.ToUpper(line)
		event := modemCallEvent{At: time.Now()}
		switch {
		case upper == "RING", upper == "+CRING: VOICE":
			event.Kind = "ring"
		case strings.HasPrefix(upper, "+CLIP:"):
			fields := atFields(line[len("+CLIP:"):])
			if len(fields) < 1 {
				continue
			}
			event.Kind, event.Number = "ring", fields[0]
		case upper == "NO CARRIER", upper == "BUSY", upper == "NO ANSWER":
			event.Kind = "ended"
		default:
			continue
		}
		m.events = append(m.events, event)
		if len(m.events) > 64 {
			m.events = m.events[len(m.events)-64:]
		}
	}
	if len(m.partial) > 8192 {
		m.partial = ""
	}
}

func (m *modemCallEvents) takeCallEvents() []modemCallEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	events := m.events
	m.events = nil
	return events
}

// State 4/5, incoming direction and voice mode establish a real incoming
// call even when caller ID is withheld. This rejects the firmware's idle
// phantom entry (2,1,0,1,0,"",128).
func incomingCLCC(response string) (string, bool) {
	for _, line := range strings.Split(response, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "+CLCC:") {
			continue
		}
		fields := atFields(strings.TrimSpace(strings.TrimPrefix(line, "+CLCC:")))
		if len(fields) < 5 || fields[1] != "1" || (fields[2] != "4" && fields[2] != "5") || fields[3] != "0" {
			continue
		}
		number := ""
		if len(fields) > 5 {
			number = fields[5]
		}
		return number, true
	}
	return "", false
}

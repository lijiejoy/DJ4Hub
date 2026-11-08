package main

import (
	"context"
	"log"
	"time"
)

// checkIncomingCallUSBAT polls AT+CLCC on the USB-AT path (no URC stream
// there) and notifies on a ringing (stat 4) or waiting (stat 5) call.
// The firmware has an idle phantom entry; incomingCLCC rejects it while
// retaining real voice calls with missing/withheld caller ID.
func (a *app) checkIncomingCallUSBAT() {
	if a.demo || a.modem != nil {
		return
	}
	resp, err := a.runATCommand("AT+CLCC", 3*time.Second)
	if err != nil {
		log.Printf("[sip] incoming call poll failed: %v", err)
		return
	}
	if number, incoming := incomingCLCC(resp); incoming {
		if a.sip != nil {
			a.sip.IncomingCall(number)
		}
		a.notifyIncomingCall(number)
	}
}

func (a *app) processUSBCallEvents() {
	if a.usbAT == nil || a.sip == nil {
		return
	}
	events := a.usbAT.takeCallEvents()
	// A release supersedes earlier indications drained together. Do not
	// flash a stale incoming popup for a call the caller already cancelled.
	start := 0
	for i, event := range events {
		if event.Kind == "ended" {
			// Reconciliation confirms releases; a delayed USB release must
			// never hang up a newer call.
			start = i + 1
		}
	}
	for _, event := range events[start:] {
		if time.Since(event.At) > 5*time.Second {
			continue
		}
		if event.Kind == "ring" {
			a.sip.IncomingCall(event.Number)
			a.notifyIncomingCall(event.Number)
		}
	}
}

func (a *app) pollUSBCallEvents(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if a.sip.Config().Enabled {
				a.processUSBCallEvents()
			} else if a.usbAT != nil {
				a.usbAT.takeCallEvents()
			}
		}
	}
}

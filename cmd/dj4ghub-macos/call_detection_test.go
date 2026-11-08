package main

import (
	"testing"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/sip"
)

func TestIncomingCLCC(t *testing.T) {
	for _, tt := range []struct {
		name, response, number string
		incoming               bool
	}{
		{"number", "+CLCC: 1,1,4,0,0,\"12345\",129\r\nOK", "12345", true},
		{"withheld", "+CLCC: 1,1,4,0,0,\"\",128", "", true},
		{"missing", "+CLCC: 1,1,4,0,0", "", true},
		{"waiting", "+CLCC: 1,1,5,0,0,\"12345\",129", "12345", true},
		{"phantom", "+CLCC: 2,1,0,1,0,\"\",128", "", false},
		{"outgoing", "+CLCC: 1,0,4,0,0,\"12345\",129", "", false},
		{"data", "+CLCC: 1,1,4,1,0,\"12345\",129", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			number, incoming := incomingCLCC(tt.response)
			if number != tt.number || incoming != tt.incoming {
				t.Fatalf("got %q %v", number, incoming)
			}
		})
	}
}

func TestIncomingEventStream(t *testing.T) {
	var events modemCallEvents
	events.feedCallBytes("AT+CSQ\r\nRI")
	events.feedCallBytes("NG\r\n+CLIP: \"12345\",129\r\n+CSQ: 20,99\r\nOK\r\n")
	events.feedCallBytes("+CRING: VOICE\r\n+CLIP: \"\",128\r\nNO CARRIER\r\n")
	got := events.takeCallEvents()
	if len(got) != 5 || got[0].Kind != "ring" || got[1].Number != "12345" ||
		got[2].Kind != "ring" || got[3].Number != "" || got[4].Kind != "ended" {
		t.Fatalf("unexpected events: %+v", got)
	}
	if len(events.takeCallEvents()) != 0 {
		t.Fatal("queue did not drain")
	}
}

func TestIncomingDispatch(t *testing.T) {
	server := sip.New(sip.Config{Enabled: true}, nil)
	a := &app{demo: true, sip: server, usbAT: &usbAT{}}
	a.usbAT.feedCallBytes("RING\r\nNO CARRIER\r\n")
	a.processUSBCallEvents()
	if len(server.Calls()) != 0 {
		t.Fatal("cancelled call flashed a reminder")
	}
	a.usbAT.feedCallBytes("RING\r\n+CLIP: \"12345\",129\r\nRING\r\n")
	a.processUSBCallEvents()
	calls := server.Calls()
	if len(calls) != 1 || calls[0].Number != "12345" {
		t.Fatalf("calls: %+v", calls)
	}
	a.usbAT.events = []modemCallEvent{{Kind: "ended", At: time.Now().Add(-time.Minute)}}
	a.processUSBCallEvents()
	if server.Calls()[0].State != sip.CallStateRinging {
		t.Fatal("stale release ended current call")
	}
}

func TestIncomingStaleRing(t *testing.T) {
	server := sip.New(sip.Config{Enabled: true}, nil)
	a := &app{demo: true, sip: server, usbAT: &usbAT{}}
	a.usbAT.events = []modemCallEvent{{Kind: "ring", At: time.Now().Add(-time.Minute)}}
	a.processUSBCallEvents()
	if len(server.Calls()) != 0 {
		t.Fatal("stale ring resurrected a call")
	}
}

package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/sip"
)

func TestIncomingNativeAlertLifecycle(t *testing.T) {
	var dialogs, sounds atomic.Int32
	opened, stopped := make(chan struct{}), make(chan struct{})
	alerts := &localCallAlerts{
		dialog: func(ctx context.Context, number string, updates <-chan string) (string, error) {
			dialogs.Add(1)
			close(opened)
			<-ctx.Done()
			close(stopped)
			return "", ctx.Err()
		},
		sound:  func(context.Context) { sounds.Add(1) },
		action: func(string, string) error { t.Error("cancelled dialog acted on call"); return nil },
	}
	defer alerts.Close()
	call := sip.PublicCall{ID: "incoming", Direction: "inbound", State: sip.CallStateRinging}
	alerts.OnCallState(call)
	alerts.OnCallState(call)
	select {
	case <-opened:
	case <-time.After(time.Second):
		t.Fatal("dialog never opened")
	}
	call.State = sip.CallStateActive
	alerts.OnCallState(call)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("dialog did not close on answer")
	}
	alerts.Close()
	if dialogs.Load() != 1 {
		t.Fatal("duplicate dialogs")
	}
}

func TestIncomingNativeAlertAction(t *testing.T) {
	acted := make(chan string, 1)
	alerts := &localCallAlerts{
		dialog: func(context.Context, string, <-chan string) (string, error) { return "接听", nil },
		sound:  func(context.Context) {},
		action: func(id, action string) error { acted <- id + action; return nil },
	}
	defer alerts.Close()
	alerts.OnCallState(sip.PublicCall{ID: "exact-id", Direction: "inbound", State: sip.CallStateRinging})
	select {
	case action := <-acted:
		if action != "exact-id接听" {
			t.Fatal(action)
		}
	case <-time.After(time.Second):
		t.Fatal("answer not dispatched")
	}
}

func TestIncomingNativeCallerIDUpdatesWithoutReopening(t *testing.T) {
	shown := make(chan string, 2)
	alerts := &localCallAlerts{
		dialog: func(ctx context.Context, number string, updates <-chan string) (string, error) {
			shown <- number
			select {
			case number := <-updates:
				shown <- number
			case <-ctx.Done():
			}
			<-ctx.Done()
			return "", ctx.Err()
		},
		sound: func(context.Context) {},
	}
	defer alerts.Close()
	call := sip.PublicCall{ID: "same-call", Direction: "inbound", State: sip.CallStateRinging, Number: "unknown"}
	alerts.OnCallState(call)
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("window did not open")
	}
	call.Number = "12345"
	alerts.OnCallState(call)
	select {
	case number := <-shown:
		if number != "12345" {
			t.Fatal(number)
		}
	case <-time.After(time.Second):
		t.Fatal("late caller ID never reached the window")
	}
}

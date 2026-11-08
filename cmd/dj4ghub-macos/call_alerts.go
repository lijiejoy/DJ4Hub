package main

import (
	"context"
	"errors"
	"log"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/sip"
)

// Native alerts remain available when the web page is closed or suspended.
type localCallAlerts struct {
	mu      sync.Mutex
	id      string
	number  string
	updates chan string
	cancel  context.CancelFunc
	closed  bool
	wg      sync.WaitGroup
	dialog  func(context.Context, string, <-chan string) (string, error)
	sound   func(context.Context)
	action  func(string, string) error
}

func newLocalCallAlerts(server *sip.Server, panel *nativeCallPanel) *localCallAlerts {
	if runtime.GOOS != "darwin" || !nativeCallWindowAvailable {
		return nil
	}
	return &localCallAlerts{
		dialog: func(ctx context.Context, number string, updates <-chan string) (string, error) {
			if err := panel.Show(); err != nil {
				return "", err
			}
			// The panel outlives ringing; only the alert sound stops on answer.
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-ticker.C:
					if !panel.IsOpen() {
						return "", errors.New("原生通话窗口已关闭")
					}
				}
			}
		},
		sound: func(ctx context.Context) {
			_ = exec.CommandContext(ctx, "/usr/bin/afplay", "/System/Library/Sounds/Glass.aiff").Run()
		},
		action: func(id, action string) error {
			if action == "接听" {
				return server.AnswerCall(id)
			}
			return server.HangupCall(id)
		},
	}
}

func (a *localCallAlerts) OnCallState(call sip.PublicCall) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	if call.Direction != "inbound" || call.State != sip.CallStateRinging {
		if a.id == call.ID && a.cancel != nil {
			a.cancel()
			a.id, a.cancel = "", nil
		}
		return
	}
	if a.id == call.ID {
		if call.Number != a.number {
			a.number = call.Number
			select {
			case <-a.updates:
			default:
			}
			a.updates <- call.Number
		}
		return
	}
	if a.cancel != nil {
		a.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.id, a.cancel = call.ID, cancel
	a.number = call.Number
	updates := make(chan string, 1)
	a.updates = updates
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer cancel()
		defer func() {
			a.mu.Lock()
			if a.id == call.ID {
				a.id, a.cancel = "", nil
			}
			a.mu.Unlock()
		}()
		soundDone := make(chan struct{})
		go func() {
			defer close(soundDone)
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				if ctx.Err() != nil {
					return
				}
				a.sound(ctx)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
		defer func() { cancel(); <-soundDone }()
		log.Printf("[sip] native incoming alert opened")
		action, err := a.dialog(ctx, call.Number, updates)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("[sip] native incoming alert failed: %v", err)
			return
		}
		if action == "接听" || action == "拒接" {
			if err := a.action(call.ID, action); err != nil {
				log.Printf("[sip] native incoming action failed: %v", err)
			}
		}
	}()
}

func (a *localCallAlerts) RingingCallID() string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.id
}

func (a *localCallAlerts) Close() {
	a.mu.Lock()
	a.closed = true
	if a.cancel != nil {
		a.cancel()
	}
	a.mu.Unlock()
	a.wg.Wait()
}

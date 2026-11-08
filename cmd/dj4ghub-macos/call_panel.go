package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/sip"
)

type callPanelAction struct {
	Action          string `json:"action"`
	CallID          string `json:"call_id"`
	Number          string `json:"number"`
	Digit           string `json:"digit"`
	MicrophoneMuted bool   `json:"microphone_muted"`
	SpeakerMuted    bool   `json:"speaker_muted"`
	Sequence        int    `json:"sequence"`
}

type callPanelSnapshot struct {
	Calls    []sip.PublicCall      `json:"calls"`
	Audio    *sip.LocalAudioStatus `json:"audio,omitempty"`
	Focus    bool                  `json:"focus,omitempty"`
	Error    string                `json:"error,omitempty"`
	Sequence int                   `json:"sequence,omitempty"`
}

// The service owns call state; the replaceable UI process only renders snapshots
// and sends explicit actions over its private pipes.
type nativeCallPanel struct {
	mu     sync.Mutex
	wg     sync.WaitGroup
	cancel context.CancelFunc
	focus  chan struct{}
	closed bool
	run    func(context.Context, <-chan struct{}) error
}

func (p *nativeCallPanel) IsOpen() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cancel != nil && !p.closed
}

func (p *nativeCallPanel) Show() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("通话服务已关闭")
	}
	if p.cancel != nil {
		select {
		case p.focus <- struct{}{}:
		default:
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel, p.focus = cancel, make(chan struct{}, 1)
	focus := p.focus
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer cancel()
		if err := p.run(ctx, focus); err != nil && ctx.Err() == nil {
			log.Printf("[sip] native call panel failed: %v", err)
		}
		p.mu.Lock()
		p.cancel = nil
		p.mu.Unlock()
	}()
	return nil
}

func (p *nativeCallPanel) Close() {
	p.mu.Lock()
	p.closed = true
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Unlock()
	p.wg.Wait()
}

func (a *app) newCallPanel() *nativeCallPanel {
	if !nativeCallWindowAvailable {
		return nil
	}
	return &nativeCallPanel{run: func(ctx context.Context, focus <-chan struct{}) error {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		return runCallPanelProcess(ctx, executable, focus, func() callPanelSnapshot {
			status := a.sip.Status()
			return callPanelSnapshot{Calls: status.Calls, Audio: status.Audio.Local}
		}, a.performCallPanelAction)
	}}
}

func runCallPanelProcess(ctx context.Context, executable string, focus <-chan struct{},
	snapshot func() callPanelSnapshot, action func(callPanelAction) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "--call-window")
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	replies := make(chan callPanelSnapshot, 8)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer input.Close()
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		encoder := json.NewEncoder(input)
		if encoder.Encode(snapshot()) != nil {
			cancel()
			return
		}
		for {
			var update callPanelSnapshot
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				update = snapshot()
			case <-focus:
				update = snapshot()
				update.Focus = true
			case update = <-replies:
			}
			if encoder.Encode(update) != nil {
				cancel()
				return
			}
		}
	}()
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 16384)
	for scanner.Scan() {
		var request callPanelAction
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			continue
		}
		err := action(request)
		reply := snapshot()
		reply.Sequence = request.Sequence
		if err != nil {
			reply.Error = err.Error()
		}
		select {
		case replies <- reply:
		case <-ctx.Done():
		}
	}
	waitErr := cmd.Wait()
	cancel()
	<-writerDone
	if err := scanner.Err(); err != nil {
		return err
	}
	return waitErr
}

func (a *app) performCallPanelAction(request callPanelAction) error {
	if a.sip == nil {
		return errors.New("通话服务尚未就绪")
	}
	if request.Action == "dial" {
		number := strings.TrimSpace(request.Number)
		for i, char := range number {
			if (char < '0' || char > '9') && !(i == 0 && char == '+') {
				return errors.New("请输入有效的电话号码")
			}
		}
		_, err := a.sip.DialFromAPI(number)
		return err
	}
	var current *sip.PublicCall
	for _, call := range a.sip.Calls() {
		if request.CallID != "" && call.ID == request.CallID && call.LocalAudio &&
			(call.State == sip.CallStateDialing || call.State == sip.CallStateRinging || call.State == sip.CallStateActive) {
			copy := call
			current = &copy
			break
		}
	}
	if current == nil {
		return errors.New("这通电话已结束")
	}
	switch request.Action {
	case "answer":
		return a.sip.AnswerCall(current.ID)
	case "hangup":
		return a.sip.HangupCall(current.ID)
	case "dtmf":
		return a.sip.SendDTMF(current.ID, request.Digit)
	case "audio":
		if a.sipAudio == nil {
			return errors.New("本机音频不可用")
		}
		return a.sipAudio.SetLocalControls(current.ID, request.MicrophoneMuted, request.SpeakerMuted)
	default:
		return errors.New("不支持的通话操作")
	}
}

func (a *app) sipShowPanel(w http.ResponseWriter, r *http.Request) {
	if a.sipPanel == nil {
		writeError(w, http.StatusServiceUnavailable, "原生通话面板不可用")
		return
	}
	if err := a.sipPanel.Show(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

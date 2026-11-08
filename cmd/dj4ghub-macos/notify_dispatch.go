package main

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/sip"
)

// --- dispatch --------------------------------------------------------------

type notifyEvent struct {
	Kind   string    `json:"kind"` // sms | call | test
	Title  string    `json:"title"`
	Body   string    `json:"body"`
	Code   string    `json:"code,omitempty"`
	Sender string    `json:"sender,omitempty"`
	Time   time.Time `json:"time"`
}

type notifyLogEntry struct {
	Time    string `json:"time"`
	Channel string `json:"channel"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

func (a *app) recordNotifyResult(channel, kind, title string, err error) {
	err = safeNotifyFailure(channel, err)
	a.notifyStatusMu.Lock()
	st := notifyChannelStatus{Channel: channel, LastOK: err == nil, LastAt: time.Now().Format(time.RFC3339)}
	if err != nil {
		st.LastError = err.Error()
	}
	if a.notifyStatus == nil {
		a.notifyStatus = make(map[string]notifyChannelStatus)
	}
	a.notifyStatus[channel] = st
	a.notifyStatusMu.Unlock()

	a.notifyLogMu.Lock()
	defer a.notifyLogMu.Unlock()
	entry := notifyLogEntry{
		Time: time.Now().Format("2006-01-02 15:04:05"), Channel: channel,
		Kind: kind, Title: title, OK: err == nil,
	}
	if err != nil {
		entry.Error = err.Error()
	}
	a.notifyLog = append([]notifyLogEntry{entry}, a.notifyLog...)
	if len(a.notifyLog) > notifyLogCap {
		a.notifyLog = a.notifyLog[:notifyLogCap]
	}
}

func (a *app) recordNotifyEvent(kind, title string) {
	a.notifyLogMu.Lock()
	defer a.notifyLogMu.Unlock()
	entry := notifyLogEntry{
		Time: time.Now().Format("2006-01-02 15:04:05"), Channel: "-",
		Kind: kind, Title: title, OK: false, Error: "未配置任何通知通道",
	}
	a.notifyLog = append([]notifyLogEntry{entry}, a.notifyLog...)
	if len(a.notifyLog) > notifyLogCap {
		a.notifyLog = a.notifyLog[:notifyLogCap]
	}
}

// dispatchNotify fans an event out to every enabled+configured channel.
// It never blocks the caller: delivery runs in its own goroutine.
func (a *app) dispatchNotify(ev notifyEvent) {
	if a.demo {
		return
	}
	go a.sendNotify(ev)
}

func (a *app) sendNotify(ev notifyEvent) {
	cfg := a.getNotifySettings()
	sent := 0
	var wg sync.WaitGroup
	for _, channel := range []string{"telegram", "feishu", "bark", "email", "pushplus", "webhook"} {
		enabled := false
		switch channel {
		case "telegram":
			enabled = cfg.Telegram.Enabled
		case "feishu":
			enabled = cfg.Feishu.Enabled
		case "bark":
			enabled = cfg.Bark.Enabled
		case "email":
			enabled = cfg.Email.Enabled
		case "pushplus":
			enabled = cfg.Pushplus.Enabled
		case "webhook":
			enabled = cfg.Webhook.Enabled
		}
		if !enabled || !cfg.channelConfigured(channel) {
			continue
		}
		sent++
		wg.Add(1)
		go func(channel string) {
			defer wg.Done()
			err := a.sendToChannel(cfg, channel, ev)
			a.recordNotifyResult(channel, ev.Kind, ev.Title, err)
			if err != nil {
				log.Printf("[notify] %s %s failed: %v", channel, ev.Kind, safeNotifyFailure(channel, err))
			}
		}(channel)
	}
	wg.Wait()
	if sent == 0 {
		a.recordNotifyEvent(ev.Kind, ev.Title)
		log.Printf("[notify] %s: no channel enabled/configured, event logged only", ev.Kind)
	}
}

// notifyNewSMS is called with messages mergeSMS has just accepted as new.
// Only recent messages notify: the startup cache fill (old stored SMS)
// must not replay as pushes.
func (a *app) notifyNewSMS(items []receivedSMS) {
	if a.demo || len(items) == 0 {
		return
	}
	cfg := a.getNotifySettings()
	if !cfg.NotifySMS {
		return
	}
	cutoff := time.Now().Add(-notifySMSRecency)
	for _, item := range items {
		if item.Timestamp.Before(cutoff) {
			continue
		}
		body := "来自 " + item.Sender + "\n" + item.Content
		title := "新短信"
		if item.Code != "" {
			title = "验证码 " + item.Code
			body += "\n验证码：" + item.Code
		}
		a.dispatchNotify(notifyEvent{
			Kind: "sms", Title: title, Body: body,
			Code: item.Code, Sender: item.Sender, Time: item.Timestamp,
		})
	}
}

// A late caller number updates the same call; a second call from the same
// number has a new identity and must still notify immediately.
func (a *app) notifyIncomingCall(number string) {
	if a.demo {
		return
	}
	cfg := a.getNotifySettings()
	if !cfg.NotifyCall {
		return
	}
	number = strings.TrimSpace(number)
	key := number
	if a.sip != nil {
		key = ""
		for _, call := range a.sip.Calls() {
			if call.Direction == "inbound" && call.State == sip.CallStateRinging {
				key = call.ID
				number = call.Number
				break
			}
		}
		if key == "" {
			return
		}
	}
	a.notifyRingMu.Lock()
	if a.notifyRingLast == nil {
		a.notifyRingLast = make(map[string]time.Time)
	}
	for id, last := range a.notifyRingLast {
		if time.Since(last) >= notifyCallDedup && (a.sip == nil || id != key) {
			delete(a.notifyRingLast, id)
		}
	}
	if last, ok := a.notifyRingLast[key]; ok && (a.sip != nil || time.Since(last) < notifyCallDedup) {
		a.notifyRingMu.Unlock()
		return
	}
	a.notifyRingLast[key] = time.Now()
	a.notifyRingMu.Unlock()

	display := number
	if display == "" {
		display = "未知号码"
	}
	a.dispatchNotify(notifyEvent{
		Kind: "call", Title: "来电",
		Body: "来电：" + display, Sender: number, Time: time.Now(),
	})
}

// --- Telegram command bot --------------------------------------------------

// startNotifier launches the Telegram command bot poller (unless demo).
// The poller re-reads settings every loop, so a token configured later in
// the UI takes effect without a restart. Delivery itself needs no
// background loop: dispatchNotify spawns a goroutine per event.
func (a *app) startNotifier() {
	if a.demo {
		return
	}
	go a.telegramBotLoop()
}

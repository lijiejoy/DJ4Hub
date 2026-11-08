package main

import (
	"context"
	"github.com/WongLoki/DJ4Hub/pkg/smscodec"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

type receivedSMS struct {
	Sender    string    `json:"sender"`
	Content   string    `json:"content"`
	Code      string    `json:"code,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	// Concat carries long-SMS reassembly metadata (USB-AT path only);
	// nil for single-part messages. Persisted to history for dedup.
	Concat *smscodec.ConcatInfo `json:"-"`
}

func (a *app) recordSMS(sender, content string, timestamp time.Time) {
	a.mergeSMS([]receivedSMS{{
		Sender: sender, Content: content, Timestamp: timestamp,
	}})
}

func (a *app) mergeSMS(messages []receivedSMS) (newCount int, total int, persistErr error) {
	a.smsMu.Lock()
	defer a.smsMu.Unlock()
	newItems := make([]receivedSMS, 0, len(messages))
	seen := make(map[string]bool, len(a.sms)+len(messages))
	for _, item := range a.sms {
		seen[smsCacheKey(item)] = true
	}
	for _, item := range messages {
		if item.Code == "" {
			item.Code = extractSMSCode(item.Content)
		}
		key := smsCacheKey(item)
		if seen[key] {
			continue
		}
		seen[key] = true
		a.sms = append(a.sms, item)
		newItems = append(newItems, item)
		newCount++
	}
	sort.SliceStable(a.sms, func(i, j int) bool {
		return a.sms[i].Timestamp.After(a.sms[j].Timestamp)
	})
	if len(a.sms) > 500 {
		a.sms = a.sms[:500]
	}
	// Batch 1: newly accepted messages notify (recency-guarded inside).
	a.notifyNewSMS(newItems)
	// 交付点 2：先落盘后清理的前提——新短信先持久化；落盘失败
	// 由调用方（pollSMSOnce）据此跳过模块清理。内存缓存不受影响。
	persistErr = a.persistReceivedSMS(newItems)
	return newCount, len(a.sms), persistErr
}

func smsCacheKey(item receivedSMS) string {
	return item.Sender + "\x00" + item.Content + "\x00" + item.Timestamp.Format(time.RFC3339Nano)
}

func (a *app) setSMSPollStatus(err error) {
	a.smsMu.Lock()
	defer a.smsMu.Unlock()
	a.smsLastPoll = time.Now()
	if err != nil {
		a.smsLastPollError = err.Error()
		return
	}
	a.smsLastPollError = ""
}

func (a *app) startSMSPoller(ctx context.Context) {
	interval := a.smsPollInterval
	if interval <= 0 {
		interval = 8 * time.Second
	}
	timer := time.NewTimer(1200 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := a.pollSMSOnce(); err != nil {
				log.Printf("SMS poll failed: %v", err)
			}
			timer.Reset(interval)
		}
	}
}

func (a *app) pollSMSOnce() error {
	if a.demo || a.modem != nil {
		return nil
	}
	if err := a.ensureUSBAT(); err != nil {
		a.setSMSPollStatus(err)
		return err
	}
	messages, err := a.readUSBATSMS()
	if err != nil {
		a.resetUSBATIfGone(err)
		a.setSMSPollStatus(err)
		return err
	}
	newCount, total, persistErr := a.mergeSMS(messages)
	if a.smsAutoCleanupME && len(messages) > 0 {
		if persistErr != nil {
			// 落盘失败：保留模块数据，跳过本轮清理；故障由
			// /api/sms/status 的 history_error 对外显示。
			log.Printf("skip auto cleanup ME SMS: history persist failed (%v); module data kept", persistErr)
		} else if before, after, cleanupErr := a.clearUSBATSMSMemory("ME"); cleanupErr != nil {
			log.Printf("auto cleanup ME SMS failed: %v", cleanupErr)
		} else if before != after {
			log.Printf("auto cleanup ME SMS: %d -> %d", before, after)
		}
	}
	a.setSMSPollStatus(nil)
	if newCount > 0 {
		log.Printf("SMS poll cached %d new message(s), total %d", newCount, total)
	}
	return nil
}

func (a *app) listSMS(w http.ResponseWriter, _ *http.Request) {
	a.smsMu.RLock()
	items := append([]receivedSMS(nil), a.sms...)
	a.smsMu.RUnlock()
	if items == nil {
		items = []receivedSMS{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *app) smsStatus(w http.ResponseWriter, _ *http.Request) {
	a.smsMu.RLock()
	lastPoll := a.smsLastPoll
	lastPollError := a.smsLastPollError
	count := len(a.sms)
	a.smsMu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"count":           count,
		"polling":         !a.demo && a.modem == nil,
		"poll_interval_s": int(a.smsPollInterval.Seconds()),
		"auto_cleanup_me": a.smsAutoCleanupME,
		"last_poll":       lastPoll,
		"last_poll_error": lastPollError,
		"history_error":   historyLastErrorString(),
	})
}

func (a *app) refreshSMS(w http.ResponseWriter, _ *http.Request) {
	if a.demo {
		writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
		return
	}
	if a.modem == nil {
		if err := a.pollSMSOnce(); err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		a.smsMu.RLock()
		count := len(a.sms)
		a.smsMu.RUnlock()
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "count": count})
		return
	}
	go a.modem.CheckAllSMS()
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (a *app) clearModuleSMS(w http.ResponseWriter, _ *http.Request) {
	if a.demo {
		writeJSON(w, http.StatusOK, map[string]any{"cleared": true, "before": 0, "after": 0})
		return
	}
	if a.modem != nil {
		writeError(w, http.StatusServiceUnavailable, "module SMS cleanup is only available through USB AT")
		return
	}
	if err := a.ensureUSBAT(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "AT serial port is unavailable: "+err.Error())
		return
	}
	before, after, err := a.clearUSBATSMSMemory("ME")
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cleared": true,
		"memory":  "ME",
		"before":  before,
		"after":   after,
	})
}

func (a *app) sendSMS(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone   string `json:"phone"`
		Message string `json:"message"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Phone) == "" || strings.TrimSpace(body.Message) == "" {
		writeError(w, http.StatusBadRequest, "phone and message are required")
		return
	}
	if a.demo {
		a.recordSMS("已发送至 "+body.Phone, body.Message, time.Now())
		a.recordSentSMS(body.Phone, []int{-1})
		a.recordOutboxAttempt(body.Phone, body.Message, 1, []int{-1}, nil)
		writeJSON(w, http.StatusOK, map[string]any{"sent": true, "segments": 1})
		return
	}
	segments, mrs, err := a.sendTextSMS(body.Phone, body.Message)
	if err != nil {
		// 发件箱记 uncertain（无法证明未发送），永不自动补发。
		a.recordOutboxAttempt(body.Phone, body.Message, segments, mrs, err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.recordSentSMS(body.Phone, mrs)
	a.recordOutboxAttempt(body.Phone, body.Message, segments, mrs, nil)
	writeJSON(w, http.StatusOK, map[string]any{"sent": true, "segments": segments, "message_refs": mrs})
}

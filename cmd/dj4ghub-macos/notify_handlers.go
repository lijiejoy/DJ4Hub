package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// --- HTTP handlers --------------------------------------------------------

func (a *app) notifySettingsGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.notifySettingsView())
}

func (a *app) notifySettingsSave(w http.ResponseWriter, r *http.Request) {
	// Partial update: only the channels/fields present in the body are
	// applied, so saving one channel's form never wipes another channel's
	// URLs. Secrets keep their stored value when sent empty.
	var raw map[string]json.RawMessage
	if !decodeJSON(w, r, &raw) {
		return
	}
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	if a.notify == nil {
		a.notify = a.loadNotifySettingsLocked()
	}
	cur := a.notify
	apply := func(key string, dst any) bool {
		b, ok := raw[key]
		if !ok {
			return false
		}
		if err := json.Unmarshal(b, dst); err != nil {
			return false
		}
		return true
	}
	var tg struct {
		Enabled  *bool   `json:"enabled"`
		BotToken *string `json:"bot_token"`
		ChatID   *int64  `json:"chat_id"`
		AdminID  *int64  `json:"admin_id"`
		BaseURL  *string `json:"base_url"`
	}
	if apply("telegram", &tg) {
		if tg.Enabled != nil {
			cur.Telegram.Enabled = *tg.Enabled
		}
		if tg.BotToken != nil && strings.TrimSpace(*tg.BotToken) != "" {
			cur.Telegram.BotToken = strings.TrimSpace(*tg.BotToken)
		}
		if tg.ChatID != nil {
			cur.Telegram.ChatID = *tg.ChatID
		}
		if tg.AdminID != nil {
			cur.Telegram.AdminID = *tg.AdminID
		}
		if tg.BaseURL != nil {
			cur.Telegram.BaseURL = strings.TrimSpace(*tg.BaseURL)
		}
	}
	var fs struct {
		Enabled    *bool   `json:"enabled"`
		WebhookURL *string `json:"webhook_url"`
	}
	if apply("feishu", &fs) {
		if fs.Enabled != nil {
			cur.Feishu.Enabled = *fs.Enabled
		}
		if fs.WebhookURL != nil {
			cur.Feishu.WebhookURL = strings.TrimSpace(*fs.WebhookURL)
		}
	}
	var bk struct {
		Enabled *bool     `json:"enabled"`
		URLs    *[]string `json:"urls"`
		Group   *string   `json:"group"`
		Level   *string   `json:"level"`
	}
	if apply("bark", &bk) {
		if bk.Enabled != nil {
			cur.Bark.Enabled = *bk.Enabled
		}
		if bk.URLs != nil {
			cur.Bark.URLs = cleanURLList(*bk.URLs)
		}
		if bk.Group != nil {
			cur.Bark.Group = strings.TrimSpace(*bk.Group)
		}
		if bk.Level != nil {
			cur.Bark.Level = strings.TrimSpace(*bk.Level)
		}
	}
	var em struct {
		Enabled     *bool     `json:"enabled"`
		UseSSL      *bool     `json:"use_ssl"`
		SMTPHost    *string   `json:"smtp_host"`
		SMTPPort    *int      `json:"smtp_port"`
		Username    *string   `json:"username"`
		Password    *string   `json:"password"`
		FromAddress *string   `json:"from_address"`
		ToAddresses *[]string `json:"to_addresses"`
	}
	if apply("email", &em) {
		if em.Enabled != nil {
			cur.Email.Enabled = *em.Enabled
		}
		if em.UseSSL != nil {
			cur.Email.UseSSL = *em.UseSSL
		}
		if em.SMTPHost != nil {
			cur.Email.SMTPHost = strings.TrimSpace(*em.SMTPHost)
		}
		if em.SMTPPort != nil {
			cur.Email.SMTPPort = *em.SMTPPort
		}
		if em.Username != nil {
			cur.Email.Username = strings.TrimSpace(*em.Username)
		}
		if em.Password != nil && *em.Password != "" {
			cur.Email.Password = *em.Password
		}
		if em.FromAddress != nil {
			cur.Email.FromAddress = strings.TrimSpace(*em.FromAddress)
		}
		if em.ToAddresses != nil {
			cur.Email.ToAddresses = cleanStringList(*em.ToAddresses)
		}
	}
	var pp struct {
		Enabled *bool   `json:"enabled"`
		Token   *string `json:"token"`
		Topic   *string `json:"topic"`
		Channel *string `json:"channel"`
	}
	if apply("pushplus", &pp) {
		if pp.Enabled != nil {
			cur.Pushplus.Enabled = *pp.Enabled
		}
		if pp.Token != nil && strings.TrimSpace(*pp.Token) != "" {
			cur.Pushplus.Token = strings.TrimSpace(*pp.Token)
		}
		if pp.Topic != nil {
			cur.Pushplus.Topic = strings.TrimSpace(*pp.Topic)
		}
		if pp.Channel != nil {
			cur.Pushplus.Channel = strings.TrimSpace(*pp.Channel)
		}
	}
	var wh struct {
		Enabled   *bool     `json:"enabled"`
		URLs      *[]string `json:"urls"`
		Secret    *string   `json:"secret"`
		TimeoutMs *int      `json:"timeout_ms"`
		RetryMax  *int      `json:"retry_max"`
	}
	if apply("webhook", &wh) {
		if wh.Enabled != nil {
			cur.Webhook.Enabled = *wh.Enabled
		}
		if wh.URLs != nil {
			cur.Webhook.URLs = cleanURLList(*wh.URLs)
		}
		if wh.Secret != nil && *wh.Secret != "" {
			cur.Webhook.Secret = *wh.Secret
		}
		if wh.TimeoutMs != nil {
			cur.Webhook.TimeoutMs = *wh.TimeoutMs
		}
		if wh.RetryMax != nil {
			cur.Webhook.RetryMax = *wh.RetryMax
		}
	}
	if b, ok := raw["notify_sms"]; ok {
		var v bool
		if err := json.Unmarshal(b, &v); err == nil {
			cur.NotifySMS = v
		}
	}
	if b, ok := raw["notify_calls"]; ok {
		var v bool
		if err := json.Unmarshal(b, &v); err == nil {
			cur.NotifyCall = v
		}
	}
	if err := validateNotifySettings(cur); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.saveNotifySettingsLocked(cur); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.notifySettingsViewFor(cur))
}

func (a *app) notifyTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Channel string `json:"channel"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	channel := strings.TrimSpace(body.Channel)
	switch channel {
	case "telegram", "feishu", "bark", "email", "pushplus", "webhook":
	default:
		writeError(w, http.StatusBadRequest, "未知通知通道："+channel)
		return
	}
	cfg := a.getNotifySettings()
	if !cfg.channelConfigured(channel) {
		writeError(w, http.StatusUnprocessableEntity, channel+" 未配置：请先在通知设置里填写该通道的凭据/地址，再测试")
		return
	}
	if a.demo {
		writeJSON(w, http.StatusOK, map[string]any{
			"channel": channel, "ok": false, "dry_run": true,
			"error": "演示模式不实际发送",
		})
		return
	}
	ev := notifyEvent{
		Kind:  "test",
		Title: "通知测试",
		Body:  "这是一条测试消息，通道工作正常。",
		Time:  time.Now(),
	}
	err := a.sendToChannel(cfg, channel, ev)
	a.recordNotifyResult(channel, ev.Kind, ev.Title, err)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"channel": channel, "ok": false, "error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channel": channel, "ok": true})
}

func (a *app) notifyLogHandler(w http.ResponseWriter, _ *http.Request) {
	a.notifyLogMu.Lock()
	defer a.notifyLogMu.Unlock()
	entries := make([]notifyLogEntry, len(a.notifyLog))
	copy(entries, a.notifyLog)
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

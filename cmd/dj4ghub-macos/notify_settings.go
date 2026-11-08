package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const notifyFileName = "notifications.json"

// pushplusEndpoint is a var so tests can point it at a local server.
var pushplusEndpoint = "https://www.pushplus.plus/send"

const (
	notifySendTimeout = 10 * time.Second
	notifyLogCap      = 100
	notifyCallDedup   = 90 * time.Second
	notifySMSRecency  = 15 * time.Minute
	botConfirmTTL     = 5 * time.Minute
)

// --- settings ------------------------------------------------------------

type telegramNotifySettings struct {
	Enabled  bool   `json:"enabled"`
	BotToken string `json:"bot_token,omitempty"`
	ChatID   int64  `json:"chat_id,omitempty"`
	AdminID  int64  `json:"admin_id,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
}

type feishuNotifySettings struct {
	Enabled    bool   `json:"enabled"`
	WebhookURL string `json:"webhook_url,omitempty"`
}

type barkNotifySettings struct {
	Enabled bool     `json:"enabled"`
	URLs    []string `json:"urls,omitempty"`
	Group   string   `json:"group,omitempty"`
	Level   string   `json:"level,omitempty"`
}

type emailNotifySettings struct {
	Enabled     bool     `json:"enabled"`
	UseSSL      bool     `json:"use_ssl,omitempty"`
	SMTPHost    string   `json:"smtp_host,omitempty"`
	SMTPPort    int      `json:"smtp_port,omitempty"`
	Username    string   `json:"username,omitempty"`
	Password    string   `json:"password,omitempty"`
	FromAddress string   `json:"from_address,omitempty"`
	ToAddresses []string `json:"to_addresses,omitempty"`
}

type pushplusNotifySettings struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token,omitempty"`
	Topic   string `json:"topic,omitempty"`
	Channel string `json:"channel,omitempty"`
}

type webhookNotifySettings struct {
	Enabled   bool     `json:"enabled"`
	URLs      []string `json:"urls,omitempty"`
	Secret    string   `json:"secret,omitempty"`
	TimeoutMs int      `json:"timeout_ms,omitempty"`
	RetryMax  int      `json:"retry_max,omitempty"`
}

type notifySettings struct {
	Telegram   telegramNotifySettings `json:"telegram"`
	Feishu     feishuNotifySettings   `json:"feishu"`
	Bark       barkNotifySettings     `json:"bark"`
	Email      emailNotifySettings    `json:"email"`
	Pushplus   pushplusNotifySettings `json:"pushplus"`
	Webhook    webhookNotifySettings  `json:"webhook"`
	NotifySMS  bool                   `json:"notify_sms"`
	NotifyCall bool                   `json:"notify_calls"`
}

func defaultNotifySettings() *notifySettings {
	return &notifySettings{
		NotifySMS:  true,
		NotifyCall: true,
		Bark:       barkNotifySettings{Group: "DJ 4G Hub", Level: "active"},
		Webhook:    webhookNotifySettings{TimeoutMs: 5000, RetryMax: 2},
	}
}

func (a *app) notifyPath() string {
	dir, err := a.sipConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, notifyFileName)
}

func (a *app) loadNotifySettingsLocked() *notifySettings {
	path := a.notifyPath()
	if path == "" {
		return defaultNotifySettings()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return defaultNotifySettings()
	}
	cfg := defaultNotifySettings()
	if err := json.Unmarshal(raw, cfg); err != nil {
		log.Printf("[notify] %s is corrupt, starting with defaults: %v", path, err)
		return defaultNotifySettings()
	}
	if cfg.Webhook.TimeoutMs <= 0 {
		cfg.Webhook.TimeoutMs = 5000
	}
	if cfg.Webhook.RetryMax < 0 {
		cfg.Webhook.RetryMax = 2
	}
	if cfg.Bark.Group == "" {
		cfg.Bark.Group = "DJ 4G Hub"
	}
	return cfg
}

func (a *app) saveNotifySettingsLocked(cfg *notifySettings) error {
	path := a.notifyPath()
	if path == "" {
		return errors.New("no config directory for notifications")
	}
	raw, err := json.MarshalIndent(cfg, "", " ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// getNotifySettings returns a copy of the current settings (lazy load).
func (a *app) getNotifySettings() *notifySettings {
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	if a.notify == nil {
		a.notify = a.loadNotifySettingsLocked()
	}
	cp := *a.notify
	return &cp
}

// --- masked view + per-channel status -------------------------------------

type notifyChannelStatus struct {
	Channel    string `json:"channel"`
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`
	LastOK     bool   `json:"last_ok"`
	LastError  string `json:"last_error,omitempty"`
	LastAt     string `json:"last_at,omitempty"`
}

func (s *notifySettings) telegramConfigured() bool {
	return strings.TrimSpace(s.Telegram.BotToken) != "" && s.Telegram.ChatID != 0
}

func (s *notifySettings) feishuConfigured() bool { return strings.TrimSpace(s.Feishu.WebhookURL) != "" }

func (s *notifySettings) barkConfigured() bool { return len(s.Bark.URLs) > 0 }

func (s *notifySettings) pushplusConfigured() bool {
	return strings.TrimSpace(s.Pushplus.Token) != ""
}

func (s *notifySettings) webhookConfigured() bool { return len(s.Webhook.URLs) > 0 }

func (s *notifySettings) emailConfigured() bool {
	return strings.TrimSpace(s.Email.SMTPHost) != "" && s.Email.SMTPPort > 0 && len(s.Email.ToAddresses) > 0
}

func (s *notifySettings) channelConfigured(channel string) bool {
	switch channel {
	case "telegram":
		return s.telegramConfigured()
	case "feishu":
		return s.feishuConfigured()
	case "bark":
		return s.barkConfigured()
	case "email":
		return s.emailConfigured()
	case "pushplus":
		return s.pushplusConfigured()
	case "webhook":
		return s.webhookConfigured()
	}
	return false
}

func validHTTPURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (a *app) notifySettingsView() map[string]any {
	return a.notifySettingsViewFor(a.getNotifySettings())
}

// notifySettingsViewFor builds the masked view from an already-loaded cfg.
// It must be used (instead of notifySettingsView) by callers that already
// hold notifyMu, e.g. notifySettingsSave: getNotifySettings would try to
// lock notifyMu a second time and deadlock (sync.Mutex is not reentrant).
func (a *app) notifySettingsViewFor(cfg *notifySettings) map[string]any {
	a.notifyStatusMu.Lock()
	defer a.notifyStatusMu.Unlock()
	status := func(channel string) notifyChannelStatus {
		st := notifyChannelStatus{Channel: channel, Configured: cfg.channelConfigured(channel)}
		switch channel {
		case "telegram":
			st.Enabled = cfg.Telegram.Enabled
		case "feishu":
			st.Enabled = cfg.Feishu.Enabled
		case "bark":
			st.Enabled = cfg.Bark.Enabled
		case "email":
			st.Enabled = cfg.Email.Enabled
		case "pushplus":
			st.Enabled = cfg.Pushplus.Enabled
		case "webhook":
			st.Enabled = cfg.Webhook.Enabled
		}
		if prev, ok := a.notifyStatus[channel]; ok {
			st.LastOK = prev.LastOK
			st.LastError = prev.LastError
			st.LastAt = prev.LastAt
		}
		return st
	}
	channels := []notifyChannelStatus{
		status("telegram"), status("feishu"), status("bark"),
		status("email"), status("pushplus"), status("webhook"),
	}
	return map[string]any{
		"telegram": map[string]any{
			"enabled": cfg.Telegram.Enabled, "configured": cfg.telegramConfigured(),
			"chat_id": cfg.Telegram.ChatID, "admin_id": cfg.Telegram.AdminID,
			"base_url": cfg.Telegram.BaseURL,
		},
		"feishu": map[string]any{
			"enabled": cfg.Feishu.Enabled, "configured": cfg.feishuConfigured(),
		},
		"bark": map[string]any{
			"enabled": cfg.Bark.Enabled, "configured": cfg.barkConfigured(),
			"urls_count": len(cfg.Bark.URLs), "group": cfg.Bark.Group, "level": cfg.Bark.Level,
		},
		"email": map[string]any{
			"enabled": cfg.Email.Enabled, "configured": cfg.emailConfigured(),
			"smtp_host": cfg.Email.SMTPHost, "smtp_port": cfg.Email.SMTPPort,
			"username": cfg.Email.Username, "from_address": cfg.Email.FromAddress,
			"to_addresses": cfg.Email.ToAddresses, "use_ssl": cfg.Email.UseSSL,
		},
		"pushplus": map[string]any{
			"enabled": cfg.Pushplus.Enabled, "configured": cfg.pushplusConfigured(),
			"topic": cfg.Pushplus.Topic, "channel": cfg.Pushplus.Channel,
		},
		"webhook": map[string]any{
			"enabled": cfg.Webhook.Enabled, "configured": cfg.webhookConfigured(),
			"urls_count": len(cfg.Webhook.URLs),
			"timeout_ms": cfg.Webhook.TimeoutMs, "retry_max": cfg.Webhook.RetryMax,
		},
		"notify_sms":   cfg.NotifySMS,
		"notify_calls": cfg.NotifyCall,
		"channels":     channels,
	}
}

func cleanStringList(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func cleanURLList(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func validateNotifySettings(cfg *notifySettings) error {
	if cfg.Telegram.Enabled && !cfg.telegramConfigured() {
		return errors.New("telegram 已启用但未配置：请填写 Bot Token 与 Chat ID")
	}
	if cfg.Feishu.Enabled {
		if !validHTTPURL(cfg.Feishu.WebhookURL) {
			return errors.New("飞书已启用但 Webhook 地址无效：请填写 http(s) 机器人地址")
		}
	}
	if cfg.Bark.Enabled {
		if len(cfg.Bark.URLs) == 0 {
			return errors.New("Bark 已启用但未配置推送地址")
		}
		for _, u := range cfg.Bark.URLs {
			if !validHTTPURL(u) {
				return fmt.Errorf("Bark 推送地址无效：%s", u)
			}
		}
	}
	if cfg.Email.Enabled && !cfg.emailConfigured() {
		return errors.New("邮件已启用但未配置：请填写 SMTP 服务器、端口与收件人")
	}
	if cfg.Pushplus.Enabled && !cfg.pushplusConfigured() {
		return errors.New("PushPlus 已启用但未配置 Token")
	}
	if cfg.Webhook.Enabled {
		if len(cfg.Webhook.URLs) == 0 {
			return errors.New("Webhook 已启用但未配置接收地址")
		}
		for _, u := range cfg.Webhook.URLs {
			if !validHTTPURL(u) {
				return fmt.Errorf("Webhook 接收地址无效：%s", u)
			}
		}
	}
	return nil
}

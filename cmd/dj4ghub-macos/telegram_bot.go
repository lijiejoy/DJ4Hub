package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type pendingBotAction struct {
	Kind    string // "sendsms" | "reboot"
	To      string
	Text    string
	Code    string
	AdminID int64
	Expires time.Time
}

func (a *app) botAdminID(cfg *notifySettings) int64 {
	if cfg.Telegram.AdminID != 0 {
		return cfg.Telegram.AdminID
	}
	return cfg.Telegram.ChatID
}

func newConfirmCode() (string, error) {
	return confirmCodeFrom(rand.Reader)
}

func confirmCodeFrom(reader io.Reader) (string, error) {
	n, err := rand.Int(reader, big.NewInt(900000))
	if err != nil {
		return "", errors.New("无法生成安全确认码")
	}
	return strconv.Itoa(int(n.Int64()) + 100000), nil
}

func (a *app) telegramBotLoop() {
	client := &http.Client{Timeout: 40 * time.Second}
	for {
		if a.backgroundContext().Err() != nil {
			return
		}
		cfg := a.getNotifySettings()
		if !cfg.Telegram.Enabled || !cfg.telegramConfigured() {
			if !waitBackground(a.backgroundContext(), 10*time.Second) {
				return
			}
			continue
		}
		if err := a.telegramPollOnce(client, cfg); err != nil {
			log.Printf("[notify] telegram bot poll failed: %v", err)
			if !waitBackground(a.backgroundContext(), 5*time.Second) {
				return
			}
		}
	}
}

func (a *app) telegramPollOnce(client *http.Client, cfg *notifySettings) (failure error) {
	defer func() { failure = safeNotifyFailure("telegram", failure) }()
	a.botMu.Lock()
	offset := a.botOffset
	a.botMu.Unlock()
	endpoint := telegramAPIURL(cfg.Telegram.BaseURL, cfg.Telegram.BotToken, "getUpdates")
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("timeout", "25")
	if offset != 0 {
		q.Set("offset", strconv.FormatInt(offset, 10))
	}
	u.RawQuery = q.Encode()
	ctx, cancel := context.WithTimeout(a.backgroundContext(), 35*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("getUpdates HTTP %d", resp.StatusCode)
	}
	var decoded struct {
		OK     bool `json:"ok"`
		Result []struct {
			UpdateID int64 `json:"update_id"`
			Message  *struct {
				MessageID int64 `json:"message_id"`
				From      *struct {
					ID int64 `json:"id"`
				} `json:"from"`
				Chat *struct {
					ID int64 `json:"id"`
				} `json:"chat"`
				Text string `json:"text"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return err
	}
	for _, update := range decoded.Result {
		a.botMu.Lock()
		if update.UpdateID >= a.botOffset {
			a.botOffset = update.UpdateID + 1
		}
		a.botMu.Unlock()
		if update.Message == nil || update.Message.From == nil || update.Message.Chat == nil {
			continue
		}
		// Admin-only: anyone else is ignored silently (no reply, no leak).
		if update.Message.From.ID != a.botAdminID(cfg) {
			log.Printf("[notify] telegram bot ignored message from non-admin id %d", update.Message.From.ID)
			continue
		}
		a.handleBotCommand(cfg, update.Message.Chat.ID, strings.TrimSpace(update.Message.Text))
	}
	return nil
}

func (a *app) botReply(cfg *notifySettings, chatID int64, text string) {
	ctx, cancel := context.WithTimeout(a.backgroundContext(), notifySendTimeout)
	defer cancel()
	// Reply in the chat the command came from, not the notify chat.
	replyCfg := cfg.Telegram
	replyCfg.ChatID = chatID
	if err := sendTelegram(ctx, replyCfg, text); err != nil {
		log.Printf("[notify] telegram bot reply failed: %v", err)
	}
}

const botHelpText = `DJ 4G Hub Bot（仅管理员可用）
/status — 模块与短信状态
/sms — 最近 5 条短信
/sendsms <号码> <内容> — 发短信（需 /confirm 确认）
/reboot — 重启模块（需 /confirm 确认）
/confirm <验证码> — 执行待确认操作
/cancel — 取消待确认操作
eSIM 下载/删除/改名/切换不通过 Bot 执行，请在控制台操作。`

func (a *app) handleBotCommand(cfg *notifySettings, chatID int64, text string) {
	if text == "" {
		return
	}
	fields := strings.Fields(text)
	cmd := strings.ToLower(fields[0])
	// eSIM sensitive ops are never offered via bot, even with confirmation.
	if strings.HasPrefix(cmd, "/esim") || cmd == "/download" || cmd == "/delete" ||
		cmd == "/rename" || cmd == "/switch" {
		a.botReply(cfg, chatID, "eSIM 下载/删除/改名/切换不通过 Bot 执行，请在控制台操作。")
		return
	}
	switch cmd {
	case "/start", "/help":
		a.botReply(cfg, chatID, botHelpText)
	case "/status":
		a.botReply(cfg, chatID, a.botStatusText())
	case "/sms":
		a.botReply(cfg, chatID, a.botSMSText())
	case "/sendsms":
		if len(fields) < 3 {
			a.botReply(cfg, chatID, "用法：/sendsms <号码> <内容>")
			return
		}
		code, err := newConfirmCode()
		if err != nil {
			a.botReply(cfg, chatID, "暂时无法生成确认码，操作未创建。")
			return
		}
		a.botPendingMu.Lock()
		a.botPending = &pendingBotAction{
			Kind: "sendsms", To: fields[1], Text: strings.Join(fields[2:], " "),
			Code: code, AdminID: a.botAdminID(cfg), Expires: time.Now().Add(botConfirmTTL),
		}
		a.botPendingMu.Unlock()
		a.botReply(cfg, chatID, fmt.Sprintf(
			"待确认：向 %s 发送短信\n%s\n请回复 /confirm %s 确认（5 分钟内有效），或 /cancel 取消。",
			fields[1], strings.Join(fields[2:], " "), code))
	case "/reboot":
		code, err := newConfirmCode()
		if err != nil {
			a.botReply(cfg, chatID, "暂时无法生成确认码，操作未创建。")
			return
		}
		a.botPendingMu.Lock()
		a.botPending = &pendingBotAction{
			Kind: "reboot", Code: code,
			AdminID: a.botAdminID(cfg), Expires: time.Now().Add(botConfirmTTL),
		}
		a.botPendingMu.Unlock()
		a.botReply(cfg, chatID, fmt.Sprintf(
			"待确认：重启模块（数据链路中断约 30 秒）。请回复 /confirm %s 确认（5 分钟内有效），或 /cancel 取消。", code))
	case "/confirm":
		if len(fields) < 2 {
			a.botReply(cfg, chatID, "用法：/confirm <验证码>")
			return
		}
		a.botConfirm(cfg, chatID, fields[1])
	case "/cancel":
		a.botPendingMu.Lock()
		a.botPending = nil
		a.botPendingMu.Unlock()
		a.botReply(cfg, chatID, "已取消待确认操作。")
	default:
		a.botReply(cfg, chatID, "未知命令。"+botHelpText)
	}
}

func (a *app) botConfirm(cfg *notifySettings, chatID int64, code string) {
	a.botPendingMu.Lock()
	pending := a.botPending
	a.botPending = nil
	a.botPendingMu.Unlock()
	if pending == nil {
		a.botReply(cfg, chatID, "没有待确认的操作。")
		return
	}
	if time.Now().After(pending.Expires) {
		a.botReply(cfg, chatID, "确认码已过期，请重新发起操作。")
		return
	}
	if code != pending.Code {
		a.botReply(cfg, chatID, "确认码不正确，操作未执行。")
		return
	}
	switch pending.Kind {
	case "sendsms":
		if a.demo {
			a.botReply(cfg, chatID, "演示模式不实际发送。")
			return
		}
		segments, _, err := a.sendTextSMS(pending.To, pending.Text)
		if err != nil {
			a.botReply(cfg, chatID, "发送失败："+err.Error())
			a.recordNotifyResult("telegram", "bot", "Bot 确认发送短信", err)
			return
		}
		a.botReply(cfg, chatID, fmt.Sprintf("短信已发送至 %s（%d 段）。", pending.To, segments))
		a.recordNotifyResult("telegram", "bot", "Bot 确认发送短信", nil)
	case "reboot":
		if a.demo {
			a.botReply(cfg, chatID, "演示模式不实际重启。")
			return
		}
		if _, err := a.runATCommand("AT+CFUN=1,1", 3*time.Second); err != nil {
			a.botReply(cfg, chatID, "重启失败："+err.Error())
			a.recordNotifyResult("telegram", "bot", "Bot 确认重启模块", err)
			return
		}
		a.botReply(cfg, chatID, "模块正在重启，数据链路约 30 秒后恢复。")
		a.recordNotifyResult("telegram", "bot", "Bot 确认重启模块", nil)
	default:
		a.botReply(cfg, chatID, "未知待确认操作，已丢弃。")
	}
}

func (a *app) botStatusText() string {
	a.smsMu.RLock()
	smsCount := len(a.sms)
	lastPoll := a.smsLastPoll
	lastErr := a.smsLastPollError
	a.smsMu.RUnlock()
	status := fmt.Sprintf("模块：%s\n短信：%d 条", a.port, smsCount)
	if !lastPoll.IsZero() {
		status += "\n上次轮询：" + lastPoll.Format("15:04:05")
		if lastErr != "" {
			status += "（失败：" + lastErr + "）"
		}
	}
	return status
}

func (a *app) botSMSText() string {
	a.smsMu.RLock()
	defer a.smsMu.RUnlock()
	if len(a.sms) == 0 {
		return "暂无短信。"
	}
	n := 5
	if len(a.sms) < n {
		n = len(a.sms)
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		m := a.sms[i]
		content := m.Content
		if len([]rune(content)) > 100 {
			content = string([]rune(content)[:100]) + "…"
		}
		fmt.Fprintf(&b, "%s %s\n%s", m.Timestamp.Format("01-02 15:04"), m.Sender, content)
		if m.Code != "" {
			fmt.Fprintf(&b, "\n验证码：%s", m.Code)
		}
		if i < n-1 {
			b.WriteString("\n---\n")
		}
	}
	return b.String()
}

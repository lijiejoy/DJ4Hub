package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// --- channel senders ---------------------------------------------------------

func (a *app) sendToChannel(cfg *notifySettings, channel string, ev notifyEvent) (failure error) {
	defer func() {
		if recover() != nil {
			failure = notificationError{channel + "：通知发送异常"}
		}
		failure = safeNotifyFailure(channel, failure)
	}()
	ctx, cancel := context.WithTimeout(a.backgroundContext(), notifySendTimeout)
	defer cancel()
	switch channel {
	case "telegram":
		return sendTelegram(ctx, cfg.Telegram, ev.Title+"\n"+ev.Body)
	case "feishu":
		return sendFeishu(ctx, cfg.Feishu, ev.Title+"\n"+ev.Body)
	case "bark":
		return sendBark(ctx, cfg.Bark, ev.Title, ev.Body)
	case "email":
		return sendEmail(ctx, cfg.Email, ev.Title, ev.Body)
	case "pushplus":
		return sendPushplus(ctx, cfg.Pushplus, ev.Title, ev.Body)
	case "webhook":
		return sendWebhook(ctx, cfg.Webhook, ev)
	}
	return fmt.Errorf("未知通知通道：%s", channel)
}

func telegramAPIURL(base, token, method string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return "https://api.telegram.org/bot" + token + "/" + method
	}
	if strings.Contains(base, "%s") {
		return fmt.Sprintf(base, token, method)
	}
	return strings.TrimSuffix(base, "/") + "/bot" + token + "/" + method
}

func postJSON(ctx context.Context, rawurl string, payload any, headers map[string]string) (int, []byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawurl, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, body, nil
}

func sendTelegram(ctx context.Context, cfg telegramNotifySettings, text string) error {
	endpoint := telegramAPIURL(cfg.BaseURL, cfg.BotToken, "sendMessage")
	status, body, err := postJSON(ctx, endpoint, map[string]any{
		"chat_id": cfg.ChatID, "text": text,
	}, nil)
	if err != nil {
		return safeNotifyFailure("telegram", err)
	}
	if status < 200 || status >= 300 {
		return notifyHTTPFailure("telegram", status)
	}
	var result struct {
		OK *bool `json:"ok"`
	}
	if json.Unmarshal(body, &result) != nil || result.OK == nil || !*result.OK {
		return notificationError{"telegram 未确认推送成功"}
	}
	return nil
}

func sendFeishu(ctx context.Context, cfg feishuNotifySettings, text string) error {
	status, body, err := postJSON(ctx, cfg.WebhookURL, map[string]any{
		"msg_type": "text", "content": map[string]string{"text": text},
	}, nil)
	if err != nil {
		return safeNotifyFailure("飞书", err)
	}
	if status < 200 || status >= 300 {
		return notifyHTTPFailure("飞书", status)
	}
	var result struct {
		Code       *int `json:"code"`
		StatusCode *int `json:"StatusCode"`
	}
	if json.Unmarshal(body, &result) != nil || (result.Code == nil && result.StatusCode == nil) ||
		(result.Code != nil && *result.Code != 0) || (result.StatusCode != nil && *result.StatusCode != 0) {
		return notificationError{"飞书未确认推送成功"}
	}
	return nil
}

func sendBark(ctx context.Context, cfg barkNotifySettings, title, body string) error {
	var errs []string
	for _, base := range cfg.URLs {
		// Each URL is a Bark push base, e.g. https://api.day.app/<key>.
		target := strings.TrimSuffix(strings.TrimSpace(base), "/") +
			"/" + url.PathEscape(title) + "/" + url.PathEscape(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			errs = append(errs, safeNotifyFailure("Bark", err).Error())
			continue
		}
		q := req.URL.Query()
		if cfg.Group != "" {
			q.Set("group", cfg.Group)
		}
		if cfg.Level != "" {
			q.Set("level", cfg.Level)
		}
		req.URL.RawQuery = q.Encode()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			errs = append(errs, safeNotifyFailure("Bark", err).Error())
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			errs = append(errs, notifyHTTPFailure("Bark", resp.StatusCode).Error())
			continue
		}
		var result struct {
			Code int `json:"code"`
		}
		if json.Unmarshal(respBody, &result) != nil || result.Code != 200 {
			errs = append(errs, "Bark 未确认推送成功")
			continue
		}
		return nil
	}
	if len(errs) > 0 {
		return notificationError{"Bark 推送失败：" + strings.Join(errs, "; ")}
	}
	return errors.New("Bark 未配置推送地址")
}

func sendPushplus(ctx context.Context, cfg pushplusNotifySettings, title, body string) error {
	payload := map[string]any{
		"token": cfg.Token, "title": title, "content": body, "template": "txt",
	}
	if cfg.Topic != "" {
		payload["topic"] = cfg.Topic
	}
	if cfg.Channel != "" {
		payload["channel"] = cfg.Channel
	}
	status, respBody, err := postJSON(ctx, pushplusEndpoint, payload, nil)
	if err != nil {
		return safeNotifyFailure("PushPlus", err)
	}
	if status < 200 || status >= 300 {
		return notifyHTTPFailure("PushPlus", status)
	}
	var decoded struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil || decoded.Code != 200 {
		return notificationError{fmt.Sprintf("PushPlus 未确认推送成功（code=%d）", decoded.Code)}
	}
	return nil
}

func signWebhook(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func sendWebhook(ctx context.Context, cfg webhookNotifySettings, ev notifyEvent) error {
	payload := map[string]any{
		"event": ev.Kind, "device": "DJ 4G Hub", "title": ev.Title,
		"body": ev.Body, "time": ev.Time.Format(time.RFC3339),
	}
	if ev.Code != "" {
		payload["code"] = ev.Code
	}
	if ev.Sender != "" {
		payload["sender"] = ev.Sender
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	retries := cfg.RetryMax
	if retries < 0 {
		retries = 0
	}
	pending := append([]string(nil), cfg.URLs...)
	var failures []string
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return safeNotifyFailure("webhook", ctx.Err())
			case <-time.After(500 * time.Millisecond):
			}
		}
		retryTargets := []string{}
		failures = nil
		for _, target := range pending {
			reqCtx, reqCancel := context.WithTimeout(ctx, timeout)
			r, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, bytes.NewReader(raw))
			if err != nil {
				reqCancel()
				failures = append(failures, safeNotifyFailure("webhook", err).Error())
				retryTargets = append(retryTargets, target)
				continue
			}
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-DJ4Hub-Event", ev.Kind)
			if cfg.Secret != "" {
				r.Header.Set("X-DJ4Hub-Signature", signWebhook(cfg.Secret, raw))
			}
			resp, err := http.DefaultClient.Do(r)
			if err != nil {
				reqCancel()
				failures = append(failures, safeNotifyFailure("webhook", err).Error())
				retryTargets = append(retryTargets, target)
				continue
			}
			_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			reqCancel()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				failures = append(failures, notifyHTTPFailure("webhook", resp.StatusCode).Error())
				retryTargets = append(retryTargets, target)
				continue
			}
			if readErr != nil {
				failures = append(failures, safeNotifyFailure("webhook", readErr).Error())
				retryTargets = append(retryTargets, target)
			}
		}
		if len(failures) == 0 && len(pending) > 0 {
			return nil
		}
		pending = retryTargets
	}
	if len(failures) > 0 {
		return notificationError{strings.Join(failures, "; ")}
	}
	return errors.New("webhook 未配置接收地址")
}

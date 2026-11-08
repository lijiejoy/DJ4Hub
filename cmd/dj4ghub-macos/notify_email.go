package main

import (
	"context"
	"crypto/tls"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

func sendEmail(ctx context.Context, cfg emailNotifySettings, title, body string) (failure error) {
	defer func() { failure = safeNotifyFailure("邮件", failure) }()
	from := cfg.FromAddress
	if from == "" {
		from = cfg.Username
	}
	for _, value := range append([]string{title, from}, cfg.ToAddresses...) {
		if strings.ContainsAny(value, "\r\n") {
			return notificationError{"邮件标题或地址包含换行，无法发送"}
		}
	}
	host := strings.TrimSpace(cfg.SMTPHost)
	conn, err := (&net.Dialer{Timeout: notifySendTimeout}).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(cfg.SMTPPort)))
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline := time.Now().Add(notifySendTimeout)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	// Deadlines cover the greeting and every SMTP step; cancellation also
	// interrupts a connection whose context has no deadline.
	rawConn := conn
	stopCancel := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
	defer stopCancel()
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if cfg.UseSSL {
		secure := tls.Client(conn, tlsConfig)
		if err := secure.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = secure
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if !cfg.UseSSL {
		if supported, _ := client.Extension("STARTTLS"); supported {
			if err := client.StartTLS(tlsConfig); err != nil {
				return notificationError{"邮件 STARTTLS 失败，已停止发送"}
			}
		} else if cfg.SMTPPort == 587 || cfg.Username != "" {
			return notificationError{"邮件服务器不支持 STARTTLS，已停止发送"}
		}
	}
	if cfg.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, host)); err != nil {
			return err
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	for _, to := range cfg.ToAddresses {
		if err := client.Rcpt(to); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	message := "From: " + from + "\r\nTo: " + strings.Join(cfg.ToAddresses, ", ") +
		"\r\nSubject: " + mime.QEncoding.Encode("utf-8", title) +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n"
	if _, err := writer.Write([]byte(message)); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

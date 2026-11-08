package main

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// Only locally constructed messages may cross the notification log/API boundary.
// URLs and remote response text may contain credentials or message contents.
type notificationError struct{ message string }

func (e notificationError) Error() string { return e.message }

func safeNotifyFailure(channel string, err error) error {
	if err == nil {
		return nil
	}
	var safe notificationError
	if errors.As(err, &safe) {
		return safe
	}
	reason := "连接或远端服务异常"
	var networkError net.Error
	switch {
	case errors.Is(err, context.Canceled):
		reason = "请求已取消"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "请求超时"
	case errors.As(err, &networkError) && networkError.Timeout():
		reason = "请求超时"
	}
	return notificationError{message: channel + "：" + reason}
}

func notifyHTTPFailure(channel string, status int) error {
	return notificationError{message: fmt.Sprintf("%s 返回 HTTP %d", channel, status)}
}

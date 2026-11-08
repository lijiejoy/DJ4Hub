package main

import (
	"context"
	"time"
)

func (a *app) backgroundContext() context.Context {
	if a.background != nil {
		return a.background
	}
	return context.Background()
}

func waitBackground(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

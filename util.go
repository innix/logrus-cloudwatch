package logruscloudwatch

import (
	"context"
	"time"
)

// A sleeper pauses the current goroutine for at least the duration d.
// A negative or zero duration causes a sleeper to return immediately.
//
// It returns true if the sleeper paused successfully.
// It returns false if the pause was interrupted by the context being canceled.
type sleeper func(ctx context.Context, d time.Duration) bool

func timeSleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

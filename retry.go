package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// retryWithBackoff calls fn until it succeeds, ctx is cancelled or the attempts are used up (0 means no limit).
// The delay between attempts starts at initial and doubles up to maxDelay.
func retryWithBackoff(ctx context.Context, what string, attempts int, initial, maxDelay time.Duration, fn func() error) error {
	delay := initial
	var err error
	for attempt := 1; attempts == 0 || attempt <= attempts; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if attempts != 0 && attempt == attempts {
			break
		}
		slog.Error("Operation failed, retrying", "operation", what, "error", err, "attempt", attempt, "retry_in", delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(2*delay, maxDelay)
	}
	return fmt.Errorf("%s failed after %d attempts: %w", what, attempts, err)
}

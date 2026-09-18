package dispatch

import (
	"context"
	"time"
)

// Idle queues back off briefly; loaded queues retain the configured cadence.
// The cap bounds cold-start latency without polling PostgreSQL thousands of
// times per second while no messages or receipts are available.
func idlePollDelay(current, base time.Duration) time.Duration {
	return min(current*2, max(base, 250*time.Millisecond))
}

func waitPoll(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

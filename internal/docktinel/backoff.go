package docktinel

import (
	"math/rand/v2"
	"time"
)

// retryBackoff belongs to one runtime worker. Values are nominal delays before
// ±20% jitter; stable processes restart immediately and reset the sequence.
type retryBackoff struct {
	delay time.Duration
}

func (b *retryBackoff) next(task Task, elapsed time.Duration, failed bool) time.Duration {
	switch task.Kind {
	case "process":
		if elapsed > 30*time.Second {
			b.delay = 0
			return 0
		}
		return b.increase(time.Second, 30*time.Second)
	case "job":
		if failed {
			return b.increase(5*time.Second, time.Minute)
		}
		b.delay = 0
	}
	return task.Interval
}

func (b *retryBackoff) increase(first, limit time.Duration) time.Duration {
	if b.delay == 0 {
		b.delay = first
	} else {
		b.delay = min(2*b.delay, limit)
	}
	return time.Duration(float64(b.delay) * (0.8 + 0.4*rand.Float64()))
}

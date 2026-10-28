package cache

import (
	"sync"
	"time"
)

type cbState int

const (
	cbClosed   cbState = iota // normal operation
	cbOpen                    // rejecting calls
	cbHalfOpen                // testing recovery
)

type circuitBreaker struct {
	mu            sync.Mutex
	state         cbState
	failures      int
	threshold     int
	openDuration  time.Duration
	openedAt      time.Time
	onStateChange func(open bool)
}

func newCircuitBreaker(threshold int, openDuration time.Duration) *circuitBreaker {
	return &circuitBreaker{threshold: threshold, openDuration: openDuration}
}

func (cb *circuitBreaker) IsOpen() bool {
	if cb.threshold == 0 {
		return false // disabled
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == cbOpen && time.Since(cb.openedAt) > cb.openDuration {
		cb.state = cbHalfOpen
		return false
	}
	return cb.state == cbOpen
}

func (cb *circuitBreaker) Record(err error) {
	if cb.threshold == 0 {
		return // disabled
	}

	cb.mu.Lock()
	changed, open := cb.recordLocked(err)
	callback := cb.onStateChange
	cb.mu.Unlock()

	if changed && callback != nil {
		callback(open)
	}
}

func (cb *circuitBreaker) recordLocked(err error) (changed, open bool) {
	switch {
	case err == nil && cb.state == cbHalfOpen:
		cb.state = cbClosed
		cb.failures = 0
		return true, false
	case err == nil:
		cb.failures = 0
		return false, false
	default:
		cb.failures++
		if cb.state == cbHalfOpen || (cb.state == cbClosed && cb.failures >= cb.threshold) {
			cb.state = cbOpen
			cb.openedAt = time.Now()
			return true, true
		}
		return false, false
	}
}

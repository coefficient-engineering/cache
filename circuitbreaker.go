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
	mu           sync.Mutex
	state        cbState
	failures     int
	threshold    int
	openDuration time.Duration
	openedAt     time.Time
	onRecovery   func()
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
		// transition to half open
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
	defer cb.mu.Unlock()

	if err == nil {
		// success: close circuit & reset counter
		wasOpen := cb.state == cbOpen || cb.state == cbHalfOpen
		if wasOpen {
			cb.state = cbClosed
			if cb.onRecovery != nil {
				go cb.onRecovery()
			}
		}
		cb.failures = 0 // always reset on success
		return
	}

	// failure
	cb.failures++
	if cb.failures >= cb.threshold && cb.state == cbClosed {
		cb.state = cbOpen
		cb.openedAt = time.Now()
	}

	if cb.state == cbHalfOpen {
		cb.state = cbOpen
		cb.openedAt = time.Now()
	}
}

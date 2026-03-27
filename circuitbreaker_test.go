package cache

import (
	"errors"
	"testing"
	"time"
)

func TestCircuitBreaker_DisabledWhenThresholdZero(t *testing.T) {
	cb := newCircuitBreaker(0, 0)
	// Should never be open
	for range 100 {
		cb.Record(errors.New("fail"))
	}
	if cb.IsOpen() {
		t.Fatal("expected disabled circuit breaker to never open")
	}
}

func TestCircuitBreaker_OpensAtThreshold(t *testing.T) {
	cb := newCircuitBreaker(3, 10*time.Second)

	cb.Record(errors.New("fail 1"))
	cb.Record(errors.New("fail 2"))
	if cb.IsOpen() {
		t.Fatal("should not be open before threshold")
	}

	cb.Record(errors.New("fail 3"))
	if !cb.IsOpen() {
		t.Fatal("should be open after 3 failures")
	}
}

func TestCircuitBreaker_SuccessResets(t *testing.T) {
	cb := newCircuitBreaker(3, 10*time.Second)

	cb.Record(errors.New("fail 1"))
	cb.Record(errors.New("fail 2"))
	cb.Record(nil) // success resets counter

	cb.Record(errors.New("fail 3"))
	cb.Record(errors.New("fail 4"))
	if cb.IsOpen() {
		t.Fatal("should not be open — success reset the counter")
	}
}

func TestCircuitBreaker_HalfOpenRecovery(t *testing.T) {
	cb := newCircuitBreaker(2, 50*time.Millisecond)

	cb.Record(errors.New("fail"))
	cb.Record(errors.New("fail"))
	if !cb.IsOpen() {
		t.Fatal("should be open")
	}

	// Wait for open duration
	time.Sleep(60 * time.Millisecond)

	// Should transition to half-open
	if cb.IsOpen() {
		t.Fatal("should be half-open after duration elapsed")
	}

	// Successful probe closes the circuit
	cb.Record(nil)
	if cb.IsOpen() {
		t.Fatal("should be closed after successful probe")
	}
}

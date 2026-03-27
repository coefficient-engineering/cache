// Package clock provides a time abstraction so cache internals can be tested
// without sleeping or time.Sleep-based races.
package clock

import "time"

// Clock is the interface used internally by cache to read the current time.
type Clock interface {
	Now() time.Time
}

// Real is the production clock, delegates to time.Now().
type Real struct{}

func (Real) Now() time.Time { return time.Now() }

// Mock is a controllable clock for tests.
type Mock struct {
	current time.Time
}

func NewMock(t time.Time) *Mock { return &Mock{current: t} }

func (m *Mock) Now() time.Time { return m.current }

// Advance moves the mock clock forward by d.
func (m *Mock) Advance(d time.Duration) { m.current = m.current.Add(d) }

// Set sets the mock clock to t.
func (m *Mock) Set(t time.Time) { m.current = t }

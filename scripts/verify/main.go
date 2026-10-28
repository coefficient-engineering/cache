// Command verify drives the cache library through its public API, the way an
// importing application does, and prints one JSON line per observed check.
//
// Usage: go run ./scripts/verify <scenario>
//
// Scenarios: stampede, failsafe, tags, shared-factory-events, breaker-events,
// lock-timeout, skip-l2-read-when-stale (L1 or in-memory L2); multinode,
// backplane-rethrow, l2-rethrow, expire-l2, l2-soft-timeout (need REDIS_ADDR).
// Exit status is 0 only when every check in the scenario holds.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/coefficient-engineering/cache"
)

// Labels shared by check output.
const (
	valueKey = "value" // the observed cached value
	errorKey = "error" // the observed error string
	foundKey = "found" // whether the value was found
)

// check is one observed result.
type check struct {
	Scenario string `json:"scenario"`
	Name     string `json:"check"`
	OK       bool   `json:"ok"`
	Observed any    `json:"observed"`
}

// report collects checks and prints them as JSON lines.
type report struct {
	scenario string
	failed   bool
	enc      *json.Encoder
}

// record prints c and marks the scenario failed when c does not hold.
func (r *report) record(c check) {
	c.Scenario = r.scenario
	if !c.OK {
		r.failed = true
	}
	if err := r.enc.Encode(c); err != nil {
		r.failed = true
	}
}

// eventLog records cache event type names for evidence.
type eventLog struct {
	mu    sync.Mutex
	names []string
}

func (l *eventLog) attach(c cache.Cache) {
	c.Events().On(func(e cache.Event) {
		name := strings.TrimPrefix(fmt.Sprintf("%T", e), "cache.")
		l.mu.Lock()
		l.names = append(l.names, name)
		l.mu.Unlock()
	})
}

func (l *eventLog) count(name string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, got := range l.names {
		if got == name {
			n++
		}
	}
	return n
}

var scenarios = map[string]func(context.Context, *report) error{
	"backplane-rethrow":       runBackplaneRethrow,
	"shared-factory-events":   runSharedFactoryEvents,
	"stampede":                runStampede,
	"failsafe":                runFailSafe,
	"breaker-events":          runBreakerEvents,
	"lock-timeout":            runLockTimeout,
	"skip-l2-read-when-stale": runSkipL2ReadWhenStale,
	"tags":                    runTags,
	"multinode":               runMultiNode,
	"l2-rethrow":              runL2Rethrow,
	"expire-l2":               runExpireL2,
	"l2-soft-timeout":         runL2SoftTimeout,
}

func main() {
	if len(os.Args) != 2 || scenarios[os.Args[1]] == nil {
		names := make([]string, 0, len(scenarios))
		for name := range scenarios {
			names = append(names, name)
		}
		sort.Strings(names)
		fmt.Fprintf(os.Stderr, "usage: verify <%s>\n", strings.Join(names, "|"))
		os.Exit(2)
	}
	r := &report{scenario: os.Args[1], enc: json.NewEncoder(os.Stdout)}
	if err := scenarios[r.scenario](context.Background(), r); err != nil {
		r.record(check{Name: "scenario-error", Observed: err.Error()})
	}
	if r.failed {
		os.Exit(1)
	}
}

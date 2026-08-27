package cmd

import (
	"testing"
	"time"
)

// On a terminal the cadence stays count-based, and the last item always reports.
func TestProgressTickerOnTerminal(t *testing.T) {
	p := &progressTicker{isTerminal: true, every: 25, interval: 30 * time.Second}

	got := 0
	for i := 1; i <= 100; i++ {
		if p.due(i, 16413) {
			got++
		}
	}
	if got != 4 { // 25, 50, 75, 100
		t.Errorf("emitted %d lines in the first 100 items, want 4", got)
	}
	if !p.due(16413, 16413) {
		t.Error("final item must always report")
	}
}

// Redirected, the count is irrelevant: at most one line per interval.
func TestProgressTickerRedirected(t *testing.T) {
	p := &progressTicker{isTerminal: false, every: 25, interval: 30 * time.Second}

	got := 0
	for i := 1; i <= 5000; i++ {
		if p.due(i, 16413) {
			got++
		}
	}
	if got != 1 {
		t.Errorf("emitted %d lines for 5000 items inside one interval, want 1", got)
	}

	// Once the interval has elapsed, exactly one more line is allowed.
	p.last = time.Now().Add(-31 * time.Second)
	if !p.due(5001, 16413) {
		t.Error("expected a line after the interval elapsed")
	}
	if p.due(5002, 16413) {
		t.Error("expected throttling to resume immediately after emitting")
	}

	if !p.due(16413, 16413) {
		t.Error("final item must always report, whatever the interval")
	}
}

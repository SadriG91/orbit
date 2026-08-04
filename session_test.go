package main

import (
	"testing"
	"time"
)

// Agents rewrite old transcripts in batches, so mtime is not the session's age.
// This is the guard against sliding back to it.
func TestEventTimePrefersRecordedTimestamp(t *testing.T) {
	mtime := time.Now()
	real := "2026-07-14T09:30:00.000Z"

	got := eventTime(real, mtime)
	want, _ := time.Parse(time.RFC3339, real)
	if !got.Equal(want) {
		t.Errorf("got %v, want the recorded timestamp %v", got, want)
	}
	if time.Since(got) < 24*time.Hour {
		t.Error("a three-week-old session must not look recent")
	}
	if got := eventTime("", mtime); !got.Equal(mtime) {
		t.Errorf("with no timestamp it should fall back to mtime, got %v", got)
	}
	if got := eventTime("not a date", mtime); !got.Equal(mtime) {
		t.Errorf("an unparseable timestamp should fall back to mtime, got %v", got)
	}
	// Codex writes fractional seconds; Claude sometimes doesn't.
	for _, s := range []string{"2026-07-14T09:30:00Z", "2026-07-14T09:30:00.074Z"} {
		if eventTime(s, mtime).Equal(mtime) {
			t.Errorf("failed to parse %q", s)
		}
	}
}

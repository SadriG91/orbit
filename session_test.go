package main

import (
	"os"
	"path/filepath"
	"strings"
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

// Claude Code appends bare `system` records to transcripts long after the
// conversation ended. Only real turns may date a session — this is the second
// layer of the same trap that mtime was the first layer of.
func TestOnlyConversationTurnsDateTheSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
	lines := []string{
		`{"type":"user","timestamp":"2026-07-30T09:29:26.000Z","cwd":"/tmp/proj","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","timestamp":"2026-07-30T09:29:50.000Z","cwd":"/tmp/proj","message":{"role":"assistant","content":[{"type":"text"}]}}`,
		// housekeeping written five days later — must not count
		`{"type":"system","timestamp":"2026-08-04T14:28:41.000Z","cwd":"/tmp/proj"}`,
		// a subagent turn, also not the user's conversation
		`{"type":"assistant","timestamp":"2026-08-04T15:00:00.000Z","isSidechain":true,"cwd":"/tmp/proj","message":{"role":"assistant","content":[{"type":"text"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := parseClaude(path, time.Now()) // mtime is "now" and must be ignored
	if s == nil {
		t.Fatal("transcript did not parse")
	}
	want, _ := time.Parse(time.RFC3339, "2026-07-30T09:29:50.000Z")
	if !s.Modified.Equal(want) {
		t.Errorf("dated %v, want the last real turn %v", s.Modified, want)
	}
}

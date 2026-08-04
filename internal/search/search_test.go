package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sadrig91/orbit/internal/session"
)

// Titles are too terse to find anything by, so search has to reach into the
// message bodies — and must not match on surrounding metadata.
func TestFindsBodyTextNotJustTitles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
	os.WriteFile(path, []byte(strings.Join([]string{
		`{"type":"user","timestamp":"2026-07-30T09:29:26.000Z","cwd":"/tmp/p","message":{"role":"user","content":"the ivanti tunnel keeps dropping"}}`,
		`{"type":"assistant","timestamp":"2026-07-30T09:29:50.000Z","cwd":"/tmp/p","message":{"role":"assistant","content":[{"type":"text","text":"check the scutil key"}]}}`,
	}, "\n")+"\n"), 0o644)

	s := &session.Session{
		Agent: session.Claude, ID: "aaaaaaaa", Path: path, Cwd: dir,
		Title: "Unrelated title", Modified: time.Now(),
	}

	got := Transcripts([]*session.Session{s}, "ivanti")
	if len(got) != 1 {
		t.Fatalf("expected a body-text match, got %d", len(got))
	}
	if !strings.Contains(strings.ToLower(got[s.ID].Snippet), "ivanti") {
		t.Errorf("snippet missing the term: %q", got[s.ID].Snippet)
	}
	if got[s.ID].Hits != 1 {
		t.Errorf("hit count = %d, want 1", got[s.ID].Hits)
	}

	if len(Transcripts([]*session.Session{s}, "nonexistentterm")) != 0 {
		t.Error("matched something it shouldn't")
	}
	if len(Transcripts([]*session.Session{s}, "a")) != 0 {
		t.Error("single-character queries should be ignored")
	}
	// "timestamp" appears in every record's metadata but in no message body.
	if len(Transcripts([]*session.Session{s}, "timestamp")) != 0 {
		t.Error("matched record metadata rather than message text")
	}
}

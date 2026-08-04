package summary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sadrig91/orbit/internal/format"
	"github.com/sadrig91/orbit/internal/session"
)

// The cache is keyed by session, not session state: a continued conversation
// must keep its summary so the next update can build on it instead of paying to
// re-read the whole transcript.
func TestSummaryCacheSurvivesNewMessages(t *testing.T) {
	base := &session.Session{Agent: session.Claude, ID: "abc", Msgs: 10, Modified: time.Unix(1000, 0)}
	grown := &session.Session{Agent: session.Claude, ID: "abc", Msgs: 14, Modified: time.Unix(2000, 0)}
	if File(base) != File(grown) {
		t.Error("a new message must not orphan the cached summary")
	}
	if File(base) == File(&session.Session{Agent: session.Codex, ID: "abc"}) {
		t.Error("different agents must not share a cache entry")
	}

	rec := Record{Text: "x", CoveredMsgs: 10}
	if rec.Stale(base) {
		t.Error("a summary covering every message is not stale")
	}
	if !rec.Stale(grown) || rec.Behind(grown) != 4 {
		t.Errorf("expected stale and 4 behind, got stale=%v behind=%d", rec.Stale(grown), rec.Behind(grown))
	}
}

// An incremental update must be preferred when a session merely continued, and
// abandoned when the new part is most of the conversation.
func TestBuildPromptChoosesIncrementalUpdate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir) // redirect the summary cache
	path := filepath.Join(dir, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, `{"type":"user","timestamp":"2026-07-30T09:`+
			pad2(i)+`:00.000Z","cwd":"`+dir+`","message":{"role":"user","content":"message `+format.Itoa(i)+`"}}`)
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	s := &session.Session{
		Agent: session.Claude, ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Path: path, Cwd: dir, Msgs: 30,
		Modified: mustTime("2026-07-30T09:29:00.000Z"),
	}

	// No prior summary: full rebuild.
	if p, gen, err := buildPrompt(s, 12000); err != nil || gen != 0 || !strings.Contains(p, "TRANSCRIPT EXCERPT") {
		t.Fatalf("expected a full prompt, got gen=%d err=%v", gen, err)
	}

	// A summary covering most of it, a few messages behind: incremental.
	covered, _ := time.Parse(time.RFC3339, "2026-07-30T09:25:00.000Z")
	save(s, Record{Text: "prior text", CoveredUntil: covered, CoveredMsgs: s.Msgs - 3})
	p, gen, err := buildPrompt(s, 12000)
	if err != nil {
		t.Fatal(err)
	}
	if gen != 1 || !strings.Contains(p, "EXISTING SUMMARY") || !strings.Contains(p, "prior text") {
		t.Errorf("expected an incremental update, got gen=%d", gen)
	}
	if strings.Contains(p, "message 0") {
		t.Error("incremental prompt must not resend messages the summary already covers")
	}

	// Too many increments in a row: rebuild to stop the summary drifting.
	save(s, Record{Text: "prior", CoveredUntil: covered,
		CoveredMsgs: s.Msgs - 3, Generation: MaxIncrements})
	if _, gen, _ := buildPrompt(s, 12000); gen != 0 {
		t.Errorf("expected a rebuild after %d increments, got gen=%d", MaxIncrements, gen)
	}

	// Barely covered: updating from the old summary buys nothing.
	save(s, Record{Text: "prior", CoveredUntil: covered, CoveredMsgs: 2})
	if _, gen, _ := buildPrompt(s, 12000); gen != 0 {
		t.Errorf("expected a rebuild when the summary covers almost nothing, got gen=%d", gen)
	}
}

func pad2(i int) string {
	if i < 10 {
		return "0" + format.Itoa(i)
	}
	return format.Itoa(i)
}

func mustTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

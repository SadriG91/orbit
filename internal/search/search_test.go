package search

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

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

// format.Clean strips control characters, newlines included. When the match was
// located before Clean ran, the snippet window slid one position per stripped
// character — and past sixty of them, which a single code block clears, the
// snippet no longer contained the term that had been searched for.
func TestSnippetSurvivesControlCharactersBeforeTheMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")

	// A long code block, then the match: 70 newlines ahead of the term.
	body := strings.Repeat("for i := range xs {\\n", 70) + "and then we fixed the vpn-proxy setting"
	os.WriteFile(path, []byte(
		`{"type":"assistant","timestamp":"2026-07-30T09:29:50.000Z","cwd":"/tmp/p","message":`+
			`{"role":"assistant","content":[{"type":"text","text":"`+body+`"}]}}`+"\n"), 0o644)

	s := &session.Session{Agent: session.Claude, ID: "a", Path: path, Cwd: dir, Modified: time.Now()}
	got := Transcripts([]*session.Session{s}, "vpn-proxy")
	if len(got) != 1 {
		t.Fatalf("expected a match, got %d", len(got))
	}
	snip := got[s.ID].Snippet
	if !strings.Contains(strings.ToLower(snip), "vpn-proxy") {
		t.Errorf("snippet does not contain the searched term:\n%q", snip)
	}
}

// The window is cut at a fixed byte offset either side of the match, which
// lands mid-character whenever the surrounding text does not happen to align.
func TestSnippetIsValidUTF8(t *testing.T) {
	// A 8-byte repeating unit, so a 60-byte lead-in cannot land on a boundary.
	body := strings.Repeat("日éxxx", 20) + "needle tail"
	i := strings.Index(body, "needle")
	snip := snippetAround(body, i)
	if !utf8.ValidString(snip) {
		t.Errorf("snippet is not valid UTF-8: % x", snip)
	}
	if !strings.Contains(snip, "needle") {
		t.Errorf("snippet lost the match: %q", snip)
	}
}

// The cheap reject must not be stricter than the match that follows it, or a
// line that genuinely contains the term is skipped and never counted.
func TestContainsFoldMatchesTheAllocatingVersion(t *testing.T) {
	lines := []string{
		`{"text":"the Ivanti tunnel keeps dropping"}`,
		`{"text":"THE IVANTI TUNNEL"}`,
		`{"text":"nothing relevant here"}`,
		`{"text":"prefix-ivanti"}`,
		`{"text":"ivanti"}`,
		`{"text":"ıvantı"}`, // dotless i, must not match
		`{"text":"日本語 ivanti 日本語"}`,
		`{"text":""}`,
		`{"text":"ivant"}`, // one short
	}
	queries := []string{"ivanti", "the", "日本語", "é", "x", "nothing relevant"}
	for _, q := range queries {
		for _, l := range lines {
			want := strings.Contains(strings.ToLower(l), q)
			if got := containsFold([]byte(l), q); got != want {
				t.Errorf("containsFold(%q, %q) = %v, want %v", l, q, got, want)
			}
		}
	}
}

// The reject filter must never be stricter than the match it guards, or a line
// that genuinely contains the term is skipped before the body match ever runs.
// Case folding outside ASCII can map a multi-byte rune onto an ASCII one —
// U+0130 onto "i", U+212A KELVIN SIGN onto "k" — which the bytewise fast path
// cannot see.
func TestContainsFoldNeverRejectsALineTheMatchWouldAccept(t *testing.T) {
	lines := []string{
		`{"text":"the Ivanti tunnel keeps dropping"}`,
		`{"text":"THE IVANTI TUNNEL"}`,
		`{"text":"İstanbul deploy"}`, // U+0130, folds to "i"
		"{\"text\":\"K elvin\"}",     // U+212A, folds to "k"
		"{\"text\":\"212K KELVIN\"}", // U+212A alongside an ASCII K
		`{"text":"nothing relevant"}`,
		`{"text":"ıvantı"}`, // dotless i, must not match "ivanti"
		`{"text":"日本語 ivanti"}`,
		`{"text":"straße"}`,
		`{"text":""}`,
	}
	queries := []string{"ivanti", "istanbul", "k", "i", "the", "日本語", "é", "straße", "nothing"}
	for _, q := range queries {
		for _, l := range lines {
			want := strings.Contains(strings.ToLower(l), q)
			if got := containsFold([]byte(l), q); got != want {
				t.Errorf("containsFold(%q, %q) = %v, want %v", l, q, got, want)
			}
		}
	}
}

// strings.ToLower is not length-preserving, so an index measured in the lowered
// string is not an offset into the original — the same class of mistake as
// measuring before format.Clean and slicing after.
func TestIndexFoldReturnsAnOffsetIntoTheOriginal(t *testing.T) {
	for _, c := range []struct{ text, q string }{
		{"KKK needle here", "needle"}, // U+212A ×3: six bytes of drift
		{"İstanbul needle", "needle"}, // U+0130: one byte
		{"plain needle", "needle"},
		{"NEEDLE upper", "needle"},
		{"日本語 needle", "needle"},
	} {
		i := indexFold(c.text, c.q)
		if i < 0 {
			t.Errorf("indexFold(%q, %q) found nothing", c.text, c.q)
			continue
		}
		if got := c.text[i:]; !strings.HasPrefix(strings.ToLower(got), c.q) {
			t.Errorf("indexFold(%q, %q) = %d, which points at %q", c.text, c.q, i, got)
		}
	}

	// It must agree with the lowered-string search on whether there is a match.
	for _, c := range []struct{ text, q string }{
		{"İstanbul", "istanbul"}, {"KELVIN", "k"}, {"ıvantı", "ivanti"}, {"none", "zzz"},
	} {
		want := strings.Contains(strings.ToLower(c.text), c.q)
		if got := indexFold(c.text, c.q) >= 0; got != want {
			t.Errorf("indexFold(%q, %q) matched=%v, want %v", c.text, c.q, got, want)
		}
	}
}

// The snippet has to be centred on the match even when the text ahead of it
// contains runes whose lowercase is a different length.
func TestSnippetSurvivesLengthChangingCaseFolds(t *testing.T) {
	body := strings.Repeat("K", 40) + " and then we fixed the VPN-PROXY setting"
	i := indexFold(body, "vpn-proxy")
	if i < 0 {
		t.Fatal("no match")
	}
	snip := snippetAround(body, i)
	if !strings.Contains(strings.ToLower(snip), "vpn-proxy") {
		t.Errorf("snippet lost the term: %q", snip)
	}
	if !utf8.ValidString(snip) {
		t.Errorf("snippet is not valid UTF-8: % x", snip)
	}
}

// containsFold's fast path is only safe because exactly two ASCII characters
// are reachable by lowering a non-ASCII rune. If a Unicode table update ever
// adds a third, the fast path would start silently rejecting lines that match,
// so pin the assumption rather than trust it.
func TestFoldSourcesFromNonASCIIAreExhaustive(t *testing.T) {
	var got []rune
	seen := map[rune]bool{}
	for r := rune(utf8.RuneSelf); r <= unicode.MaxRune; r++ {
		if l := unicode.ToLower(r); l < utf8.RuneSelf && !seen[l] {
			seen[l] = true
			got = append(got, l)
		}
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if string(got) != foldsFromNonASCII {
		t.Errorf("ASCII characters reachable by lowering a non-ASCII rune = %q, "+
			"but containsFold assumes %q — the fast path is now unsound",
			string(got), foldsFromNonASCII)
	}
}

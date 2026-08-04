package search

import (
	"bufio"
	"os"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/sadrig91/orbit/internal/format"
	"github.com/sadrig91/orbit/internal/session"
)

// Full-text search over transcript bodies. Titles are terse — "Check branch
// status against main" says nothing about which branch — so the question you
// actually have ("where did I fix the VPN proxy?") can only be answered by
// looking inside. Content isn't held in memory: transcripts total tens of
// megabytes and are only scanned when you ask.

type Match struct {
	SessionID string
	Snippet   string
	Hits      int
}

// Transcripts scans every session's body for q, returning the sessions
// that matched with a snippet of the first hit.
func Transcripts(sessions []*session.Session, q string) map[string]Match {
	q = strings.TrimSpace(strings.ToLower(q))
	res := map[string]Match{}
	if len(q) < 2 {
		return res
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())

	for _, s := range sessions {
		if s.Path == "" {
			continue // copilot lives in sqlite; handled below
		}
		wg.Add(1)
		go func(s *session.Session) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if m, ok := scanTranscript(s.Path, q); ok {
				m.SessionID = s.ID
				mu.Lock()
				res[s.ID] = m
				mu.Unlock()
			}
		}(s)
	}
	wg.Wait()

	// Copilot's turns are columns, not a file — search what we already hold.
	for _, s := range sessions {
		if s.Path != "" {
			continue
		}
		body := format.Clean(s.Title + " " + s.Last)
		if i := strings.Index(strings.ToLower(body), q); i >= 0 {
			res[s.ID] = Match{SessionID: s.ID, Hits: 1, Snippet: snippetAround(body, i)}
		}
	}
	return res
}

func scanTranscript(path, q string) (Match, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Match{}, false
	}
	defer f.Close()

	var m Match
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 256*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		// Cheap reject first: most lines don't contain the term at all, and
		// unmarshalling every record would dominate the cost.
		if !containsFold(line, q) {
			continue
		}
		// Clean before locating the match, not after. Clean deletes control
		// characters — newlines included — so an index measured on the raw text
		// no longer points at the match once it has run, and the snippet window
		// slides by one position per control character before the match. Past
		// sixty of them, which one code block clears, the snippet stopped
		// containing the term that was searched for at all.
		text := format.Clean(session.RecordText(line))
		if text == "" {
			continue
		}
		i := strings.Index(strings.ToLower(text), q)
		if i < 0 {
			continue // matched only inside metadata, not the message body
		}
		m.Hits++
		if m.Snippet == "" {
			m.Snippet = snippetAround(text, i)
		}
	}
	return m, m.Hits > 0
}

// containsFold is the cheap reject in front of the JSON unmarshal, so it runs
// on every line of every transcript. Lower-casing a copy of each line cost two
// allocations and a full copy per line — roughly twice the corpus in garbage
// per search — so it folds only the bytes it is comparing.
//
// The ASCII path is the one that matters; a non-ASCII query falls back to the
// allocating version rather than risk rejecting a line that would have matched,
// since Unicode case folding can change a string's byte length.
func containsFold(b []byte, lower string) bool {
	if len(lower) == 0 {
		return true
	}
	if !isASCII(lower) {
		return strings.Contains(strings.ToLower(string(b)), lower)
	}
	first := lower[0]
	for i := 0; i+len(lower) <= len(b); i++ {
		if foldByte(b[i]) != first {
			continue
		}
		if hasPrefixFold(b[i:], lower) {
			return true
		}
	}
	return false
}

func foldByte(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// hasPrefixFold assumes lower is ASCII and already lower-cased. An ASCII byte
// can never occur inside a multi-byte sequence, so matching bytewise cannot
// land in the middle of a character.
func hasPrefixFold(b []byte, lower string) bool {
	for j := 0; j < len(lower); j++ {
		if foldByte(b[j]) != lower[j] {
			return false
		}
	}
	return true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// snippetAround windows the text around a match. i must be an offset into text
// as it stands — see scanTranscript on why that is worth saying out loud.
func snippetAround(text string, i int) string {
	if i < 0 || i > len(text) {
		i = 0
	}
	start, end := max(0, i-60), min(len(text), i+140)
	s := strings.TrimSpace(format.SliceRunes(text, start, end))
	if start > 0 {
		s = "…" + s
	}
	if end < len(text) {
		s += "…"
	}
	return s
}

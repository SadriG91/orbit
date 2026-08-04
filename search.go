package main

import (
	"bufio"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"sync"
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

// SearchTranscripts scans every session's body for q, returning the sessions
// that matched with a snippet of the first hit.
func SearchTranscripts(sessions []*Session, q string) map[string]Match {
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
		go func(s *Session) {
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
		hay := strings.ToLower(s.Title + " " + s.Last)
		if i := strings.Index(hay, q); i >= 0 {
			res[s.ID] = Match{SessionID: s.ID, Hits: 1, Snippet: snippetAround(s.Title+" "+s.Last, i)}
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
		text := recordText(line)
		if text == "" {
			continue
		}
		low := strings.ToLower(text)
		i := strings.Index(low, q)
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

func containsFold(b []byte, lower string) bool {
	return strings.Contains(strings.ToLower(string(b)), lower)
}

// recordText pulls the human-readable text out of a transcript record, for
// both Claude's message blocks and Codex's event payloads.
func recordText(line []byte) string {
	var r struct {
		Message *struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Payload *struct {
			Message string `json:"message"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &r) != nil {
		return ""
	}
	if r.Payload != nil && r.Payload.Message != "" {
		return r.Payload.Message
	}
	if r.Message == nil || len(r.Message.Content) == 0 {
		return ""
	}
	if r.Message.Content[0] == '"' {
		var s string
		json.Unmarshal(r.Message.Content, &s)
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(r.Message.Content, &blocks) != nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Text != "" {
			sb.WriteString(b.Text)
			sb.WriteByte(' ')
		}
	}
	return sb.String()
}

func snippetAround(text string, i int) string {
	text = clean(text)
	if i > len(text) {
		i = 0
	}
	start := max(0, i-60)
	end := min(len(text), i+140)
	s := strings.TrimSpace(text[start:end])
	if start > 0 {
		s = "…" + s
	}
	if end < len(text) {
		s += "…"
	}
	return s
}

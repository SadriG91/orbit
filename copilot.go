package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Copilot CLI keeps a real database rather than transcripts:
// ~/.copilot/session-store.db, with sessions(id, cwd, branch, summary, updated_at)
// and turns(session_id, user_message, assistant_response).
//
// Queried through the sqlite3 CLI so orbit stays a cgo-free static binary. The
// result is cached against the db + WAL mtimes, so a tick usually costs two
// stat() calls and no subprocess.

const copilotQuery = `
SELECT s.id, COALESCE(s.cwd,''), COALESCE(s.branch,''), COALESCE(s.summary,''),
       COALESCE(s.updated_at,''),
       (SELECT COUNT(*) FROM turns t WHERE t.session_id = s.id),
       COALESCE((SELECT t.user_message FROM turns t WHERE t.session_id = s.id
                 ORDER BY t.turn_index DESC LIMIT 1), ''),
       COALESCE((SELECT LENGTH(COALESCE(t.assistant_response,'')) FROM turns t
                 WHERE t.session_id = s.id ORDER BY t.turn_index DESC LIMIT 1), 0),
       COALESCE((SELECT MAX(t.timestamp) FROM turns t WHERE t.session_id = s.id), ''),
       COALESCE((SELECT SUM(COALESCE(u.input_tokens,0) + COALESCE(u.output_tokens,0)
                          + COALESCE(u.cache_read_tokens,0) + COALESCE(u.cache_write_tokens,0))
                 FROM assistant_usage_events u WHERE u.session_id = s.id), 0)
FROM sessions s ORDER BY s.updated_at DESC;`

func copilotTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (ix *Index) scanCopilot() []*Session {
	db := home(".copilot", "session-store.db")
	stamp := ""
	for _, p := range []string{db, db + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			stamp += fmt.Sprintf("%d:%d|", fi.ModTime().UnixNano(), fi.Size())
		}
	}
	if stamp == "" {
		return nil
	}
	if stamp == ix.cpStamp {
		return ix.cpCache
	}

	// Read-only + immutable-free URI so a live Copilot holding the WAL can't block us.
	uri := "file:" + db + "?mode=ro"
	cmd := exec.Command("sqlite3", "-readonly", "-separator", "\x1f", uri, copilotQuery)
	out, err := cmd.Output()
	if err != nil {
		ix.Errs = append(ix.Errs, "copilot: "+err.Error())
		return ix.cpCache
	}

	var res []*Session
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) < 10 || f[0] == "" {
			continue
		}
		cwd := f[1]
		if cwd == "" || cwd == "/" {
			continue // Copilot records stub sessions with no real workspace
		}
		msgs, _ := strconv.Atoi(f[5])
		if msgs == 0 && f[3] == "" {
			continue // never actually used
		}
		// sessions.updated_at is not reliably maintained — turns can be days
		// newer than it — so the session is dated by whichever is later.
		mod := copilotTime(f[4])
		if turned := copilotTime(f[8]); turned.After(mod) {
			mod = turned
		}
		respLen, _ := strconv.Atoi(f[7])
		tokens, _ := strconv.ParseInt(f[9], 10, 64)
		s := &Session{
			Agent:    Copilot,
			ID:       f[0],
			Cwd:      cwd,
			Branch:   f[2],
			Title:    f[3],
			Last:     f[6],
			Msgs:     msgs,
			Tokens:   tokens,
			Modified: mod,
		}
		// No event stream to read: a turn with an assistant response written back
		// is finished, one without means Copilot is still mid-turn.
		if respLen > 0 {
			s.hint = HintDone
		} else {
			s.hint = HintBusy
		}
		res = append(res, s)
	}
	ix.cpCache, ix.cpStamp = res, stamp
	return res
}

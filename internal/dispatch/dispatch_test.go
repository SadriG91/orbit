package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func save(t *testing.T, r *Record) *Record {
	t.Helper()
	if err := Save(r); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return r
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want := save(t, &Record{
		ID: "d1", Agent: "claude", SessionID: "s1", Cwd: "/w",
		Prompt: "look at feature X", Status: Running, Started: time.Now(),
	})

	got, ok := Load("d1")
	if !ok {
		t.Fatal("Load found nothing")
	}
	if got.Agent != want.Agent || got.SessionID != want.SessionID ||
		got.Prompt != want.Prompt || got.Status != want.Status {
		t.Errorf("round trip lost something: %+v", got)
	}
	if got.Updated.IsZero() {
		t.Error("Save did not stamp Updated")
	}
}

func TestLoadRejectsRubbish(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// Half a file, which is exactly what a non-atomic writer would leave for
	// the scan to read.
	if err := os.WriteFile(filepath.Join(Dir(), "torn.json"), []byte(`{"id":"torn","age`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Well-formed but not a record.
	if err := os.WriteFile(filepath.Join(Dir(), "empty.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Load("torn"); ok {
		t.Error("Load accepted a truncated file")
	}
	if _, ok := Load("empty"); ok {
		t.Error("Load accepted a record with no agent")
	}
	if got := Active(); len(got) != 0 {
		t.Errorf("Active returned %v from unusable files", got)
	}
}

func TestActiveKeysBySession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	save(t, &Record{ID: "d1", Agent: "claude", SessionID: "s1", Status: Done, Started: time.Now()})
	save(t, &Record{ID: "d2", Agent: "codex", SessionID: "s1", Status: Running, Started: time.Now()})
	// A codex run that has not heard its thread_id yet has nothing to join to.
	save(t, &Record{ID: "d3", Agent: "codex", Status: Running, Started: time.Now()})

	got := Active()
	if len(got) != 2 {
		t.Fatalf("Active returned %d records, want 2", len(got))
	}
	// Same session id, different agents: ids are only unique within one store,
	// so the key has to carry both.
	if r := got[Key("claude", "s1")]; r == nil || r.ID != "d1" {
		t.Errorf("claude/s1 = %v, want d1", r)
	}
	if r := got[Key("codex", "s1")]; r == nil || r.ID != "d2" {
		t.Errorf("codex/s1 = %v, want d2", r)
	}
}

// A session dispatched to twice has two records; the newest is the one
// describing what is happening now.
func TestActivePrefersTheNewest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := time.Now().Add(-time.Hour)
	save(t, &Record{ID: "new", Agent: "claude", SessionID: "s", Status: Running, Started: time.Now()})
	save(t, &Record{ID: "old", Agent: "claude", SessionID: "s", Status: Done, Started: old})

	if r := Active()[Key("claude", "s")]; r == nil || r.ID != "new" {
		t.Errorf("got %v, want the newer record", r)
	}
}

func TestForgetDropsEveryRecordForASession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	save(t, &Record{ID: "a", Agent: "claude", SessionID: "s", Status: NeedsYou, Started: time.Now()})
	save(t, &Record{ID: "b", Agent: "claude", SessionID: "s", Status: Done, Started: time.Now()})
	keep := save(t, &Record{ID: "c", Agent: "claude", SessionID: "other", Status: Done, Started: time.Now()})
	if err := os.WriteFile(LogPath("a"), []byte("run log"), 0o600); err != nil {
		t.Fatal(err)
	}

	Forget("claude", "s")

	if _, ok := Load("a"); ok {
		t.Error("Forget left a record behind")
	}
	if _, ok := Load("b"); ok {
		t.Error("Forget dropped only one of the two records for the session")
	}
	if _, err := os.Stat(LogPath("a")); err == nil {
		t.Error("Forget left the log behind")
	}
	if _, ok := Load(keep.ID); !ok {
		t.Error("Forget took another session's record with it")
	}

	// A session with no id would otherwise match every codex run that has not
	// yet heard its thread_id.
	save(t, &Record{ID: "d", Agent: "codex", Status: Running, Started: time.Now()})
	Forget("codex", "")
	if _, ok := Load("d"); !ok {
		t.Error("Forget with an empty id deleted an unrelated record")
	}
}

func TestPruneKeepsWhatMatters(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := time.Now().Add(-48 * time.Hour)

	stale := save(t, &Record{ID: "stale", Agent: "claude", SessionID: "s1", Status: Done, Started: old})
	stale.Updated = old
	writeRaw(t, stale)
	if err := os.WriteFile(LogPath("stale"), []byte("log"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A dispatch left running over a weekend is still a dispatch: deleting its
	// record would strand the run with nothing on screen to say it exists.
	running := save(t, &Record{ID: "running", Agent: "claude", SessionID: "s2", Status: Running, Started: old})
	running.Updated = old
	writeRaw(t, running)

	save(t, &Record{ID: "fresh", Agent: "claude", SessionID: "s3", Status: Done, Started: time.Now()})

	Prune(24 * time.Hour)

	if _, ok := Load("stale"); ok {
		t.Error("Prune kept a finished record from two days ago")
	}
	if _, err := os.Stat(LogPath("stale")); err == nil {
		t.Error("Prune kept the log of a record it deleted")
	}
	if _, ok := Load("running"); !ok {
		t.Error("Prune deleted a running dispatch")
	}
	if _, ok := Load("fresh"); !ok {
		t.Error("Prune deleted a record that had just finished")
	}
}

func TestSanitizeRefusesTraversal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// The id reaches the runner as argv from a shell command line. Nothing
	// traversal-shaped may become a path, on principle.
	save(t, &Record{ID: "../../evil", Agent: "claude", SessionID: "s", Status: Done, Started: time.Now()})
	if _, err := os.Stat(filepath.Join(home, "..", "evil.json")); err == nil {
		t.Fatal("a record escaped the dispatch directory")
	}
	if _, err := os.Stat(filepath.Join(Dir(), "evil.json")); err != nil {
		t.Errorf("the sanitized name is not where it should be: %v", err)
	}
}

func TestNewIDLooksLikeAUUID(t *testing.T) {
	// claude and copilot both validate --session-id and reject anything that
	// is not a version 4 UUID, so this shape is a contract, not cosmetics.
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := NewID()
		if len(id) != 36 {
			t.Fatalf("NewID() = %q, want 36 characters", id)
		}
		for _, pos := range []int{8, 13, 18, 23} {
			if id[pos] != '-' {
				t.Fatalf("NewID() = %q, want a dash at %d", id, pos)
			}
		}
		if id[14] != '4' {
			t.Fatalf("NewID() = %q, want version 4", id)
		}
		if c := id[19]; c != '8' && c != '9' && c != 'a' && c != 'b' {
			t.Fatalf("NewID() = %q, want the RFC 4122 variant", id)
		}
		if seen[id] {
			t.Fatalf("NewID() repeated %q", id)
		}
		seen[id] = true
	}
}

// writeRaw rewrites a record without Save's Updated stamp, so a test can age
// one past the prune cutoff.
func writeRaw(t *testing.T, r *Record) {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file(r.ID), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file(r.ID), r.Updated, r.Updated); err != nil {
		t.Fatal(err)
	}
}

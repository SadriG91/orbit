package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".config", "orbit", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Writing back a preference must change the one value and nothing else. A file
// that comes back reformatted after a keypress is its own kind of betrayal.
func TestSetReplacesInPlaceAndKeepsEverythingElse(t *testing.T) {
	path := writeCfg(t, `# leading note
sort = "age"

# a comment that explains grouping
group = false

[summary]
enabled = true
`)
	if err := SetString("", "sort", "tokens"); err != nil {
		t.Fatalf("SetString: %v", err)
	}
	body, _ := os.ReadFile(path)
	got := string(body)

	if !strings.Contains(got, `sort = "tokens"`) {
		t.Errorf("the value did not change:\n%s", got)
	}
	for _, keep := range []string{"# leading note", "# a comment that explains grouping",
		"group = false", "[summary]", "enabled = true"} {
		if !strings.Contains(got, keep) {
			t.Errorf("lost %q:\n%s", keep, got)
		}
	}
	// Position matters too: the value must stay under its own comment.
	if strings.Index(got, "# leading note") > strings.Index(got, `sort = "tokens"`) {
		t.Errorf("the setting moved away from its comment:\n%s", got)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sort != "tokens" {
		t.Errorf("Sort = %q, want tokens", cfg.Sort)
	}
}

func TestSetBoolRoundTrips(t *testing.T) {
	writeCfg(t, "group = false\n")
	if err := SetBool("", "group", true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Group {
		t.Error("group did not round-trip to true")
	}
}

// A key that isn't there — retired, or predating the setting — has to be added
// inside its own table. Appended at the end of the file it would land under
// whatever [section] came last and quietly become a different setting.
func TestSetAddsAMissingKeyInsideItsTable(t *testing.T) {
	path := writeCfg(t, `sort = "age"

[summary]
enabled = true
`)
	if err := SetBool("summary", "auto", true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	body, _ := os.ReadFile(path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Summary.Auto {
		t.Errorf("summary.auto did not take:\n%s", body)
	}
	if strings.Index(string(body), "[summary]") > strings.Index(string(body), "auto = true") {
		t.Errorf("auto landed above its table:\n%s", body)
	}
}

func TestSetCreatesAMissingTable(t *testing.T) {
	writeCfg(t, "sort = \"age\"\n")
	if err := SetBool("update", "auto", false); err != nil {
		t.Fatalf("Set: %v", err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Update.Auto {
		t.Error("update.auto did not take")
	}
}

// Setting what the file already says must not rewrite it — otherwise cycling
// `o` back round to where it started churns the file for nothing.
func TestSetIsANoOpWhenUnchanged(t *testing.T) {
	path := writeCfg(t, "sort = \"age\"\n")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := SetString("", "sort", "age"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("the file was rewritten despite saying the same thing already")
	}
}

// A file orbit cannot parse is one it must not rewrite: someone is mid-edit,
// and a keystroke should not flatten that.
func TestSetRefusesABrokenFile(t *testing.T) {
	path := writeCfg(t, "this is not = = toml\n")
	if err := SetString("", "sort", "tokens"); err == nil {
		t.Fatal("Set rewrote a file it could not parse")
	}
	body, _ := os.ReadFile(path)
	if string(body) != "this is not = = toml\n" {
		t.Errorf("the broken file was modified:\n%s", body)
	}
}

func TestSetKeepsFileMode(t *testing.T) {
	path := writeCfg(t, "sort = \"age\"\n")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetString("", "sort", "project"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode became %v, want 0600", fi.Mode().Perm())
	}
}

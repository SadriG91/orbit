package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A managed value must not be written into anyone's file, or it pins that
// install to whatever was current the day it was created — which is how a
// default that turned out to be broken became unfixable.
func TestUserTemplateWithholdsManagedValues(t *testing.T) {
	tmpl := UserTemplate()

	for _, want := range []string{"[summary.claude]", "[summary.codex]", "[summary.copilot]"} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("the template lost the %s table entirely", want)
		}
	}
	// No line may *assign* a managed key. A string search would be wrong here:
	// the comments deliberately name the flags, and prose about a value is
	// exactly what should survive.
	for i, line := range strings.Split(tmpl, "\n") {
		if key, ok := keyOf(line); ok && key == "command" {
			t.Errorf("line %d assigns a managed key: %q", i+1, line)
		}
	}
	if !strings.Contains(tmpl, "orbit owns") {
		t.Error("the template dropped the comments explaining the managed settings")
	}
	// Still valid TOML, and unmanaged settings survive untouched.
	cfg, err := parseTemplate(tmpl)
	if err != nil {
		t.Fatalf("the template does not parse: %v", err)
	}
	if cfg.RecentDays == 0 || cfg.Attach == "" {
		t.Errorf("ordinary settings were lost: %+v", cfg)
	}
	if len(cfg.Summary.Claude.Command) != 0 {
		t.Errorf("the template still sets a command: %q", cfg.Summary.Claude.Command)
	}
}

// An absent managed key has to fall through to the shipped default, or
// withholding it would simply break summarising.
func TestAbsentManagedKeyFallsBackToTheDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := Load(); err != nil { // writes the template
		t.Fatalf("Load: %v", err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !slices.Contains(cfg.Summary.For("claude"), "--no-session-persistence") {
		t.Errorf("claude command = %q, want the shipped default", cfg.Summary.For("claude"))
	}
	if !slices.Contains(cfg.Summary.For("codex"), "--ephemeral") {
		t.Errorf("codex command = %q, want the shipped default", cfg.Summary.For("codex"))
	}
}

func writeConfig(t *testing.T, body string) string {
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

// A command orbit itself wrote is not a decision anyone made, so it can go —
// and must, or every install made before these keys became managed keeps a
// value that a later release has already replaced.
func TestSyncRetiresAValueOrbitShipped(t *testing.T) {
	path := writeConfig(t, `[summary.codex]
# some comment worth keeping
command = ["codex", "exec", "--model", "gpt-5-mini", "--sandbox", "read-only", "--skip-git-repo-check"]
`)

	_, removed, err := Sync()
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !slices.Contains(removed, "summary.codex.command") {
		t.Fatalf("removed %v, want summary.codex.command", removed)
	}

	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "gpt-5-mini") {
		t.Errorf("the superseded value is still in the file:\n%s", body)
	}
	if !strings.Contains(string(body), "some comment worth keeping") {
		t.Errorf("retiring took the comments with it:\n%s", body)
	}
	// And the config now resolves to the current default.
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !slices.Contains(cfg.Summary.For("codex"), "--ephemeral") {
		t.Errorf("codex command = %q, want the current default", cfg.Summary.For("codex"))
	}
}

// A value orbit does not recognise is somebody's choice and goes on winning.
// This is the whole reason retiring is safe: it cannot touch a customisation.
func TestSyncLeavesACustomisedCommandAlone(t *testing.T) {
	path := writeConfig(t, `[summary.codex]
command = ["codex", "exec", "--model", "my-own-cheap-model"]
`)

	_, removed, err := Sync()
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if slices.Contains(removed, "summary.codex.command") {
		t.Error("a customised command was retired")
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "my-own-cheap-model") {
		t.Errorf("the customisation was removed:\n%s", body)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !slices.Contains(cfg.Summary.For("codex"), "my-own-cheap-model") {
		t.Errorf("codex command = %q, want the customisation to win", cfg.Summary.For("codex"))
	}
}

// Retiring must not come back round: once a managed key is gone, the merge
// must not helpfully add it again on the next start.
func TestRetiredKeysAreNotAddedBack(t *testing.T) {
	writeConfig(t, `[summary.codex]
command = ["codex", "exec", "--model", "gpt-5-mini", "--sandbox", "read-only", "--skip-git-repo-check"]
`)
	if _, _, err := Sync(); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	added, removed, err := Sync()
	if err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if slices.Contains(added, "summary.codex.command") {
		t.Error("the managed key was added straight back")
	}
	if len(removed) != 0 {
		t.Errorf("second Sync removed %v, want nothing left to do", removed)
	}
}

func parseTemplate(s string) (Config, error) {
	var cfg Config
	_, err := tomlDecode(s, &cfg)
	return cfg, err
}

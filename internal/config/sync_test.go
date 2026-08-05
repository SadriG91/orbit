package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// The trap this whole thing exists to avoid: a top-level key appended at the
// end of the file lands under whatever [section] came last, and TOML reads it
// as a different setting entirely. `icons` must come back as icons, not as
// summary.icons.
func TestMergePutsTopLevelKeysAboveSections(t *testing.T) {
	user := `icons = "text"

[summary]
enabled = true
`
	def := `icons = "auto"

# what Enter does
attach = "smart"

[summary]
enabled = true
`
	merged, added := mergeConfig(user, def)
	if !reflect.DeepEqual(added, []string{"attach"}) {
		t.Fatalf("added %v, want [attach]", added)
	}

	var got struct {
		Icons   string `toml:"icons"`
		Attach  string `toml:"attach"`
		Summary struct {
			Enabled bool   `toml:"enabled"`
			Attach  string `toml:"attach"`
		} `toml:"summary"`
	}
	if _, err := toml.Decode(merged, &got); err != nil {
		t.Fatalf("merged file does not parse: %v\n%s", err, merged)
	}
	if got.Attach != "smart" {
		t.Errorf("attach = %q, want smart — it did not land at the top level", got.Attach)
	}
	if got.Summary.Attach != "" {
		t.Errorf("attach was swallowed by [summary]: %q\n%s", got.Summary.Attach, merged)
	}
	if got.Icons != "text" {
		t.Errorf("icons = %q; an existing value was rewritten", got.Icons)
	}
}

// A value in the file is a decision, even when orbit's own default has moved
// on. Merging must never overwrite one.
func TestMergeNeverChangesExistingValues(t *testing.T) {
	user := `icons = "text"
attach = "inline"

[summary]
enabled = false
command = ["my", "own", "thing"]
`
	merged, added := mergeConfig(user, defaultConfigTOML)
	if len(added) == 0 {
		t.Fatal("nothing was added from the real defaults")
	}
	for _, keep := range []string{`icons = "text"`, `attach = "inline"`, `enabled = false`,
		`command = ["my", "own", "thing"]`} {
		if !strings.Contains(merged, keep) {
			t.Errorf("merge lost or rewrote %q", keep)
		}
	}
	// And the effective config still says what the user asked for.
	cfg, err := LoadDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := toml.Decode(merged, &cfg); err != nil {
		t.Fatalf("merged file does not parse: %v", err)
	}
	if cfg.Icons != "text" || cfg.Attach != "inline" || cfg.Summary.Enabled {
		t.Errorf("merge changed the effective config: %+v", cfg)
	}
}

// Merging must not change what orbit reads — only what the file shows. A file
// missing everything and a file missing nothing have to load identically.
func TestMergeDoesNotChangeEffectiveConfig(t *testing.T) {
	for name, user := range map[string]string{
		"empty":           "",
		"one key":         "icons = \"logo\"\n",
		"a whole section": "[summary]\nenabled = false\n",
	} {
		t.Run(name, func(t *testing.T) {
			before, err := LoadDefaults()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := toml.Decode(user, &before); err != nil {
				t.Fatal(err)
			}
			merged, _ := mergeConfig(user, defaultConfigTOML)
			after, err := LoadDefaults()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := toml.Decode(merged, &after); err != nil {
				t.Fatalf("merged file does not parse: %v\n%s", err, merged)
			}
			if !reflect.DeepEqual(before, after) {
				t.Errorf("merging changed the effective config\nbefore: %+v\nafter:  %+v", before, after)
			}
		})
	}
}

// A missing key inside a section it already has must go in that section.
func TestMergeAddsKeyInsideExistingSection(t *testing.T) {
	user := "[summary]\nenabled = true\n"
	def := "[summary]\nenabled = true\n\n# how many\nauto_min_new_messages = 8\n"
	merged, added := mergeConfig(user, def)
	if !reflect.DeepEqual(added, []string{"summary.auto_min_new_messages"}) {
		t.Fatalf("added %v", added)
	}
	var got struct {
		Summary struct {
			Min int `toml:"auto_min_new_messages"`
		} `toml:"summary"`
	}
	if _, err := toml.Decode(merged, &got); err != nil {
		t.Fatal(err)
	}
	if got.Summary.Min != 8 {
		t.Errorf("landed outside [summary]: %+v\n%s", got, merged)
	}
	if !strings.Contains(merged, "# how many") {
		t.Error("the comment explaining the setting was dropped")
	}
}

// Added settings have to look like they belong: one blank line between a new
// table and the one before it, the same as everywhere else in the file.
func TestMergeSpacesAdditionsLikeTheRestOfTheFile(t *testing.T) {
	merged, _ := mergeConfig("[summary]\nenabled = true\n", defaultConfigTOML)
	lines := strings.Split(merged, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "[") || i == 0 {
			continue
		}
		// A table may be introduced by its own comment block — that is the
		// file's style. The blank line belongs above whichever comes first.
		j := i - 1
		for j >= 0 && strings.HasPrefix(strings.TrimSpace(lines[j]), "#") {
			j--
		}
		if j >= 0 && strings.TrimSpace(lines[j]) != "" {
			t.Errorf("line %d, %q, is jammed against %q", i, line, lines[j])
		}
	}
	// No run of two or more blank lines, either.
	if strings.Contains(merged, "\n\n\n") {
		t.Errorf("merge left a double blank line:\n%s", merged)
	}
	// An empty file must not gain a leading blank line.
	fromNothing, _ := mergeConfig("", defaultConfigTOML)
	if strings.HasPrefix(fromNothing, "\n") {
		t.Error("merging into an empty file produced a leading blank line")
	}
}

// Nothing missing means nothing written — the file keeps its mtime and its
// formatting, however the person who edited it likes them.
func TestSyncLeavesACurrentFileAlone(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".config", "orbit"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := Path()
	if err := os.WriteFile(path, []byte(defaultConfigTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	added, err := Sync()
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(added) != 0 {
		t.Errorf("added %v to a file that already has everything", added)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("the file was rewritten despite having nothing to add")
	}
	if after.Mode().Perm() != 0o600 {
		t.Errorf("mode changed to %v", after.Mode().Perm())
	}
}

// Sync writes through a rename, and must preserve the file's mode when it
// does — a config that suddenly becomes world-readable is a surprise.
func TestSyncWritesAndKeepsMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".config", "orbit"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := Path()
	if err := os.WriteFile(path, []byte("icons = \"text\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	added, err := Sync()
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(added) == 0 {
		t.Fatal("nothing added to a nearly-empty config")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode is %v, want 0600", fi.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		t.Fatalf("wrote a file that doesn't parse: %v", err)
	}
	if cfg.Icons != "text" {
		t.Errorf("icons = %q, want the value that was already there", cfg.Icons)
	}
	if !cfg.Update.Auto {
		t.Error("[update] was not added")
	}
	// No debris beside the config from the temp-file write.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Errorf("left a temp file behind: %s", e.Name())
		}
	}

	// Running again is a no-op: everything is now present.
	again, err := Sync()
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("second run added %v", again)
	}
}

// A file orbit can't parse is one it must not rewrite: the merge would be
// built on a misreading and hand-edits would go down with it.
func TestSyncRefusesToTouchABrokenFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".config", "orbit"), 0o755); err != nil {
		t.Fatal(err)
	}
	broken := "icons = \nthis is not toml [[[\n"
	if err := os.WriteFile(Path(), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(); err == nil {
		t.Error("Sync reported success on an unparseable file")
	}
	data, _ := os.ReadFile(Path())
	if string(data) != broken {
		t.Errorf("the broken file was modified:\n%s", data)
	}
}

// A missing file is the first-run case, which Load handles by writing the
// annotated default. Sync must stay out of its way.
func TestSyncIgnoresAMissingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	added, err := Sync()
	if err != nil || len(added) != 0 {
		t.Errorf("Sync on a missing file: %v, %v", added, err)
	}
	if _, err := os.Stat(Path()); !os.IsNotExist(err) {
		t.Error("Sync created the config file; that is Load's job")
	}
}

// The real case this was built for: the config people already have on disk,
// written before [update] and the icons default existed.
func TestMergeOnAPreUpdateConfig(t *testing.T) {
	old := strings.ReplaceAll(defaultConfigTOML, "icons = \"auto\"", "icons = \"text\"")
	if i := strings.Index(old, "[update]"); i >= 0 {
		old = old[:i]
	}
	merged, added := mergeConfig(old, defaultConfigTOML)
	if !reflect.DeepEqual(added, []string{"update.auto"}) {
		t.Fatalf("added %v, want just update.auto", added)
	}
	cfg, err := LoadDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := toml.Decode(merged, &cfg); err != nil {
		t.Fatalf("merged file does not parse: %v", err)
	}
	if !cfg.Update.Auto {
		t.Error("update.auto did not take effect")
	}
	if cfg.Icons != "text" {
		t.Errorf("icons = %q — an existing choice was overwritten by the new default", cfg.Icons)
	}
	if !strings.Contains(merged, "~/.cache/orbit/update.json") {
		t.Error("the new section arrived without the comments explaining it")
	}
}

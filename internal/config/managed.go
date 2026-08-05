package config

import (
	"strings"

	"github.com/BurntSushi/toml"
)

// tomlDecode is the one place the TOML library is reached for outside
// config.go, kept named so tests can parse a template the same way Load does.
func tomlDecode(data string, v any) (toml.MetaData, error) { return toml.Decode(data, v) }

// Some settings belong to orbit rather than to you.
//
// A summary command is not a preference. It is part of how a release works:
// which flag stops that CLI recording a session of its own, which model the
// provider will actually accept, which subcommand exists in the version people
// have. When any of that changes, every install needs the new value — and the
// old arrangement made that impossible, because a command written into a config
// file on first run was indistinguishable from one you had chosen, so orbit
// could never touch it again. A default shipped broken stayed broken.
//
// So orbit keeps them out of your file. They live in the embedded defaults,
// which is still the single source of truth for values, and are withheld from
// what gets written to disk. An absent key falls through to the default, so
// upgrading orbit upgrades the command.
//
// Setting one yourself still wins, and now means something unambiguous: a
// managed key present in your file is a decision, and Sync leaves it alone
// forever. The one exception is a value orbit itself shipped — see retire.

type managedKey struct {
	section string
	key     string
	value   func(Config) []string
}

func (m managedKey) dotted() string { return dotted(m.section, m.key) }

var managedKeys = []managedKey{
	{"summary.claude", "command", func(c Config) []string { return c.Summary.Claude.Command }},
	{"summary.codex", "command", func(c Config) []string { return c.Summary.Codex.Command }},
	{"summary.copilot", "command", func(c Config) []string { return c.Summary.Copilot.Command }},
}

// superseded lists values orbit has written into config files in the past and
// no longer ships.
//
// A file still holding one of these was never edited: orbit put it there and
// nobody changed it. That is the only way to tell a stale default from a
// deliberate choice, and it is what makes removing it safe rather than
// presumptuous. When a managed default changes, its previous value belongs
// here — otherwise existing installs keep it forever.
var superseded = map[string][][]string{
	"summary.claude.command": {
		{"claude", "-p", "--model", "claude-haiku-4-5-20251001", "--allowed-tools", ""},
	},
	"summary.codex.command": {
		// Rejected outright by ChatGPT-account logins, so codex summaries
		// failed for anyone signed in that way.
		{"codex", "exec", "--model", "gpt-5-mini", "--sandbox", "read-only", "--skip-git-repo-check"},
	},
	"summary.copilot.command": {},
}

func isManaged(section, key string) bool {
	for _, m := range managedKeys {
		if m.section == section && m.key == key {
			return true
		}
	}
	return false
}

// wasShipped reports whether a value is one orbit put there — either what it
// ships now, or something it shipped before.
func wasShipped(m managedKey, have []string) bool {
	if len(have) == 0 {
		return false
	}
	if def, err := LoadDefaults(); err == nil && sameCommand(have, m.value(def)) {
		return true
	}
	for _, old := range superseded[m.dotted()] {
		if sameCommand(have, old) {
			return true
		}
	}
	return false
}

func sameCommand(a, b []string) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// UserTemplate is the config as it should be written to a file: the shipped
// default with orbit-owned values withheld. Their explanatory comments stay,
// so the setting is still documented where you would look for it — you just
// have to write the value yourself to take it over.
func UserTemplate() string {
	regions := parseRegions(defaultConfigTOML)
	for i, r := range regions {
		regions[i].lines = dropKeys(r.lines, func(key string) bool {
			return isManaged(r.name, key)
		})
	}
	var out []string
	for _, r := range regions {
		out = append(out, r.lines...)
	}
	tmpl := strings.Join(out, "\n")
	if strings.HasSuffix(defaultConfigTOML, "\n") && !strings.HasSuffix(tmpl, "\n") {
		tmpl += "\n"
	}
	return tmpl
}

// retire removes managed keys whose value orbit itself shipped, so an install
// made before those keys became managed converges on the current default
// instead of being pinned to whatever was current the day it was created.
//
// A value orbit does not recognise is left alone: that is someone's choice, and
// it goes on overriding the default exactly as it should. This is the only
// thing in Sync that removes rather than adds, and it is bounded by both
// conditions — a managed key, holding a value orbit is certain it wrote.
func retire(text string, cfg Config) (string, []string) {
	var removed []string
	regions := parseRegions(text)
	for i, r := range regions {
		regions[i].lines = dropKeys(r.lines, func(key string) bool {
			for _, m := range managedKeys {
				if m.section != r.name || m.key != key || !wasShipped(m, m.value(cfg)) {
					continue
				}
				removed = append(removed, m.dotted())
				return true
			}
			return false
		})
	}
	if len(removed) == 0 {
		return text, nil
	}
	var out []string
	for _, r := range regions {
		out = append(out, r.lines...)
	}
	merged := strings.Join(out, "\n")
	if strings.HasSuffix(text, "\n") && !strings.HasSuffix(merged, "\n") {
		merged += "\n"
	}
	return merged, removed
}

// dropKeys removes the assignments a predicate selects, leaving the comments
// that explain them in place. A value spread over several lines goes whole.
func dropKeys(lines []string, drop func(key string) bool) []string {
	var out []string
	for i := 0; i < len(lines); i++ {
		key, ok := keyOf(lines[i])
		if !ok || !drop(key) {
			out = append(out, lines[i])
			continue
		}
		body := []string{lines[i]}
		for !balanced(strings.Join(body, "\n")) && i+1 < len(lines) {
			i++
			body = append(body, lines[i])
		}
	}
	return out
}

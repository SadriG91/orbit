package config

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Set writes one setting back to the config file, so a preference discovered
// in the UI can be kept without going and editing a file.
//
// Everything else here is careful never to rewrite a value, because a value in
// the file is someone's decision. This is the same rule seen from the other
// side: pressing `o` until the list looks right *is* the decision, and having
// to reproduce it by hand afterwards is the awkward part.
//
// Only the one assignment changes. Comments, ordering, spacing and every other
// setting are left exactly as they were — a file that comes back reformatted
// after a keypress would be its own kind of betrayal.
func Set(section, key, literal string) error {
	path := Path()
	data, err := os.ReadFile(path)
	if err != nil {
		return err // no file yet: Load writes one, and there is nothing to edit
	}
	// The same guard Sync uses: a file orbit cannot parse is one it must not
	// rewrite, or a hand-edit in progress gets flattened by a keystroke.
	var probe map[string]any
	if _, err := toml.Decode(string(data), &probe); err != nil {
		return err
	}

	updated, ok := setIn(string(data), section, key, literal)
	if !ok {
		return nil // already says that; leave the file's mtime alone
	}
	if err := writeAtomic(path, updated); err != nil {
		return err
	}

	// Read it back rather than trust that it took. Applying the same change to
	// what actually landed must now be a no-op; if it still wants to change
	// something, the file does not say what we think it says and the caller
	// should hear about it rather than the UI quietly disagreeing with disk.
	after, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, again := setIn(string(after), section, key, literal); again {
		return errors.New(dotted(section, key) + " did not take")
	}
	return nil
}

// SetBool and SetString spell the value the way TOML wants it, so callers
// aren't quoting things by hand at the call site.
func SetBool(section, key string, v bool) error {
	return Set(section, key, strconv.FormatBool(v))
}

func SetString(section, key, v string) error {
	return Set(section, key, strconv.Quote(v))
}

// setIn replaces an assignment in place, or adds it to its table if absent.
// Reports whether anything actually changed.
func setIn(text, section, key, literal string) (string, bool) {
	want := key + " = " + literal
	regions := parseRegions(text)

	for i, r := range regions {
		if r.name != section {
			continue
		}
		lines, replaced, same := replaceKey(r.lines, key, want)
		if same {
			return text, false
		}
		if !replaced {
			// The key has never been in the file — retired, or predating the
			// setting. It belongs inside its own table: appended at the end of
			// the file it would land under whatever [section] came last and
			// quietly become a different setting.
			lines = append(lines, want)
		}
		regions[i].lines = lines
		return join(regions, text), true
	}

	// No such table yet, so make one rather than dropping the preference.
	regions = append(regions, region{name: section, lines: sectionLines(section, want)})
	return join(regions, text), true
}

func sectionLines(section, assignment string) []string {
	if section == "" {
		return []string{assignment}
	}
	return []string{"[" + section + "]", assignment}
}

// replaceKey swaps the assignment for key, keeping its comments and position.
func replaceKey(lines []string, key, want string) (out []string, replaced, same bool) {
	for i := 0; i < len(lines); i++ {
		got, ok := keyOf(lines[i])
		if !ok || got != key {
			out = append(out, lines[i])
			continue
		}
		// A value spread over several lines is replaced whole.
		body := []string{lines[i]}
		for !balanced(strings.Join(body, "\n")) && i+1 < len(lines) {
			i++
			body = append(body, lines[i])
		}
		if len(body) == 1 && strings.TrimSpace(body[0]) == want {
			same = true
		}
		out = append(out, want)
		replaced = true
	}
	return out, replaced, same
}

func join(regions []region, original string) string {
	var out []string
	for _, r := range regions {
		out = append(out, r.lines...)
	}
	merged := strings.Join(out, "\n")
	if strings.HasSuffix(original, "\n") && !strings.HasSuffix(merged, "\n") {
		merged += "\n"
	}
	return merged
}

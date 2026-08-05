package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Sync writes settings orbit has gained since your config file was created
// into that file, so a new feature is something you can see and tune rather
// than something you have to already know about.
//
// A value in the file is never rewritten, reordered or reformatted. Where
// orbit's own default has since changed the file still wins, because a value
// someone put there is a decision and this cannot see the difference between a
// deliberate one and a leftover.
//
// The single exception is a managed key holding a value orbit itself shipped —
// see managed.go. That is not someone's decision, it is orbit's own, and it is
// retired so the setting goes back to tracking releases. Both conditions are
// required, so a value orbit does not recognise is left alone whatever it is.
//
// Returns the names of what it added and what it retired, for the caller to
// mention.
func Sync() (added, removed []string, err error) {
	path := Path()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil, nil // Load writes the template; nothing to merge
	}
	if err != nil {
		return nil, nil, err
	}
	// A file orbit can't parse is a file orbit must not rewrite — the
	// merge would be built on a misreading, and hand-edits would go with it.
	var probe map[string]any
	if _, err := toml.Decode(string(data), &probe); err != nil {
		return nil, nil, err
	}
	var have Config
	if _, err := toml.Decode(string(data), &have); err != nil {
		return nil, nil, err
	}

	// Merged against the template rather than the raw default, so managed
	// settings are never added back after being retired.
	merged, added := mergeConfig(string(data), UserTemplate())
	merged, removed = retire(merged, have)
	if len(added) == 0 && len(removed) == 0 {
		return nil, nil, nil // untouched, so the file keeps its mtime
	}

	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	// Written beside the original and renamed over it: a config half-written
	// by an interrupted start is one orbit would refuse to parse next time.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return nil, nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(merged); err != nil {
		tmp.Close()
		return nil, nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return nil, nil, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, nil, err
	}
	return added, removed, nil
}

// A region is a table and the lines that belong to it: the top-level one,
// then each [section] from its header to just before the next.
type region struct {
	name  string // "" for the top-level table
	lines []string
}

// A block is one setting with the comments that explain it, kept together so
// an added key arrives documented rather than bare.
type block struct {
	key   string
	lines []string
}

// mergeConfig returns the user's file with any settings missing from it
// appended in the right table, and the dotted names of what was added.
func mergeConfig(user, def string) (string, []string) {
	userRegions := parseRegions(user)
	defRegions := parseRegions(def)

	index := map[string]int{}
	for i, r := range userRegions {
		index[r.name] = i
	}

	var added []string
	var newRegions []region
	touched := map[int]bool{} // regions gaining a key, for the spacing pass
	for _, dr := range defRegions {
		ui, exists := index[dr.name]
		if !exists {
			// A whole table the file has never had: append it entire, with
			// its heading comments, after everything the user already has.
			newRegions = append(newRegions, dr)
			for _, b := range blocks(dr.lines) {
				added = append(added, dotted(dr.name, b.key))
			}
			continue
		}
		have := keysIn(userRegions[ui].lines)
		for _, b := range blocks(dr.lines) {
			if have[b.key] {
				continue
			}
			// Appended inside its own table. A top-level key appended at the
			// end of the file would land under whatever [section] came last
			// and quietly become a different setting.
			userRegions[ui].lines = appendSeparated(userRegions[ui].lines, b.lines)
			touched[ui] = true
			added = append(added, dotted(dr.name, b.key))
		}
	}
	if len(added) == 0 {
		return user, nil
	}

	// A table that gained a key and is followed by another needs a blank line
	// at the end, or the next [header] ends up butted against the new
	// setting. Only tables that changed are adjusted — untouched ones keep
	// whatever spacing the file already had.
	for i := range userRegions {
		if !touched[i] || i == len(userRegions)-1 || len(userRegions[i].lines) == 0 {
			continue
		}
		if last := userRegions[i].lines[len(userRegions[i].lines)-1]; strings.TrimSpace(last) != "" {
			userRegions[i].lines = append(userRegions[i].lines, "")
		}
	}

	var out []string
	for _, r := range userRegions {
		out = append(out, r.lines...)
	}
	// Separated rather than concatenated: a new table butted straight up
	// against the last line of the previous one parses fine but reads as an
	// afterthought, which is exactly what it must not look like.
	for _, r := range newRegions {
		out = appendSeparated(out, r.lines)
	}
	merged := strings.Join(out, "\n")
	if strings.HasSuffix(user, "\n") && !strings.HasSuffix(merged, "\n") {
		merged += "\n"
	}
	return merged, added
}

func dotted(section, key string) string {
	if section == "" {
		return key
	}
	return section + "." + key
}

// appendSeparated joins two runs of lines with exactly one blank line between
// them, whatever blank lines each already had — so an added setting or table
// sits apart from its neighbour the way the rest of the file does.
func appendSeparated(lines, add []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	for len(add) > 0 && strings.TrimSpace(add[0]) == "" {
		add = add[1:]
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	return append(lines, add...)
}

func parseRegions(s string) []region {
	regions := []region{{name: ""}}
	var pending []string // comments waiting to be attached to what follows
	for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		if name, ok := sectionHeader(line); ok {
			// The comments directly above a header introduce it, so they
			// travel with the new table rather than staying in the old one.
			cur := &regions[len(regions)-1]
			cur.lines = append(cur.lines, splitTrailingComments(&pending)...)
			regions = append(regions, region{name: name, lines: append(pending, line)})
			pending = nil
			continue
		}
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			pending = append(pending, line)
			continue
		}
		cur := &regions[len(regions)-1]
		cur.lines = append(cur.lines, pending...)
		cur.lines = append(cur.lines, line)
		pending = nil
	}
	cur := &regions[len(regions)-1]
	cur.lines = append(cur.lines, pending...)
	return regions
}

// splitTrailingComments takes the blank lines that separate the previous
// table from a heading comment, leaving the comment itself for the heading.
func splitTrailingComments(pending *[]string) []string {
	p := *pending
	i := 0
	for i < len(p) && strings.TrimSpace(p[i]) == "" {
		i++
	}
	// Everything up to the first comment stays behind; the rest moves on.
	keep := p[:i]
	*pending = p[i:]
	return keep
}

func sectionHeader(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
		return strings.Trim(t, "[]"), true
	}
	return "", false
}

// blocks groups a table's lines into one entry per setting, each carrying the
// comment lines immediately above it.
func blocks(lines []string) []block {
	var out []block
	var pending []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if _, ok := sectionHeader(line); ok {
			pending = nil
			continue
		}
		key, ok := keyOf(line)
		if !ok {
			pending = append(pending, line)
			continue
		}
		body := append(append([]string{}, pending...), line)
		// A value spread over several lines (an array, say) belongs with it.
		for !balanced(strings.Join(body, "\n")) && i+1 < len(lines) {
			i++
			body = append(body, lines[i])
		}
		out = append(out, block{key: key, lines: body})
		pending = nil
	}
	return out
}

func keysIn(lines []string) map[string]bool {
	out := map[string]bool{}
	for _, b := range blocks(lines) {
		out[b.key] = true
	}
	return out
}

// keyOf reads the key from an assignment, ignoring comments and headers.
func keyOf(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "[") {
		return "", false
	}
	name, _, ok := strings.Cut(t, "=")
	if !ok {
		return "", false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	return strings.Trim(name, `"'`), true
}

// balanced reports whether every bracket opened in s is closed, which is how
// a multi-line array is recognised as still being unfinished.
func balanced(s string) bool {
	depth, inStr := 0, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' && (i == 0 || s[i-1] != '\\'):
			inStr = !inStr
		case inStr:
		case c == '#':
			// A comment runs to the end of the line.
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		}
	}
	return depth <= 0
}

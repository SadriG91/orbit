package term

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sadrig91/orbit/internal/tmux"
)

// The scripted routes type the attach line into a shell rather than exec, so
// its arguments have to survive word splitting. A macOS home directory with a
// space in it — "/Users/First Last" — would otherwise break the tmux -f path
// in two and the tab would open to a failed attach.
func TestShellCommandQuotesArguments(t *testing.T) {
	t.Setenv("HOME", "/Users/First Last")

	argv := tmux.AttachArgv("cl-work-api-gateway-aaaaaaaa")
	got := shellCommand(argv)
	want := "'tmux' '-u' '-L' 'orbit' '-f' '/Users/First Last/.config/orbit/tmux.conf' 'attach' '-t' 'cl-work-api-gateway-aaaaaaaa'"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}

	// Round-trip through a real shell: the argv the line produces must match
	// the argv exec would have passed.
	out, err := exec.Command("sh", "-c", "printf '%s\\n' "+strings.TrimPrefix(got, "'tmux' ")).Output()
	if err != nil {
		t.Fatalf("shell rejected the command: %v", err)
	}
	split := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(split) != len(argv)-1 {
		t.Fatalf("shell split into %d args, want %d: %q", len(split), len(argv)-1, split)
	}
	for i := range split {
		if split[i] != argv[i+1] {
			t.Errorf("arg %d = %q, want %q", i, split[i], argv[i+1])
		}
	}
}

func TestShellQuoteHandlesQuotes(t *testing.T) {
	for _, s := range []string{"plain", "with space", "it's", `a"b`, `back\slash`, "$HOME", "`cmd`"} {
		out, err := exec.Command("sh", "-c", "printf '%s' "+shellQuote(s)).Output()
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if string(out) != s {
			t.Errorf("%q round-tripped as %q", s, out)
		}
	}
}

// The AppleScript literals wrap user-controlled text (session titles, paths),
// so escaping has to hold for the characters AppleScript treats specially.
func TestAsStringEscapes(t *testing.T) {
	for in, want := range map[string]string{
		"plain":       `"plain"`,
		`say "hi"`:    `"say \"hi\""`,
		`back\slash`:  `"back\\slash"`,
		"work · tidy": `"work · tidy"`,
	} {
		if got := asString(in); got != want {
			t.Errorf("asString(%q) = %s, want %s", in, got, want)
		}
	}
}

// The preflight warning must fire exactly when the experience would silently
// degrade: old Ghostty (no sdef in the bundle) or a fossil iTerm. A current
// terminal, or none at all, must stay quiet — a warning on every start is one
// nobody reads.
func TestPreflight(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("preflight only speaks on macOS")
	}
	// A fake bundle stands in for the real app so the sdef probe is testable:
	// GHOSTTY_RESOURCES_DIR points inside it, exactly as Ghostty sets it.
	bundle := filepath.Join(t.TempDir(), "Ghostty.app")
	resources := filepath.Join(bundle, "Contents", "Resources")
	if err := os.MkdirAll(resources, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TERM_PROGRAM", "ghostty")
	t.Setenv("GHOSTTY_RESOURCES_DIR", filepath.Join(resources, "ghostty"))
	t.Setenv("TERM_PROGRAM_VERSION", "1.2.3")
	t.Setenv("LC_TERMINAL", "")
	if got := Preflight(); !strings.Contains(got, "1.2.3") || !strings.Contains(got, "1.3+") {
		t.Errorf("old Ghostty should warn with its version and the fix, got %q", got)
	}

	// The sdef appearing is what makes it current, whatever the version says.
	if err := os.WriteFile(filepath.Join(resources, "Ghostty.sdef"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Preflight(); got != "" {
		t.Errorf("current Ghostty warned: %q", got)
	}

	t.Setenv("TERM_PROGRAM", "iTerm.app")
	t.Setenv("GHOSTTY_RESOURCES_DIR", "")
	t.Setenv("TERM_PROGRAM_VERSION", "2.1.4")
	if got := Preflight(); !strings.Contains(got, "2.1.4") {
		t.Errorf("fossil iTerm should warn with its version, got %q", got)
	}
	t.Setenv("TERM_PROGRAM_VERSION", "3.5.11")
	if got := Preflight(); got != "" {
		t.Errorf("current iTerm warned: %q", got)
	}

	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	t.Setenv("TERM_PROGRAM_VERSION", "")
	if got := Preflight(); got != "" {
		t.Errorf("an unscriptable terminal is normal, not warnable: %q", got)
	}
}

// Dev and nightly builds ship version strings like "1.3.0-main+abc" or none
// at all; those must count as new enough, because the functional probes are
// what actually decide behaviour.
func TestVersionAtLeast(t *testing.T) {
	for _, tc := range []struct {
		v     string
		major int
		want  bool
	}{
		{"3.5.11", 3, true},
		{"2.9", 3, false},
		{"10.0", 3, true},
		{"", 3, true},
		{"tip", 3, true},
	} {
		if got := versionAtLeast(tc.v, tc.major); got != tc.want {
			t.Errorf("versionAtLeast(%q, %d) = %v, want %v", tc.v, tc.major, got, tc.want)
		}
	}
}

// Detection is env-driven so it can be tested without the terminals present.
// Only the inside-checks are portable across machines; the full detect()
// depends on what's installed and is exercised by the live test.
func TestInsideChecks(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "ghostty")
	t.Setenv("GHOSTTY_RESOURCES_DIR", "")
	t.Setenv("LC_TERMINAL", "")
	if !insideGhostty() || insideITerm() {
		t.Error("TERM_PROGRAM=ghostty should read as Ghostty and not iTerm")
	}
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	if insideGhostty() || !insideITerm() {
		t.Error("TERM_PROGRAM=iTerm.app should read as iTerm and not Ghostty")
	}
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	if insideGhostty() || insideITerm() {
		t.Error("Terminal.app is neither")
	}
}

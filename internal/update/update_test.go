package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Version comparison decides whether orbit replaces itself, so its edges
// matter more than the happy path: a wrong answer either strands people on an
// old build or downgrades them on every start.
func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"v0.2.1", "v0.2.0", true},
		{"v0.2.0", "v0.2.1", false},
		{"v0.2.0", "v0.2.0", false}, // equal is not newer, or it would loop
		{"v0.10.0", "v0.9.0", true}, // numeric, not lexical
		{"v1.0.0", "v0.99.99", true},
		{"v0.2.1", "v0.2", true},
		{"0.2.1", "v0.2.0", true}, // a missing v must not change the answer
		// A pre-release is older than the release it precedes, and the
		// release is newer than it — so neither is offered to the other.
		{"v0.3.0-rc1", "v0.3.0", false},
		{"v0.3.0", "v0.3.0-rc1", true},
	} {
		if got := newer(tc.a, tc.b); got != tc.want {
			t.Errorf("newer(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// A build that isn't a release has nothing to compare against, and must never
// be "upgraded" into a published binary — that would replace whatever someone
// is developing with the last release, mid-work.
func TestDevBuildsAreNeverUpdated(t *testing.T) {
	for _, v := range []string{"dev", "", "unknown", "some-branch"} {
		if comparable(v) {
			t.Errorf("comparable(%q) = true; a non-release must not be updated", v)
		}
		if got := Check(v); got != "" {
			t.Errorf("Check(%q) = %q, want no update", v, got)
		}
	}
	for _, v := range []string{"v0.2.1", "0.2.1", "v1.0.0-rc1"} {
		if !comparable(v) {
			t.Errorf("comparable(%q) = false, want true", v)
		}
	}
}

// The Homebrew case is the one that must never be got wrong: overwriting a
// binary brew installed leaves its receipt lying about what's on disk.
func TestDetectClassifiesInstallPaths(t *testing.T) {
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", "/Users/u/go")
	for _, tc := range []struct {
		path string
		want Method
	}{
		{"/opt/homebrew/Caskroom/orbit/0.2.1/orbit", Homebrew},
		{"/usr/local/Caskroom/orbit/0.2.1/orbit", Homebrew},
		{"/opt/homebrew/Cellar/orbit/0.2.1/bin/orbit", Homebrew},
		{"/Users/u/go/bin/orbit", GoInstall},
		{"/usr/local/bin/orbit", Binary},
		{"/Users/u/bin/orbit", Binary},
	} {
		if got := classify(tc.path); got != tc.want {
			t.Errorf("classify(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// Homebrew's own cask index contains an unrelated `orbit` (Orbit for Mac).
// The bare token resolves to that one, so `brew upgrade --cask orbit`
// uninstalls this program and installs a different app in its place —
// observed, not theorised. The tap prefix is the only thing preventing it.
func TestCaskTokenIsFullyQualified(t *testing.T) {
	if !strings.HasPrefix(caskToken, "sadrig91/tap/") {
		t.Fatalf("caskToken = %q; a bare token upgrades the wrong cask", caskToken)
	}
}

// Nothing execs into a binary that hasn't said what it is. This is the last
// guard between a bad install — wrong cask, half-written file, a toolchain
// that quietly built something else — and orbit launching it.
func TestConfirmChecksWhatWasInstalled(t *testing.T) {
	dir := t.TempDir()
	fake := func(name, output string) string {
		path := filepath.Join(dir, name)
		script := "#!/bin/sh\necho '" + output + "'\n"
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}

	if err := confirm(fake("good", "orbit v0.2.2"), "v0.2.2"); err != nil {
		t.Errorf("a correct install was rejected: %v", err)
	}
	// Releases before v0.2.2 print the version without its v.
	if err := confirm(fake("bare", "orbit 0.2.2"), "v0.2.2"); err != nil {
		t.Errorf("the pre-v0.2.2 version spelling was rejected: %v", err)
	}
	// The failure this exists for: a different program under the same name.
	if err := confirm(fake("wrong", "Orbit for Mac 1.1.4"), "v0.2.2"); err == nil {
		t.Error("a different application passed as orbit")
	}
	if err := confirm(fake("stale", "orbit v0.1.2"), "v0.2.2"); err == nil {
		t.Error("an install that didn't take was accepted")
	}
	if err := confirm(filepath.Join(dir, "does-not-exist"), "v0.2.2"); err == nil {
		t.Error("a missing binary was accepted")
	}
}

// An archive nothing verified is an archive nobody should execute.
func TestVerifyRejectsBadChecksums(t *testing.T) {
	archive := []byte("pretend tarball")
	sum := sha256.Sum256(archive)
	name := "orbit_0.2.1_darwin_arm64.tar.gz"
	good := hex.EncodeToString(sum[:]) + "  " + name + "\n"

	if err := verify(archive, []byte(good), name); err != nil {
		t.Errorf("a matching checksum was rejected: %v", err)
	}
	// GoReleaser lists every platform; the right line must be picked out.
	multi := "aaaa  orbit_0.2.1_linux_amd64.tar.gz\n" + good + "bbbb  checksums.txt\n"
	if err := verify(archive, []byte(multi), name); err != nil {
		t.Errorf("failed to find the line among many: %v", err)
	}
	if err := verify([]byte("tampered"), []byte(good), name); err == nil {
		t.Error("a mismatched archive was accepted")
	}
	if err := verify(archive, []byte("aaaa  other.tar.gz\n"), name); err == nil {
		t.Error("a missing checksum line was accepted")
	}
}

func TestExtractFindsTheBinary(t *testing.T) {
	want := []byte("\x7fELF pretend binary")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		body []byte
	}{
		{"README.md", []byte("# orbit")},
		{"orbit", want},
	} {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg})
		tw.Write(f.body)
	}
	tw.Close()
	gz.Close()

	got, err := extract(buf.Bytes())
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("extracted %q, want %q", got, want)
	}

	if _, err := extract([]byte("not a gzip")); err == nil {
		t.Error("garbage was accepted as an archive")
	}
}

// The day-long cache is what keeps a dashboard people reopen all day from
// calling GitHub every time. A fresh cache must answer without the network.
func TestCheckUsesTheCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".cache", "orbit"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(s state) {
		data, _ := json.Marshal(s)
		if err := os.WriteFile(filepath.Join(dir, ".cache", "orbit", "update.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Fresh cache, newer release: offered without touching the network.
	write(state{Checked: time.Now(), Latest: "v9.9.9"})
	if got := Check("v0.2.1"); got != "v9.9.9" {
		t.Errorf("Check = %q, want v9.9.9 from cache", got)
	}
	// Fresh cache, already current: nothing to do.
	write(state{Checked: time.Now(), Latest: "v0.2.1"})
	if got := Check("v0.2.1"); got != "" {
		t.Errorf("Check = %q, want no update when current", got)
	}
	// Running ahead of the last release (a local build of an unreleased
	// version) must not be dragged backwards.
	write(state{Checked: time.Now(), Latest: "v0.2.1"})
	if got := Check("v0.3.0"); got != "" {
		t.Errorf("Check = %q, want no downgrade", got)
	}
}

func TestStateRoundTrips(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if !strings.HasPrefix(statePath(), dir) {
		t.Fatalf("statePath %q escaped the test home", statePath())
	}
	want := state{Checked: time.Now().Truncate(time.Second), Latest: "v0.2.1"}
	saveState(want)
	got := loadState()
	if got.Latest != want.Latest || !got.Checked.Equal(want.Checked) {
		t.Errorf("round-tripped %+v, want %+v", got, want)
	}
	// A missing or corrupt file is normal on a first run, not an error.
	os.Remove(statePath())
	if s := loadState(); s.Latest != "" {
		t.Errorf("missing state read as %+v", s)
	}
	os.WriteFile(statePath(), []byte("{not json"), 0o644)
	if s := loadState(); s.Latest != "" {
		t.Errorf("corrupt state read as %+v", s)
	}
}

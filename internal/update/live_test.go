package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLiveSelfReplace runs the whole self-managed-binary path against the real
// GitHub: fetch the latest release, download the archive for this platform,
// check it against the published checksums, and swap a throwaway binary. It
// hits the network, so it only runs when asked:
//
//	ORBIT_UPDATE_LIVE=1 go test ./internal/update -run TestLive -v
//
// Nothing outside the temp directory is touched — the real orbit on this
// machine is never a subject of this test.
func TestLiveSelfReplace(t *testing.T) {
	if os.Getenv("ORBIT_UPDATE_LIVE") != "1" {
		t.Skip("set ORBIT_UPDATE_LIVE=1 to run the live update test")
	}

	latest, err := fetchLatest()
	if err != nil {
		t.Fatalf("fetchLatest: %v", err)
	}
	t.Logf("latest release: %s", latest)

	// A stand-in for an old orbit, in a directory of its own.
	dir := t.TempDir()
	target := filepath.Join(dir, "orbit")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho orbit v0.0.1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}

	if err := replaceBinary(latest, target); err != nil {
		t.Fatalf("replaceBinary: %v", err)
	}

	after, err := os.Stat(target)
	if err != nil {
		t.Fatalf("the binary is gone after the swap: %v", err)
	}
	if after.Size() == before.Size() {
		t.Error("the file was not replaced")
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Errorf("mode changed: %v -> %v", before.Mode().Perm(), after.Mode().Perm())
	}
	// No leftovers: a failed or partial download must not litter the target
	// directory with temp files.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".orbit-update-") {
			t.Errorf("left a temp file behind: %s", e.Name())
		}
	}

	// The real proof: the swapped-in binary runs and reports the new version.
	out, err := exec.Command(target, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("the replaced binary does not run: %v (%s)", err, out)
	}
	// Releases up to v0.2.1 were built with ldflags that dropped the leading
	// v, so accept either spelling — the same normalisation newer() does.
	got := strings.TrimPrefix(strings.TrimSpace(string(out)), "orbit ")
	if strings.TrimPrefix(got, "v") != strings.TrimPrefix(latest, "v") {
		t.Errorf("replaced binary reports %q, want %q", got, latest)
	}
}

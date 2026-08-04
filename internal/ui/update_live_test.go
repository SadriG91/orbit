package ui

import (
	"os"
	"strings"
	"testing"
)

// TestLiveUpdateCheckWiring runs the real startup check — config gate, the
// update package, GitHub — and asserts the model gets a message it knows how
// to act on. The unit tests cover the sequence after that point with
// synthetic messages; this is the join between them and the real world.
//
//	ORBIT_UPDATE_LIVE=1 go test ./internal/ui -run TestLiveUpdate -v
func TestLiveUpdateCheckWiring(t *testing.T) {
	if os.Getenv("ORBIT_UPDATE_LIVE") != "1" {
		t.Skip("set ORBIT_UPDATE_LIVE=1 to run the live update test")
	}
	// A cache directory of its own, so the machine's real once-a-day state is
	// neither read nor overwritten by the test.
	t.Setenv("HOME", t.TempDir())

	// Pretend to be an ancient build, so a newer release certainly exists.
	m := New(testConfig(), "inline", "v0.1.0")
	cmd := m.updateCheckCmd()
	if cmd == nil {
		t.Fatal("no check scheduled with auto on")
	}
	msg := cmd()
	found, ok := msg.(updateFoundMsg)
	if !ok {
		t.Fatalf("check returned %T, want updateFoundMsg — no update was offered", msg)
	}
	if !strings.HasPrefix(found.version, "v") {
		t.Errorf("offered %q, want a v-prefixed release tag", found.version)
	}
	t.Logf("live check offered %s", found.version)

	// And the model turns that into something the user can see.
	m.Update(found)
	if !m.updating {
		t.Error("the model did not enter the updating state")
	}
	if s := stripANSI(m.footer()); !strings.Contains(s, found.version) {
		t.Errorf("footer does not name the version being installed: %q", s)
	}

	// A current build must be offered nothing, from the same live data.
	current := New(testConfig(), "inline", found.version)
	if cmd := current.updateCheckCmd(); cmd != nil && cmd() != nil {
		t.Errorf("an up-to-date orbit was offered an update: %v", cmd())
	}
}

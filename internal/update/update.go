// Package update keeps orbit current: it asks GitHub for the newest release
// at most once a day, and installs it the way this copy of orbit was
// installed in the first place.
//
// That last part is the whole reason this isn't fifty lines. A binary under
// Homebrew's Caskroom is not orbit's to overwrite — brew records what it put
// there, and a file swapped underneath it leaves the receipt lying, the
// version brew reports wrong, and the change undone by the next upgrade. So
// the installation method is detected and respected: brew upgrades through
// brew, a self-managed binary is replaced in place, and anything orbit isn't
// sure about is left alone.
package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sadrig91/orbit/internal/format"
)

const (
	repo    = "SadriG91/orbit"
	apiBase = "https://api.github.com/repos/" + repo
	dlBase  = "https://github.com/" + repo + "/releases/download"
)

// How long a release check stays good. Once a day keeps orbit — a dashboard
// people reopen all day — from calling GitHub on every launch, and stays far
// inside the unauthenticated rate limit even on a shared address.
const checkEvery = 24 * time.Hour

// Networks hang; a dashboard must not. Both bounds are generous enough for a
// slow connection and short enough that nothing waits on them for long.
const (
	netTimeout  = 30 * time.Second
	brewTimeout = 10 * time.Minute // brew updates its taps first, which is slow
)

// Method is how this copy of orbit was installed, and therefore how it may be
// replaced.
type Method int

const (
	Unknown Method = iota // can't tell, or shouldn't touch it — never updated
	Homebrew
	GoInstall
	Binary // a self-managed file orbit may overwrite
)

func (m Method) String() string {
	switch m {
	case Homebrew:
		return "homebrew"
	case GoInstall:
		return "go install"
	case Binary:
		return "binary"
	}
	return "unknown"
}

// Detect works out how orbit got here, from where its own executable lives.
// Symlinks are resolved first: the Homebrew binary on PATH is a link into the
// Caskroom, and the link is what would otherwise be inspected.
func Detect() (Method, string) {
	exe, err := os.Executable()
	if err != nil {
		return Unknown, ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return classify(exe), exe
}

func classify(exe string) Method {
	switch {
	// Cellar covers a formula install, in case orbit is ever packaged as one.
	case strings.Contains(exe, "/Caskroom/"), strings.Contains(exe, "/Cellar/"):
		return Homebrew
	case underGoBin(exe):
		return GoInstall
	// A binary in a temp directory is `go run`, not an installation.
	case strings.HasPrefix(exe, os.TempDir()):
		return Unknown
	}
	return Binary
}

func underGoBin(exe string) bool {
	dirs := []string{os.Getenv("GOBIN"), filepath.Join(os.Getenv("GOPATH"), "bin"), format.Home("go", "bin")}
	for _, d := range dirs {
		if d != "" && d != "bin" && strings.HasPrefix(exe, d+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// state is the once-a-day memory, in the cache directory rather than the
// config one: it is orbit's bookkeeping, not something to hand-edit.
type state struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest"`
}

func statePath() string { return format.Home(".cache", "orbit", "update.json") }

func loadState() state {
	var s state
	data, err := os.ReadFile(statePath())
	if err != nil {
		return s
	}
	json.Unmarshal(data, &s)
	return s
}

func saveState(s state) {
	path := statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if data, err := json.Marshal(s); err == nil {
		os.WriteFile(path, data, 0o644)
	}
}

// Check reports the newest release when it is newer than current, and ""
// otherwise. It is safe to call on every start: the answer is cached for a
// day, so only the first call of the day touches the network.
//
// Every failure is silent by design — no network, GitHub down, a rate limit,
// a garbled tag. None of that is worth interrupting someone who opened a
// dashboard to look at their sessions.
func Check(current string) string {
	if !comparable(current) {
		return "" // a dev build has no release to be behind
	}
	st := loadState()
	if st.Latest == "" || time.Since(st.Checked) > checkEvery {
		latest, err := fetchLatest()
		if err != nil {
			return ""
		}
		st = state{Checked: time.Now(), Latest: latest}
		saveState(st)
	}
	if newer(st.Latest, current) {
		return st.Latest
	}
	return ""
}

func fetchLatest() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), netTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	// GitHub rejects requests without a User-Agent.
	req.Header.Set("User-Agent", "orbit")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github: %s", resp.Status)
	}
	var out struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", err
	}
	if out.Tag == "" {
		return "", errors.New("no tag in release")
	}
	return out.Tag, nil
}

// comparable rejects versions that aren't a release: "dev" from a plain
// `go build`, or anything without a leading digit once v is stripped.
func comparable(v string) bool {
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return false
	}
	_, err := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
	return err == nil
}

// newer compares two release tags numerically, field by field. A build suffix
// (-rc1, +dirty) makes a version older than the plain one it hangs off, which
// is what keeps a pre-release from being offered as an upgrade to the release
// it precedes.
func newer(a, b string) bool {
	an, apre := parseVersion(a)
	bn, bpre := parseVersion(b)
	for i := 0; i < 3; i++ {
		if an[i] != bn[i] {
			return an[i] > bn[i]
		}
	}
	return !apre && bpre
}

func parseVersion(v string) ([3]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	pre := false
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v, pre = v[:i], true
	}
	var out [3]int
	for i, part := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		out[i], _ = strconv.Atoi(part)
	}
	return out, pre
}

// Apply installs the given release, by whichever route suits the way orbit
// was installed. It returns the path to run afterwards, which is not always
// the path orbit is running from now: brew installs a new Caskroom directory
// and re-points the symlink, so the old path is gone by the time this
// returns.
func Apply(version string) (string, error) {
	method, exe := Detect()
	var err error
	switch method {
	case Homebrew:
		exe, err = brewUpgrade()
	case GoInstall:
		err = goInstall(version)
	case Binary:
		err = replaceBinary(version, exe)
	default:
		return "", errors.New("orbit can't tell how it was installed")
	}
	if err != nil {
		return "", err
	}
	return exe, confirm(exe, version)
}

// confirm asks the new binary what it is before anyone execs into it.
//
// Every install route hands work to something else — brew, the go toolchain,
// a tarball off the internet — and none of them promise that what lands is
// orbit at the version asked for. Homebrew makes the point: its cask index
// has a different `orbit`, and a token that resolves to the wrong one
// replaces this program with an unrelated app that would then be launched in
// its place. One subprocess is cheap next to that.
func confirm(exe, version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), netTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "--version").Output()
	if err != nil {
		return fmt.Errorf("the installed binary does not run: %w", err)
	}
	got := strings.TrimSpace(string(out))
	// Releases before v0.2.2 report the version without its leading v.
	if !strings.HasPrefix(got, "orbit ") ||
		strings.TrimPrefix(strings.TrimPrefix(got, "orbit "), "v") != strings.TrimPrefix(version, "v") {
		return fmt.Errorf("installed %q, expected orbit %s", got, version)
	}
	return nil
}

// caskToken must stay fully qualified. Homebrew's own cask index has an
// `orbit` — "Orbit for Mac", an unrelated app — and the bare token resolves
// to that one: `brew upgrade --cask orbit` on a machine running this orbit
// uninstalls it and installs a Google-accounts browser in its place. The tap
// prefix is the only thing that makes the name mean this project.
const caskToken = "sadrig91/tap/orbit"

// brewUpgrade lets brew do it. HOMEBREW_NO_AUTO_UPDATE is deliberately not
// set: the new version only exists in the tap once brew refreshes it, so the
// tap update is the point rather than overhead.
func brewUpgrade() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), brewTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "brew", "upgrade", "--cask", caskToken)
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_ENV_HINTS=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("brew upgrade: %w: %s", err, lastLine(out))
	}
	// The symlink now points into the new Caskroom directory; the path orbit
	// is running from has been removed.
	path, err := exec.LookPath("orbit")
	if err != nil {
		return "", fmt.Errorf("orbit is gone from PATH after upgrade: %w", err)
	}
	return path, nil
}

func goInstall(version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), brewTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "install", "github.com/sadrig91/orbit/cmd/orbit@"+version)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go install: %w: %s", err, lastLine(out))
	}
	return nil
}

// replaceBinary downloads the release archive for this platform, checks it
// against the published checksums, and swaps the binary.
//
// The new file is written beside the old one and renamed over it, so the
// binary is never half-written: rename within a directory is atomic, and a
// download that dies partway leaves the running orbit untouched.
func replaceBinary(version, exe string) error {
	name := fmt.Sprintf("orbit_%s_%s_%s.tar.gz", strings.TrimPrefix(version, "v"), runtime.GOOS, runtime.GOARCH)
	archive, err := download(dlBase + "/" + version + "/" + name)
	if err != nil {
		return err
	}
	sums, err := download(dlBase + "/" + version + "/checksums.txt")
	if err != nil {
		return err
	}
	if err := verify(archive, sums, name); err != nil {
		return err
	}
	bin, err := extract(archive)
	if err != nil {
		return err
	}

	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".orbit-update-*")
	if err != nil {
		return fmt.Errorf("%s is not writable: %w", dir, err)
	}
	defer os.Remove(tmp.Name()) // no-op once the rename succeeds
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	mode := os.FileMode(0o755)
	if fi, err := os.Stat(exe); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), exe)
}

func download(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), netTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "orbit")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", filepath.Base(url), resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 128<<20))
}

// verify checks the archive against the line for it in checksums.txt. A
// downloaded binary that nothing has checked is one nobody should run.
func verify(archive, sums []byte, name string) error {
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		// GoReleaser writes "<sha256>  <filename>"; some tools prefix a *.
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			want = f[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("no checksum published for %s", name)
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("checksum mismatch for %s", name)
	}
	return nil
}

// extract pulls the orbit binary out of the release tarball.
func extract(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("no orbit binary in the release archive")
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == "orbit" {
			return io.ReadAll(io.LimitReader(tr, 256<<20))
		}
	}
}

func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return lines[len(lines)-1]
}

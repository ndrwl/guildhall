package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureMain is a stand-in guildhall that answers version --json with what go build stamped into it, in the shape
// release install's probe reads.
const fixtureMain = `package main

import (
	"encoding/json"
	"os"
	"runtime/debug"
)

func main() {
	info, _ := debug.ReadBuildInfo()
	r := map[string]any{"version": info.Main.Version, "store_schema": 1, "release_contract": 1, "modified": false}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			r["revision"] = s.Value
		case "vcs.modified":
			r["modified"] = s.Value == "true"
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(r)
}
`

// gitFixture runs git in dir with a fixed identity and commit time, and no signing, and returns its trimmed output.
func gitFixture(t *testing.T, dir string, at time.Time, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com",
		"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	date := at.Format(time.RFC3339)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestReleaseInstallBuildsFromSource builds a fixture module from a git repository through the real git and go,
// settling what a source build reports: a main commit builds as v0.0.0-<committer time in UTC>-<commit[:12]> with
// the full revision and modified false, and a tagged commit as its tag (d149 R1).
func TestReleaseInstallBuildsFromSource(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git command not on PATH")
	}
	bin, _ := buildRelease(t, t.TempDir(), "v0.0.0-release-test")
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	source := filepath.Join(tmp, "source")
	if err := os.MkdirAll(filepath.Join(source, "cmd", "guildhall"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.com/fixture\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "cmd", "guildhall", "main.go"), []byte(fixtureMain), 0o644); err != nil {
		t.Fatal(err)
	}
	// The commit time is in another zone, so the pseudo-version's UTC time differs from its local one.
	at := time.Date(2026, 11, 1, 12, 30, 45, 0, time.FixedZone("UTC+2", 2*60*60))
	gitFixture(t, source, at, "init", "--quiet")
	gitFixture(t, source, at, "add", "-A")
	gitFixture(t, source, at, "commit", "--quiet", "-m", "fixture")
	commit := gitFixture(t, source, at, "rev-parse", "HEAD")

	run := func(args ...string) (string, error) {
		cmd := binCommand(t, bin, args...)
		cmd.Env = append(os.Environ(), "GUILDHALL_HOME="+home)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	report := func(version string) map[string]any {
		t.Helper()
		out, err := binCommand(t, filepath.Join(home, "releases", version, filepath.Base(bin)), "version", "--json").Output()
		var r map[string]any
		if err != nil || json.Unmarshal(out, &r) != nil {
			t.Fatalf("installed %s's version --json = %q, %v", version, out, err)
		}
		return r
	}

	pseudo := "v0.0.0-20261101103045-" + commit[:12]
	out, err := run("release", "install", pseudo, "--source", source)
	if err != nil || !strings.HasPrefix(out, "building "+pseudo+" from "+source+" at "+commit+" with go") ||
		!strings.HasSuffix(out, ", built from "+source+" at "+commit+"\n") {
		t.Fatalf("release install %s = %q, %v", pseudo, out, err)
	}
	if r := report(pseudo); r["version"] != pseudo || r["revision"] != commit || r["modified"] != false {
		t.Errorf("built %s reports %v, want its version, revision %s and modified false", pseudo, r, commit)
	}

	// A commit the source lacks refuses, naming the source, and leaves nothing behind.
	missing := "v0.0.0-20261101103045-0123456789ab"
	if out, err := run("release", "install", missing, "--source", source); err == nil || !strings.Contains(out, "commit 0123456789ab is not in "+source) {
		t.Errorf("release install of a missing commit = %q, %v; want a refusal", out, err)
	}
	if _, err := os.Stat(filepath.Join(home, "releases", missing)); err == nil {
		t.Errorf("release %s was installed", missing)
	}
	if left, _ := os.ReadDir(filepath.Join(home, "releases", ".incoming")); len(left) > 0 {
		t.Errorf(".incoming holds %v", left)
	}

	// A tagged commit builds as its tag.
	if err := os.WriteFile(filepath.Join(source, "NOTES"), []byte("tagged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, source, at.Add(time.Hour), "add", "-A")
	gitFixture(t, source, at.Add(time.Hour), "commit", "--quiet", "-m", "tagged")
	gitFixture(t, source, at.Add(time.Hour), "tag", "v1.2.3")
	tagged := gitFixture(t, source, at, "rev-parse", "HEAD")
	if out, err := run("release", "install", "v1.2.3", "--source", source); err != nil || !strings.HasSuffix(out, ", built from "+source+" at "+tagged+"\n") {
		t.Fatalf("release install v1.2.3 = %q, %v", out, err)
	}
	if r := report("v1.2.3"); r["version"] != "v1.2.3" || r["revision"] != tagged || r["modified"] != false {
		t.Errorf("built v1.2.3 reports %v, want revision %s", r, tagged)
	}
}

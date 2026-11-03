package releasetest

import (
	"context"
	"debug/buildinfo"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/release"
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

// FixtureTag is the tag of Fixture's tagged commit.
const FixtureTag = "v1.2.3"

// Fixture makes a temporary git repository holding a Go module whose ./cmd/guildhall prints its build info as
// `version --json`: an untagged commit, then a tagged child, FixtureTag, each with a fixed identity and commit
// time and no signing. It returns the repository and the two commits, the untagged one with its pseudo-version.
// Its own git runs drop GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE, so it is safe to call with them set.
func Fixture(t *testing.T) (source string, tagged, untagged Commit) {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source = filepath.Join(tmp, "source")
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/fixture\n\ngo 1.24\n")
	write("cmd/guildhall/main.go", fixtureMain)
	// The commit time is in another zone, so the pseudo-version's UTC time differs from its local one.
	at := time.Date(2026, 11, 1, 12, 30, 45, 0, time.FixedZone("UTC+2", 2*60*60))
	git(t, source, at, "init", "--quiet")
	git(t, source, at, "add", "-A")
	git(t, source, at, "commit", "--quiet", "-m", "fixture")
	untagged.ID = git(t, source, at, "rev-parse", "HEAD")
	untagged.Version = "v0.0.0-" + at.UTC().Format("20060102150405") + "-" + untagged.ID[:12]

	write("NOTES", "tagged\n")
	later := at.Add(time.Hour)
	git(t, source, later, "add", "-A")
	git(t, source, later, "commit", "--quiet", "-m", "tagged")
	git(t, source, later, "tag", FixtureTag)
	tagged = Commit{ID: git(t, source, later, "rev-parse", "HEAD"), Version: FixtureTag}
	return source, tagged, untagged
}

// git runs git in dir with a fixed identity and commit time, no signing and none of the variables that redirect it
// to another repository, and returns its trimmed output.
func git(t *testing.T, dir string, at time.Time, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com",
		"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	date := at.Format(time.RFC3339)
	cmd.Env = append(GitEnv(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// GitEnv is the process's environment without GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE, matched
// case-insensitively, for a test's own git runs.
func GitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE":
		default:
			env = append(env, kv)
		}
	}
	return env
}

// BuildInfoProbe is the release.Probe of a binary go built: the version and revision stamped into it and whether
// it was modified, read with debug/buildinfo rather than run, with the store schema and release contract 1 that
// the fixture's binary claims.
func BuildInfoProbe(_ context.Context, path string) (release.Report, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return release.Report{}, err
	}
	r := release.Report{Version: info.Main.Version, StoreSchema: 1, ReleaseContract: 1}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			r.Revision = s.Value
		case "vcs.modified":
			r.Modified = s.Value == "true"
		}
	}
	return r, nil
}

package release_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/release/releasetest"
)

func TestFakeBuilderContract(t *testing.T) {
	releasetest.RunBuilderContract(t, releasetest.NewFakeHarness)
}

// execHarness is release.ExecBuilder under the contract, over a fixture source read by BuildInfoProbe; it skips
// the test when git or go is not on PATH.
func execHarness(t *testing.T) releasetest.Harness {
	t.Helper()
	for _, tool := range []string{"git", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s command not on PATH", tool)
		}
	}
	source, tagged, untagged := releasetest.Fixture(t)
	return releasetest.Harness{Builder: release.ExecBuilder{}, Probe: releasetest.BuildInfoProbe, Source: source,
		Tagged: tagged, Untagged: untagged, Missing: releasetest.AbsentCommit}
}

// TestExecBuilderContract runs the contract against the real git and go on a fixture repository.
func TestExecBuilderContract(t *testing.T) {
	releasetest.RunBuilderContract(t, execHarness)
}

// TestExecBuilderIgnoresAnInheritedGitDir: with GIT_DIR naming another repository, ExecBuilder still builds from
// its source, and the other repository's refs, HEAD, objects and files are unchanged.
func TestExecBuilderIgnoresAnInheritedGitDir(t *testing.T) {
	h := execHarness(t)
	other := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "README"), []byte("another repository\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", other, "-c", "user.name=Other", "-c", "user.email=other@example.com",
			"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Env = releasetest.GitEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %q: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet")
	git("add", "-A")
	git("commit", "--quiet", "-m", "other")
	state := func() []string {
		t.Helper()
		var files []string
		for _, dir := range []string{other, filepath.Join(other, ".git")} {
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
		return append([]string{git("for-each-ref"), git("rev-parse", "HEAD"), git("symbolic-ref", "HEAD"), git("count-objects", "-v")}, files...)
	}
	before := state()

	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))
	home := release.Home{Dir: filepath.Join(t.TempDir(), "home"), GOOS: runtime.GOOS}
	res, err := home.Build(context.Background(), release.BuildRequest{Version: h.Tagged.Version, Source: h.Source, Builder: h.Builder,
		Probe: h.Probe, Now: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC), LockWait: 10 * time.Second})
	if err != nil || res.Release.Commit != h.Tagged.ID {
		t.Fatalf("Build(%s) with GIT_DIR set = %+v, %v; want it built from %s at %s", h.Tagged.Version, res, err, h.Source, h.Tagged.ID)
	}
	if r, err := h.Probe(context.Background(), home.Binary(h.Tagged.Version)); err != nil || r.Revision != h.Tagged.ID || r.Version != h.Tagged.Version {
		t.Fatalf("built %s reports %+v, %v; want revision %s", h.Tagged.Version, r, err, h.Tagged.ID)
	}
	if after := state(); !slices.Equal(before, after) {
		t.Fatalf("the repository GIT_DIR names changed:\nbefore %q\nafter  %q", before, after)
	}
}

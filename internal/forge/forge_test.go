package forge_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
)

// fakeGHEnv names the state file of the fake gh this test binary acts as when it is set.
const fakeGHEnv = "GUILDHALL_TEST_FAKE_GH"

// fakeGHRemoteEnv names the bare repository the fake gh reads branch heads from.
const fakeGHRemoteEnv = "GUILDHALL_TEST_FAKE_GH_REMOTE"

func TestMain(m *testing.M) {
	if state := os.Getenv(fakeGHEnv); state != "" {
		os.Exit(fakeGH(state, os.Getenv(fakeGHRemoteEnv), os.Args[1:]))
	}
	code := m.Run()
	if fixture.dir != "" {
		_ = os.RemoveAll(fixture.dir)
	}
	os.Exit(code)
}

// fixture is the repository and bare remote newGitHub builds with real git once per test binary and copies for
// each test, with the commits it made.
var fixture struct {
	mu               sync.Mutex
	dir              string
	base, h0, h1, h2 string
}

// remote is the GitHub remote every test repository is configured with; git maps it to a local bare repository.
const remote = "https://github.com/acme/widgets.git"

func TestForgeContract(t *testing.T) {
	t.Run("fake", func(t *testing.T) { forgetest.RunForgeContract(t, forgetest.NewFakeHarness) })
	t.Run("github", func(t *testing.T) {
		forgetest.RunForgeContract(t, func(t *testing.T) forgetest.Harness { return newGitHub(t).harness })
	})
}

// gh is one GitHub under test: real git against a local bare remote, and this test binary as gh.
type gh struct {
	harness       forgetest.Harness
	state, global string
	bare          string
}

func newGitHub(t *testing.T) *gh {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	g := &gh{state: filepath.Join(dir, "gh-state.json"), global: filepath.Join(dir, "gitconfig"), bare: filepath.Join(dir, "remote.git")}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv(fakeGHEnv, g.state)
	t.Setenv(fakeGHRemoteEnv, g.bare)
	base, h0, h1, h2 := copyFixture(t, dir)
	if err := writeGitConfig(g.global, g.bare); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", g.global)
	work := filepath.Join(dir, "work")
	g.writeState(t, ghState{})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	g.harness = forgetest.Harness{
		Forge:      forge.GitHub{GH: exe},
		Repository: work, Remote: remote, Base: "main", BaseHead: base, Heads: [3]string{h0, h1, h2},
		Branch: func(t *testing.T, name string) string { return remoteBranch(g.bare, name) },
		Refuse: func(t *testing.T, reason string) {
			s := g.readState(t)
			s.Refuse = reason
			g.writeState(t, s)
		},
		SetChecks: func(t *testing.T, head string, checks []forge.Check) {
			var nodes []map[string]any
			for _, c := range checks {
				node := map[string]any{"__typename": "CheckRun", "id": c.ID, "name": c.Name, "status": "COMPLETED", "conclusion": "SUCCESS", "detailsUrl": c.URL}
				if c.Job != "" {
					job, err := strconv.ParseInt(c.Job, 10, 64)
					if err != nil {
						t.Fatalf("check %q: job %q is not an Actions job id", c.Name, c.Job)
					}
					node["databaseId"], node["checkSuite"] = job, map[string]any{"app": map[string]any{"slug": "github-actions"}}
				}
				switch c.State {
				case forge.CheckPending:
					node["status"], node["conclusion"] = "IN_PROGRESS", nil
				case forge.CheckFailed:
					node["conclusion"] = "FAILURE"
				}
				nodes = append(nodes, node)
			}
			g.setChecks(t, head, nodes)
		},
		Text: func(t *testing.T, number int) (string, string) {
			for _, p := range g.readState(t).PRs {
				if p.Number == number {
					return p.Title, p.Body
				}
			}
			return "", ""
		},
		Edits: func(t *testing.T) int {
			n := 0
			for _, c := range g.readState(t).Calls {
				if len(c) > 1 && c[0] == "pr" && c[1] == "edit" {
					n++
				}
			}
			return n
		},
	}
	return g
}

// writeGitConfig writes the global git configuration that maps remote to the bare repository bare.
func writeGitConfig(path, bare string) error {
	url := filepath.ToSlash(bare)
	if !strings.HasPrefix(url, "/") {
		url = "/" + url
	}
	config := fmt.Sprintf("[url \"file://%s\"]\n\tinsteadOf = %s\n[user]\n\tname = t\n\temail = t@example.com\n[commit]\n\tgpgsign = false\n[init]\n\tdefaultBranch = main\n", url, remote)
	return os.WriteFile(path, []byte(config), 0o644)
}

// copyFixture copies the fixture's work repository and bare remote into dir, building the fixture with real git
// on first use: main at base and then h0 and h1, with only base pushed, and h2 a rewrite of h0 on base.
func copyFixture(t *testing.T, dir string) (base, h0, h1, h2 string) {
	t.Helper()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.dir == "" {
		root, err := os.MkdirTemp("", "forge-fixture")
		if err != nil {
			t.Fatal(err)
		}
		g := &gh{global: filepath.Join(root, "gitconfig"), bare: filepath.Join(root, "remote.git")}
		if err := writeGitConfig(g.global, g.bare); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_CONFIG_GLOBAL", g.global)
		g.git(t, "", "init", "-q", "--bare", g.bare)
		work := filepath.Join(root, "work")
		g.git(t, "", "init", "-q", work)
		commit := func(name string) string {
			if err := os.WriteFile(filepath.Join(work, name), []byte(name+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			g.git(t, work, "add", name)
			g.git(t, work, "commit", "-q", "-m", name)
			return g.git(t, work, "rev-parse", "HEAD")
		}
		fixture.base = commit("base")
		g.git(t, work, "push", "-q", remote, "main")
		fixture.h0 = commit("one")
		fixture.h1 = commit("two")
		g.git(t, work, "checkout", "-q", "--detach", fixture.base)
		fixture.h2 = commit("rewrite")
		g.git(t, work, "checkout", "-q", "main")
		fixture.dir = root
	}
	for _, name := range []string{"work", "remote.git"} {
		if err := os.CopyFS(filepath.Join(dir, name), os.DirFS(filepath.Join(fixture.dir, name))); err != nil {
			t.Fatalf("copy the fixture's %s: %v", name, err)
		}
	}
	return fixture.base, fixture.h0, fixture.h1, fixture.h2
}

// setChecks makes nodes, in GitHub's statusCheckRollup context form, the checks the fake gh reports for head.
func (g *gh) setChecks(t *testing.T, head string, nodes []map[string]any) {
	t.Helper()
	s := g.readState(t)
	if s.Checks == nil {
		s.Checks = map[string][]map[string]any{}
	}
	s.Checks[head] = nodes
	g.writeState(t, s)
}

func (g *gh) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (g *gh) readState(t *testing.T) ghState {
	t.Helper()
	s, err := loadState(g.state)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (g *gh) writeState(t *testing.T, s ghState) {
	t.Helper()
	if err := s.save(g.state); err != nil {
		t.Fatal(err)
	}
}

// TestGitHubArguments pins what the GitHub forge asks gh for: the repository named from the remote, a pull
// request against the default branch, and a squash merge bound to the head, without --admin or --auto.
func TestGitHubArguments(t *testing.T) {
	g := newGitHub(t)
	h := g.harness
	ctx := context.Background()
	pr, err := h.Forge.Publish(ctx, forge.Change{Repository: h.Repository, Remote: remote, Branch: "guildhall/t7", Head: h.Heads[0], Title: "Task t7", Body: "Body text"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Forge.Publish(ctx, forge.Change{Repository: h.Repository, Remote: remote, Branch: "guildhall/t7", Head: h.Heads[0], Title: "Task t7 again", Body: "Body text again"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Forge.Checks(ctx, forge.Commit{Repository: h.Repository, Remote: remote, Head: h.Heads[0]}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Forge.Merge(ctx, forge.Merge{Repository: h.Repository, Remote: remote, Number: pr.Number, Head: h.Heads[0], Title: "Task t7 again"}); err != nil {
		t.Fatal(err)
	}
	s := g.readState(t)
	s.Logs = map[string]string{"123": "log\n"}
	g.writeState(t, s)
	if _, err := h.Forge.(forge.GitHub).CheckLog(ctx, forge.Commit{Repository: h.Repository, Remote: remote, Head: h.Heads[0]}, "123", 1<<20); err != nil {
		t.Fatal(err)
	}
	calls := g.readState(t).Calls
	logs := []string{"api", "repos/acme/widgets/actions/jobs/123/logs", "--hostname", "github.com", "--allow-escape-sequences"}
	if !slices.ContainsFunc(calls, func(c []string) bool { return slices.Equal(c, logs) }) {
		t.Errorf("no gh %q call reads the job log: %v", logs, calls)
	}
	t.Run("installed-gh-flags", func(t *testing.T) {
		for _, c := range calls {
			checkInstalledGHFlags(t, c)
		}
	})
	for _, c := range calls {
		t.Logf("gh %q", c)
		if slices.Equal(c, logs) {
			continue
		}
		if len(c) > 1 && c[0] == "api" {
			if !slices.Equal(flagValues(c, "--hostname"), []string{"github.com"}) || !slices.Contains(c, "owner=acme") || !slices.Contains(c, "name=widgets") ||
				!slices.Contains(c, "oid="+h.Heads[0]) {
				t.Errorf("gh %q does not name github.com, acme/widgets and the head", c)
			}
		} else if c[0] == "repo" {
			if len(c) != 5 || c[2] != "github.com/acme/widgets" || slices.Contains(c, "--repo") {
				t.Errorf("gh %q does not name its repository positionally", c)
			}
		} else if i := slices.Index(c, "--repo"); i < 0 || i+1 >= len(c) || c[i+1] != "github.com/acme/widgets" {
			t.Errorf("gh %q does not name --repo github.com/acme/widgets", c)
		}
		if slices.Contains(c, "--admin") || slices.Contains(c, "--auto") || slices.Contains(c, "--base") {
			t.Errorf("gh %q passes a bypass or a base", c)
		}
	}
	create := slices.IndexFunc(calls, func(c []string) bool { return len(c) > 1 && c[0] == "pr" && c[1] == "create" })
	merge := slices.IndexFunc(calls, func(c []string) bool { return len(c) > 1 && c[0] == "pr" && c[1] == "merge" })
	if create < 0 || !slices.Equal(flagValues(calls[create], "--head", "--title", "--body"), []string{"guildhall/t7", "Task t7", "Body text"}) {
		t.Errorf("pr create = %v", calls)
	}
	edit := slices.IndexFunc(calls, func(c []string) bool { return len(c) > 1 && c[0] == "pr" && c[1] == "edit" })
	if edit < 0 || calls[edit][2] != strconv.Itoa(pr.Number) || !slices.Equal(flagValues(calls[edit], "--title", "--body"), []string{"Task t7 again", "Body text again"}) {
		t.Errorf("pr edit = %v", calls)
	}
	if merge < 0 || !slices.Contains(calls[merge], "--squash") ||
		!slices.Equal(flagValues(calls[merge], "--match-head-commit", "--subject", "--body"), []string{h.Heads[0], "Task t7 again (#1)", ""}) {
		t.Errorf("pr merge = %v", calls)
	}
	// The subject is the published title plus (#N) and the body is explicitly empty, so GitHub's default
	// squash message is never used.
	if got := g.readState(t).PRs[0]; got.MergeSubject != "Task t7 again (#1)" || got.MergeBody != "" {
		t.Errorf("merged with subject %q and body %q, want %q and an empty body", got.MergeSubject, got.MergeBody, "Task t7 again (#1)")
	}
	if !slices.ContainsFunc(calls, func(c []string) bool { return len(c) > 1 && c[0] == "api" && c[1] == "graphql" }) {
		t.Errorf("no api graphql call reads the checks: %v", calls)
	}
}

// TestGitHubCloseArguments pins what closing a cancelled task's pull request asks gh for: pr close of the number
// on the repository named from the remote, with the comment, and never --delete-branch, so the branch is kept.
func TestGitHubCloseArguments(t *testing.T) {
	g := newGitHub(t)
	h := g.harness
	ctx := context.Background()
	pr, err := h.Forge.Publish(ctx, forge.Change{Repository: h.Repository, Remote: remote, Branch: "guildhall/t7", Head: h.Heads[0], Title: "Task t7", Body: "Body text"})
	if err != nil {
		t.Fatal(err)
	}
	const comment = "Cancelled by Guildhall: duplicate of t3"
	if _, err := h.Forge.Close(ctx, forge.Close{Repository: h.Repository, Remote: remote, Number: pr.Number, Comment: comment}); err != nil {
		t.Fatal(err)
	}
	s := g.readState(t)
	closes := slices.DeleteFunc(slices.Clone(s.Calls), func(c []string) bool { return len(c) < 2 || c[0] != "pr" || c[1] != "close" })
	if len(closes) != 1 {
		t.Fatalf("pr close calls = %q, want one", closes)
	}
	c := closes[0]
	t.Logf("gh %q", c)
	t.Run("installed-gh-flags", func(t *testing.T) { checkInstalledGHFlags(t, c) })
	if c[2] != strconv.Itoa(pr.Number) || !slices.Equal(flagValues(c, "--repo", "--comment"), []string{"github.com/acme/widgets", comment}) {
		t.Errorf("pr close = %q, want number %d, --repo and --comment", c, pr.Number)
	}
	if slices.Contains(c, "--delete-branch") || slices.Contains(c, "-d") {
		t.Errorf("pr close %q deletes the branch", c)
	}
	if got := s.PRs[0].CloseComment; got != comment {
		t.Errorf("close comment = %q, want %q", got, comment)
	}
	if _, err := h.Forge.Close(ctx, forge.Close{Repository: h.Repository, Remote: remote, Number: pr.Number, Comment: comment}); err != nil {
		t.Fatal(err)
	}
	if again := g.readState(t).Calls; slices.ContainsFunc(again[len(s.Calls):], func(c []string) bool { return len(c) > 1 && c[1] == "close" }) {
		t.Errorf("a repeated close ran pr close again: %q", again[len(s.Calls):])
	}
}

// TestGitHubCheckStates: every check run status and conclusion and every commit status state GitHub reports
// maps to pending, passed or failed, a commit status is named by its context, and the checks are sorted.
func TestGitHubCheckStates(t *testing.T) {
	g := newGitHub(t)
	h := g.harness
	ctx := context.Background()
	if _, err := h.Forge.Publish(ctx, forge.Change{Repository: h.Repository, Remote: remote, Branch: "guildhall/t7", Head: h.Heads[0], Title: "Task t7"}); err != nil {
		t.Fatal(err)
	}
	jobs := 0
	run := func(name, status string, conclusion any) map[string]any {
		jobs++
		return map[string]any{"__typename": "CheckRun", "id": "CR_" + name, "databaseId": 1000 + jobs, "name": name, "status": status, "conclusion": conclusion,
			"detailsUrl": "https://ci.invalid/" + name, "checkSuite": map[string]any{"app": map[string]any{"slug": "github-actions"}}}
	}
	status := func(context, state string) map[string]any {
		return map[string]any{"__typename": "StatusContext", "id": "SC_" + context, "context": context, "state": state, "targetUrl": nil}
	}
	nodes := []map[string]any{
		run("queued", "QUEUED", nil), run("in-progress", "IN_PROGRESS", nil), run("waiting", "WAITING", nil),
		run("pending", "PENDING", nil), run("requested", "REQUESTED", nil),
		run("success", "COMPLETED", "SUCCESS"), run("neutral", "COMPLETED", "NEUTRAL"), run("skipped", "COMPLETED", "SKIPPED"),
		run("failure", "COMPLETED", "FAILURE"), run("cancelled", "COMPLETED", "CANCELLED"), run("timed-out", "COMPLETED", "TIMED_OUT"),
		run("action-required", "COMPLETED", "ACTION_REQUIRED"), run("stale", "COMPLETED", "STALE"), run("startup-failure", "COMPLETED", "STARTUP_FAILURE"),
		status("s-expected", "EXPECTED"), status("s-pending", "PENDING"), status("s-success", "SUCCESS"), status("s-failure", "FAILURE"), status("s-error", "ERROR"),
	}
	g.setChecks(t, h.Heads[0], nodes)
	got, err := h.Forge.Checks(ctx, forge.Commit{Repository: h.Repository, Remote: remote, Head: h.Heads[0]})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]forge.CheckState{
		"queued": forge.CheckPending, "in-progress": forge.CheckPending, "waiting": forge.CheckPending, "pending": forge.CheckPending, "requested": forge.CheckPending,
		"success": forge.CheckPassed, "neutral": forge.CheckPassed, "skipped": forge.CheckPassed,
		"failure": forge.CheckFailed, "cancelled": forge.CheckFailed, "timed-out": forge.CheckFailed, "action-required": forge.CheckFailed,
		"stale": forge.CheckFailed, "startup-failure": forge.CheckFailed,
		"s-expected": forge.CheckPending, "s-pending": forge.CheckPending, "s-success": forge.CheckPassed, "s-failure": forge.CheckFailed, "s-error": forge.CheckFailed,
	}
	if len(got) != len(want) || !slices.IsSortedFunc(got, func(a, b forge.Check) int { return strings.Compare(a.Name, b.Name) }) {
		t.Errorf("Checks = %+v, want %d checks sorted by name", got, len(want))
	}
	for _, c := range got {
		if c.State != want[c.Name] {
			t.Errorf("check %s is %s, want %s", c.Name, c.State, want[c.Name])
		}
		id := "CR_" + c.Name
		if strings.HasPrefix(c.Name, "s-") {
			id = "SC_" + c.Name
		}
		if c.ID != id {
			t.Errorf("check %s has id %q, want %q", c.Name, c.ID, id)
		}
		if strings.HasPrefix(c.Name, "s-") != (c.Job == "") {
			t.Errorf("check %s has job %q; only a GitHub Actions check run has one", c.Name, c.Job)
		}
		if strings.HasPrefix(c.Name, "s-") != (c.URL == "") || !strings.HasPrefix(c.Name, "s-") && c.URL != "https://ci.invalid/"+c.Name {
			t.Errorf("check %s has URL %q", c.Name, c.URL)
		}
	}

	// Only a GitHub Actions check run has a job, and the job is not part of a check's JSON.
	other, bare := run("other-app", "COMPLETED", "FAILURE"), run("no-suite", "COMPLETED", "FAILURE")
	other["checkSuite"] = map[string]any{"app": map[string]any{"slug": "circleci-checks"}}
	delete(bare, "checkSuite")
	actions := run("actions", "COMPLETED", "FAILURE")
	g.setChecks(t, h.Heads[0], []map[string]any{other, bare, actions})
	got, err = h.Forge.Checks(ctx, forge.Commit{Repository: h.Repository, Remote: remote, Head: h.Heads[0]})
	if err != nil {
		t.Fatal(err)
	}
	jobOf := map[string]string{}
	for _, c := range got {
		jobOf[c.Name] = c.Job
	}
	if want := map[string]string{"actions": strconv.Itoa(1000 + jobs), "no-suite": "", "other-app": ""}; !maps.Equal(jobOf, want) {
		t.Errorf("jobs = %v, want %v", jobOf, want)
	}
	if data, err := json.Marshal(got[0]); err != nil || strings.Contains(string(data), "job") || strings.Contains(string(data), "100") {
		t.Errorf("check JSON = %s, %v; want no job", data, err)
	}

	// A status GitHub adds later would be read wrongly, so an unknown state or kind fails the read.
	for _, bad := range []map[string]any{run("new", "COMPLETED", "SOMETHING_NEW"), run("new", "LATER", nil), status("new", "LATER"), {"__typename": "Other"}} {
		g.setChecks(t, h.Heads[0], []map[string]any{bad})
		if got, err := h.Forge.Checks(ctx, forge.Commit{Repository: h.Repository, Remote: remote, Head: h.Heads[0]}); err == nil || errors.Is(err, forge.ErrUnknownHead) {
			t.Errorf("Checks with %v = %+v, %v, want a plain error", bad, got, err)
		}
	}

	// More checks than one page holds fail the read rather than leaving some out.
	s := g.readState(t)
	s.Checks[h.Heads[0]], s.MoreChecks = nodes[:1], true
	g.writeState(t, s)
	if got, err := h.Forge.Checks(ctx, forge.Commit{Repository: h.Repository, Remote: remote, Head: h.Heads[0]}); err == nil {
		t.Errorf("Checks with another page = %+v, want an error", got)
	}
}

// TestGitHubMergeSignedOut: a merge gh cannot make because it is not signed in (gh's exit 4) never reached the
// forge, so it is a plain error and not ErrRefused.
func TestGitHubMergeSignedOut(t *testing.T) {
	g := newGitHub(t)
	h := g.harness
	ctx := context.Background()
	pr, err := h.Forge.Publish(ctx, forge.Change{Repository: h.Repository, Remote: remote, Branch: "guildhall/t7", Head: h.Heads[0], Title: "Task t7"})
	if err != nil {
		t.Fatal(err)
	}
	s := g.readState(t)
	s.SignedOut = true
	g.writeState(t, s)
	_, err = h.Forge.Merge(ctx, forge.Merge{Repository: h.Repository, Remote: remote, Number: pr.Number, Head: h.Heads[0], Title: "Task t7"})
	if err == nil || errors.Is(err, forge.ErrRefused) || !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("Merge signed out = %v, want a plain error carrying gh's message", err)
	}
	if got := remoteBranch(g.bare, "main"); got != h.BaseHead {
		t.Errorf("main = %s, want it unmoved at %s", got, h.BaseHead)
	}
}

// TestGitHubMergeUnreachable: a merge gh fails whose outcome a view cannot then learn, as when the connection
// drops, is a plain error and not ErrRefused.
func TestGitHubMergeUnreachable(t *testing.T) {
	g := newGitHub(t)
	h := g.harness
	ctx := context.Background()
	pr, err := h.Forge.Publish(ctx, forge.Change{Repository: h.Repository, Remote: remote, Branch: "guildhall/t7", Head: h.Heads[0], Title: "Task t7"})
	if err != nil {
		t.Fatal(err)
	}
	s := g.readState(t)
	s.DropMerge = true
	g.writeState(t, s)
	_, err = h.Forge.Merge(ctx, forge.Merge{Repository: h.Repository, Remote: remote, Number: pr.Number, Head: h.Heads[0], Title: "Task t7"})
	if err == nil || errors.Is(err, forge.ErrRefused) || !strings.Contains(err.Error(), "error connecting") {
		t.Errorf("Merge unreachable = %v, want a plain error carrying gh's message", err)
	}
	calls := g.readState(t).Calls
	if last := calls[len(calls)-1]; len(last) < 2 || last[0] != "pr" || last[1] != "view" {
		t.Errorf("last gh call = %q, want the view checking the failed merge", last)
	}
}

// TestGitHubMergeTimeout: a gh run that outlasts the forge's timeout is ended and is a plain error naming gh
// and the timeout, never ErrRefused, and nothing is merged. The timeout applies to each gh run, so the pr view
// before the merge must also finish within it; the test tries a ladder of rising timeouts and asserts on the
// first rung where that view answered, so a slow runner moves up the ladder instead of failing.
func TestGitHubMergeTimeout(t *testing.T) {
	g := newGitHub(t)
	h := g.harness
	ctx := context.Background()
	pr, err := h.Forge.Publish(ctx, forge.Change{Repository: h.Repository, Remote: remote, Branch: "guildhall/t7", Head: h.Heads[0], Title: "Task t7"})
	if err != nil {
		t.Fatal(err)
	}
	s := g.readState(t)
	s.HangMerge = true
	g.writeState(t, s)
	f := h.Forge.(forge.GitHub)
	// timing:allow the shortest rung deliberately expires a gh run that never answers (HangMerge); a rung whose
	// preceding pr view misses its budget changes nothing and is retried at a larger rung; the last rung is policy-sized.
	rungs := []time.Duration{500 * time.Millisecond, 2 * time.Second, 10 * time.Second}
	missed := true
	for _, f.Timeout = range rungs {
		calls := len(g.readState(t).Calls)
		began := time.Now()
		_, err = f.Merge(ctx, forge.Merge{Repository: h.Repository, Remote: remote, Number: pr.Number, Head: h.Heads[0], Title: "Task t7"})
		if took := time.Since(began); took > f.Timeout+30*time.Second {
			t.Fatalf("Merge took %v past a %v timeout", took, f.Timeout)
		}
		merged := slices.ContainsFunc(g.readState(t).Calls[calls:], func(c []string) bool { return len(c) > 1 && c[0] == "pr" && c[1] == "merge" })
		if err == nil || merged || !strings.Contains(err.Error(), f.GH+" pr view") || !strings.Contains(err.Error(), "no answer within") {
			missed = false
			break
		}
	}
	if missed {
		t.Fatalf("pr view never finished within the largest rung, %v: %v", f.Timeout, err)
	}
	if err == nil || errors.Is(err, forge.ErrRefused) || !strings.Contains(err.Error(), f.GH+" pr merge") || !strings.Contains(err.Error(), f.Timeout.String()) {
		t.Errorf("Merge hung = %v, want a plain error naming gh and the %v timeout", err, f.Timeout)
	}
	if head := remoteBranch(g.bare, "main"); head != h.BaseHead {
		t.Errorf("main = %s, want it unmoved at %s", head, h.BaseHead)
	}
}

func flagValues(args []string, flags ...string) []string {
	var out []string
	for _, f := range flags {
		if i := slices.Index(args, f); i >= 0 && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

// TestGitHubRemotes: only github.com remotes in the URL forms git uses are accepted; anything else is refused
// with ErrUnsupportedRemote before git or gh runs.
func TestGitHubRemotes(t *testing.T) {
	g := newGitHub(t)
	h := g.harness
	for _, r := range []string{"https://gitlab.com/acme/widgets.git", "/srv/git/widgets.git", "https://github.com/acme", "https://github.com/acme/widgets/extra", "ftp://github.com/acme/widgets"} {
		_, err := h.Forge.Publish(context.Background(), forge.Change{Repository: h.Repository, Remote: r, Branch: "guildhall/t7", Head: h.Heads[0], Title: "t"})
		if !errors.Is(err, forge.ErrUnsupportedRemote) {
			t.Errorf("Publish to %q = %v, want ErrUnsupportedRemote", r, err)
		}
	}
	if calls := g.readState(t).Calls; len(calls) != 0 {
		t.Errorf("gh ran for unsupported remotes: %q", calls)
	}
	for r, want := range map[string]string{
		"https://github.com/acme/widgets.git":     "github.com/acme/widgets",
		"https://github.com/acme/widgets":         "github.com/acme/widgets",
		"ssh://git@github.com/acme/widgets.git":   "github.com/acme/widgets",
		"git@github.com:acme/widgets.git":         "github.com/acme/widgets",
		"https://www.github.com/acme/widgets.git": "",
	} {
		got, err := forge.GitHubRepository(r)
		if want == "" {
			if !errors.Is(err, forge.ErrUnsupportedRemote) {
				t.Errorf("GitHubRepository(%q) = %q, %v, want ErrUnsupportedRemote", r, got, err)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("GitHubRepository(%q) = %q, %v, want %q", r, got, err, want)
		}
	}
}

// TestGitHubLive runs the contract against a real GitHub repository when GUILDHALL_LIVE_FORGE_REMOTE names a
// disposable sandbox remote the signed-in gh may push to, open pull requests on and merge into. It never runs in CI.
func TestGitHubLive(t *testing.T) {
	live := os.Getenv("GUILDHALL_LIVE_FORGE_REMOTE")
	if live == "" {
		t.Skip("set GUILDHALL_LIVE_FORGE_REMOTE to a sandbox GitHub remote to run the live forge contract")
	}
	forgetest.RunForgeContract(t, func(t *testing.T) forgetest.Harness {
		dir := t.TempDir()
		run := func(args ...string) string {
			t.Helper()
			out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
			if err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
			return strings.TrimSpace(string(out))
		}
		run("clone", "-q", live, ".")
		base := run("symbolic-ref", "--short", "refs/remotes/origin/HEAD")
		base = strings.TrimPrefix(base, "origin/")
		baseHead := run("rev-parse", "HEAD")
		commit := func(name string) string {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(t.Name()+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			run("add", name)
			run("-c", "user.name=guildhall-live", "-c", "user.email=guildhall-live@example.com", "commit", "-q", "-m", name)
			return run("rev-parse", "HEAD")
		}
		h0 := commit("live-one")
		h1 := commit("live-two")
		run("checkout", "-q", "--detach", baseHead)
		h2 := commit("live-rewrite")
		return forgetest.Harness{Forge: forge.GitHub{}, Repository: dir, Remote: live, Base: base, BaseHead: baseHead, Heads: [3]string{h0, h1, h2},
			Branch: func(t *testing.T, name string) string {
				out, _ := exec.Command("git", "-C", dir, "ls-remote", live, "refs/heads/"+name).Output()
				f := strings.Fields(string(out))
				if len(f) == 0 {
					return ""
				}
				return f[0]
			}}
	})
}

func TestValidBranch(t *testing.T) {
	for name, want := range map[string]bool{
		"guildhall/t7": true, "main": true, "feature/a.b": true,
		"": false, "@": false, "-x": false, "/a": false, "a/": false, "a.": false, "a..b": false, "a//b": false, "a@{b": false,
		"a b": false, "a~b": false, "a^b": false, "a:b": false, "a?b": false, "a*b": false, "a[b": false, `a\b`: false,
		"a/.b": false, "a.lock": false, "a/b.lock/c": false, "a\x7fb": false,
	} {
		if got := forge.ValidBranch(name); got != want {
			t.Errorf("ValidBranch(%q) = %v, want %v", name, got, want)
		}
	}
}

// ghState is the fake gh's persisted state: its pull requests, a merge refusal, whether a merge finds gh
// signed out, whether the connection drops at the next merge and stays down, whether a merge hangs, and every
// call it was given.
type ghState struct {
	PRs       []ghPR     `json:"prs"`
	Refuse    string     `json:"refuse"`
	SignedOut bool       `json:"signed_out"`
	DropMerge bool       `json:"drop_merge"`
	HangMerge bool       `json:"hang_merge"`
	Offline   bool       `json:"offline"`
	Calls     [][]string `json:"calls"`
	// Checks are each head's statusCheckRollup contexts, in GitHub's form; a head without an entry has no rollup.
	Checks map[string][]map[string]any `json:"checks"`
	// MoreChecks reports another page of contexts.
	MoreChecks bool `json:"more_checks"`
	// Logs are each Actions job's plain-text log; LogStatus is an HTTP error a job's log read answers with, and
	// HangLogs makes every log read hang.
	Logs      map[string]string `json:"logs,omitempty"`
	LogStatus map[string]int    `json:"log_status,omitempty"`
	HangLogs  bool              `json:"hang_logs,omitempty"`
	// RerunStatus is an HTTP error every job rerun answers with.
	RerunStatus      int    `json:"rerun_status,omitempty"`
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"merge_state_status"`
}

type ghPR struct {
	Number      int    `json:"number"`
	Branch      string `json:"branch"`
	State       string `json:"state"`
	MergedHead  string `json:"merged_head,omitempty"`
	MergeCommit string `json:"merge_commit,omitempty"`
	// RecordedBase is the main tip at pr create. Like GitHub's baseRefOid, it goes stale when main moves.
	RecordedBase string `json:"recorded_base,omitempty"`
	// Title and Body are what pr create and pr edit set.
	Title string `json:"title"`
	Body  string `json:"body"`
	// CloseComment is the comment pr close posted.
	CloseComment string `json:"close_comment,omitempty"`
	// MergeSubject and MergeBody are the --subject and --body pr merge was given.
	MergeSubject string `json:"merge_subject,omitempty"`
	MergeBody    string `json:"merge_body,omitempty"`
}

func loadState(file string) (ghState, error) {
	var s ghState
	data, err := os.ReadFile(file)
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

func (s ghState) save(file string) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0o644)
}

func remoteBranch(bare, name string) string {
	out, err := exec.Command("git", "--git-dir", bare, "rev-parse", "--verify", "--quiet", "refs/heads/"+name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// fakeGH is the gh program as far as the GitHub forge uses it, over the state file and bare remote. It refuses
// any argument it does not know, so a change in what the forge asks for fails the tests.
func fakeGH(stateFile, bare string, args []string) int {
	s, err := loadState(stateFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake gh:", err)
		return 1
	}
	s.Calls = append(s.Calls, args)
	code := runGH(&s, bare, args)
	if err := s.save(stateFile); err != nil {
		fmt.Fprintln(os.Stderr, "fake gh:", err)
		return 1
	}
	return code
}

func runGH(s *ghState, bare string, args []string) int {
	fail := func(code int, format string, a ...any) int {
		fmt.Fprintf(os.Stderr, format+"\n", a...)
		return code
	}
	if err := validateGHArgs(args); err != nil {
		return fail(2, "fake gh: %v", err)
	}
	cmd, rest := ghCommand(args), args[2:]
	switch cmd {
	case "api graphql":
		return graphQL(s, bare, rest)
	case "api jobs-logs":
		return jobLogs(s, args[1], rest)
	case "api jobs-rerun":
		return jobRerun(s, args[1], rest)
	}
	flags := map[string]string{}
	var positional []string
	bools := map[string]bool{"--squash": true}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case bools[a]:
			flags[a] = "true"
		case strings.HasPrefix(a, "--"):
			if i+1 >= len(rest) {
				return fail(2, "fake gh: %s needs a value", a)
			}
			flags[a] = rest[i+1]
			i++
		default:
			positional = append(positional, a)
		}
	}
	if s.Offline {
		return fail(1, "error connecting to api.github.com")
	}
	repo := flags["--repo"]
	if cmd == "repo view" {
		repo = positional[0]
	}
	if repo != "github.com/acme/widgets" {
		return fail(1, "fake gh: no repository %q", flags["--repo"])
	}
	out := func(v any) int {
		data, err := json.Marshal(v)
		if err != nil {
			return fail(1, "fake gh: %v", err)
		}
		fmt.Println(string(data))
		return 0
	}
	view := func(p ghPR) map[string]any {
		head := p.MergedHead
		if p.State == "OPEN" || p.State == "CLOSED" {
			head = remoteBranch(bare, p.Branch)
		}
		v := map[string]any{"number": p.Number, "url": fmt.Sprintf("https://github.com/acme/widgets/pull/%d", p.Number),
			"state": p.State, "headRefName": p.Branch, "headRefOid": head, "mergeCommit": nil,
			"baseRefOid": p.RecordedBase, "baseRefName": "main", "mergeable": s.Mergeable, "mergeStateStatus": s.MergeStateStatus,
			"title": p.Title, "body": p.Body}
		if p.MergeCommit != "" {
			v["mergeCommit"] = map[string]string{"oid": p.MergeCommit}
		}
		return v
	}
	number := func() (*ghPR, int) {
		if len(positional) != 1 {
			return nil, fail(2, "fake gh: %s takes one pull request", cmd)
		}
		n, err := strconv.Atoi(positional[0])
		for i := range s.PRs {
			if err == nil && s.PRs[i].Number == n {
				return &s.PRs[i], 0
			}
		}
		return nil, fail(1, "fake gh: no pull request %q", positional[0])
	}
	switch cmd {
	case "repo view":
		if flags["--json"] != "defaultBranchRef" {
			return fail(2, "fake gh: repo view --json %q", flags["--json"])
		}
		return out(map[string]any{"defaultBranchRef": map[string]string{"name": "main"}})
	case "pr list":
		if flags["--state"] != "open" {
			return fail(2, "fake gh: pr list --state %q", flags["--state"])
		}
		list := []map[string]any{}
		for _, p := range s.PRs {
			if p.Branch == flags["--head"] && p.State == "OPEN" {
				list = append(list, view(p))
			}
		}
		return out(list)
	case "pr create":
		if remoteBranch(bare, flags["--head"]) == "" {
			return fail(1, "fake gh: head branch %q does not exist", flags["--head"])
		}
		for _, p := range s.PRs {
			if p.Branch == flags["--head"] && p.State == "OPEN" {
				return fail(1, "fake gh: a pull request for branch %q already exists", p.Branch)
			}
		}
		n := len(s.PRs) + 1
		s.PRs = append(s.PRs, ghPR{Number: n, Branch: flags["--head"], State: "OPEN", RecordedBase: remoteBranch(bare, "main"), Title: flags["--title"], Body: flags["--body"]})
		fmt.Printf("https://github.com/acme/widgets/pull/%d\n", n)
		return 0
	case "pr edit":
		p, code := number()
		if p == nil {
			return code
		}
		if p.State != "OPEN" {
			return fail(1, "fake gh: pull request %d is not open", p.Number)
		}
		p.Title, p.Body = flags["--title"], flags["--body"]
		fmt.Printf("https://github.com/acme/widgets/pull/%d\n", p.Number)
		return 0
	case "pr view":
		if flags["--json"] != "number,url,state,headRefName,headRefOid,mergeCommit" && flags["--json"] != "url,state,headRefOid,baseRefName,mergeable,mergeStateStatus" {
			return fail(2, "fake gh: pr view --json %q", flags["--json"])
		}
		p, code := number()
		if p == nil {
			return code
		}
		return out(view(*p))
	case "pr close":
		p, code := number()
		if p == nil {
			return code
		}
		switch {
		case flags["--comment"] == "":
			return fail(2, "fake gh: close without --comment")
		case p.State == "MERGED":
			return fail(1, "X Pull request acme/widgets#%d can't be closed because it was already merged", p.Number)
		case p.State == "CLOSED":
			fmt.Fprintf(os.Stderr, "! Pull request acme/widgets#%d is already closed\n", p.Number)
			return 0
		}
		p.State, p.CloseComment = "CLOSED", flags["--comment"]
		return 0
	default: // pr merge
		p, code := number()
		if p == nil {
			return code
		}
		switch head := remoteBranch(bare, p.Branch); {
		case flags["--squash"] != "true":
			return fail(2, "fake gh: merge without --squash")
		case flags["--subject"] == "":
			return fail(2, "fake gh: merge without --subject")
		case s.SignedOut:
			return fail(4, "To get started with GitHub CLI, please run:  gh auth login")
		case s.HangMerge:
			time.Sleep(time.Minute) // timing:allow hung forge subprocess; caller timeout kills and reaps it.
			return fail(1, "fake gh: hung merge was not killed")
		case s.DropMerge:
			s.Offline = true
			return fail(1, "error connecting to api.github.com")
		case s.Refuse != "":
			return fail(1, "X Pull request acme/widgets#%d is not mergeable: %s.", p.Number, s.Refuse)
		case p.State != "OPEN":
			return fail(1, "X Pull request acme/widgets#%d was already merged or closed", p.Number)
		case flags["--match-head-commit"] != head:
			return fail(1, "X Pull request acme/widgets#%d: head branch was modified. Review and try the merge again.", p.Number)
		default:
			sum := sha1.Sum([]byte("merge " + head))
			p.State, p.MergedHead, p.MergeCommit = "MERGED", head, hex.EncodeToString(sum[:])
			p.MergeSubject, p.MergeBody = flags["--subject"], flags["--body"]
			return 0
		}
	}
}

// jobLogs answers gh api repos/<owner>/<repo>/actions/jobs/<job>/logs as gh does once it has followed GitHub's
// redirect: the log as plain text, or gh's message for an HTTP error, exit 1, or a sign-in failure, exit 4.
// Like gh, it refuses a log holding an escape sequence, exit 1, unless --allow-escape-sequences is passed.
func jobLogs(s *ghState, endpoint string, args []string) int {
	fail := func(code int, format string, a ...any) int {
		fmt.Fprintf(os.Stderr, format+"\n", a...)
		return code
	}
	m := jobLogsEndpoint.FindStringSubmatch(endpoint)
	escapes := slices.Contains(args, "--allow-escape-sequences")
	if escapes {
		args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--allow-escape-sequences" })
	}
	switch {
	case len(args) != 2 || args[0] != "--hostname" || args[1] != "github.com":
		return fail(2, "fake gh: api %s %q", endpoint, args)
	case s.SignedOut:
		return fail(4, "To get started with GitHub CLI, please run:  gh auth login")
	case s.Offline:
		return fail(1, "error connecting to api.github.com")
	case m[1] != "acme" || m[2] != "widgets":
		return fail(1, "gh: Not Found (HTTP 404)")
	case s.HangLogs:
		time.Sleep(time.Minute) // timing:allow hung forge subprocess; caller timeout kills and reaps it.
		return fail(1, "fake gh: hung log read was not killed")
	}
	if code := s.LogStatus[m[3]]; code != 0 {
		return fail(1, "gh: %s (HTTP %d)", map[int]string{403: "Forbidden", 404: "Not Found", 410: "Gone"}[code], code)
	}
	text, ok := s.Logs[m[3]]
	if !ok {
		return fail(1, "gh: Not Found (HTTP 404)")
	}
	if !escapes && strings.Contains(text, "\x1b") {
		return fail(1, "the response contains terminal escape sequences; pass --allow-escape-sequences to output it anyway")
	}
	fmt.Print(text)
	return 0
}

// jobRerun answers gh api repos/<owner>/<repo>/actions/jobs/<job>/rerun --method POST as GitHub does: the check
// run of that job becomes a new, in-progress run of the same name with a new node id and job id, and the answer is
// an empty object; a job no head's checks run is gh's message for an HTTP 404, exit 1. RerunStatus is an HTTP error
// the rerun answers with instead, as GitHub's 403 for a run still in progress or too old.
func jobRerun(s *ghState, endpoint string, args []string) int {
	fail := func(code int, format string, a ...any) int {
		fmt.Fprintf(os.Stderr, format+"\n", a...)
		return code
	}
	m := jobRerunEndpoint.FindStringSubmatch(endpoint)
	switch {
	case !slices.Equal(args, []string{"--method", "POST", "--hostname", "github.com"}):
		return fail(2, "fake gh: api %s %q", endpoint, args)
	case s.SignedOut:
		return fail(4, "To get started with GitHub CLI, please run:  gh auth login")
	case s.Offline:
		return fail(1, "error connecting to api.github.com")
	case m[1] != "acme" || m[2] != "widgets":
		return fail(1, "gh: Not Found (HTTP 404)")
	case s.RerunStatus != 0:
		return fail(1, "gh: This workflow is already running (HTTP %d)", s.RerunStatus)
	}
	job, err := strconv.ParseInt(m[3], 10, 64)
	if err != nil {
		return fail(1, "gh: Not Found (HTTP 404)")
	}
	for head, nodes := range s.Checks {
		for i, n := range nodes {
			if id, ok := n["databaseId"].(float64); ok && int64(id) == job {
				n = maps.Clone(n)
				n["id"], n["databaseId"] = fmt.Sprintf("%v-rerun", n["id"]), job*10+1
				n["status"], n["conclusion"] = "QUEUED", nil
				s.Checks[head][i] = n
				fmt.Println("{}")
				return 0
			}
		}
	}
	return fail(1, "gh: Not Found (HTTP 404)")
}

// graphQL answers the statusCheckRollup query the GitHub forge reads checks with, and refuses any other.
func graphQL(s *ghState, bare string, args []string) int {
	fail := func(code int, format string, a ...any) int {
		fmt.Fprintf(os.Stderr, format+"\n", a...)
		return code
	}
	fields := map[string]string{}
	hostname := ""
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			return fail(2, "fake gh: %s needs a value", args[i])
		}
		switch args[i] {
		case "--hostname":
			hostname = args[i+1]
		case "-f":
			k, v, ok := strings.Cut(args[i+1], "=")
			if !ok {
				return fail(2, "fake gh: -f %q is not key=value", args[i+1])
			}
			fields[k] = v
		default:
			return fail(2, "fake gh: api graphql does not take %s", args[i])
		}
		i++
	}
	if len(fields) != 4 || !strings.Contains(fields["query"], "statusCheckRollup") || fields["oid"] == "" {
		return fail(2, "fake gh: unknown query %v", fields)
	}
	if s.Offline {
		return fail(1, "error connecting to api.github.com")
	}
	if hostname != "github.com" || fields["owner"] != "acme" || fields["name"] != "widgets" {
		return fail(1, "fake gh: no repository %s/%s/%s", hostname, fields["owner"], fields["name"])
	}
	var object any
	if exec.Command("git", "--git-dir", bare, "cat-file", "-e", fields["oid"]+"^{commit}").Run() == nil {
		var rollup any
		if nodes, ok := s.Checks[fields["oid"]]; ok {
			rollup = map[string]any{"contexts": map[string]any{"pageInfo": map[string]any{"hasNextPage": s.MoreChecks}, "nodes": nodes}}
		}
		object = map[string]any{"__typename": "Commit", "statusCheckRollup": rollup}
	}
	data, err := json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"object": object}}})
	if err != nil {
		return fail(1, "fake gh: %v", err)
	}
	fmt.Println(string(data))
	return 0
}

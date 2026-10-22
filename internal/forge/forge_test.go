package forge_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

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
	os.Exit(m.Run())
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
	url := filepath.ToSlash(g.bare)
	if !strings.HasPrefix(url, "/") {
		url = "/" + url
	}
	config := fmt.Sprintf("[url \"file://%s\"]\n\tinsteadOf = %s\n[user]\n\tname = t\n\temail = t@example.com\n[commit]\n\tgpgsign = false\n[init]\n\tdefaultBranch = main\n", url, remote)
	if err := os.WriteFile(g.global, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", g.global)
	t.Setenv(fakeGHEnv, g.state)
	t.Setenv(fakeGHRemoteEnv, g.bare)
	g.git(t, "", "init", "-q", "--bare", g.bare)
	work := filepath.Join(dir, "work")
	g.git(t, "", "init", "-q", work)
	commit := func(name string) string {
		if err := os.WriteFile(filepath.Join(work, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		g.git(t, work, "add", name)
		g.git(t, work, "commit", "-q", "-m", name)
		return g.git(t, work, "rev-parse", "HEAD")
	}
	base := commit("base")
	g.git(t, work, "push", "-q", remote, "main")
	h0 := commit("one")
	h1 := commit("two")
	g.git(t, work, "checkout", "-q", "--detach", base)
	h2 := commit("rewrite")
	g.git(t, work, "checkout", "-q", "main")
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
	}
	return g
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
	if _, err := h.Forge.Merge(ctx, forge.Merge{Repository: h.Repository, Remote: remote, Number: pr.Number, Head: h.Heads[0]}); err != nil {
		t.Fatal(err)
	}
	calls := g.readState(t).Calls
	for _, c := range calls {
		t.Logf("gh %q", c)
		if i := slices.Index(c, "--repo"); i < 0 || i+1 >= len(c) || c[i+1] != "github.com/acme/widgets" {
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
	if merge < 0 || !slices.Contains(calls[merge], "--squash") || !slices.Equal(flagValues(calls[merge], "--match-head-commit"), []string{h.Heads[0]}) {
		t.Errorf("pr merge = %v", calls)
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
	_, err = h.Forge.Merge(ctx, forge.Merge{Repository: h.Repository, Remote: remote, Number: pr.Number, Head: h.Heads[0]})
	if err == nil || errors.Is(err, forge.ErrRefused) || !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("Merge signed out = %v, want a plain error carrying gh's message", err)
	}
	if got := remoteBranch(g.bare, "main"); got != h.BaseHead {
		t.Errorf("main = %s, want it unmoved at %s", got, h.BaseHead)
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
// signed out, and every call it was given.
type ghState struct {
	PRs       []ghPR     `json:"prs"`
	Refuse    string     `json:"refuse"`
	SignedOut bool       `json:"signed_out"`
	Calls     [][]string `json:"calls"`
}

type ghPR struct {
	Number      int    `json:"number"`
	Branch      string `json:"branch"`
	State       string `json:"state"`
	MergedHead  string `json:"merged_head,omitempty"`
	MergeCommit string `json:"merge_commit,omitempty"`
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
	if len(args) < 2 {
		return fail(2, "fake gh: unknown command %q", args)
	}
	cmd, rest := args[0]+" "+args[1], args[2:]
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
	allowed := map[string][]string{
		"repo view": {"--repo", "--json"},
		"pr list":   {"--repo", "--head", "--state", "--json"},
		"pr create": {"--repo", "--head", "--title", "--body"},
		"pr view":   {"--repo", "--json"},
		"pr merge":  {"--repo", "--squash", "--match-head-commit"},
	}
	known, ok := allowed[cmd]
	if !ok {
		return fail(2, "fake gh: unknown command %q", cmd)
	}
	for f := range flags {
		if !slices.Contains(known, f) {
			return fail(2, "fake gh: %s does not take %s", cmd, f)
		}
	}
	if flags["--repo"] != "github.com/acme/widgets" {
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
			"state": p.State, "headRefName": p.Branch, "headRefOid": head, "mergeCommit": nil}
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
		s.PRs = append(s.PRs, ghPR{Number: n, Branch: flags["--head"], State: "OPEN"})
		fmt.Printf("https://github.com/acme/widgets/pull/%d\n", n)
		return 0
	case "pr view":
		p, code := number()
		if p == nil {
			return code
		}
		return out(view(*p))
	default: // pr merge
		p, code := number()
		if p == nil {
			return code
		}
		switch head := remoteBranch(bare, p.Branch); {
		case flags["--squash"] != "true":
			return fail(2, "fake gh: merge without --squash")
		case s.SignedOut:
			return fail(4, "To get started with GitHub CLI, please run:  gh auth login")
		case s.Refuse != "":
			return fail(1, "X Pull request acme/widgets#%d is not mergeable: %s.", p.Number, s.Refuse)
		case p.State != "OPEN":
			return fail(1, "X Pull request acme/widgets#%d was already merged or closed", p.Number)
		case flags["--match-head-commit"] != head:
			return fail(1, "X Pull request acme/widgets#%d: head branch was modified. Review and try the merge again.", p.Number)
		default:
			sum := sha1.Sum([]byte("merge " + head))
			p.State, p.MergedHead, p.MergeCommit = "MERGED", head, hex.EncodeToString(sum[:])
			return 0
		}
	}
}

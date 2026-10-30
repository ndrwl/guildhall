package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/policies"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// fakePolicyGit is a PolicyGit over scripted files: the policy file each repository path shows at policies.Ref,
// a repository with none scripted having no ref yet; it records every fetch.
type fakePolicyGit struct {
	mu      sync.Mutex
	shown   map[string]git.Shown
	fetched []string
}

func (f *fakePolicyGit) FetchDefault(_ context.Context, repo, remote, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetched = append(f.fetched, repo+" "+remote+" "+ref)
	return nil
}

func (f *fakePolicyGit) Show(_ context.Context, repo, rev, path string) (git.Shown, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.shown[repo]
	if !ok || rev != policies.Ref || path != policies.Path {
		return git.Shown{}, git.ErrNoRevision
	}
	return s, nil
}

// fetches returns every fetch recorded, as "<repo> <remote> <ref>".
func (f *fakePolicyGit) fetches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.fetched)
}

// testPrompter is the App's baseline prompter with no repository policies.
func testPrompter(cfg *config.Config, cfgDir, release, skills string) interp.PromptFunc {
	return (&App{PolicyGit: &fakePolicyGit{}}).baselinePrompter(cfg, cfgDir, release, skills)
}

const mainCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// TestPromptCarriesRepositoryPolicies: the prompter fills the stage repository's policies from the policies ref,
// reports a refused file in the prompt rather than refusing to render, and leaves the section out before the
// first fetch.
func TestPromptCarriesRepositoryPolicies(t *testing.T) {
	cfg, cfgDir, release := configOnlyCopy(t)
	build := workflow.Step{ID: "build", Type: "build", Version: 1}
	for _, c := range []struct {
		name  string
		shown map[string]git.Shown
		want  string
	}{
		{"policies", map[string]git.Shown{appRepo: {Commit: mainCommit, Data: []byte("- Keep the up-to-date branch protection on main.\n")}},
			"## Repository policies\n\nThese are the Guildmaster's standing rules for this repository, read from its default branch; they are guidance this stage must follow.\n\n```text\n- Keep the up-to-date branch protection on main.\n```\n"},
		{"refused", map[string]git.Shown{appRepo: {Commit: mainCommit, Data: []byte("- a\n  b\n")}},
			"## Repository policies\n\nThe repository policies could not be read: .guildhall/policies.md line 2: an indented line continues the item on line 1; write each policy on one line. Tell the Director in your result.\n"},
		{"absent", map[string]git.Shown{appRepo: {Commit: mainCommit, Absent: true}}, ""},
		{"not read yet", nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if appRepo != "/srv/app" {
				t.Skip("the run config names /srv/app")
			}
			p := (&App{PolicyGit: &fakePolicyGit{shown: c.shown}}).baselinePrompter(cfg, cfgDir, release, t.TempDir())
			r, err := renderStep(t, p, build)
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(r.PromptPath)
			if err != nil {
				t.Fatal(err)
			}
			if c.want == "" {
				if strings.Contains(string(b), "Repository policies") {
					t.Errorf("prompt has a policies section:\n%s", b)
				}
				return
			}
			if !strings.Contains(string(b), c.want) {
				t.Errorf("prompt lacks %q:\n%s", c.want, b)
			}
		})
	}
}

// TestDirectorStatusWithoutPolicyFiles: with no repository holding a policy file, or none read yet, the feed has no
// repository_policies; one repository with a file lists every repository, each as read.
func TestDirectorStatusWithoutPolicyFiles(t *testing.T) {
	dir := statusCopy(t)
	writeFile(t, filepath.Join(dir, "config.toml"), `schema_version = 1
mode = "director"
name = "alpha"

[[repository]]
id = "app"
path = "/srv/app"
remote = "git@github.com:example/app.git"
owner_machine = "workstation"

[[repository]]
id = "lib"
path = "/srv/lib"
remote = "git@github.com:example/lib.git"
owner_machine = "workstation"
`)
	for name, shown := range map[string]map[string]git.Shown{
		"absent":       {"/srv/app": {Commit: mainCommit, Absent: true}, "/srv/lib": {Commit: mainCommit, Absent: true}},
		"not read yet": nil,
		"empty file":   {"/srv/app": {Commit: mainCommit, Data: []byte("# Policies\n")}},
	} {
		if code, out, stderr := directorCmdPolicies(t, nil, &fakePolicyGit{shown: shown}, "status", "--copy", dir, "--json"); code != 0 || strings.Contains(out, "repository_policies") {
			t.Errorf("%s: exit %d, stderr %s; the feed has repository_policies:\n%s", name, code, stderr, out)
		}
	}
	g := &fakePolicyGit{shown: map[string]git.Shown{"/srv/app": {Commit: mainCommit, Data: []byte("- One\n")}}}
	code, out, stderr := directorCmdPolicies(t, nil, g, "status", "--copy", dir, "--json")
	want := `"repository_policies":[{"repository":"app","commit":"` + mainCommit + `","policies":["One"]},{"repository":"lib","not_read_yet":true}]`
	if code != 0 || !strings.Contains(out, want) {
		t.Errorf("exit %d, stderr %s; feed lacks %s:\n%s", code, stderr, want, out)
	}
}

// TestDirectorStartShowsRepositoryPolicies: a session fetches each repository's default branch into the policies
// ref, then lists its policies in startup.md before the notes.
func TestDirectorStartShowsRepositoryPolicies(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "/exit")
	cfg, err := config.Load(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := cfg.ListedRepository("app")
	if err != nil {
		t.Fatal(err)
	}
	g := &fakePolicyGit{shown: map[string]git.Shown{repo.Path: {Commit: mainCommit, Data: []byte("- Keep the up-to-date branch protection on main.\n")}}}
	if code, out, stderr := runDirectorCmdWith(t, func(a *App) { a.PolicyGit = g }, "start", "--copy", dir); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
	}
	if got, want := g.fetches(), []string{repo.Path + " " + repo.Remote + " " + policies.Ref}; !slices.Equal(got, want) {
		t.Errorf("fetches = %q, want %q", got, want)
	}
	b, err := os.ReadFile(filepath.Join(dir, "director", "session-1", render.StartupFile))
	if err != nil {
		t.Fatal(err)
	}
	if want := "\n## Repository policies\n\nThe Guildmaster's standing rules for each repository, from its default branch as this session started.\n\n- app: Keep the up-to-date branch protection on main.\n"; !strings.Contains(string(b), want) {
		t.Errorf("startup.md lacks %q:\n%s", want, b)
	}
}

// TestAdvanceRefreshesRepositoryPolicies: each advance pass fetches the repository's default branch into the
// policies ref before it renders, and the attempt's prompt carries the policies.
func TestAdvanceRefreshesRepositoryPolicies(t *testing.T) {
	d := newRunDirector(t)
	d.policies = &fakePolicyGit{shown: map[string]git.Shown{appRepo: {Commit: mainCommit, Data: []byte("- Squash merges only.\n")}}}
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	if got, want := d.policies.fetches(), []string{appRepo + " git@github.com:example/app.git " + policies.Ref}; !slices.Equal(got, want) {
		t.Errorf("fetches = %q, want %q", got, want)
	}
	// The interpreter publishes the rendered attempt under the attempt directory's prompt directory.
	prompt := filepath.Join(artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1}.Dir(d.state), "prompt", render.PromptFile)
	b, err := os.ReadFile(prompt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "## Repository policies\n") || !strings.Contains(string(b), "```text\n- Squash merges only.\n```\n") {
		t.Errorf("prompt lacks the policies:\n%s", b)
	}
}

// TestFeedAndStartupPolicyLines: a refused file is one error line in each; an absent file or one not read yet has
// none in startup.md.
func TestFeedAndStartupPolicyLines(t *testing.T) {
	reads := []repositoryPolicies{
		{id: "app", Read: policies.Read{Commit: mainCommit, Policies: []string{"One", "Two"}}},
		{id: "lib", Read: policies.Read{Commit: mainCommit, Err: os.ErrPermission}},
		{id: "web", Read: policies.Read{Commit: mainCommit}},
		{id: "cli", Read: policies.Read{NotRead: true}},
	}
	if got, want := startupPolicies(reads), []string{"app: One", "app: Two", "lib: policies cannot be read (permission denied)"}; !slices.Equal(got, want) {
		t.Errorf("startup lines = %q, want %q", got, want)
	}
	feed := feedPolicies(reads)
	if len(feed) != 4 || feed[1].Error != "permission denied" || feed[1].Policies != nil || feed[2].Policies == nil || len(feed[2].Policies) != 0 || !feed[3].NotReadYet {
		t.Errorf("feed = %+v", feed)
	}
	if feedPolicies(reads[2:]) != nil {
		t.Error("feed without a policy file or an error is not empty")
	}
}

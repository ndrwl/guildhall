package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

const (
	testRevision = "abd974855ac9d1e8f0c0a7b6e2f5d4c3b2a19087"
	testModule   = "github.com/ndrwl/guildhall"
)

// testVersionClock is the clock version tests run at: a fetch that succeeds is recorded at it.
var testVersionClock = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

// testBuildInfo is a build of testRevision from a git checkout of the module, with settings changed by edit.
func testBuildInfo(edit func(*debug.BuildInfo)) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		info := &debug.BuildInfo{GoVersion: "go1.27.2", Path: testModule + "/cmd/guildhall", Main: debug.Module{Path: testModule, Version: "(devel)"},
			Settings: []debug.BuildSetting{{Key: "GOARCH", Value: "amd64"}, {Key: "GOOS", Value: "linux"}, {Key: "vcs", Value: "git"},
				{Key: "vcs.revision", Value: testRevision}, {Key: "vcs.time", Value: "2026-10-27T21:00:24Z"}, {Key: "vcs.modified", Value: "false"}}}
		if edit != nil {
			edit(info)
		}
		return info, true
	}
}

// fakeVersionGit is the VersionGit double: subjects by revision, the commits behind main once the ref exists, and
// a record of every call.
type fakeVersionGit struct {
	subjects map[string]string
	behind   []string
	// ref is whether refs/guildhall/version/main exists; a fetch that succeeds creates it.
	ref        bool
	fetchErr   error
	unreadable bool
	calls      []string
}

func (f *fakeVersionGit) Subject(_ context.Context, repo, rev string) (string, error) {
	f.calls = append(f.calls, "subject "+rev)
	if s, ok := f.subjects[rev]; ok && !f.unreadable {
		return s, nil
	}
	return "", fmt.Errorf("git log %s: unknown revision", rev)
}

func (f *fakeVersionGit) FetchDefault(_ context.Context, repo, remote, ref string) error {
	f.calls = append(f.calls, "fetch "+remote+" "+ref)
	if f.unreadable {
		return errors.New("not a git repository")
	}
	if f.fetchErr != nil {
		return f.fetchErr
	}
	f.ref = true
	return nil
}

func (f *fakeVersionGit) Behind(_ context.Context, repo, rev, ref string) (int, []string, error) {
	f.calls = append(f.calls, "behind "+rev+" "+ref)
	if !f.ref || f.unreadable {
		return 0, nil, errors.New("unknown ref")
	}
	return len(f.behind), f.behind, nil
}

// versionCopy writes a copy whose config lists one repository at remote, with a store, and returns it.
func versionCopy(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "config.toml"), fmt.Sprintf(`schema_version = 1
mode = "director"
name = "alpha"

[local]

[[repository]]
id = "guildhall"
path = '%s'
remote = '%s'
owner_machine = "workstation"
`, filepath.Join(dir, "repository"), remote))
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writePullRequest records that this copy delivered pull request pr for task in the guildhall repository.
func writePullRequest(t *testing.T, copyDir, task string, pr int) {
	t.Helper()
	dir := filepath.Join(copyDir, "state", "documents", "tasks", "guildhall", task, "deliver")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "pull-request.json"), fmt.Sprintf(`{"schema":"guildhall.pull-request","remote":"x","number":%d,"url":"u","branch":"guildhall/%s","head":"h","state":"open"}`, pr, task))
}

// runVersion runs version with args in the environment env, with the build info and git given.
func runVersion(t *testing.T, info func() (*debug.BuildInfo, bool), g VersionGit, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.BuildInfo, app.VersionGit, app.Now = info, g, func() time.Time { return testVersionClock }
	app.Getenv = func(k string) string { return env[k] }
	code := app.Run(append([]string{"version"}, args...))
	return code, stdout.String(), stderr.String()
}

// TestVersionForms pins the plain, --startup and --json forms, run in that order over one copy, for each build
// and repository: the plain and --startup forms fetch the default branch, --json reads it as last fetched.
func TestVersionForms(t *testing.T) {
	setVersion(t, "v1.2.3")
	const head = `{"version":"v1.2.3","revision":"` + testRevision + `","time":"2026-10-27T21:00:24Z",`
	const rev = "revision: " + testRevision + " (2026-10-27T21:00:24Z)\n"
	const first = "guildhall v1.2.3 linux/amd64 go1.27.2\n"
	merged := map[string]string{testRevision: "max_attempts_per_day = \"0\" means no daily cap (#158)"}
	var twelve []string
	for pr := 170; pr > 158; pr-- {
		twelve = append(twelve, fmt.Sprintf("Change %d (#%d)", pr, pr))
	}
	tests := []struct {
		name string
		edit func(*debug.BuildInfo)
		// copy is the remote of the copy's one repository; "" runs with no copy.
		copy       string
		git        *fakeVersionGit
		delivered  bool
		fetchedAt  string
		plain      string
		startup    string
		json       string
		fetchCalls int
	}{
		{name: "full vcs info, no copy", git: &fakeVersionGit{},
			plain:   first + rev,
			startup: "guildhall abd97485 2026-10-27\n",
			json:    head + `"modified":false,"go_version":"go1.27.2","platform":"linux/amd64","pr":null,"task":null,"behind_main":null}`},
		{name: "no vcs info", git: &fakeVersionGit{}, edit: func(i *debug.BuildInfo) { i.Settings = i.Settings[:2] },
			plain:   first,
			startup: "guildhall v1.2.3\n",
			json:    `{"version":"v1.2.3","revision":"","time":"","modified":false,"go_version":"go1.27.2","platform":"linux/amd64","pr":null,"task":null,"behind_main":null}`},
		{name: "modified build", git: &fakeVersionGit{}, edit: func(i *debug.BuildInfo) { i.Settings[5].Value = "true" },
			plain:   first + "revision: " + testRevision + " (2026-10-27T21:00:24Z, modified)\n",
			startup: "guildhall abd97485 2026-10-27, modified\nwarning: this binary was built with uncommitted or untracked files\n",
			json:    head + `"modified":true,"go_version":"go1.27.2","platform":"linux/amd64","pr":null,"task":null,"behind_main":null}`},
		{name: "copy with no matching repository", copy: "git@github.com:example/app.git", git: &fakeVersionGit{subjects: merged, ref: true},
			plain:   first + rev,
			startup: "guildhall abd97485 2026-10-27\n",
			json:    head + `"modified":false,"go_version":"go1.27.2","platform":"linux/amd64","pr":null,"task":null,"behind_main":null}`},
		{name: "unreadable repository path", copy: "https://github.com/ndrwl/guildhall.git", git: &fakeVersionGit{subjects: merged, unreadable: true},
			plain:   first + rev,
			startup: "guildhall abd97485 2026-10-27\n",
			json:    head + `"modified":false,"go_version":"go1.27.2","platform":"linux/amd64","pr":null,"task":null,"behind_main":null}`, fetchCalls: 2},
		{name: "subject without a pull request", copy: "git@github.com:ndrwl/guildhall.git", git: &fakeVersionGit{subjects: map[string]string{testRevision: "Merge branch 'x'"}},
			plain:   first + rev + "behind main: 0 merges\n",
			startup: "guildhall abd97485 2026-10-27, up to date with main\n",
			json:    head + `"modified":false,"go_version":"go1.27.2","platform":"linux/amd64","pr":null,"task":null,"behind_main":{"count":0,"missing_prs":[],"as_of":"2026-10-28T09:00:00Z"}}`, fetchCalls: 2},
		{name: "pull request this copy did not deliver", copy: "ssh://git@github.com/ndrwl/guildhall", git: &fakeVersionGit{subjects: merged},
			plain:   first + rev + "latest merge: #158\nbehind main: 0 merges\n",
			startup: "guildhall abd97485 #158 2026-10-27, up to date with main\n",
			json:    head + `"modified":false,"go_version":"go1.27.2","platform":"linux/amd64","pr":158,"task":null,"behind_main":{"count":0,"missing_prs":[],"as_of":"2026-10-28T09:00:00Z"}}`, fetchCalls: 2},
		{name: "pull request delivered by this copy", copy: "git@github.com:ndrwl/guildhall.git", delivered: true, git: &fakeVersionGit{subjects: merged},
			plain:   first + rev + "latest merge: #158 (task d10)\nbehind main: 0 merges\n",
			startup: "guildhall abd97485 #158 d10 2026-10-27, up to date with main\n",
			json:    head + `"modified":false,"go_version":"go1.27.2","platform":"linux/amd64","pr":158,"task":"d10","behind_main":{"count":0,"missing_prs":[],"as_of":"2026-10-28T09:00:00Z"}}`, fetchCalls: 2},
		{name: "behind 2", copy: "git@github.com:ndrwl/guildhall.git", delivered: true,
			git:     &fakeVersionGit{subjects: merged, behind: []string{"Close finished workspaces (#160)", "State the finding-id format (#159)"}},
			plain:   first + rev + "latest merge: #158 (task d10)\nbehind main: 2 merges (#160, #159)\n",
			startup: "guildhall abd97485 #158 d10 2026-10-27, 2 behind main (#160, #159)\nwarning: this binary is 2 merges behind main\n",
			json:    head + `"modified":false,"go_version":"go1.27.2","platform":"linux/amd64","pr":158,"task":"d10","behind_main":{"count":2,"missing_prs":[160,159],"as_of":"2026-10-28T09:00:00Z"}}`, fetchCalls: 2},
		{name: "behind 12 names 10", copy: "git@github.com:ndrwl/guildhall.git", git: &fakeVersionGit{subjects: merged, behind: twelve},
			plain:   first + rev + "latest merge: #158\nbehind main: 12 merges (#170, #169, #168, #167, #166, #165, #164, #163, #162, #161 and 2 more)\n",
			startup: "guildhall abd97485 #158 2026-10-27, 12 behind main (#170, #169, #168, #167, #166, #165, #164, #163, #162, #161 and 2 more)\nwarning: this binary is 12 merges behind main\n",
			json:    head + `"modified":false,"go_version":"go1.27.2","platform":"linux/amd64","pr":158,"task":null,"behind_main":{"count":12,"missing_prs":[170,169,168,167,166,165,164,163,162,161,160,159],"as_of":"2026-10-28T09:00:00Z"}}`, fetchCalls: 2},
		{name: "fetch failure falls back to the old ref", copy: "git@github.com:ndrwl/guildhall.git", fetchedAt: "2026-10-26T08:00:00Z",
			git:     &fakeVersionGit{subjects: merged, ref: true, fetchErr: errors.New("could not resolve host"), behind: []string{"Direct push", "Fix (#159)"}},
			plain:   first + rev + "latest merge: #158\nbehind main: 2 merges (#159) as of 2026-10-26T08:00:00Z\n",
			startup: "guildhall abd97485 #158 2026-10-27, 2 behind main (#159) as of 2026-10-26T08:00:00Z\nwarning: this binary is 2 merges behind main\n",
			json:    head + `"modified":false,"go_version":"go1.27.2","platform":"linux/amd64","pr":158,"task":null,"behind_main":{"count":2,"missing_prs":[159],"as_of":"2026-10-26T08:00:00Z"}}`, fetchCalls: 2},
		{name: "fetch failure with no ref at all", copy: "git@github.com:ndrwl/guildhall.git", git: &fakeVersionGit{subjects: merged, fetchErr: errors.New("offline")},
			plain:   first + rev + "latest merge: #158\n",
			startup: "guildhall abd97485 #158 2026-10-27\n",
			json:    head + `"modified":false,"go_version":"go1.27.2","platform":"linux/amd64","pr":158,"task":null,"behind_main":null}`, fetchCalls: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{}
			if tt.copy != "" {
				dir := versionCopy(t, tt.copy)
				env[copyEnvVar] = dir
				if tt.delivered {
					writePullRequest(t, dir, "d10", 158)
				}
				if tt.fetchedAt != "" {
					writeFile(t, filepath.Join(dir, "state", versionFetchFile), fmt.Sprintf(`{"repository":%q,"fetched_at":%q}`, filepath.Join(dir, "repository"), tt.fetchedAt))
				}
			}
			info := testBuildInfo(tt.edit)
			for _, form := range []struct {
				args []string
				want string
			}{{nil, tt.plain}, {[]string{"--startup"}, tt.startup}, {[]string{"--json"}, tt.json + "\n"}} {
				code, out, stderr := runVersion(t, info, tt.git, env, form.args...)
				if code != 0 || stderr != "" {
					t.Errorf("version %q: exit %d, stderr %q", form.args, code, stderr)
				}
				if out != form.want {
					t.Errorf("version %q =\n%s\nwant\n%s", form.args, out, form.want)
				}
			}
			fetches := 0
			for _, c := range tt.git.calls {
				if strings.HasPrefix(c, "fetch ") {
					fetches++
				}
			}
			if fetches != tt.fetchCalls {
				t.Errorf("fetches = %d, want %d (plain and --startup only): %q", fetches, tt.fetchCalls, tt.git.calls)
			}
		})
	}
}

// TestVersionCopyFlag reads the copy --copy names, over GUILDHALL_COPY.
func TestVersionCopyFlag(t *testing.T) {
	setVersion(t, "v1.2.3")
	dir := versionCopy(t, "git@github.com:ndrwl/guildhall.git")
	g := &fakeVersionGit{subjects: map[string]string{testRevision: "x (#158)"}}
	code, out, _ := runVersion(t, testBuildInfo(nil), g, map[string]string{copyEnvVar: versionCopy(t, "git@github.com:example/app.git")}, "--json", "--copy", dir)
	if code != 0 || !strings.Contains(out, `"pr":158`) {
		t.Errorf("version --json --copy = %d, %s; want pr 158 from the named copy", code, out)
	}
}

// TestVersionSupervisorRelease warns in --startup when the session's supervisor recorded another release.
func TestVersionSupervisorRelease(t *testing.T) {
	setVersion(t, "v1.2.3")
	for _, tt := range []struct{ release, want string }{
		{"v1.2.3", "guildhall abd97485 2026-10-27\n"},
		{"v1.2.2", "guildhall abd97485 2026-10-27\nwarning: this binary is not the one running this session's supervisor (v1.2.2)\n"},
	} {
		t.Run(tt.release, func(t *testing.T) {
			dir := versionCopy(t, "git@github.com:example/app.git")
			withTestStore(t, dir, func(s *store.Store) (store.Receipt, error) {
				return s.StartDirectorSession(store.DirectorSession{Number: 1, State: store.DirectorRecovering, Identity: store.ProcessIdentity{PID: 7, Token: "boot:7"},
					Release: tt.release, ConfigDigest: "sha256:" + strings.Repeat("c", 64)})
			})
			env := map[string]string{copyEnvVar: dir, session.NumberEnv: "1"}
			if code, out, _ := runVersion(t, testBuildInfo(nil), &fakeVersionGit{}, env, "--startup"); code != 0 || out != tt.want {
				t.Errorf("version --startup = %d\n%s\nwant\n%s", code, out, tt.want)
			}
			// Outside the session there is no supervisor to compare with.
			delete(env, session.NumberEnv)
			if code, out, _ := runVersion(t, testBuildInfo(nil), &fakeVersionGit{}, env, "--startup"); code != 0 || out != "guildhall abd97485 2026-10-27\n" {
				t.Errorf("version --startup outside the session = %d\n%s", code, out)
			}
		})
	}
}

// TestDirectorStatusVersionReadsLocally: the feed's version block reads the repository but never fetches, and
// names when the default branch was last fetched.
func TestDirectorStatusVersionReadsLocally(t *testing.T) {
	dir := versionCopy(t, "git@github.com:ndrwl/guildhall.git")
	writeFile(t, filepath.Join(dir, "state", versionFetchFile), fmt.Sprintf(`{"repository":%q,"fetched_at":"2026-10-26T08:00:00Z"}`, filepath.Join(dir, "repository")))
	g := &fakeVersionGit{subjects: map[string]string{testRevision: "x (#158)"}, ref: true, behind: []string{"y (#159)"}}
	app, stdout, stderr := newTestApp(nil, nil)
	app.BuildInfo, app.VersionGit = testBuildInfo(nil), g
	if code := app.Run([]string{"director", "status", "--json", "--copy", dir}); code != 0 {
		t.Fatalf("director status = %d, %s", code, stderr)
	}
	var out struct {
		Version versionInfo `json:"version"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if v := out.Version; v.PR == nil || *v.PR != 158 || v.BehindMain == nil || v.BehindMain.Count != 1 || v.BehindMain.AsOf != "2026-10-26T08:00:00Z" {
		t.Errorf("version = %+v", v)
	}
	for _, c := range g.calls {
		if strings.HasPrefix(c, "fetch ") {
			t.Errorf("director status ran %q", c)
		}
	}
	if len(g.calls) != 2 {
		t.Errorf("git calls = %q, want the subject and behind reads", g.calls)
	}
}

func TestRemoteModule(t *testing.T) {
	for _, remote := range []string{"https://github.com/ndrwl/guildhall.git", "https://github.com/ndrwl/guildhall", "ssh://git@github.com/ndrwl/guildhall.git",
		"git@github.com:ndrwl/guildhall.git", "git@github.com:ndrwl/guildhall", "github.com:ndrwl/guildhall/", "https://GitHub.com/ndrwl/Guildhall.git"} {
		if got := remoteModule(remote); got != testModule {
			t.Errorf("remoteModule(%q) = %q, want %q", remote, got, testModule)
		}
	}
	for _, remote := range []string{"git@github.com:ndrwl/guildhall-fork.git", "/srv/guildhall.git", `C:\src\guildhall.git`, ""} {
		if got := remoteModule(remote); got == testModule {
			t.Errorf("remoteModule(%q) matched the module", remote)
		}
	}
}

// TestVersionRepositoryGit runs version over real git: a repository whose remote, a local bare repository reached
// through url.insteadOf, has two merges after the binary's revision.
func TestVersionRepositoryGit(t *testing.T) {
	setVersion(t, "v1.2.3")
	global := filepath.Join(t.TempDir(), "gitconfig")
	writeFile(t, global, "")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := versionCopy(t, "https://github.com/ndrwl/guildhall.git")
	repo, remote, seed := filepath.Join(dir, "repository"), filepath.Join(dir, "remote.git"), filepath.Join(dir, "seed")
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(dir, "init", "-q", "--bare", "--initial-branch", "main", remote)
	git(dir, "init", "-q", "--initial-branch", "main", seed)
	commit := func(subject string) string {
		writeFile(t, filepath.Join(seed, "f.txt"), subject)
		git(seed, "add", "f.txt")
		git(seed, "commit", "-q", "-m", subject)
		git(seed, "push", "-q", remote, "main")
		return git(seed, "rev-parse", "HEAD")
	}
	revision := commit("d10: no daily cap (#158)")
	git(dir, "clone", "-q", remote, repo)
	git(repo, "config", "url."+filepath.ToSlash(remote)+".insteadOf", "https://github.com/ndrwl/guildhall.git")
	commit("State the finding-id format (#159)")
	commit("Close finished workspaces (#160)")
	writePullRequest(t, dir, "d10", 158)

	info := testBuildInfo(func(i *debug.BuildInfo) { i.Settings[3].Value = revision })
	env := map[string]string{copyEnvVar: dir}
	if code, out, stderr := runVersion(t, info, nil, env, "--startup"); code != 0 || out != "guildhall "+revision[:8]+" #158 d10 2026-10-27, 2 behind main (#160, #159)\nwarning: this binary is 2 merges behind main\n" {
		t.Errorf("version --startup = %d\n%s%s", code, out, stderr)
	}
	code, out, _ := runVersion(t, info, nil, env, "--json")
	var v versionInfo
	if err := json.Unmarshal([]byte(out), &v); code != 0 || err != nil || v.PR == nil || *v.PR != 158 || v.Task == nil || *v.Task != "d10" ||
		v.BehindMain == nil || v.BehindMain.Count != 2 || !slices.Equal(v.BehindMain.MissingPRs, []int{160, 159}) || v.BehindMain.AsOf != "2026-10-28T09:00:00Z" {
		t.Errorf("version --json = %d, %s", code, out)
	}
	// With the remote gone, the last fetched ref still counts, as of that fetch.
	if err := os.RemoveAll(remote); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := runVersion(t, info, nil, env); code != 0 || !strings.HasSuffix(out, "behind main: 2 merges (#160, #159) as of 2026-10-28T09:00:00Z\n") {
		t.Errorf("version after the remote went = %d\n%s", code, out)
	}
}

// TestVersionUsage keeps version's usage errors at exit 2 with the usage text, and an unwritable stdout at exit 1.
func TestVersionUsage(t *testing.T) {
	for _, args := range [][]string{{"extra"}, {"--bogus"}, {"--json", "--startup"}} {
		code, out, stderr := runVersion(t, testBuildInfo(nil), &fakeVersionGit{}, nil, args...)
		if code != 2 || out != "" || !strings.HasPrefix(stderr, "guildhall: version: ") || !strings.Contains(stderr, "Usage: guildhall <command>") {
			t.Errorf("version %q = %d, %q, %q; want a usage error", args, code, out, stderr)
		}
	}
	for _, args := range [][]string{nil, {"--startup"}, {"--json"}} {
		app, _, _ := newTestApp(nil, nil)
		app.BuildInfo, app.Stdout = testBuildInfo(nil), failingWriter{}
		if code := app.Run(append([]string{"version"}, args...)); code != 1 {
			t.Errorf("version %q to an unwritable stdout = %d, want 1", args, code)
		}
	}
}

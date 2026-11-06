package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/steward/stewardtest"
	"github.com/ndrwl/guildhall/internal/store"
)

// localCopier is a steward.Copier over this machine's files: a Remote's path is a local path, whatever its alias.
type localCopier struct{}

func (localCopier) Fetch(_ context.Context, src steward.Remote, dst string) error {
	data, err := os.ReadFile(filepath.FromSlash(src.Path))
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

func (localCopier) Put(_ context.Context, src string, dst steward.Remote) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.FromSlash(dst.Path), data, 0o600)
}

// takeoverSetup is a standalone Director copy with a real store, reached through a Fleet that runs the fixed commands
// on it with this binary's App and a copier over local files, and a Steward copy whose private config, a clone of a
// bare remote, lists the copy's machine but not the copy.
type takeoverSetup struct {
	director, steward, private, remote string
	fleet                              *stewardtest.Fleet
}

func newTakeoverSetup(t *testing.T) *takeoverSetup {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("this end-to-end takeover reaches a local copy as the roster's Linux machine; internal/steward's TestTakeoverWindows covers a Windows machine")
	}
	global := filepath.Join(t.TempDir(), "gitconfig")
	writeFile(t, global, "")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ts := &takeoverSetup{steward: t.TempDir(), remote: t.TempDir()}
	dir := bareCopy(t)
	app, _, stderr := newTestApp(nil, nil)
	if code := app.Run([]string{"store", "init", filepath.Join(dir, "director.store")}); code != 0 {
		t.Fatalf("store init: exit %d, stderr %s", code, stderr)
	}
	approveRepo(t, filepath.Join(dir, "director.store"), filepath.Join(dir, "config.toml"), "app")
	// The takeover's copy directory admits no space, which the copy's directory has, so it is reached through a link.
	ts.director = filepath.Join(t.TempDir(), "alpha")
	if err := os.Symlink(dir, ts.director); err != nil {
		t.Skipf("no symbolic link to the copy: %v", err)
	}
	roster := "schema_version = 1\n\n[[machine]]\nlabel = \"workstation\"\nssh_alias = \"guild-ws\"\nos = \"linux\"\nroles = [\"steward\", \"director\"]\n"
	if !regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`).MatchString(ts.director) {
		t.Skipf("the temporary copy %s is outside the roster's restricted path characters", ts.director)
	}
	ts.private = filepath.Join(ts.steward, "private")
	collectGit(t, ts.remote, "init", "-q", "--bare", "-b", "main")
	collectGit(t, ts.steward, "clone", "-q", ts.remote, "private")
	collectGit(t, ts.private, "checkout", "-q", "-b", "main")
	writeFile(t, filepath.Join(ts.private, "roster.toml"), roster)
	writeFile(t, filepath.Join(ts.private, "managed.toml"), "schema_version = 1\n")
	collectGit(t, ts.private, "add", ".")
	collectGit(t, ts.private, "commit", "-q", "-m", "private config")
	collectGit(t, ts.private, "push", "-q", "-u", "origin", "main")
	writeFile(t, filepath.Join(ts.steward, "config.toml"), stewardSessionConfig(ts.private, "[steward]\nname = \"steward\"\nadapter = \"fake\"\n"))
	s, err := store.Open(filepath.Join(ts.steward, stewardStoreFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ts.fleet = stewardtest.NewFleet()
	ts.fleet.Handle("guild-ws", func(argv []string) ([]byte, bool, error) {
		app, stdout, stderr := newTestApp(nil, nil)
		if code := app.Run(argv[1:]); code != 0 {
			return nil, true, fmt.Errorf("exit %d: %s", code, stderr)
		}
		return stdout.Bytes(), true, nil
	})
	return ts
}

// run runs the CLI as the Steward's commands run, inside a session when session is set.
func (ts *takeoverSetup) run(t *testing.T, inSession bool, stdin string, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Copier, app.Runner = localCopier{}, ts.fleet
	app.LookPath = exec.LookPath
	app.Output = func(ctx context.Context, path string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, path, args...).Output()
	}
	if inSession {
		app.Getenv = func(k string) string { return map[string]string{session.NumberEnv: "1"}[k] }
	}
	app.Stdin = strings.NewReader(stdin)
	code := app.Run(args)
	return code, stdout.String(), stderr.String()
}

// effective is the copy's effective config as the admission boundary loads it, with its managed section left out.
func (ts *takeoverSetup) effective(t *testing.T) any {
	t.Helper()
	cfg, err := admitDirector(filepath.Join(ts.director, "config.toml"))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	cfg.Managed = nil
	return cfg
}

// TestStewardTakeover: the dry run plans a real copy's takeover and changes nothing; the apply refuses until the
// Guildmaster, and only they, answers its approval decision; then it leaves the copy managed by the Steward with the
// same effective settings and every task as it was, and commits the roster entry.
func TestStewardTakeover(t *testing.T) {
	ts := newTakeoverSetup(t)
	cfgPath := filepath.Join(ts.steward, "config.toml")
	planDir := filepath.Join(ts.steward, "rollouts", "takeover-alpha")
	if code, _, stderr := ts.run(t, false, "", "task", "new", "--copy", ts.director, "--repo", "app", "--title", "Kept"); code != 0 {
		t.Fatalf("task new: %s", stderr)
	}
	before := ts.effective(t)
	code, out, stderr := ts.run(t, false, "", "steward", "takeover", "--config", cfgPath, "--machine", "workstation", "--director", "alpha", "--copy", ts.director, "--plan", planDir, "--dry-run")
	if code != 0 {
		t.Fatalf("dry run: exit %d, %s", code, stderr)
	}
	m := regexp.MustCompile(`^takeover plan (sha256:[0-9a-f]{64})\n`).FindStringSubmatch(out)
	if m == nil || !strings.Contains(out, "session no session") || !strings.Contains(out, "director enrol --steward steward --copy "+ts.director) {
		t.Fatalf("dry run printed:\n%s", out)
	}
	plan := m[1]
	if _, err := os.Stat(filepath.Join(ts.director, "managed.toml")); err == nil {
		t.Fatal("the dry run wrote managed.toml")
	}
	if code, again, _ := ts.run(t, false, "", "steward", "takeover", "--config", cfgPath, "--machine", "workstation", "--director", "alpha", "--copy", ts.director, "--plan", planDir, "--dry-run"); code != 0 || !strings.HasPrefix(again, "takeover plan "+plan+"\n") {
		t.Errorf("second dry run: exit %d, %s", code, again)
	}

	apply := []string{"steward", "takeover", "--config", cfgPath, "--plan", planDir, "--apply"}
	if code, _, stderr := ts.run(t, false, "", apply...); code != 1 || !strings.Contains(stderr, "steward approval request --plan") {
		t.Errorf("apply without approval: exit %d, %s", code, stderr)
	}
	if code, _, stderr := ts.run(t, true, "", append(apply, "--approve", plan)...); code != 1 || !strings.Contains(stderr, "steward/setup/"+setupDecisionID(plan)) {
		t.Errorf("apply in a session before the answer: exit %d, %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(planDir, "steps.json")); err == nil {
		t.Fatal("a refused apply ran a step")
	}
	code, out, stderr = ts.run(t, true, "", "steward", "approval", "request", "--plan", planDir, "--copy", ts.steward)
	var req struct {
		Decision store.Decision `json:"decision"`
		Answer   string         `json:"answer"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &req) != nil || req.Decision.TaskID != stewardSetupTask || req.Decision.ID != setupDecisionID(plan) || req.Decision.Subject.Digest != plan {
		t.Fatalf("approval request: exit %d, %s %s", code, out, stderr)
	}
	answer := []string{"decide", "answer", "--copy", ts.steward, "--repo", stewardRepository, "--task", stewardSetupTask, "--decision", req.Decision.ID, "--answer-file", "-"}
	if code, _, stderr := ts.run(t, true, "approve\n", answer...); code != 1 || !strings.Contains(stderr, "approves a setup plan") {
		t.Errorf("answer in a session: exit %d, %s; want it refused", code, stderr)
	}
	if code, _, stderr := ts.run(t, false, "approve\n", answer...); code != 0 {
		t.Fatalf("answer: %s", stderr)
	}
	code, out, stderr = ts.run(t, true, "", append(apply, "--approve", plan)...)
	if code != 0 || !strings.HasSuffix(out, "\ndone\n") {
		t.Fatalf("apply: exit %d\n%s%s", code, out, stderr)
	}

	code, out, stderr = ts.run(t, false, "", "director", "mode", "--json", "--copy", ts.director)
	var mode struct {
		Mode, Steward string
		Authorized    bool
	}
	if code != 0 || json.Unmarshal([]byte(out), &mode) != nil || mode.Mode != "managed" || mode.Steward != "steward" || !mode.Authorized {
		t.Errorf("director mode after the takeover: exit %d, %s %s", code, out, stderr)
	}
	if after := ts.effective(t); !reflect.DeepEqual(before, after) {
		t.Errorf("effective config changed:\nbefore %+v\nafter  %+v", before, after)
	}
	if code, out, _ := ts.run(t, false, "", "task", "list", "--json", "--copy", ts.director); code != 0 || !strings.Contains(out, `"t1"`) {
		t.Errorf("task list after the takeover: exit %d, %s", code, out)
	}
	exports, _ := filepath.Glob(filepath.Join(ts.director, "state", "steward", "takeover", "takeover-alpha", "*"))
	if len(exports) == 0 {
		t.Error("the takeover left no store export")
	}
	roster := collectGit(t, ts.private, "show", "HEAD:roster.toml")
	if !strings.Contains(roster, "[[machine.director]]\nname = \"alpha\"\ncopy_dir = \""+ts.director+"\"\nrepositories = [\"app\", \"lib\"]") ||
		collectGit(t, ts.private, "rev-parse", "HEAD") != collectGit(t, ts.remote, "rev-parse", "main") {
		t.Errorf("committed roster:\n%s", roster)
	}
	code, out, _ = ts.run(t, false, "", "steward", "status", "--copy", ts.steward)
	if code != 0 || !strings.Contains(out, `"decision":"`+req.Decision.ID+`"`) || !strings.Contains(out, `"state":"approved"`) || !strings.Contains(out, `"mode":"managed"`) {
		t.Errorf("steward status: exit %d\n%s", code, out)
	}
	if code, out, _ := ts.run(t, false, "", append(apply, "--approve", plan)...); code != 0 || !strings.HasSuffix(out, "\ndone\n") {
		t.Errorf("apply of the finished plan: exit %d, %s", code, out)
	}
}

// TestStewardTakeoverRefusesAManagedStewardCopy: takeover admits the Steward config as the session does, so a
// managed.toml beside it refuses the dry run with the session's reason, and nothing is planned.
func TestStewardTakeoverRefusesAManagedStewardCopy(t *testing.T) {
	ts := newTakeoverSetup(t)
	writeFile(t, filepath.Join(ts.steward, "managed.toml"), "schema_version = 1\n")
	planDir := filepath.Join(ts.steward, "rollouts", "takeover-alpha")
	code, _, stderr := ts.run(t, false, "", "steward", "takeover", "--config", filepath.Join(ts.steward, "config.toml"), "--machine", "workstation", "--director", "alpha", "--copy", ts.director, "--plan", planDir, "--dry-run")
	if code != 1 || !strings.Contains(stderr, "a Steward copy is never managed") {
		t.Fatalf("dry run: exit %d, %s; want the managed copy refused", code, stderr)
	}
	if _, err := os.Stat(planDir); err == nil {
		t.Error("the refused dry run wrote a plan")
	}
}

// TestStewardTakeoverUsage:the modes are exclusive, and the copy's names belong to the dry run.
func TestStewardTakeoverUsage(t *testing.T) {
	for _, args := range [][]string{
		{"--config", "c.toml", "--plan", "p"},
		{"--config", "c.toml", "--plan", "p", "--dry-run", "--apply"},
		{"--config", "c.toml", "--plan", "p", "--dry-run"},
		{"--config", "c.toml", "--plan", "p", "--apply", "--machine", "m"},
		{"--config", "c.toml", "--plan", "p", "--abandon", "--approve", "sha256:00"},
		{"--plan", "p", "--apply"},
	} {
		app, _, stderr := newTestApp(nil, nil)
		if code := app.Run(append([]string{"steward", "takeover"}, args...)); code != 2 {
			t.Errorf("%q: exit %d, %s; want a usage error", args, code, stderr)
		}
	}
}

//go:build unix

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/runtimes"
	"github.com/ndrwl/guildhall/internal/config"
)

// TestSigninListIsTheRuntimesThenGh: guildhall signin knows each runtime's own entry, in name order, skipping a
// runtime that needs no sign-in, then gh: today claude, codex, gh.
func TestSigninListIsTheRuntimesThenGh(t *testing.T) {
	var want []string
	for _, name := range runtimes.Names() {
		rt, err := runtimes.Lookup(name, "/bin/guildhall")
		if err != nil {
			t.Fatal(err)
		}
		if !rt.Signin().Zero() {
			want = append(want, name)
		}
	}
	want = append(want, ghName)
	rs := signinRuntimes()
	if got := strings.Join(signinNames(rs), ","); got != strings.Join(want, ",") || got != "claude,codex,gh" {
		t.Fatalf("signin runtimes = %s, want %s and claude,codex,gh", got, strings.Join(want, ","))
	}
	for _, r := range rs[:len(rs)-1] {
		rt, _ := runtimes.Lookup(r.name, "")
		if s := rt.Signin(); r.Title != s.Title || r.Program != s.Program || strings.Join(r.Verify, " ") != strings.Join(s.Verify, " ") {
			t.Errorf("%s: signin entry %+v, want the runtime's %+v", r.name, r.Signin, s)
		}
	}
}

// signinSecret is a token-shaped string every fake runtime prints in its output, which no probe line may echo.
const signinSecret = "sk-ant-oat01-SIGNIN-TEST-SECRET"

// fakeSigninRuntimes writes fake claude, codex and gh to a new directory prepended to PATH and returns the state
// directory that drives them: a file <runtime>-status makes the status command pass, <runtime>-works makes the real
// call succeed. Each fake appends its arguments to <state>/calls and prints signinSecret in every output.
func fakeSigninRuntimes(t *testing.T) string {
	t.Helper()
	bin, state := t.TempDir(), t.TempDir()
	scripts := map[string]string{
		"claude": `case "$1" in
--version) echo '2.1.400 (Claude Code)'; exit 0;;
auth) [ -f "$S/claude-status" ] && { echo '{"loggedIn": true, "token": "` + signinSecret + `"}'; exit 0; }; echo '{"loggedIn": false}'; exit 1;;
--print) [ -f "$S/claude-works" ] && { echo OK; exit 0; }; echo 'API Error: 401 invalid bearer token ` + signinSecret + `' >&2; exit 1;;
esac
exit 64`,
		"codex": `case "$1" in
login) [ "$2" = status ] || exit 64; [ -f "$S/codex-status" ] && { echo 'Logged in using ChatGPT ` + signinSecret + `'; exit 0; }; echo 'Not logged in'; exit 1;;
exec) [ -f "$S/codex-works" ] && { echo OK; exit 0; }; echo 'unauthorized ` + signinSecret + `' >&2; exit 1;;
esac
exit 64`,
		"gh": `case "$1" in
auth) [ -f "$S/gh-status" ] && { echo 'Logged in to github.com Token: ` + signinSecret + `'; exit 0; }; echo 'You are not logged into any GitHub hosts' >&2; exit 1;;
api) [ "$GH_PROMPT_DISABLED" = 1 ] || exit 65; [ -f "$S/gh-works" ] && { echo octocat; exit 0; }; echo 'HTTP 401: Bad credentials ` + signinSecret + `' >&2; exit 1;;
esac
exit 64`,
	}
	for name, body := range scripts {
		script := "#!/bin/sh\nS='" + state + "'\necho \"" + name + " $*\" >> \"$S/calls\"\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return state
}

// signedIn makes the fake runtime's status pass and, with works, its real call succeed.
func signedIn(t *testing.T, state, runtime string, works bool) {
	t.Helper()
	writeFile(t, filepath.Join(state, runtime+"-status"), "")
	if works {
		writeFile(t, filepath.Join(state, runtime+"-works"), "")
	}
}

// runSignin runs guildhall signin with args over the real PATH and command runner, stdin in as the Guildmaster's
// terminal.
func runSignin(t *testing.T, stdin interface{ Read([]byte) (int, error) }, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.LookPath = exec.LookPath
	app.RunEnv = runEnv
	app.Stdin = stdin
	app.Getwd = func() (string, error) { return t.TempDir(), nil }
	code := app.Run(append([]string{"signin"}, args...))
	return code, stdout.String(), stderr.String()
}

// TestSigninCheckReportsAWorkingSignin: --check over signed-in runtimes verifies each with a real call, names gh's
// login and exits 0, echoing no token.
func TestSigninCheckReportsAWorkingSignin(t *testing.T) {
	state := fakeSigninRuntimes(t)
	for _, r := range []string{"claude", "codex", "gh"} {
		signedIn(t, state, r, true)
	}
	code, out, stderr := runSignin(t, strings.NewReader(""), "--check")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
	}
	for _, want := range []string{"signin: no copy found, so every runtime: claude, codex, gh", "claude: check: signed in; a real call succeeded",
		"codex: check: signed in; a real call succeeded", "gh: check: signed in as octocat; a real call succeeded"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out+stderr, signinSecret) {
		t.Errorf("output echoes the token:\n%s\n%s", out, stderr)
	}
	calls, err := os.ReadFile(filepath.Join(state, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"claude auth status", "claude --print " + adapter.SigninPrompt + " --max-turns 1", "codex login status",
		"codex exec --skip-git-repo-check --sandbox read-only " + adapter.SigninPrompt, "gh auth status --hostname github.com", "gh api user --jq .login"} {
		if !strings.Contains(string(calls), want+"\n") {
			t.Errorf("calls lack %q:\n%s", want, calls)
		}
	}
}

// TestSigninStatusAloneIsNotSignedIn: a runtime whose status passes while its real call fails, as a Claude token that
// passes auth status and then gets 401s, is reported not signed in, and no output echoes the token.
func TestSigninStatusAloneIsNotSignedIn(t *testing.T) {
	state := fakeSigninRuntimes(t)
	signedIn(t, state, "claude", false)
	code, out, stderr := runSignin(t, strings.NewReader(""), "--check", "claude")
	if code != 1 || !strings.Contains(stderr, "1 of 1 runtimes not signed in") {
		t.Errorf("exit %d, stderr %q; want 1 and the count", code, stderr)
	}
	if want := "claude: check: not signed in: claude auth status says signed in, but a real call failed with exit status 1, so the sign-in does not work"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if strings.Contains(out+stderr, signinSecret) || strings.Contains(out+stderr, "401") {
		t.Errorf("output echoes the runtime's output:\n%s\n%s", out, stderr)
	}
	if strings.Contains(out, "act:") {
		t.Errorf("--check acted:\n%s", out)
	}
}

// loginReader is the Guildmaster's terminal: its first read runs login, as the Guildmaster signing in by hand while
// signin waits, then returns line.
type loginReader struct {
	once  sync.Once
	login func()
	line  string
	done  bool
}

func (r *loginReader) Read(p []byte) (int, error) {
	r.once.Do(r.login)
	if r.done {
		return 0, os.ErrClosed
	}
	r.done = true
	return copy(p, r.line), nil
}

// TestSigninActPrintsTheCommandAndWaits: a runtime not signed in gets its exact sign-in command and link, which
// Guildhall never runs; once the Guildmaster presses Enter, verify probes again with a real call.
func TestSigninActPrintsTheCommandAndWaits(t *testing.T) {
	state := fakeSigninRuntimes(t)
	in := &loginReader{login: func() { signedIn(t, state, "codex", true) }, line: "\n"}
	code, out, stderr := runSignin(t, in, "codex")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
	}
	for _, want := range []string{"codex: check: not signed in: codex login status failed with exit status 1",
		"codex: act: sign Codex in on this machine yourself; Guildhall runs and opens nothing", "  run: codex login --device-auth",
		"device code authorization for Codex", "  help: https://developers.openai.com/codex/auth", "never copy ~/.codex/auth.json",
		"codex: press Enter once signed in, or type skip", "codex: verify: signed in; a real call succeeded"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	calls, err := os.ReadFile(filepath.Join(state, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "--device-auth") || strings.Count(string(calls), "codex login status\n") != 2 {
		t.Errorf("calls %q; want two status checks and no login run", calls)
	}
	if strings.Contains(out+stderr, signinSecret) {
		t.Errorf("output echoes the token:\n%s\n%s", out, stderr)
	}
}

// TestSigninActSkipOrEnd: typing skip, or the end of the terminal's input, leaves the runtime not signed in, so
// signin exits 1; a runtime missing from PATH is named for install.
func TestSigninActSkipOrEnd(t *testing.T) {
	state := fakeSigninRuntimes(t)
	for name, in := range map[string]string{"skip": "skip\n", "end": ""} {
		t.Run(name, func(t *testing.T) {
			code, out, _ := runSignin(t, strings.NewReader(in), "gh", "claude")
			if code != 1 || strings.Count(out, "verify: skipped; not signed in") != 2 ||
				!strings.Contains(out, "  run: gh auth login --hostname github.com") || !strings.Contains(out, "  run: claude auth login") {
				t.Errorf("exit %d; want 1, both skipped and both commands printed:\n%s", code, out)
			}
		})
	}
	if calls, _ := os.ReadFile(filepath.Join(state, "calls")); strings.Contains(string(calls), "login\n") || strings.Contains(string(calls), "auth login") {
		t.Errorf("signin ran a login: %q", calls)
	}
	t.Setenv("PATH", t.TempDir())
	code, out, _ := runSignin(t, strings.NewReader(""), "--check", "gh")
	if code != 1 || !strings.Contains(out, "gh: check: gh is not on PATH; install it first") {
		t.Errorf("exit %d; want 1 naming gh missing from PATH:\n%s", code, out)
	}
}

// TestSigninRefusesAnUnknownRuntime is a usage error naming the runtimes.
func TestSigninRefusesAnUnknownRuntime(t *testing.T) {
	code, _, stderr := runSignin(t, strings.NewReader(""), "aider")
	if code != 2 || !strings.Contains(stderr, `unknown runtime "aider"; the runtimes are claude, codex, gh`) {
		t.Errorf("exit %d, stderr %q; want a usage error naming the runtimes", code, stderr)
	}
}

// TestCopySigninRuntimes: a Director copy uses the runtimes its session and profiles run, and gh, which publishes;
// a Steward copy does not publish, so it needs no gh.
func TestCopySigninRuntimes(t *testing.T) {
	names := func(rs []signinRuntime) string { return strings.Join(signinNames(rs), ",") }
	director := &config.Config{Mode: config.ModeDirector, Director: &config.Director{Adapter: "fake"},
		Profiles: map[string]config.Profile{"builder": {Adapter: "fake"}, "reviewer": {Adapter: "codex"}}}
	if got := names(copySigninRuntimes(director, "")); got != "codex,gh" {
		t.Errorf("director copy = %s, want codex,gh", got)
	}
	if got := names(copySigninRuntimes(director, "claude")); got != "claude,codex,gh" {
		t.Errorf("director copy run with claude = %s, want claude,codex,gh", got)
	}
	steward := &config.Config{Mode: config.ModeSteward, Steward: &config.Steward{Adapter: "fake"}}
	if got := names(copySigninRuntimes(steward, "")); got != "none" {
		t.Errorf("fake steward copy = %s, want none", got)
	}
	steward.Steward.Adapter = "claude"
	if got := names(copySigninRuntimes(steward, "")); got != "claude" {
		t.Errorf("claude steward copy = %s, want claude", got)
	}
}

// TestDirectorStartCheckProbesSignins: director start --check runs the sign-in probes of the copy's runtimes in
// report-only mode: a runtime not signed in is a warning naming guildhall signin and the check still exits 0, and
// with GUILDHALL_SIGNIN_CHECK=skip, as a reload's preflight of a new binary sets it, nothing is probed.
func TestDirectorStartCheckProbesSignins(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n\n[profile.builder]\nadapter = \"claude\"\n", "/exit")
	state := fakeSigninRuntimes(t)
	signedIn(t, state, "gh", true)
	signedIn(t, state, "claude", false)
	code, out, stderr := runDirectorCmdWith(t, func(a *App) { a.RunEnv = runEnv }, "start", "--check", "--copy", dir)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
	}
	for _, want := range []string{"signin claude: warning: not signed in: claude auth status says signed in, but a real call failed",
		"; run guildhall signin", "signin gh: signed in as octocat; a real call succeeded"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "signin codex") || strings.Contains(out+stderr, signinSecret) {
		t.Errorf("output probes codex, which the copy does not use, or echoes the token:\n%s\n%s", out, stderr)
	}
	if got := sessions(t, dir); len(got) != 0 {
		t.Errorf("--check recorded %+v", got)
	}
	if err := os.Remove(filepath.Join(state, "calls")); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = runDirectorCmdWith(t, func(a *App) {
		a.RunEnv = runEnv
		a.Getenv = func(k string) string { return map[string]string{SigninCheckEnv: SigninCheckSkip}[k] }
	}, "start", "--check", "--copy", dir)
	if code != 0 || !strings.Contains(out, "signin: skipped (GUILDHALL_SIGNIN_CHECK=skip)") || strings.Contains(out, "signin claude") {
		t.Errorf("exit %d; want 0 and the probes skipped:\n%s\n%s", code, out, stderr)
	}
	if calls, _ := os.ReadFile(filepath.Join(state, "calls")); strings.Contains(string(calls), "auth status") || strings.Contains(string(calls), "--print") {
		t.Errorf("a skipped check probed a sign-in: %q", calls)
	}
}

// TestStewardStartCheckProbesSignins: steward start --check runs the same probes, report only, for a Steward copy's
// runtimes, which never include gh.
func TestStewardStartCheckProbesSignins(t *testing.T) {
	dir := stewardCopy(t, "/exit")
	fakeSigninRuntimes(t)
	check := func() (int, string, string) {
		app, stdout, stderr := supervisorApp(nil, time.Hour)
		app.LookPath = exec.LookPath
		app.RunEnv = runEnv
		code := app.Run([]string{"steward", "start", "fake", "--check", "--copy", dir})
		return code, stdout.String(), stderr.String()
	}
	code, out, stderr := check()
	if code != 0 || !strings.Contains(out, "signin: no runtime of this copy needs a sign-in") {
		t.Errorf("a fake Steward: exit %d; want 0 and no runtime to sign in:\n%s\n%s", code, out, stderr)
	}
	// The Steward's [steward] adapter is claude; the session runs the fake runtime named on the command line.
	writeFile(t, filepath.Join(dir, "config.toml"), stewardSessionConfig(filepath.Join(dir, "private"), "[steward]\nname = \"steward\"\nadapter = \"claude\"\n"))
	code, out, stderr = check()
	if code != 0 || strings.Contains(out, "signin gh") ||
		!strings.Contains(out, "signin claude: warning: not signed in: claude auth status failed with exit status 1; run guildhall signin") {
		t.Errorf("a claude Steward: exit %d; want 0, a report-only claude warning and no gh line:\n%s\n%s", code, out, stderr)
	}
}

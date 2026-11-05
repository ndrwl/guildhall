package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/runtimes"
	"github.com/ndrwl/guildhall/internal/config"
)

// SigninCheckEnv, set to SigninCheckSkip, makes `director start --check` and `steward start --check` print that the
// sign-in probes were skipped instead of running them; a reload's preflight of a new binary sets it, so a reload
// spends no model call (docs/signin.md#in-the-start-preflight).
const (
	SigninCheckEnv  = "GUILDHALL_SIGNIN_CHECK"
	SigninCheckSkip = "skip"
)

// signinStatusTimeout bounds each runtime's sign-in status command, and signinVerifyTimeout its real call.
const (
	signinStatusTimeout = 30 * time.Second
	signinVerifyTimeout = 2 * time.Minute
)

// signinRuntime is one runtime `guildhall signin` checks, acts on and verifies (docs/signin.md): its entry, by
// the name it has on the command line.
type signinRuntime struct {
	// name is the runtime's name on the command line: a runtime's name, such as claude or codex, or gh.
	name string
	adapter.Signin
}

// ghName is the GitHub CLI's name on the command line.
const ghName = "gh"

// ghSignin is the GitHub CLI's entry, which a Director copy publishes through; it is no runtime, so signin owns it.
var ghSignin = signinRuntime{name: ghName, Signin: adapter.Signin{
	Title: "GitHub CLI", Program: "gh",
	Status: []string{"auth", "status", "--hostname", "github.com"},
	Verify: []string{"api", "user", "--jq", ".login"},
	Env:    []string{"GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"},
	Act: []string{
		"run: gh auth login --hostname github.com",
		"then: gh auth setup-git, so git pushes use the same sign-in",
		"help: https://cli.github.com/manual/gh_auth_login",
	},
}}

// signinRuntimes are the runtimes guildhall signin knows, in the order it handles them: each runtime's entry
// (runtimes.Runtime.Signin) in name order, skipping a runtime that needs no sign-in, then gh.
func signinRuntimes() []signinRuntime {
	var out []signinRuntime
	for _, name := range runtimes.Names() {
		rt, err := runtimes.Lookup(name, "")
		if err != nil {
			continue
		}
		if s := rt.Signin(); !s.Zero() {
			out = append(out, signinRuntime{name: name, Signin: s})
		}
	}
	return append(out, ghSignin)
}

// signinRuntimeNames is the names of signinRuntimes.
func signinRuntimeNames() []string {
	var out []string
	for _, r := range signinRuntimes() {
		out = append(out, r.name)
	}
	return out
}

// lookupSigninRuntime is the signin runtime called name.
func lookupSigninRuntime(name string) (signinRuntime, bool) {
	for _, r := range signinRuntimes() {
		if r.name == name {
			return r, true
		}
	}
	return signinRuntime{}, false
}

// signinState is what one probe of a runtime found.
type signinState struct {
	// ok is set when the status passed and the real call succeeded.
	ok bool
	// detail says what was found, in words that never hold the runtime's output, so never a token.
	detail string
}

// githubLogin matches a GitHub login, the only output of a probe ever printed: gh's verify call prints it.
var githubLogin = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

// probeSignin runs the check and verify of r: r.Program on PATH, its status command, then its real call. No output
// of either command is printed or returned, except a GitHub login gh's verify prints, which is not a secret.
func (a *App) probeSignin(r signinRuntime) signinState {
	path, err := a.LookPath(r.Program)
	if err != nil {
		return signinState{detail: fmt.Sprintf("%s is not on PATH; install it first", r.Program)}
	}
	if _, err := a.runSigninStep(signinStatusTimeout, r, path, r.Status, false); err != nil {
		return signinState{detail: fmt.Sprintf("not signed in: %s %s %s", r.Program, strings.Join(r.Status, " "), err)}
	}
	out, err := a.runSigninStep(signinVerifyTimeout, r, path, r.Verify, true)
	if err != nil {
		return signinState{detail: fmt.Sprintf("not signed in: %s says signed in, but a real call %s, so the sign-in does not work", strings.Join(append([]string{r.Program}, r.Status...), " "), err)}
	}
	detail := "signed in; a real call succeeded"
	if r.name == ghName {
		if login := strings.TrimSpace(string(out)); githubLogin.MatchString(login) {
			detail = fmt.Sprintf("signed in as %s; a real call succeeded", login)
		}
	}
	return signinState{ok: true, detail: detail}
}

// runSigninStep runs one probe command with r.Env in a new empty directory, so no copy's project settings or hooks
// load, and returns its output; a failure is described by its exit status or timeout only, never its output. With
// reply set, a command that prints nothing has failed, as a real model call always replies.
func (a *App) runSigninStep(timeout time.Duration, r signinRuntime, path string, args []string, reply bool) ([]byte, error) {
	if a.RunEnv == nil {
		return nil, errors.New("could not run: no command runner is configured")
	}
	dir, err := os.MkdirTemp("", "guildhall-signin-*")
	if err != nil {
		return nil, fmt.Errorf("could not run: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := a.RunEnv(ctx, dir, r.Env, path, args...)
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return nil, fmt.Errorf("timed out after %s", timeout)
	case errors.As(err, &exit):
		return nil, fmt.Errorf("failed with exit status %d", exit.ExitCode())
	case err != nil:
		return nil, errors.New("could not run")
	case reply && strings.TrimSpace(string(out)) == "":
		return nil, errors.New("printed no reply")
	}
	return out, nil
}

// runEnv is the real App.RunEnv: path run with args in dir, its environment this process's with env added, and its
// combined output.
func runEnv(ctx context.Context, dir string, env []string, path string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd.CombinedOutput()
}

// copySigninRuntimes are the signin runtimes a copy's config uses, in signinRuntimes order: Claude Code or Codex
// when the session runtime, the [director] or [steward] adapter or any profile's adapter runs it, and gh for a
// Director copy, which publishes through it.
func copySigninRuntimes(cfg *config.Config, session string) []signinRuntime {
	used := map[string]bool{session: true}
	if cfg.Director != nil {
		used[cfg.Director.Adapter] = true
	}
	if cfg.Steward != nil {
		used[cfg.Steward.Adapter] = true
	}
	for _, p := range cfg.Profiles {
		used[p.Adapter] = true
	}
	used[ghName] = cfg.Mode != config.ModeSteward
	var out []signinRuntime
	for _, r := range signinRuntimes() {
		if used[r.name] {
			out = append(out, r)
		}
	}
	return out
}

// signinReport is the sign-in part of `director start --check` and `steward start --check`: one line per runtime
// the copy uses, report only, never refusing; with SigninCheckEnv set to SigninCheckSkip, or no App.RunEnv, it
// prints that nothing was probed.
func (a *App) signinReport(p startPlan) {
	if a.getenv(SigninCheckEnv) == SigninCheckSkip {
		a.preflightLine("signin", "skipped (%s=%s)", SigninCheckEnv, SigninCheckSkip)
		return
	}
	if a.RunEnv == nil {
		a.preflightLine("signin", "not probed: no command runner is configured")
		return
	}
	rs := copySigninRuntimes(p.cfg, p.name)
	if len(rs) == 0 {
		a.preflightLine("signin", "no runtime of this copy needs a sign-in")
		return
	}
	for _, r := range rs {
		st := a.probeSignin(r)
		if st.ok {
			a.preflightLine("signin "+r.name, "%s", st.detail)
		} else {
			a.preflightLine("signin "+r.name, "warning: %s; run guildhall signin", st.detail)
		}
	}
}

// signinCmd runs `guildhall signin [--check] [--copy <dir>] [claude|codex|gh]...`: for each runtime named, else each
// the copy uses, else all of them, check, act and verify (docs/signin.md). Check probes the sign-in; when it fails,
// act prints the exact command and link, never running or opening them, and waits for the Guildmaster's Enter; verify
// probes again, a real call. --check reports only. It exits 1 when any runtime is not signed in at the end.
func (a *App) signinCmd(args []string) int {
	fl := flag.NewFlagSet("signin", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	check := fl.Bool("check", false, "report only")
	if err := parseInterspersed(fl, args); err != nil {
		return a.usageError("signin: " + err.Error())
	}
	var rs []signinRuntime
	for _, name := range fl.Args() {
		r, ok := lookupSigninRuntime(name)
		if !ok {
			return a.usageError(fmt.Sprintf("signin: unknown runtime %q; the runtimes are %s", name, strings.Join(signinRuntimeNames(), ", ")))
		}
		if !slices.ContainsFunc(rs, func(x signinRuntime) bool { return x.name == name }) {
			rs = append(rs, r)
		}
	}
	if len(rs) == 0 {
		var cfgPath string
		if err := cp.apply(a, nil, &cfgPath, nil); err != nil {
			return a.usageError("signin: " + err.Error())
		}
		if cp.dir == "" {
			rs = signinRuntimes()
			a.preflightLine("signin", "no copy found, so every runtime: %s", strings.Join(signinRuntimeNames(), ", "))
		} else {
			cfg, refusal, err := appliedConfig(cfgPath)
			if err != nil {
				return a.fail(fmt.Errorf("signin: %w", err))
			}
			if refusal != "" {
				if _, err := fmt.Fprintf(a.Stderr, "signin: managed settings not applied, using config.toml alone: %s\n", refusal); err != nil {
					return 1
				}
			}
			rs = copySigninRuntimes(cfg, "")
			a.preflightLine("signin", "the runtimes copy %s uses: %s", cp.dir, strings.Join(signinNames(rs), ", "))
		}
	}
	in := bufio.NewReader(a.stdin())
	failed := 0
	for _, r := range rs {
		st := a.probeSignin(r)
		a.preflightLine(r.name, "check: %s", st.detail)
		if !st.ok && !*check {
			st = a.signinAct(r, in)
		}
		if !st.ok {
			failed++
		}
	}
	if failed > 0 {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: signin: %d of %d runtimes not signed in\n", failed, len(rs))
		return 1
	}
	return 0
}

// signinAct is the act and verify steps of r: print its sign-in command and link, wait for a line on in, then probe
// again. A line "skip", or the end of in, skips r without probing.
func (a *App) signinAct(r signinRuntime, in *bufio.Reader) signinState {
	a.preflightLine(r.name, "act: sign %s in on this machine yourself; Guildhall runs and opens nothing", r.Title)
	for _, l := range r.Act {
		_, _ = fmt.Fprintf(a.Stdout, "  %s\n", l)
	}
	_, _ = fmt.Fprintf(a.Stdout, "%s: press Enter once signed in, or type skip\n", r.name)
	line, err := in.ReadString('\n')
	if strings.TrimSpace(line) == "skip" || (err != nil && line == "") {
		st := signinState{detail: "skipped; not signed in"}
		a.preflightLine(r.name, "verify: %s", st.detail)
		return st
	}
	st := a.probeSignin(r)
	a.preflightLine(r.name, "verify: %s", st.detail)
	return st
}

// signinNames is the names of rs.
func signinNames(rs []signinRuntime) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.name)
	}
	if len(out) == 0 {
		return []string{"none"}
	}
	return out
}

// stdin is a.Stdin, or an empty reader when it is nil.
func (a *App) stdin() io.Reader {
	if a.Stdin == nil {
		return strings.NewReader("")
	}
	return a.Stdin
}

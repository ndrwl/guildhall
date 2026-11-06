package setup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Timeouts bound each command the engine runs: a check or verify, and an act.
const (
	checkTimeout  = 2 * time.Minute
	actionTimeout = 30 * time.Minute
)

// outputTail is how much of a command's output a report or error keeps.
const outputTail = 2000

// Facts are what a run of the engine knows about the machine and its caller.
type Facts struct {
	// SetupDir holds the engine's journal, completion records and ownership manifest; required for any act.
	SetupDir string
	// Home is the user's home directory, substituted as ${home}; empty means os.UserHomeDir.
	Home string
	// Vars are values for the step file's params, from the caller (such as a plan's pins); a name the file does
	// not declare is refused.
	Vars map[string]string
	// GOOS and GOARCH are the platform; empty means the running one.
	GOOS, GOARCH string
	// Stdin, Stdout and Stderr are attached to a human step's run command; nil means none.
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	// HTTP downloads fetches; nil means http.DefaultClient.
	HTTP *http.Client
	// Now is the clock of journals, records and the manifest; nil means time.Now.
	Now func() time.Time
}

// defaultVars are the values the engine sets for every step file.
var defaultVars = map[string]bool{"home": true, "setup_dir": true, "os": true, "arch": true, "exe": true}

// varPattern is a ${name} substitution.
var varPattern = regexp.MustCompile(`\$\{([a-z][a-z0-9_]*)\}`)

// env is one run's resolved facts.
type env struct {
	f    Facts
	s    Steps
	vars map[string]string
}

func newEnv(s Steps, f Facts) (*env, error) {
	if f.GOOS == "" {
		f.GOOS = runtime.GOOS
	}
	if f.GOARCH == "" {
		f.GOARCH = runtime.GOARCH
	}
	if f.Now == nil {
		f.Now = time.Now
	}
	if f.HTTP == nil {
		f.HTTP = http.DefaultClient
	}
	if f.Home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("home directory: %w", err)
		}
		f.Home = h
	}
	vars := maps.Clone(s.Params)
	if vars == nil {
		vars = map[string]string{}
	}
	for _, k := range slices.Sorted(maps.Keys(f.Vars)) {
		if _, ok := s.Params[k]; !ok {
			return nil, fmt.Errorf("%w: %s declares no param %q", ErrInvalid, s.Name, k)
		}
		vars[k] = f.Vars[k]
	}
	exe := ""
	if f.GOOS == "windows" {
		exe = ".exe"
	}
	vars["home"], vars["setup_dir"], vars["os"], vars["arch"], vars["exe"] = f.Home, f.SetupDir, f.GOOS, f.GOARCH, exe
	return &env{f: f, s: s, vars: vars}, nil
}

// expand substitutes ${name} values in t.
func (e *env) expand(t string) string {
	return varPattern.ReplaceAllStringFunc(t, func(m string) string { return e.vars[m[2:len(m)-1]] })
}

// environ is the environment of a command: this process's, with every value as SETUP_<NAME> and extra added.
func (e *env) environ(extra map[string]string) []string {
	out := os.Environ()
	for _, k := range slices.Sorted(maps.Keys(e.vars)) {
		out = append(out, "SETUP_"+strings.ToUpper(k)+"="+e.vars[k])
	}
	for _, k := range slices.Sorted(maps.Keys(extra)) {
		out = append(out, "SETUP_"+strings.ToUpper(k)+"="+extra[k])
	}
	return out
}

// powershell is the program a pwsh script runs with: pwsh, PowerShell 7, when it is on PATH, else, on Windows, the
// Windows PowerShell every Windows machine has, so a built-in's Windows form runs on a stock machine.
func powershell(goos string) string {
	if _, err := exec.LookPath("pwsh"); err != nil && goos == "windows" {
		return "powershell"
	}
	return "pwsh"
}

// exec runs c, returning its combined output. Argv elements are substituted and run with no shell; a script is
// given to its shell unsubstituted and reads values from SETUP_* variables. interactive attaches Facts' streams.
func (e *env) exec(ctx context.Context, c Command, timeout time.Duration, extra map[string]string, interactive bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var argv []string
	if c.Script != "" {
		if c.Shell == "pwsh" {
			argv = []string{powershell(e.f.GOOS), "-NoProfile", "-NonInteractive", "-Command", c.Script}
		} else {
			argv = []string{"sh", "-c", c.Script}
		}
	} else {
		for _, a := range c.Argv {
			argv = append(argv, e.expand(a))
		}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = e.environ(extra)
	var out bytes.Buffer
	if interactive {
		cmd.Stdin = e.f.Stdin
		cmd.Stdout = io.MultiWriter(&out, orDiscard(e.f.Stdout))
		cmd.Stderr = io.MultiWriter(&out, orDiscard(e.f.Stderr))
	} else {
		cmd.Stdout, cmd.Stderr = &out, &out
	}
	err := cmd.Run()
	if ctx.Err() != nil && err != nil {
		err = fmt.Errorf("%w after %s", ctx.Err(), timeout)
	}
	return out.Bytes(), err
}

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// check runs c as a check: it passes when it exits 0 and its output contains the step's expanded Expect.
func (e *env) check(ctx context.Context, st Step, c Command) (bool, string) {
	out, err := e.exec(ctx, c, checkTimeout, nil, false)
	detail := tail(out)
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return false, joinDetail(err.Error(), detail)
		}
		return false, joinDetail("exit "+fmt.Sprint(exitErr.ExitCode()), detail)
	}
	if want := e.expand(st.Expect); want != "" && !strings.Contains(string(out), want) {
		return false, joinDetail(fmt.Sprintf("output does not contain %q", want), detail)
	}
	return true, detail
}

// verifyCommand is the command that verifies st: its verify, else its check.
func verifyCommand(st Step) Command {
	if st.Verify.IsZero() {
		return st.Check
	}
	return st.Verify
}

func tail(out []byte) string {
	s := strings.TrimSpace(string(out))
	if len(s) > outputTail {
		s = "…" + s[len(s)-outputTail:]
	}
	return s
}

func joinDetail(a, b string) string {
	if b == "" {
		return a
	}
	return a + ": " + b
}

// StepError is a step whose act or verify failed, with what the engine saw.
type StepError struct {
	Step string
	Err  error
}

func (e *StepError) Error() string { return fmt.Sprintf("step %s: %v", e.Step, e.Err) }
func (e *StepError) Unwrap() error { return e.Err }

// act closes a fixable step's gap: check, claim its files, run the action or fetch, record the files, verify.
// It returns done=false when the check already passed and nothing ran.
func (e *env) act(ctx context.Context, st Step, j Journal) (bool, error) {
	if ok, _ := e.check(ctx, st, st.Check); ok {
		e.journal(j, st.ID, "already-done", "")
		return false, nil
	}
	m, err := loadManifest(e.f.SetupDir)
	if err != nil {
		return false, err
	}
	paths, err := m.claim(e, st)
	if err != nil {
		e.journal(j, st.ID, "refused", err.Error())
		return false, &StepError{Step: st.ID, Err: err}
	}
	e.journal(j, st.ID, "act", st.actDisplay())
	if st.Fetch != nil {
		err = e.fetch(ctx, *st.Fetch)
	} else {
		var out []byte
		out, err = e.exec(ctx, st.Action, actionTimeout, nil, false)
		if err != nil {
			err = fmt.Errorf("action %s: %v: %s", st.Action, err, tail(out))
		}
	}
	if rerr := m.record(e, st, paths); rerr != nil && err == nil {
		err = rerr
	}
	if err != nil {
		e.journal(j, st.ID, "failed", err.Error())
		return true, &StepError{Step: st.ID, Err: err}
	}
	if ok, detail := e.check(ctx, st, verifyCommand(st)); !ok {
		e.journal(j, st.ID, "failed", "verify: "+detail)
		return true, &StepError{Step: st.ID, Err: fmt.Errorf("verify failed: %s", detail)}
	}
	e.journal(j, st.ID, "done", "")
	return true, nil
}

// actDisplay is a fixable step's act as one line.
func (s Step) actDisplay() string {
	if s.Fetch != nil {
		return "fetch " + s.Fetch.URL + " to " + s.Fetch.Dest
	}
	return s.Action.String()
}

// requireSetupDir refuses a run that would write without an absolute setup directory.
func (e *env) requireSetupDir() error {
	if e.f.SetupDir == "" || !filepath.IsAbs(e.f.SetupDir) {
		return fmt.Errorf("setup directory %q is not an absolute path", e.f.SetupDir)
	}
	return nil
}

func (e *env) journal(j Journal, step, event, detail string) {
	if j == nil {
		return
	}
	// A journal that cannot be written loses its line, not the step; the step's result is reported either way.
	_ = j.Append(Entry{Time: e.f.Now().UTC(), Steps: e.s.Name, Digest: e.s.Digest, Step: step, Event: event, Detail: detail})
}

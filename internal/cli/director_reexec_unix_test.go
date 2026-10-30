//go:build unix

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// putOnPath replaces the guildhall on PATH, which directorCopy linked to the test binary, with a new file holding
// content, as an install renames a new binary into place, and returns its path and sha256.
func putOnPath(t *testing.T, content []byte) (string, string) {
	t.Helper()
	path, err := exec.LookPath("guildhall")
	if err != nil {
		t.Fatal(err)
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, content, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, sum
}

// differentBinary is the test binary with another sha256 that runs the same: with bytes appended, or on macOS,
// which kills a binary whose code signature does not cover the end of its file, signed again ad hoc under another
// identifier.
func differentBinary(t *testing.T) []byte {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "darwin" {
		return append(b, []byte("\nanother build\n")...)
	}
	copied := filepath.Join(t.TempDir(), "guildhall")
	if err := os.WriteFile(copied, b, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("codesign", "--force", "--sign", "-", "--identifier", "guildhall.another-build", copied).CombinedOutput(); err != nil {
		t.Fatalf("codesign: %v\n%s", err, out)
	}
	if b, err = os.ReadFile(copied); err != nil {
		t.Fatal(err)
	}
	return b
}

// testBinarySHA256 is the sha256 of the test binary, which an in-process supervisor runs.
func testBinarySHA256(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA256(exe)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

// execRecorder is an App.Exec that records its calls and returns err.
type execRecorder struct {
	mu    sync.Mutex
	calls [][]string
	err   error
	// at, when set, runs at each call, before it returns.
	at func()
}

func (r *execRecorder) exec(argv0 string, argv, _ []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{argv0}, argv...))
	if r.at != nil {
		r.at()
	}
	return r.err
}

func (r *execRecorder) recorded() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// reloadAtTurnEnd requests a plain reload of dir's supervisor and ends the Director's turn so it is applied.
func reloadAtTurnEnd(t *testing.T, dir string) {
	t.Helper()
	copyRun(t, dir, "director", "reload")
	copyRun(t, dir, "director", "turn-ended")
}

// TestReloadReexecsOntoADifferentBinary: with another guildhall on PATH, a reload pre-flights it, then, with session
// 1 recorded stopped as reloaded and the session's skills cleaned up, execs it with the supervisor's own arguments;
// the reload is left requested for the new image's session to apply.
func TestReloadReexecsOntoADifferentBinary(t *testing.T) {
	dir, _ := supervisedCopy(t)
	var skillsDir string
	var atExec struct {
		sessions    []store.DirectorSession
		skillsGone  bool
		statSkillsE error
	}
	rec := &execRecorder{}
	rec.at = func() {
		atExec.sessions = sessions(t, dir)
		_, atExec.statSkillsE = os.Stat(skillsDir)
		atExec.skillsGone = errors.Is(atExec.statSkillsE, os.ErrNotExist)
	}
	pane := &lockedBuffer{}
	// The runtime and the Herdr pane binding are the supervisor's own, which the new image needs to bind the
	// same pane.
	args := []string{"fake", "--herdr-session", "lab", "--herdr-workspace", "w1", "--herdr-tab", "w1:t1", "--herdr-pane", "w1:p1"}
	done := superviseWith(t, dir, time.Hour, func(a *App) {
		a.Exec, a.Stdout = rec.exec, pane
		install := a.InstallSkills
		a.InstallSkills = func(d string) error { skillsDir = d; return install(d) }
	}, args...)
	path, sum := putOnPath(t, differentBinary(t))
	reloadAtTurnEnd(t, dir)
	if code := ended(t, done); code != 0 {
		t.Fatalf("director start exited %d, want 0 once its image is replaced; pane:\n%s", code, pane)
	}
	want := [][]string{append([]string{path, path, "director", "start", "--copy", dir}, args...)}
	if got := rec.recorded(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("exec calls = %q, want %q", got, want)
	}
	if s := atExec.sessions; len(s) != 1 || s[0].State != store.DirectorStopped || s[0].EndReason != "reloaded" {
		t.Errorf("sessions at the exec = %+v; want session 1 stopped as reloaded", s)
	}
	if !atExec.skillsGone {
		t.Errorf("the session's skills %s at the exec: %v; want them cleaned up", skillsDir, atExec.statSkillsE)
	}
	if got := sessions(t, dir); got[0].BinarySHA256 != testBinarySHA256(t) {
		t.Errorf("session 1 binary sha256 = %q, want the test binary's", got[0].BinarySHA256)
	}
	if r := readReload(t, filepath.Join(dir, "director.store")); r.State != store.ReloadRequested {
		t.Errorf("reload = %+v; want it left for the new image's session", r)
	}
	if !strings.Contains(pane.String(), "session: 1 moves onto guildhall dev "+shortSHA(sum)) {
		t.Errorf("pane = %s; want it to name the binary it moves onto", pane)
	}
}

// TestStopWhileEndingForAReloadWinsOverTheBinary: a director stop recorded after the runtime ended for a reload,
// with another guildhall on PATH, ends the supervisor with status 0 as a stop does: the binary is neither
// pre-flighted nor execed, and no session 2 starts.
func TestStopWhileEndingForAReloadWinsOverTheBinary(t *testing.T) {
	dir, _ := supervisedCopy(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ran := filepath.Join(t.TempDir(), "ran.log")
	// Every command runs as guildhall does, each logged, so a pre-flight shows as a version or --check run.
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >>%q\nexec %q \"$@\"\n", ran, exe)
	rec := &execRecorder{}
	var stop struct {
		ran         bool
		code        int
		out, stderr string
	}
	done := superviseWith(t, dir, time.Hour, func(a *App) {
		a.Exec = rec.exec
		a.endedForReload = func(int) {
			stop.ran = true
			stop.code, stop.out, stop.stderr = runCLI(t, "director", "stop", "--copy", dir)
		}
	})
	putOnPath(t, []byte(script))
	reloadAtTurnEnd(t, dir)
	if code := ended(t, done); code != 0 {
		t.Fatalf("director start exited %d, want 0 for a stop", code)
	}
	if !stop.ran || stop.code != 0 {
		t.Fatalf("director stop while ending for the reload: ran %v, exit %d, %q, stderr %s", stop.ran, stop.code, stop.out, stop.stderr)
	}
	if calls := rec.recorded(); len(calls) != 0 {
		t.Errorf("exec calls = %q, want none", calls)
	}
	if b, err := os.ReadFile(ran); err == nil && (strings.Contains(string(b), "--check") || strings.Contains(string(b), "version")) {
		t.Errorf("guildhall on PATH ran:\n%s; want no pre-flight", b)
	}
	if got := sessions(t, dir); len(got) != 1 || got[0].State != store.DirectorStopped || got[0].EndReason == "reloaded" {
		t.Errorf("sessions = %+v; want only session 1, stopped as a stop does", got)
	}
	if r := readReload(t, filepath.Join(dir, "director.store")); r.State != store.ReloadRequested {
		t.Errorf("reload = %+v; want it left requested", r)
	}
}

// TestReloadOnTheSameBinaryStaysInProcess: with the test binary itself on PATH, a reload never execs and records
// both sessions' binary sha256, the same, on the applied reload.
func TestReloadOnTheSameBinaryStaysInProcess(t *testing.T) {
	dir, _ := supervisedCopy(t)
	rec := &execRecorder{}
	done := superviseWith(t, dir, time.Hour, func(a *App) { a.Exec = rec.exec })
	reloadAtTurnEnd(t, dir)
	waitSessions(t, dir, 2)
	own := testBinarySHA256(t)
	r := readReload(t, filepath.Join(dir, "director.store"))
	if r.State != store.ReloadApplied || r.OldSHA256 != own || r.NewSHA256 != own || r.OldRelease != Version() || r.NewRelease != Version() || r.RefusedReason != "" {
		t.Errorf("reload = %+v; want it applied on the same binary", r)
	}
	copyRun(t, dir, "director", "stop", "--now")
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d, want 0", code)
	}
	if calls := rec.recorded(); len(calls) != 0 {
		t.Errorf("exec calls = %q, want none", calls)
	}
}

// TestReloadRefusesABinaryThatFailsItsPreflight: a guildhall on PATH whose director start --check fails is
// refused: the supervisor reloads in-process, records the reload applied with the refusal, says so in its pane,
// and director status and the new session's version --startup show it.
func TestReloadRefusesABinaryThatFailsItsPreflight(t *testing.T) {
	dir, _ := supervisedCopy(t)
	rec := &execRecorder{}
	pane := &lockedBuffer{}
	done := superviseWith(t, dir, time.Hour, func(a *App) { a.Exec, a.Stdout = rec.exec, pane })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Every other command runs as guildhall does, so the session's own commands keep working.
	script := fmt.Sprintf("#!/bin/sh\ncase \" $* \" in *\" --check \"*) echo 'store: schema 2 is newer than this binary' >&2; exit 1;; esac\nexec %q \"$@\"\n", exe)
	path, sum := putOnPath(t, []byte(script))
	reloadAtTurnEnd(t, dir)
	got := waitSessions(t, dir, 2)
	own := testBinarySHA256(t)
	if got[0].Identity != got[1].Identity || got[1].BinarySHA256 != own {
		t.Errorf("sessions = %+v; want session 2 in the same supervisor on its own binary", got)
	}
	r := readReload(t, filepath.Join(dir, "director.store"))
	if r.State != store.ReloadApplied || r.RefusedSHA256 != sum || !strings.Contains(r.RefusedReason, "director start --check") ||
		!strings.Contains(r.RefusedReason, "schema 2 is newer") || r.OldSHA256 != own || r.NewSHA256 != own {
		t.Errorf("reload = %+v; want it applied with the refusal of %s", r, sum)
	}
	if line := "session: 1 reloads on this binary, refusing guildhall " + path + " " + shortSHA(sum) + ": director start --check"; !strings.Contains(pane.String(), line) {
		t.Errorf("pane = %s; want %q", pane, line)
	}

	app, stdout, stderr := newTestApp(nil, nil)
	app.LookPath = exec.LookPath
	if code := app.Run([]string{"director", "status", "--json", "--copy", dir}); code != 0 {
		t.Fatalf("status: exit %d, stderr %s", code, stderr)
	}
	var status directorStatus
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if rr := status.Session.Reload; rr == nil || rr.RefusedSHA256 != sum || rr.RefusedReason != r.RefusedReason {
		t.Errorf("status reload = %+v; want the refusal", rr)
	}
	if b := status.SupervisorBinary; b == nil || !b.Differs || b.SHA256 != own || b.PathSHA256 != sum || b.Path != path {
		t.Errorf("status supervisor_binary = %+v; want the supervisor's binary and the refused one on PATH, differing", b)
	}

	env := map[string]string{cwdKey: markCopy(t, dir), session.NumberEnv: "2"}
	_, out, _ := runVersion(t, testBuildInfo(nil), &fakeVersionGit{}, env, "--startup")
	if want := "warning: this session's supervisor refused to move onto guildhall " + shortSHA(sum) + " at its reload"; !strings.Contains(out, want) ||
		strings.Contains(out, "not the one running") {
		t.Errorf("version --startup = %s; want the refusal, and no mixed-version warning from the supervisor's own binary", out)
	}

	copyRun(t, dir, "director", "stop", "--now")
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d, want 0", code)
	}
	if calls := rec.recorded(); len(calls) != 0 {
		t.Errorf("exec calls = %q, want none for a refused binary", calls)
	}
}

// TestReloadFallsBackWhenTheExecFails: an exec that returns an error leaves the supervisor reloading in-process
// with its skills installed again, the reload recorded applied with the refusal.
func TestReloadFallsBackWhenTheExecFails(t *testing.T) {
	dir, _ := supervisedCopy(t)
	rec := &execRecorder{err: errors.New("exec format error")}
	var skillsDir string
	done := superviseWith(t, dir, time.Hour, func(a *App) {
		a.Exec = rec.exec
		install := a.InstallSkills
		a.InstallSkills = func(d string) error { skillsDir = d; return install(d) }
	})
	_, sum := putOnPath(t, differentBinary(t))
	reloadAtTurnEnd(t, dir)
	waitSessions(t, dir, 2)
	r := readReload(t, filepath.Join(dir, "director.store"))
	if r.State != store.ReloadApplied || r.RefusedSHA256 != sum || !strings.Contains(r.RefusedReason, "exec format error") {
		t.Errorf("reload = %+v; want it applied with the failed exec's refusal", r)
	}
	if _, err := os.Stat(skillsDir); err != nil {
		t.Errorf("the session's skills %s after the failed exec: %v; want them installed again", skillsDir, err)
	}
	copyRun(t, dir, "director", "stop", "--now")
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d, want 0", code)
	}
	if calls := rec.recorded(); len(calls) != 1 {
		t.Errorf("exec calls = %q, want one", calls)
	}
}

// TestReloadReexecsInPlace runs the supervisor as its own process: a reload with another guildhall on PATH
// replaces its image, and the new image records session 2 in the same process, with the new binary's sha256, and
// the reload applied from the old binary to the new.
func TestReloadReexecsInPlace(t *testing.T) {
	dir, _ := supervisedCopy(t)
	old, err := exec.LookPath("guildhall")
	if err != nil {
		t.Fatal(err)
	}
	stdin, typing, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = typing.Close() })
	var pane lockedBuffer
	cmd := exec.Command(old, "director", "start", "--copy", dir)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, &pane, &pane
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			t.Logf("killed the supervisor: %v", <-exited)
		}
	})
	waitSessions(t, dir, 1)
	oldSHA := testBinarySHA256(t)
	_, newSHA := putOnPath(t, differentBinary(t))
	reloadAtTurnEnd(t, dir)
	var got []store.DirectorSession
	for end := time.Now().Add(testenv.Ceiling(2 * time.Minute)); ; time.Sleep(50 * time.Millisecond) {
		if got = sessions(t, dir); len(got) == 2 && got[1].State == store.DirectorRunning {
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("the supervisor ended (%v) before session 2 ran; sessions = %+v; pane:\n%s", err, got, pane.String())
		default:
		}
		if len(got) > 2 || time.Now().After(end) {
			t.Fatalf("sessions = %+v; want session 2 running; pane:\n%s", got, pane.String())
		}
	}
	// Linux's identity, the boot id and the start time, survives an exec; elsewhere the process id is the one
	// guarantee, as the token is read from a start time the kernel may reset at an exec.
	if got[0].Identity.PID != cmd.Process.Pid || got[1].Identity.PID != cmd.Process.Pid ||
		runtime.GOOS == "linux" && got[0].Identity != got[1].Identity {
		t.Errorf("sessions = %+v; want both in the supervisor process %d; pane:\n%s", got, cmd.Process.Pid, pane.String())
	}
	if got[0].BinarySHA256 != oldSHA || got[1].BinarySHA256 != newSHA {
		t.Errorf("session binaries = %s, %s; want %s, then %s; pane:\n%s", got[0].BinarySHA256, got[1].BinarySHA256, oldSHA, newSHA, pane.String())
	}
	r := readReload(t, filepath.Join(dir, "director.store"))
	if r.State != store.ReloadApplied || r.OldSession != 1 || r.NewSession != 2 || r.OldSHA256 != oldSHA || r.NewSHA256 != newSHA || r.RefusedReason != "" {
		t.Errorf("reload = %+v; want it applied from %s to %s; pane:\n%s", r, oldSHA, newSHA, pane.String())
	}
	copyRun(t, dir, "director", "stop", "--now")
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("supervisor: %v; pane:\n%s", err, pane.String())
		}
	case <-time.After(testenv.Ceiling(2 * time.Minute)):
		t.Fatalf("the supervisor did not end after director stop; pane:\n%s", pane.String())
	}
}

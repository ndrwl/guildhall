package cli

import (
	"bytes"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// The resume command a pane registers starts with a plain command name, as
// Herdr requires, and its flags name the same attempt through this CLI.
func TestResumeArgvShape(t *testing.T) {
	at := launch.Attempt{Store: filepath.Join(t.TempDir(), "g.store"), StateDir: t.TempDir(), Key: artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 3}}
	argv := launch.ResumeArgv(at)
	if len(argv) < 3 || argv[0] != "guildhall" || argv[1] != "artisan" || argv[2] != "resume" {
		t.Fatalf("ResumeArgv = %q, want guildhall artisan resume ...", argv)
	}
	if strings.ContainsAny(argv[0], `/\`) {
		t.Errorf("resume command %q is a path", argv[0])
	}
	fl := flag.NewFlagSet("artisan resume", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var f attemptFlags
	f.register(fl)
	if err := fl.Parse(argv[3:]); err != nil || fl.NArg() != 0 {
		t.Fatalf("resume flags %q do not parse: %v, %q", argv[3:], err, fl.Args())
	}
	got, err := f.parse(nil)
	if err != nil || got != at {
		t.Errorf("resume flags name %+v (%v), want %+v", got, err, at)
	}
	o := newOwnedAttempt(t, "x")
	at.Store, at.StateDir, at.Key.Attempt = o.store, o.state, 1
	code, stdout, stderr := guildhall(t, "", launch.ResumeArgv(at)[1:]...)
	if r := decodeJSON[resumeReport](t, stdout); code != 0 || r.Owner != "none" {
		t.Errorf("the registered command: exit %d, %+v; stderr %q", code, r, stderr)
	}
}

type resumeReport struct {
	Owner          string             `json:"owner"`
	Swept          []artisan.Identity `json:"swept"`
	AttemptOutcome string             `json:"attempt_outcome"`
	Outcome        *struct {
		State string `json:"state"`
	} `json:"outcome"`
	Classification classification `json:"classification"`
}

// launcherProcess starts `artisan start` for o in its own process and waits
// until it owns the running attempt.
func launcherProcess(t *testing.T, o ownedAttempt) (*exec.Cmd, chan struct{}, *bytes.Buffer) {
	t.Helper()
	run := exec.Command(testExecutable(t), o.args("start")...)
	run.Env = append(os.Environ(), roleEnv+"=guildhall")
	var runErr bytes.Buffer
	run.Stderr = &runErr
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = run.Wait(); close(exited) }()
	t.Cleanup(func() {
		select {
		case <-exited:
		case <-time.After(testenv.Ceiling(30 * time.Second)):
			_ = run.Process.Kill()
			t.Errorf("the launcher did not exit; stderr %s", runErr.String())
		}
	})
	for end := time.Now().Add(testenv.Ceiling(30 * time.Second)); ; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("the launcher did not record a running attempt; stderr %s", runErr.String())
		}
		s, err := store.Open(o.store, store.Options{ReadOnly: true})
		if err != nil {
			continue
		}
		own, err := s.Ownership("app", "t-1", "build", 1)
		_ = s.Close()
		if err == nil && own.State == store.OwnerRunning && own.LauncherIdentity.PID == run.Process.Pid {
			return run, exited, &runErr
		}
	}
}

// inventoryOf waits for n processes recorded in inv and returns them.
func inventoryOf(t *testing.T, inv string, n int) []artisan.Identity {
	t.Helper()
	for end := time.Now().Add(testenv.Ceiling(30 * time.Second)); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		recs, _ := artisan.ReadProcessRecords(inv)
		var ids []artisan.Identity
		for _, r := range recs {
			ids = append(ids, r.Identity)
		}
		if len(ids) >= n {
			return ids
		}
	}
	t.Fatalf("waited for %d recorded processes in %s", n, inv)
	return nil
}

func TestArtisanResumeRefusesALiveLauncher(t *testing.T) {
	o := newOwnedAttempt(t, "guildhall-fake: result=never\nguildhall-fake: lifetime=60s\n")
	_, exited, _ := launcherProcess(t, o)
	code, stdout, stderr := guildhall(t, "", o.args("resume")...)
	if r := decodeJSON[resumeReport](t, stdout); code != 1 || r.Owner != "live" || len(r.Swept) != 0 {
		t.Errorf("resume of a live launcher: exit %d, %+v; stderr %q", code, r, stderr)
	}
	if code, stdout, stderr := guildhall(t, "", o.args("stop")...); code != 0 {
		t.Fatalf("stop: exit %d; %q; %q", code, stdout, stderr)
	}
	<-exited
}

// TestArtisanResumeSweepsAfterTheLauncherIsKilled is L13: a launcher killed
// outright leaves its worker tree to reconcile, and resume sweeps the
// recorded unit, settles the attempt failed and releases ownership.
func TestArtisanResumeSweepsAfterTheLauncherIsKilled(t *testing.T) {
	inv := t.TempDir()
	o := newOwnedAttempt(t, "guildhall-fake: result=never\nguildhall-fake: lifetime=60s\nguildhall-fake: grandchildren=1\nguildhall-fake: inventory="+inv+"\n")
	run, exited, _ := launcherProcess(t, o)
	tree := inventoryOf(t, inv, 2)
	t.Cleanup(func() {
		for _, id := range tree {
			_ = artisan.Kill(id)
		}
	})
	if err := run.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-exited
	code, stdout, stderr := guildhall(t, "", o.args("resume")...)
	r := decodeJSON[resumeReport](t, stdout)
	if code != 0 || r.Owner != "exited" || r.AttemptOutcome != "failed" || r.Outcome == nil || r.Outcome.State != "failed" {
		t.Fatalf("resume: exit %d, %+v; stderr %q", code, r, stderr)
	}
	for _, id := range tree {
		if lv, _ := artisan.Probe(id); lv != artisan.Gone {
			t.Errorf("%v is %v after resume", id, lv)
		}
	}
	if own := o.ownership(t); own.State != store.OwnerReleased {
		t.Errorf("ownership %+v not released", own)
	}
	code, stdout, stderr = guildhall(t, "", o.args("resume")...)
	if r := decodeJSON[resumeReport](t, stdout); code != 0 || r.Owner != "released" || r.AttemptOutcome != "" {
		t.Errorf("second resume: exit %d, %+v; stderr %q", code, r, stderr)
	}
}

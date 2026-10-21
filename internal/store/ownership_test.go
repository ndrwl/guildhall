package store

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

var (
	launcherA = ProcessIdentity{PID: 101, Token: "boot:1"}
	launcherB = ProcessIdentity{PID: 202, Token: "boot:2"}
)

func sampleOwnership(id ProcessIdentity) Ownership {
	return Ownership{Repository: "app", TaskID: "t-1", Step: "build", LauncherIdentity: id}
}

// probeSays returns a probe answering lv and err, counting its calls.
func probeSays(lv Liveness, err error, calls *int) OwnerProbe {
	return func(Ownership) (Liveness, error) {
		*calls++
		return lv, err
	}
}

func noProbe(t *testing.T) OwnerProbe {
	return func(o Ownership) (Liveness, error) {
		t.Errorf("probe called for %+v; the take must not probe", o)
		return ProbeUnverified, nil
	}
}

// ownedAttempt creates task t-1 and starts attempt 1 of build owned by launcherA.
func ownedAttempt(t *testing.T, s *Store) {
	t.Helper()
	mustCreate(t, s, "m-task", sampleTask("app", "t-1"))
	if _, err := s.StartOwnedAttempt("m-start", sampleOwnership(launcherA)); err != nil {
		t.Fatalf("StartOwnedAttempt: %v", err)
	}
}

func takeB(n int) Ownership {
	o := sampleOwnership(launcherB)
	o.Attempt = n
	return o
}

func mustOwnership(t *testing.T, s *Store, n int) Ownership {
	t.Helper()
	o, err := s.Ownership("app", "t-1", "build", n)
	if err != nil {
		t.Fatalf("Ownership: %v", err)
	}
	return o
}

func TestStartOwnedAttemptRecordsAttemptAndOwnerInOneMutation(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-task", sampleTask("app", "t-1"))
	r, err := s.StartOwnedAttempt("m-start", sampleOwnership(launcherA))
	if err != nil {
		t.Fatal(err)
	}
	if r.Attempt != 1 || r.Record != "attempt/build/1" || r.Operation != "start-owned-attempt" {
		t.Fatalf("receipt = %+v", r)
	}
	want := sampleOwnership(launcherA)
	want.Attempt, want.State = 1, OwnerStarting
	if got := mustOwnership(t, s, 1); got != want {
		t.Fatalf("ownership = %+v, want %+v", got, want)
	}
	attempts, err := s.Attempts("app", "t-1")
	if err != nil || len(attempts) != 1 || attempts[0].Outcome != "" {
		t.Fatalf("attempts = %+v, %v", attempts, err)
	}
	if _, err := s.StartOwnedAttempt("m-again", sampleOwnership(launcherB)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("second owned attempt while one runs: err = %v, want ErrInvalid", err)
	}
}

func TestStartOwnedAttemptCrashWritesNeither(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-task", sampleTask("app", "t-1"))
	testHookBeforeCommit = func() error { return errors.New("crash") }
	_, err := s.StartOwnedAttempt("m-start", sampleOwnership(launcherA))
	testHookBeforeCommit = nil
	if err == nil {
		t.Fatal("StartOwnedAttempt succeeded through a failed commit")
	}
	if attempts, _ := s.Attempts("app", "t-1"); len(attempts) != 0 {
		t.Fatalf("attempts after rollback = %+v", attempts)
	}
	if _, err := s.Ownership("app", "t-1", "build", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ownership after rollback: err = %v, want ErrNotFound", err)
	}
	if r, err := s.StartOwnedAttempt("m-start", sampleOwnership(launcherA)); err != nil || r.Sequence != 2 || r.Attempt != 1 {
		t.Fatalf("retry after rollback = %+v, %v; want sequence 2, attempt 1", r, err)
	}
}

func TestTakeOwnershipRefusesLiveOwner(t *testing.T) {
	s, _ := newStore(t)
	ownedAttempt(t, s)
	calls := 0
	_, err := s.TakeOwnership("m-take", takeB(1), probeSays(ProbeAlive, nil, &calls))
	if !errors.Is(err, ErrOwnerLive) || calls != 1 {
		t.Fatalf("take from a live owner: err = %v after %d probes, want ErrOwnerLive after 1", err, calls)
	}
	if got := mustOwnership(t, s, 1); got.LauncherIdentity != launcherA {
		t.Fatalf("owner after refusal = %+v", got.LauncherIdentity)
	}
	if _, err := s.Receipt("m-take"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refused take left a receipt: %v", err)
	}
}

func TestTakeOwnershipRefusesUnknownOwner(t *testing.T) {
	cases := []struct {
		name string
		lv   Liveness
		err  error
	}{
		{"unverified", ProbeUnverified, nil},
		{"unverified with error", ProbeUnverified, errors.New("permission denied")},
		{"gone with error", ProbeGone, errors.New("partial read")},
		{"unrecognised liveness", Liveness("stale"), nil},
		{"no verdict", Liveness(""), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStore(t)
			ownedAttempt(t, s)
			calls := 0
			if _, err := s.TakeOwnership("m-take", takeB(1), probeSays(tc.lv, tc.err, &calls)); !errors.Is(err, ErrOwnerUnknown) {
				t.Fatalf("err = %v, want ErrOwnerUnknown", err)
			}
			if got := mustOwnership(t, s, 1); got.LauncherIdentity != launcherA {
				t.Fatalf("owner after refusal = %+v", got.LauncherIdentity)
			}
		})
	}
}

// TestStoreDoesNotImportArtisan pins the dependency direction: owner identity
// and probe verdicts are store data, and the OS probe comes from the caller.
func TestStoreDoesNotImportArtisan(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	out, err := exec.Command(gobin, "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == "github.com/ndrwl/guildhall/internal/artisan" {
			t.Fatalf("internal/store depends on %s", dep)
		}
	}
}

// TestTakeOwnershipRequiresProbe: a nil probe is refused with ErrInvalid and
// writes nothing, whether or not the attempt has an owner.
func TestTakeOwnershipRequiresProbe(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-task", sampleTask("app", "t-1"))
	if _, err := s.StartAttempt("m-start-unowned", "app", "t-1", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TakeOwnership("m-take-unowned", takeB(1), nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("take of an unowned attempt without a probe: %v; want ErrInvalid", err)
	}
	if _, err := s.Ownership("app", "t-1", "build", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("refused take recorded an owner: %v", err)
	}
	if _, err := s.FinishAttempt("m-finish", "app", "t-1", "build", 1, AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartOwnedAttempt("m-start-owned", sampleOwnership(launcherA)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TakeOwnership("m-take-owned", takeB(2), nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("take of an owned attempt without a probe: %v; want ErrInvalid", err)
	}
	if got := mustOwnership(t, s, 2); got.LauncherIdentity != launcherA {
		t.Errorf("owner after refusal = %+v", got.LauncherIdentity)
	}
	for _, m := range []string{"m-take-unowned", "m-take-owned"} {
		if _, err := s.Receipt(m); !errors.Is(err, ErrNotFound) {
			t.Errorf("refused take %s left a receipt: %v", m, err)
		}
	}
}

// TestProbeLauncherNeverClassifiesSurvivorsGone: with containment recorded the
// identity probe is not consulted and the verdict is ProbeUnverified, even when
// the identity probe would call every process gone; without containment the
// recorded launcher's verdict is returned unchanged.
func TestProbeLauncherNeverClassifiesSurvivorsGone(t *testing.T) {
	var probed []ProcessIdentity
	identity := func(id ProcessIdentity) (Liveness, error) {
		probed = append(probed, id)
		return ProbeGone, nil
	}
	contained := sampleOwnership(launcherA)
	contained.Containment = Containment{Kind: ContainmentGroup, Worker: worker}
	if lv, err := ProbeLauncher(identity)(contained); lv != ProbeUnverified || err != nil || len(probed) != 0 {
		t.Errorf("contained owner = %q, %v after probing %v; want unverified without probing", lv, err, probed)
	}
	if lv, err := ProbeLauncher(identity)(sampleOwnership(launcherA)); lv != ProbeGone || err != nil || len(probed) != 1 || probed[0] != launcherA {
		t.Errorf("uncontained owner = %q, %v after probing %v; want gone from probing %v", lv, err, probed, launcherA)
	}
}

func TestTakeOwnershipAfterProvenExit(t *testing.T) {
	s, _ := newStore(t)
	ownedAttempt(t, s)
	running := sampleOwnership(launcherA)
	running.Attempt, running.State = 1, OwnerRunning
	running.Containment, running.Endpoint = Containment{Kind: ContainmentGroup, Worker: worker}, "/state/a1/ctl.sock"
	if _, err := s.UpdateOwnership("m-run", running); err != nil {
		t.Fatal(err)
	}
	var probed Ownership
	r, err := s.TakeOwnership("m-take", takeB(1), func(o Ownership) (Liveness, error) {
		probed = o
		return ProbeGone, nil
	})
	if err != nil {
		t.Fatalf("take from a proven-exited owner: %v", err)
	}
	if probed != running {
		t.Fatalf("probe saw %+v, want the recorded owner %+v", probed, running)
	}
	if r.Record != "owner/build/1" || r.Attempt != 1 || r.Operation != "take-ownership" {
		t.Fatalf("receipt = %+v", r)
	}
	want := takeB(1)
	want.State = OwnerStarting
	if got := mustOwnership(t, s, 1); got != want {
		t.Fatalf("ownership = %+v, want %+v", got, want)
	}
}

func TestTakeOwnershipOfUnownedAttemptDoesNotProbe(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-task", sampleTask("app", "t-1"))
	if _, err := s.StartAttempt("m-start", "app", "t-1", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TakeOwnership("m-take", takeB(1), noProbe(t)); err != nil {
		t.Fatal(err)
	}
	if got := mustOwnership(t, s, 1); got.LauncherIdentity != launcherB || got.State != OwnerStarting {
		t.Fatalf("ownership = %+v", got)
	}
}

func TestTakeOwnershipRefusesMissingAndFinishedAttempts(t *testing.T) {
	s, _ := newStore(t)
	ownedAttempt(t, s)
	if _, err := s.TakeOwnership("m-take-2", takeB(2), noProbe(t)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("take of a missing attempt: err = %v, want ErrNotFound", err)
	}
	if _, err := s.FinishAttempt("m-finish", "app", "t-1", "build", 1, AttemptFailed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TakeOwnership("m-take-1", takeB(1), noProbe(t)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("take of a finished attempt: err = %v, want ErrInvalid", err)
	}
}

func TestFinishAttemptReleasesOwnership(t *testing.T) {
	s, _ := newStore(t)
	ownedAttempt(t, s)
	if _, err := s.FinishAttempt("m-finish", "app", "t-1", "build", 1, AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	got := mustOwnership(t, s, 1)
	if got.State != OwnerReleased || got.LauncherIdentity != launcherA {
		t.Fatalf("ownership after finish = %+v, want released and still naming its launcher", got)
	}
	released := got
	released.State = OwnerRunning
	if _, err := s.UpdateOwnership("m-update", released); !errors.Is(err, ErrInvalid) {
		t.Fatalf("update of a released record: err = %v, want ErrInvalid", err)
	}
	// An attempt finished without an owner still finishes.
	if _, err := s.StartAttempt("m-start-2", "app", "t-1", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishAttempt("m-finish-2", "app", "t-1", "build", 2, AttemptFailed); err != nil {
		t.Fatalf("finish of an unowned attempt: %v", err)
	}
	if _, err := s.Ownership("app", "t-1", "build", 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unowned attempt gained an owner: %v", err)
	}
}

func TestTakeOwnershipReplaysByMutationID(t *testing.T) {
	s, _ := newStore(t)
	ownedAttempt(t, s)
	calls := 0
	first, err := s.TakeOwnership("m-take", takeB(1), probeSays(ProbeGone, nil, &calls))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishAttempt("m-finish", "app", "t-1", "build", 1, AttemptStopped); err != nil {
		t.Fatal(err)
	}
	again, err := s.TakeOwnership("m-take", takeB(1), noProbe(t))
	if err != nil || again != first {
		t.Fatalf("replay = %+v, %v; want the original receipt %+v", again, err, first)
	}
	if got := mustOwnership(t, s, 1); got.State != OwnerReleased {
		t.Fatalf("replay changed the record: %+v", got)
	}
	other := takeB(1)
	other.LauncherIdentity = launcherA
	if _, err := s.TakeOwnership("m-take", other, noProbe(t)); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("same id, different owner: err = %v, want ErrMutationConflict", err)
	}
	if calls != 1 {
		t.Fatalf("probe ran %d times, want once", calls)
	}
}

func TestUpdateOwnership(t *testing.T) {
	s, _ := newStore(t)
	ownedAttempt(t, s)
	running := sampleOwnership(launcherA)
	running.Attempt, running.State = 1, OwnerRunning
	running.Containment, running.Endpoint = Containment{Kind: "job", Name: "guildhall-a1"}, `C:\state\a1\ctl.sock`
	r, err := s.UpdateOwnership("m-run", running)
	if err != nil {
		t.Fatal(err)
	}
	if r.Record != "owner/build/1" || r.Operation != "update-ownership" {
		t.Fatalf("receipt = %+v", r)
	}
	if got := mustOwnership(t, s, 1); got != running {
		t.Fatalf("ownership = %+v, want %+v", got, running)
	}
	back := running
	back.State = OwnerStarting
	if _, err := s.UpdateOwnership("m-back", back); !errors.Is(err, ErrInvalid) {
		t.Fatalf("running back to starting: err = %v, want ErrInvalid", err)
	}
	stranger := running
	stranger.LauncherIdentity = launcherB
	if _, err := s.UpdateOwnership("m-stranger", stranger); !errors.Is(err, ErrOwnerLive) {
		t.Fatalf("update by another launcher: err = %v, want ErrOwnerLive", err)
	}
	reused := running
	reused.LauncherIdentity.Token = "boot:other"
	if _, err := s.UpdateOwnership("m-reused", reused); !errors.Is(err, ErrOwnerLive) {
		t.Fatalf("update by a reused PID: err = %v, want ErrOwnerLive", err)
	}
	missing := running
	missing.Attempt = 2
	if _, err := s.UpdateOwnership("m-missing", missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of a missing record: err = %v, want ErrNotFound", err)
	}
}

func TestUpdateOwnershipKeepsRecoveryEvidence(t *testing.T) {
	s, _ := newStore(t)
	ownedAttempt(t, s)
	running := sampleOwnership(launcherA)
	running.Attempt, running.State = 1, OwnerRunning
	running.Containment = Containment{Kind: ContainmentGroup, Worker: ProcessIdentity{PID: 4242, Token: "boot:w"}}
	if _, err := s.UpdateOwnership("m-contained", running); err != nil {
		t.Fatal(err)
	}
	running.Endpoint = "/state/a1/ctl.sock"
	if _, err := s.UpdateOwnership("m-endpoint", running); err != nil {
		t.Fatalf("setting the endpoint from empty: %v", err)
	}
	heartbeat, err := s.UpdateOwnership("m-heartbeat", running)
	if err != nil {
		t.Fatalf("an update carrying equal evidence: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Ownership)
	}{
		{"clearing containment", func(o *Ownership) { o.Containment = Containment{} }},
		{"changing its kind", func(o *Ownership) { o.Containment.Kind = ContainmentSubreaper }},
		{"changing the worker", func(o *Ownership) { o.Containment.Worker = ProcessIdentity{PID: 4343, Token: "boot:x"} }},
		{"clearing the endpoint", func(o *Ownership) { o.Endpoint = "" }},
		{"changing the endpoint", func(o *Ownership) { o.Endpoint = "/state/a1/other.sock" }},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			update := running
			tc.mutate(&update)
			mutation := fmt.Sprintf("m-erase-%d", i)
			if _, err := s.UpdateOwnership(mutation, update); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if got := mustOwnership(t, s, 1); got != running {
				t.Fatalf("refused update changed the record: %+v; want %+v", got, running)
			}
			if _, err := s.Receipt(mutation); !errors.Is(err, ErrNotFound) {
				t.Fatalf("refused update left a receipt: %v", err)
			}
		})
	}
	if again, err := s.UpdateOwnership("m-heartbeat", running); err != nil || again != heartbeat {
		t.Fatalf("replay = %+v, %v; want the original receipt %+v", again, err, heartbeat)
	}
}

func TestOwnershipValidates(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Ownership)
	}{
		{"bad repository", func(o *Ownership) { o.Repository = "App" }},
		{"bad task", func(o *Ownership) { o.TaskID = "" }},
		{"bad step", func(o *Ownership) { o.Step = "Build" }},
		{"no pid", func(o *Ownership) { o.LauncherIdentity.PID = 0 }},
		{"negative pid", func(o *Ownership) { o.LauncherIdentity.PID = -1 }},
		{"blank token", func(o *Ownership) { o.LauncherIdentity.Token = " " }},
		{"long token", func(o *Ownership) { o.LauncherIdentity.Token = strings.Repeat("t", 257) }},
		{"released state", func(o *Ownership) { o.State = OwnerReleased }},
		{"running state", func(o *Ownership) { o.State = OwnerRunning }},
		{"unknown state", func(o *Ownership) { o.State = "paused" }},
		{"containment without name", func(o *Ownership) { o.Containment = Containment{Kind: "job"} }},
		{"containment without kind", func(o *Ownership) { o.Containment = Containment{Name: "4242"} }},
		{"long endpoint", func(o *Ownership) { o.Endpoint = strings.Repeat("e", MaxEndpointBytes+1) }},
		{"invalid endpoint", func(o *Ownership) { o.Endpoint = "\xff" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStore(t)
			mustCreate(t, s, "m-task", sampleTask("app", "t-1"))
			start := sampleOwnership(launcherA)
			tc.mutate(&start)
			if _, err := s.StartOwnedAttempt("m-owned", start); !errors.Is(err, ErrInvalid) {
				t.Fatalf("StartOwnedAttempt: err = %v, want ErrInvalid", err)
			}
			if _, err := s.StartAttempt("m-start", "app", "t-1", "build"); err != nil {
				t.Fatal(err)
			}
			take := start
			take.Attempt = 1
			if _, err := s.TakeOwnership("m-take", take, noProbe(t)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("TakeOwnership: err = %v, want ErrInvalid", err)
			}
		})
	}
	s, _ := newStore(t)
	numbered := sampleOwnership(launcherA)
	numbered.Attempt = 1
	if _, err := s.StartOwnedAttempt("m-owned", numbered); !errors.Is(err, ErrInvalid) {
		t.Fatalf("StartOwnedAttempt with a preset number: err = %v, want ErrInvalid", err)
	}
	if _, err := s.TakeOwnership("m-take", sampleOwnership(launcherA), noProbe(t)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("TakeOwnership of attempt 0: err = %v, want ErrInvalid", err)
	}
	update := numbered
	update.State = OwnerReleased
	if _, err := s.UpdateOwnership("m-update", update); !errors.Is(err, ErrInvalid) {
		t.Fatalf("UpdateOwnership to released: err = %v, want ErrInvalid", err)
	}
}

func TestConcurrentTakeOversHaveOneWinner(t *testing.T) {
	s, _ := newStore(t)
	ownedAttempt(t, s)
	// The dead owner is launcherA; every other identity is live.
	probe := func(o Ownership) (Liveness, error) {
		if o.LauncherIdentity == launcherA {
			return ProbeGone, nil
		}
		return ProbeAlive, nil
	}
	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			o := takeB(1)
			o.LauncherIdentity = ProcessIdentity{PID: 1000 + i, Token: "boot:x"}
			_, errs[i] = s.TakeOwnership(fmt.Sprintf("m-take-%d", i), o, probe)
		})
	}
	wg.Wait()
	won := 0
	for i, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, ErrOwnerLive):
			t.Errorf("take %d: err = %v, want nil or ErrOwnerLive", i, err)
		}
	}
	if won != 1 {
		t.Fatalf("%d take-overs succeeded, want exactly 1", won)
	}
}

func TestOwnershipNotFound(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Ownership("app", "t-1", "build", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDoctorCountsOwnershipRecords(t *testing.T) {
	s, _ := newStore(t)
	ownedAttempt(t, s)
	rep, err := s.Doctor()
	if err != nil || len(rep.Problems) != 0 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
	if len(rep.Repositories) != 1 || rep.Repositories[0].Owners != 1 {
		t.Fatalf("repositories = %+v, want one owner", rep.Repositories)
	}
}

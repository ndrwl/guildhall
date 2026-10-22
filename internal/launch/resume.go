package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/reconcile"
	"github.com/ndrwl/guildhall/internal/store"
)

// Owner states a ResumeReport names.
const (
	// OwnerNone is an attempt with no ownership record.
	OwnerNone = "none"
	// OwnerReleased is an attempt whose ownership record is released.
	OwnerReleased = "released"
	// OwnerLive is an owner whose launcher identity is alive.
	OwnerLive = "live"
	// OwnerUnverified is an owner that cannot be proven exited.
	OwnerUnverified = "unverified"
	// OwnerExited is an owner whose launcher identity is proven gone.
	OwnerExited = "exited"
)

// SweepTimeout bounds how long Resume repeats artisan.Sweep for the
// recorded unit to empty.
const SweepTimeout = 10 * time.Second

// ResumeReport is what Resume found and did.
type ResumeReport struct {
	// Attempt is the attempt key, "<repository>/<task>/<step>/<n>".
	Attempt string `json:"attempt"`
	// Owner is one of the Owner constants, as found before anything was done.
	Owner string `json:"owner"`
	// Swept lists the contained processes the sweep found and killed.
	Swept []artisan.Identity `json:"swept,omitempty"`
	// Outcome is the attempt's decision once settled.
	Outcome *artisan.Outcome `json:"outcome,omitempty"`
	// AttemptOutcome is what FinishAttempt recorded; empty when nothing was
	// recorded.
	AttemptOutcome store.AttemptOutcome `json:"attempt_outcome,omitempty"`
	// Classification is the attempt's classification afterwards.
	Classification reconcile.Classification `json:"classification"`
}

// Resume reconciles an attempt whose launcher has exited, such as after its
// terminal or the Herdr server restarted: it is what a pane registers to
// run after a restart (ResumeArgv), and it never starts a worker.
//
//  1. With no ownership record, or a released one, there is nothing to
//     resume: it returns the classification and no error.
//  2. A live launcher identity is refused with store.ErrOwnerLive, and one
//     that cannot be proven gone, or a record without containment, with
//     store.ErrOwnerUnknown; nothing is signalled or written. A named unit
//     (cgroup, job) is recorded when ownership is taken, so only a process
//     group's owner gone before its worker was recorded is refused for that
//     (docs/artisan.md#limits).
//  3. With the launcher proven gone it sweeps the recorded unit with
//     artisan.Sweep until artisan.Contained proves it empty, within ctx and
//     SweepTimeout; a unit that does not empty returns artisan.ErrNotEmpty
//     with the survivors left owned.
//  4. Under the attempt lock it checks the record still names that launcher
//     and is unreleased, settles the attempt from its files with
//     artisan.Settle (an existing outcome.json is kept; otherwise an owner
//     still starting fails with "launcher exited before its worker was
//     recorded" and any other with "launcher exited without a decision"),
//     and records the store outcome with FinishAttempt, which releases the
//     ownership. A record that changed meanwhile, such as one a takeover or
//     another Resume wrote, is left to its new writer.
//
// Resume is idempotent: its store write has a mutation id derived from the
// attempt and the gone launcher's identity, so a Resume interrupted after
// outcome.json was written finishes the same outcome when run again, and a
// Resume after a finished one finds the record released.
func Resume(ctx context.Context, a Attempt) (ResumeReport, error) {
	rep := ResumeReport{Attempt: a.Key.String()}
	classify := func(err error) (ResumeReport, error) {
		c, cerr := Status(ctx, a)
		rep.Classification = c
		return rep, errors.Join(err, cerr)
	}
	o, err := a.owner()
	switch {
	case err != nil:
		return rep, err
	case o == nil:
		rep.Owner = OwnerNone
		return classify(nil)
	case o.State == store.OwnerReleased:
		rep.Owner = OwnerReleased
		return classify(nil)
	}
	w := reconcile.ProbeOwner(*o)
	switch {
	case w.Launcher == artisan.Alive:
		rep.Owner = OwnerLive
		return classify(fmt.Errorf("launch: resume %s: %w", a.Key, store.ErrOwnerLive))
	case w.Launcher != artisan.Gone || w.ProbeErr != nil:
		rep.Owner = OwnerUnverified
		return classify(fmt.Errorf("launch: resume %s: %w: %v", a.Key, store.ErrOwnerUnknown, w.ProbeErr))
	case !w.Recorded:
		rep.Owner = OwnerUnverified
		return classify(fmt.Errorf("launch: resume %s: %w: no containment recorded", a.Key, store.ErrOwnerUnknown))
	}
	rep.Owner = OwnerExited
	unit, worker := w.Unit, w.Worker
	if err := sweepUnit(ctx, *o, &rep); err != nil {
		return classify(fmt.Errorf("launch: resume %s: %w", a.Key, err))
	}
	err = a.settle(ctx, *o, &rep)
	// Sweeping the emptied unit again removes what it leaves, such as a cgroup.
	_, _ = artisan.Sweep(unit, worker)
	if err != nil {
		err = fmt.Errorf("launch: resume %s: %w", a.Key, err)
	}
	return classify(err)
}

// sweepUnit repeats artisan.Sweep until the owner-gone rule
// (reconcile.ProbeOwner) proves the owner o's unit empty, within ctx and
// SweepTimeout, recording what it swept.
func sweepUnit(ctx context.Context, o store.Ownership, rep *ResumeReport) error {
	ctx, cancel := context.WithTimeout(ctx, SweepTimeout)
	defer cancel()
	for {
		w := reconcile.ProbeOwner(o)
		switch {
		case w.Gone():
			return nil
		case w.UnitErr != nil:
			return w.UnitErr
		case w.Launcher != artisan.Gone:
			return fmt.Errorf("%w: launcher is %v: %v", store.ErrOwnerUnknown, w.Launcher, w.ProbeErr)
		}
		swept, err := artisan.Sweep(w.Unit, w.Worker)
		for _, id := range swept {
			if !slices.Contains(rep.Swept, id) {
				rep.Swept = append(rep.Swept, id)
			}
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %d contained processes survive the sweep: %v", artisan.ErrNotEmpty, len(w.Survivors), w.Survivors)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// settle decides the attempt and records it in the store under the attempt
// lock, while the record still names the gone owner o.
func (a Attempt) settle(ctx context.Context, o store.Ownership, rep *ResumeReport) error {
	if err := os.MkdirAll(a.Dir(), 0o700); err != nil {
		return err
	}
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	release, err := artisan.LockAttempt(lockCtx, a.Dir())
	if err != nil {
		return err
	}
	defer release()
	if now, err := a.owner(); err != nil || now == nil || *now != o {
		return err
	}
	detail := "launcher exited without a decision"
	if o.State == store.OwnerStarting {
		detail = "launcher exited before its worker was recorded"
	}
	out, err := artisan.Settle(a.Dir(), a.Files(), detail)
	if err != nil {
		return err
	}
	rep.Outcome = &out
	var r *adapter.Result
	if out.State == artisan.Finished {
		res, err := a.Files().Result()
		if err != nil {
			return err
		}
		r = &res
	}
	outcome, err := attemptOutcome(out, r)
	if err != nil {
		return err
	}
	k := a.Key
	mutation := "resume-finish:" + attemptTag(k, artisan.Identity(o.LauncherIdentity))
	if err := a.write(func(s *store.Store) error {
		_, err := s.FinishAttempt(mutation, k.Repository, k.TaskID, k.Step, k.Attempt, outcome)
		return err
	}); err != nil {
		return err
	}
	rep.AttemptOutcome = outcome
	return nil
}

// ResumeCommand is the program name ResumeArgv starts with: Herdr refuses a
// resume_argv whose first element is a path, so the guildhall on the pane's
// PATH runs it.
const ResumeCommand = "guildhall"

// ResumeArgv is the command that resumes attempt a after a restart,
// `guildhall artisan resume` with the attempt's flags, for registering with
// a terminal multiplexer such as Herdr's pane report-agent. The store and
// state paths are as given to the launcher, which uses absolute ones.
func ResumeArgv(a Attempt) []string {
	return []string{ResumeCommand, "artisan", "resume", "--store", a.Store, "--state", a.StateDir,
		"--repo", a.Key.Repository, "--task", a.Key.TaskID, "--step", a.Key.Step, "--attempt", strconv.Itoa(a.Key.Attempt)}
}

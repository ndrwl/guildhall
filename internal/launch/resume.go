package launch

import (
	"context"
	"errors"
	"strconv"
	"time"

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
//     store.ErrOwnerUnknown; nothing is signalled or written.
//  3. With the launcher proven gone it sweeps the recorded unit with
//     artisan.Sweep until artisan.Contained proves it empty, within ctx and
//     SweepTimeout; a unit that does not empty returns artisan.ErrNotEmpty
//     with the survivors left owned.
//  4. Under the attempt lock it checks the record still names that launcher
//     and is unreleased, settles the attempt from its files with
//     artisan.Settle (an existing outcome.json is kept), and records the store
//     outcome with FinishAttempt, which releases the ownership. A record that
//     changed meanwhile, such as one a takeover or another Resume wrote, is
//     left to its new writer.
//
// Resume is idempotent: its store write has a mutation id derived from the
// attempt and the gone launcher's identity, so a Resume interrupted after
// outcome.json was written finishes the same outcome when run again, and a
// Resume after a finished one finds the record released.
func Resume(ctx context.Context, a Attempt) (ResumeReport, error) {
	return ResumeReport{Attempt: a.Key.String()}, errors.ErrUnsupported
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
	_ = strconv.Itoa
	return nil
}

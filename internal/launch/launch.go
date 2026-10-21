// Package launch composes one owned Artisan attempt from the adapter's files,
// the containment unit and stop ladder of artisan, the Director store's
// ownership record and reconcile's classification (docs/artisan.md#commands).
// It is what `guildhall artisan run`, `stop` and `status` call.
//
// Run owns and runs one attempt the store already records as running:
//
//  1. it takes the attempt's ownership for this process's identity with
//     store.TakeOwnership, so a live or unproven owner refuses it
//     (store.ErrOwnerLive, store.ErrOwnerUnknown) before anything starts;
//  2. it binds the attempt's control endpoint (artisan.Listen) and records
//     its path;
//  3. an artisan.Launcher starts the worker in a fresh artisan unit, and once
//     the unit reports the worker the containment is recorded in
//     containment.json and in the ownership record, with state running;
//  4. it supervises until the launcher decides the attempt and the unit is
//     empty; ctx ending, the endpoint's stop and a relayed stop signal each
//     request the one stop ladder;
//  5. it records the store outcome with FinishAttempt, translated by one
//     rule (attemptOutcome), which also releases the ownership.
//
// When survivors outlive the last rung (artisan.ErrNotEmpty) the attempt is
// left running and owned, so reconcile sees the survivors once this process
// is gone; outcome.json still holds the decision.
//
// The store is opened for each record operation and closed before the next,
// never held while the worker runs, so other commands read and write it
// meanwhile (custody: docs/artisan.md#commands). Ownership is durable state,
// not a held handle: closing the store releases nothing.
//
// Stop asks the attempt's owner to stop it through the recorded endpoint and
// otherwise reconciles; it never signals a process itself. Status classifies.
package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/reconcile"
	"github.com/ndrwl/guildhall/internal/store"
)

// ResultFile is the result file's name in the attempt directory.
const ResultFile = "result.json"

// ErrNotStopped reports that after Stop the attempt may still be running:
// its classification is running, stuck or unknown.
var ErrNotStopped = errors.New("attempt is not proven stopped")

// Attempt names one owned stage attempt: the Director store recording its
// ownership, the absolute state directory holding its files and endpoint,
// and its key.
type Attempt struct {
	Store    string
	StateDir string
	Key      artisan.AttemptKey
}

// Dir is the attempt directory, <StateDir>/<repository>/<task>/<step>-<n>,
// as the interpreter prepares it.
func (a Attempt) Dir() string {
	k := a.Key
	return filepath.Join(a.StateDir, k.Repository, k.TaskID, fmt.Sprintf("%s-%d", k.Step, k.Attempt))
}

// Identity is the adapter identity the attempt's result must name: its task
// and "<step>-<n>".
func (a Attempt) Identity() adapter.Identity {
	return adapter.Identity{TaskID: a.Key.TaskID, AttemptID: fmt.Sprintf("%s-%d", a.Key.Step, a.Key.Attempt)}
}

// Files returns the attempt's result file and cancel marker.
func (a Attempt) Files() Files {
	return Files{ResultFile: filepath.Join(a.Dir(), ResultFile), Identity: a.Identity()}
}

// Files is the real artisan.AttemptFiles: the adapter's result file, parsed
// with adapter.ParseResult (identity-bound and version-checked, as
// adapter.ReadResult parses), and its cancel marker, the adapter's
// `.cancelled` sidecar (adapter.Cancelled, adapter.MarkCancelled).
// Launcher and reconcile both read through it, so marker and result have one
// reader each.
type Files struct {
	ResultFile string
	Identity   adapter.Identity
}

var _ artisan.AttemptFiles = Files{}

// ValidResult reports whether a valid result for the attempt is published,
// whether or not the marker is present, so a late result is still seen.
// A missing, malformed, other-attempt or newer-version result is false with
// no error; a result that cannot be read is an error.
func (f Files) ValidResult() (bool, error) {
	return false, errors.New("launch: not implemented")
}

// Result reads the published result; a missing one matches fs.ErrNotExist and
// an invalid one is adapter's error.
func (f Files) Result() (adapter.Result, error) {
	return adapter.Result{}, errors.New("launch: not implemented")
}

// Marked reports whether the attempt's cancel marker is present.
func (f Files) Marked() (bool, error) {
	return false, errors.New("launch: not implemented")
}

// Mark writes the attempt's cancel marker; marking a marked attempt changes
// nothing.
func (f Files) Mark() error {
	return errors.New("launch: not implemented")
}

// attemptOutcome is the one translation of how an attempt ended into the
// store's outcome: cancelled is stopped, failed is failed, and finished is the
// result's status, succeeded or failed. A finished outcome without a result,
// or with a status the store does not record, is refused.
func attemptOutcome(o artisan.Outcome, r *adapter.Result) (store.AttemptOutcome, error) {
	return "", errors.New("launch: not implemented")
}

// Config is one run of an owned attempt.
type Config struct {
	Attempt
	// Command is the worker, such as the adapter's command for the attempt;
	// its Env entries are added to this process's environment.
	Command adapter.Command
	// Stdin, Stdout and Stderr are the worker's; nil is the null device. The
	// command line passes its own, so the worker owns the pane's terminal.
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	// Signals relays artisan.StopSignals to the launcher; nil relays none.
	Signals <-chan os.Signal
	// Ladder, ResultGrace and HeartbeatInterval tune the launcher; zero values
	// are its defaults.
	Ladder            artisan.Ladder
	ResultGrace       time.Duration
	HeartbeatInterval time.Duration
}

// Report is how an owned attempt ended.
type Report struct {
	// Attempt is the attempt key, "<repository>/<task>/<step>/<n>".
	Attempt string          `json:"attempt"`
	Outcome artisan.Outcome `json:"outcome"`
	// AttemptOutcome is what FinishAttempt recorded; empty when the attempt
	// was left running because survivors remain.
	AttemptOutcome store.AttemptOutcome `json:"attempt_outcome,omitempty"`
	// Result is the accepted result of a finished attempt.
	Result *adapter.Result `json:"result,omitempty"`
}

// Run owns, runs and records attempt c.Attempt as the package comment
// describes, returning how it ended. A refused ownership, endpoint or start
// is an error with nothing left running; artisan.ErrNotEmpty reports
// survivors left owned for reconcile.
func Run(ctx context.Context, c Config) (Report, error) {
	return Report{}, errors.New("launch: not implemented")
}

// StopReport is what Stop did and the attempt's classification afterwards.
type StopReport struct {
	// Endpoint is "stopped" when the owner's endpoint answered the stop, and
	// otherwise why it was not asked or did not answer.
	Endpoint       string                   `json:"endpoint"`
	Classification reconcile.Classification `json:"classification"`
}

// Stop asks the attempt's owner, through the endpoint its unreleased
// ownership record names, to stop it, waiting within ctx for the stop
// ladder's end. Without a record, endpoint or answer it only classifies:
// it never signals a process. Either way the report carries the
// classification afterwards, and the error is ErrNotStopped when that is
// running, stuck or unknown.
func Stop(ctx context.Context, a Attempt) (StopReport, error) {
	return StopReport{}, errors.New("launch: not implemented")
}

// Status classifies the attempt with reconcile.Classify from its files and
// ownership record; the error reports only a store that cannot be read.
func Status(ctx context.Context, a Attempt) (reconcile.Classification, error) {
	return reconcile.Classification{}, errors.New("launch: not implemented")
}

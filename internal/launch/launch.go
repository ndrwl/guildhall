// Package launch composes one owned Artisan attempt from the adapter's files,
// the containment unit and stop ladder of artisan, the Director store's
// ownership record and reconcile's classification (docs/artisan.md#commands).
// It is what `guildhall artisan run`, `stop` and `status` call.
//
// Run owns and runs one attempt the store already records as running:
//
//  1. it takes the attempt's ownership for this process's identity with
//     store.TakeOwnership and ProbeOwner, so a live or unproven owner refuses
//     it (store.ErrOwnerLive, store.ErrOwnerUnknown) before anything starts;
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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/reconcile"
	"github.com/ndrwl/guildhall/internal/store"
)

// ResultFile is the result file's name in the attempt directory.
const ResultFile = "result.json"

// ErrNotStopped reports that Stop could not verify termination.
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
	_, err := f.Result()
	var invalid *parseError
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist), errors.As(err, &invalid):
		return false, nil
	}
	return false, err
}

// parseError is a result that was read but is not valid for the attempt.
type parseError struct{ err error }

func (e *parseError) Error() string { return e.err.Error() }
func (e *parseError) Unwrap() error { return e.err }

// Result reads the published result; a missing one matches fs.ErrNotExist and
// an invalid one is adapter's error.
func (f Files) Result() (adapter.Result, error) {
	fh, err := os.Open(f.ResultFile)
	if err != nil {
		return adapter.Result{}, err
	}
	data, err := io.ReadAll(io.LimitReader(fh, adapter.MaxResultBytes+1))
	// The file was opened read-only, so a close error loses nothing.
	_ = fh.Close()
	if err != nil {
		return adapter.Result{}, err
	}
	r, err := adapter.ParseResult(data, f.Identity)
	if err != nil {
		return adapter.Result{}, &parseError{err}
	}
	return r, nil
}

// Marked reports whether the attempt's cancel marker is present.
func (f Files) Marked() (bool, error) { return adapter.Cancelled(f.ResultFile, f.Identity) }

// Mark writes the attempt's cancel marker; marking a marked attempt changes
// nothing.
func (f Files) Mark() error { return adapter.MarkCancelled(f.ResultFile, f.Identity) }

// attemptOutcome is the one translation of how an attempt ended into the
// store's outcome: cancelled is stopped, failed is failed, and finished is the
// result's status, succeeded or failed. A finished outcome without a result,
// or with a status the store does not record, is refused.
func attemptOutcome(o artisan.Outcome, r *adapter.Result) (store.AttemptOutcome, error) {
	switch {
	case o.State == artisan.Cancelled:
		return store.AttemptStopped, nil
	case o.State == artisan.Failed:
		return store.AttemptFailed, nil
	case o.State != artisan.Finished:
		return "", fmt.Errorf("launch: outcome state %q is not an attempt's end", o.State)
	case r == nil:
		return "", errors.New("launch: a finished attempt has no result")
	case r.Status == adapter.StatusSucceeded:
		return store.AttemptSucceeded, nil
	case r.Status == adapter.StatusFailed:
		return store.AttemptFailed, nil
	}
	return "", fmt.Errorf("launch: result status %q is not an attempt outcome", r.Status)
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
	a, rep := c.Attempt, Report{Attempt: c.Key.String()}
	self, err := artisan.Self()
	if err != nil {
		return rep, err
	}
	if len(c.Command.Argv) == 0 {
		return rep, errors.New("launch: the worker command is empty")
	}
	k := a.Key
	own := store.Ownership{Repository: k.Repository, TaskID: k.TaskID, Step: k.Step, Attempt: k.Attempt, LauncherIdentity: store.ProcessIdentity(self), State: store.OwnerStarting}
	sum := sha256.Sum256([]byte(k.String() + "\n" + strconv.Itoa(self.PID) + "\n" + self.Token))
	tag := hex.EncodeToString(sum[:16])
	mutation := func(op string) string { return "launch-" + op + ":" + tag }
	path, err := artisan.EndpointPath(a.StateDir, k)
	if err != nil {
		return rep, err
	}
	if err := os.MkdirAll(a.Dir(), 0o700); err != nil {
		return rep, err
	}
	unit, err := artisan.NewUnit(tag[:24])
	if err != nil {
		return rep, err
	}
	defer func() { _ = unit.Close() }()
	recorded := &recordingUnit{Unit: unit, a: a, own: own, mutation: mutation("running")}
	l, err := artisan.NewLauncher(artisan.LauncherConfig{Dir: a.Dir(), Unit: recorded, Files: a.Files(), Ladder: c.Ladder,
		ResultGrace: c.ResultGrace, Key: k, HeartbeatInterval: c.HeartbeatInterval})
	if err != nil {
		return rep, err
	}
	err = func() error {
		lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		release, err := artisan.LockAttempt(lockCtx, a.Dir())
		if err != nil {
			return err
		}
		defer release()
		outcomePath := filepath.Join(a.Dir(), artisan.OutcomeFile)
		if _, err := os.Lstat(outcomePath); !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("launch: %w: %s", artisan.ErrDecided, outcomePath)
		}
		return a.write(func(s *store.Store) error { _, err := s.TakeOwnership(mutation("own"), own, ProbeOwner); return err })
	}()
	if err != nil {
		return rep, fmt.Errorf("launch: take ownership of %s: %w", k, err)
	}
	finish := func(outcome store.AttemptOutcome, runErr error) (Report, error) {
		err := a.write(func(s *store.Store) error {
			_, err := s.FinishAttempt(mutation("finish"), k.Repository, k.TaskID, k.Step, k.Attempt, outcome)
			return err
		})
		if err == nil {
			rep.AttemptOutcome = outcome
		}
		return rep, errors.Join(runErr, err)
	}
	ep, err := artisan.Listen(path, k, self, a.launcher, l)
	if err != nil {
		return finish(store.AttemptFailed, err)
	}
	defer func() { _ = ep.Close() }()
	own.Endpoint = path
	if err := a.write(func(s *store.Store) error { _, err := s.UpdateOwnership(mutation("endpoint"), own); return err }); err != nil {
		return finish(store.AttemptFailed, err)
	}
	recorded.own = own
	cmd := exec.Command(c.Command.Argv[0], c.Command.Argv[1:]...)
	cmd.Dir, cmd.Env = c.Command.Dir, append(os.Environ(), c.Command.Env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = c.Stdin, c.Stdout, c.Stderr
	stopOnEnd := context.AfterFunc(ctx, func() { _, _ = l.Stop() })
	out, runErr := l.Run(cmd, c.Signals)
	stopOnEnd()
	rep.Outcome = out
	if out.State == "" || errors.Is(runErr, artisan.ErrNotEmpty) {
		return rep, runErr
	}
	if out.State == artisan.Finished {
		r, err := a.Files().Result()
		if err != nil {
			return rep, errors.Join(runErr, err)
		}
		rep.Result = &r
	}
	outcome, err := attemptOutcome(out, rep.Result)
	if err != nil {
		return rep, errors.Join(runErr, err)
	}
	return finish(outcome, runErr)
}

// ProbeOwner is the containment-aware take-over probe: alive while the
// recorded launcher identity lives, and gone only when it is gone and its
// recorded unit is proven empty by artisan.Contained; missing containment,
// survivors, or a unit that cannot be checked, are unverified.
func ProbeOwner(o store.Ownership) (store.Liveness, error) {
	lv, err := artisan.Probe(artisan.Identity(o.LauncherIdentity))
	switch {
	case lv == artisan.Alive:
		return store.ProbeAlive, err
	case lv != artisan.Gone || err != nil:
		return store.ProbeUnverified, err
	case o.Containment == (store.Containment{}):
		return store.ProbeUnverified, nil
	}
	c := o.Containment
	left, err := artisan.Contained(artisan.Containment{Kind: c.Kind, Name: c.Name, Reason: c.Reason}, artisan.Identity(c.Worker))
	switch {
	case err != nil:
		return store.ProbeUnverified, err
	case len(left) > 0:
		return store.ProbeUnverified, fmt.Errorf("%d contained processes survive", len(left))
	}
	return store.ProbeGone, nil
}

// recordingUnit records the containment once the unit reports the worker,
// in containment.json and the ownership record with state running. A failed
// record kills the worker, so nothing runs that a sweep could not find.
type recordingUnit struct {
	artisan.Unit
	a        Attempt
	own      store.Ownership
	mutation string
}

func (u *recordingUnit) Start(cmd *exec.Cmd) (artisan.Identity, error) {
	w, err := u.Unit.Start(cmd)
	if err != nil {
		return w, err
	}
	c := u.Containment()
	own := u.own
	own.Containment = store.Containment{Kind: c.Kind, Name: c.Name, Reason: c.Reason, Worker: store.ProcessIdentity(w)}
	own.State = store.OwnerRunning
	err = errors.Join(artisan.RecordContainment(u.a.Dir(), c),
		u.a.write(func(s *store.Store) error { _, err := s.UpdateOwnership(u.mutation, own); return err }))
	if err != nil {
		return artisan.Identity{}, errors.Join(fmt.Errorf("launch: record containment: %w", err), u.Kill())
	}
	return w, nil
}

// write runs fn on the store opened for writing and closes it.
func (a Attempt) write(fn func(*store.Store) error) error {
	return a.open(store.Options{RequireStore: true}, fn)
}

func (a Attempt) open(o store.Options, fn func(*store.Store) error) error {
	s, err := store.Open(a.Store, o)
	if err != nil {
		return err
	}
	return errors.Join(fn(s), s.Close())
}

// owner reads the attempt's ownership record, nil when it has none.
func (a Attempt) owner() (*store.Ownership, error) {
	var o store.Ownership
	err := a.open(store.Options{ReadOnly: true, RequireStore: true}, func(s *store.Store) error {
		var err error
		o, err = s.Ownership(a.Key.Repository, a.Key.TaskID, a.Key.Step, a.Key.Attempt)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// launcher is the endpoint's OwnerLookup: the unreleased owner's identity.
func (a Attempt) launcher() (artisan.Identity, error) {
	o, err := a.owner()
	switch {
	case err != nil:
		return artisan.Identity{}, err
	case o == nil:
		return artisan.Identity{}, errors.New("no ownership record")
	case o.State == store.OwnerReleased:
		return artisan.Identity{}, errors.New("ownership released")
	}
	return artisan.Identity(o.LauncherIdentity), nil
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
// ladder's end. It never signals a process itself. Without a successful
// answer, termination requires no ownership record, or a proven gone owner
// and a recorded unit proven empty. Otherwise it returns ErrNotStopped.
// Endpoint request errors are preserved. The report carries the classification
// afterwards, which alone does not prove termination.
func Stop(ctx context.Context, a Attempt) (StopReport, error) {
	var rep StopReport
	var stopErr error
	o, err := a.owner()
	switch {
	case err != nil:
		return rep, err
	case o == nil:
		rep.Endpoint = "not asked: no ownership record"
	case o.State == store.OwnerReleased:
		rep.Endpoint = "not asked: ownership released"
	case o.Endpoint == "":
		rep.Endpoint = "not asked: no endpoint recorded"
	default:
		rep.Endpoint = "stopped"
		if _, stopErr = artisan.Call(ctx, o.Endpoint, a.Key, artisan.Identity(o.LauncherIdentity), artisan.OpStop); stopErr != nil {
			rep.Endpoint = "no stop: " + stopErr.Error()
		}
	}
	if rep.Classification, err = Status(ctx, a); err != nil {
		return rep, errors.Join(stopErr, err)
	}
	if rep.Endpoint == "stopped" {
		return rep, nil
	}
	if errors.Is(stopErr, artisan.ErrEndpointRequest) {
		return rep, errors.Join(ErrNotStopped, stopErr)
	}
	o, err = a.owner()
	if err != nil {
		return rep, errors.Join(ErrNotStopped, stopErr, err)
	}
	if o == nil {
		return rep, nil
	}
	lv, err := ProbeOwner(*o)
	if err != nil || lv != store.ProbeGone {
		return rep, errors.Join(ErrNotStopped, stopErr, err)
	}
	return rep, nil
}

// Status classifies the attempt with reconcile.Classify from its files and
// ownership record; the error reports only a store that cannot be read.
func Status(ctx context.Context, a Attempt) (reconcile.Classification, error) {
	o, err := a.owner()
	if err != nil {
		return reconcile.Classification{}, err
	}
	if _, err := os.Stat(a.Dir()); o == nil && errors.Is(err, fs.ErrNotExist) {
		return reconcile.Classification{State: reconcile.Absent, Reason: "no attempt directory and no ownership record"}, nil
	}
	return reconcile.Classify(ctx, reconcile.Attempt{Dir: a.Dir(), Files: a.Files(), Owner: o}, reconcile.Options{}), nil
}

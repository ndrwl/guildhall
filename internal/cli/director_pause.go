package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
)

// sessionArgs parses the flags of `director pause`, `resume` and `stop`: a copy, or --store and, when withState,
// --state. It returns the store and state paths, or the usage error's exit status.
func (a *App) sessionArgs(name string, args []string, withState bool, register func(*flag.FlagSet)) (string, string, int) {
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	path := fl.String("store", "", "Director store file")
	var state *string
	if withState {
		state = fl.String("state", "", "state directory")
	}
	if register != nil {
		register(fl)
	}
	if err := fl.Parse(args); err != nil {
		return "", "", a.usageError(name + ": " + err.Error())
	}
	switch err := cp.apply(a.Getenv, path, nil, state); {
	case fl.NArg() > 0:
		return "", "", a.usageError(fmt.Sprintf("%s: unexpected argument %q", name, fl.Arg(0)))
	case err != nil:
		return "", "", a.usageError(name + ": " + err.Error())
	case *path == "" || withState && *state == "":
		if withState {
			return "", "", a.usageError(name + ": --store and --state are required, or --copy")
		}
		return "", "", a.usageError(name + ": --store is required, or --copy")
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return "", "", a.fail(fmt.Errorf("%s: %w", name, noStore(*path)))
	}
	if !withState {
		return *path, "", -1
	}
	return *path, *state, -1
}

// liveSession reads the newest session for pause, resume and stop, refusing no session and a stopped one, and
// reports whether its supervisor's identity is proven gone.
func liveSession(s *store.Store) (store.DirectorSession, bool, error) {
	d, err := s.LatestDirectorSession()
	switch {
	case errors.Is(err, store.ErrNotFound):
		return d, false, errors.New("no Director session; start one with guildhall director start")
	case err != nil:
		return d, false, err
	case d.State == store.DirectorStopped:
		return d, false, fmt.Errorf("the Director session %d is stopped", d.Number)
	}
	lv, err := artisan.Probe(artisan.Identity(d.Identity))
	return d, err == nil && lv == artisan.Gone, nil
}

// pauseMove moves a live newest session from one of running and paused to the other; a session already there
// is returned unchanged.
func pauseMove(s *store.Store, from, to store.DirectorState) (store.DirectorSession, error) {
	d, gone, err := liveSession(s)
	switch {
	case err != nil:
		return d, err
	case gone:
		return d, fmt.Errorf("the Director session %d's supervisor is gone; record it with guildhall director stop", d.Number)
	case d.State == to:
		return d, nil
	case d.State == store.DirectorRecovering:
		return d, fmt.Errorf("the Director session %d is recovering; run guildhall director ready first", d.Number)
	case d.State != from:
		return d, fmt.Errorf("the Director session %d is %s, not %s", d.Number, d.State, from)
	}
	if _, err := s.SetDirectorState(d.Number, to, d.StateSequence); err != nil {
		return d, err
	}
	return s.DirectorSession(d.Number)
}

// directorPause runs `guildhall director pause [--copy <dir> | --store <file>]`: the newest session moves from
// running to paused, holding new attempts while running ones finish, and is printed (docs/director.md#pause-and-resume).
func (a *App) directorPause(args []string) int {
	path, _, code := a.sessionArgs("director pause", args, false, nil)
	if code >= 0 {
		return code
	}
	d, err := withStore(path, false, func(s *store.Store) (store.DirectorSession, error) {
		return pauseMove(s, store.DirectorRunning, store.DirectorPaused)
	})
	if err != nil {
		return a.fail(fmt.Errorf("director pause: %w", err))
	}
	return a.printJSON(d)
}

// resumed is what `director resume` prints: the session and the status feed's needs_me.
type resumed struct {
	Session store.DirectorSession `json:"session"`
	NeedsMe []needsMeItem         `json:"needs_me"`
}

// directorResume runs `guildhall director resume [--copy <dir> | --store <file> --state <dir>]`: the newest
// session moves from paused to running and is printed with needs_me, which the Director then works
// (docs/director.md#pause-and-resume).
func (a *App) directorResume(args []string) int {
	path, state, code := a.sessionArgs("director resume", args, true, nil)
	if code >= 0 {
		return code
	}
	d, err := withStore(path, false, func(s *store.Store) (store.DirectorSession, error) {
		return pauseMove(s, store.DirectorPaused, store.DirectorRunning)
	})
	if err != nil {
		return a.fail(fmt.Errorf("director resume: %w", err))
	}
	status, err := readDirectorStatus(context.Background(), path, state, a.Now)
	if err != nil {
		return a.fail(fmt.Errorf("director resume: needs_me: %w", err))
	}
	return a.printJSON(resumed{Session: d, NeedsMe: status.NeedsMe})
}

// directorStop runs `guildhall director stop [--now] [--copy <dir> | --store <file> --state <dir>]`: the newest
// session is recorded stopping, or stopped as superseded when its supervisor is gone, every running attempt is
// stopped, and the session is printed (docs/director.md#stop).
func (a *App) directorStop(args []string) int {
	var now bool
	path, state, code := a.sessionArgs("director stop", args, true, func(fl *flag.FlagSet) {
		fl.BoolVar(&now, "now", false, "end the session at once")
	})
	if code >= 0 {
		return code
	}
	d, err := withStore(path, false, func(s *store.Store) (store.DirectorSession, error) {
		d, gone, err := liveSession(s)
		switch {
		case err != nil:
			return d, err
		case gone:
			_, err = s.EndDirectorSession(d.Number, "superseded", d.StateSequence)
		case d.State != store.DirectorStopping || now && !d.StopNow:
			_, err = s.RequestDirectorStop(d.Number, now, d.StateSequence)
		}
		if err != nil {
			return d, err
		}
		return s.DirectorSession(d.Number)
	})
	if err != nil {
		return a.fail(fmt.Errorf("director stop: %w", err))
	}
	stopped := a.stopAttempts(path, state)
	if code := a.printJSON(d); code != 0 || stopped {
		return code
	}
	return 1
}

// stopAttempts stops every attempt in the store with no outcome, reporting each that is not proven stopped on
// stderr, and reports whether all were. An attempt with an owner is stopped as `artisan stop` stops it; one nothing
// owns, a Director stage or an Artisan attempt recorded and not yet launched, is stopped by stopUnowned.
func (a *App) stopAttempts(path, state string) bool {
	state, err := filepath.Abs(state)
	if err != nil {
		a.notef("guildhall: director stop: %v\n", err)
		return false
	}
	v, err := withStore(path, true, func(s *store.Store) (store.DirectorView, error) { return s.DirectorView() })
	if err != nil {
		a.notef("guildhall: director stop: attempts: %v\n", err)
		return false
	}
	ok := true
	for _, at := range v.Attempts {
		if at.Outcome != "" {
			continue
		}
		key := artisan.AttemptKey{Repository: at.Repository, TaskID: at.TaskID, Step: at.Step, Attempt: at.Number}
		ctx, cancel := context.WithTimeout(context.Background(), defaultStopTimeout)
		err := stopAttempt(ctx, launch.Attempt{Store: path, StateDir: state, Key: key})
		cancel()
		if err != nil {
			a.notef("guildhall: director stop: attempt %s: %v\n", key, err)
			ok = false
		}
	}
	return ok
}

// stopAttempt stops one attempt for director stop: through its owner when it has an unreleased ownership record,
// otherwise with stopUnowned, falling back to the owner when one took it meanwhile.
func stopAttempt(ctx context.Context, at launch.Attempt) error {
	o, err := withStore(at.Store, true, func(s *store.Store) (*store.Ownership, error) {
		o, err := s.Ownership(at.Key.Repository, at.Key.TaskID, at.Key.Step, at.Key.Attempt)
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return &o, err
	})
	if err != nil {
		return err
	}
	if o == nil || o.State == store.OwnerReleased {
		if err := stopUnowned(ctx, at); !errors.Is(err, store.ErrOwned) {
			return err
		}
	}
	_, err = launch.Stop(ctx, at)
	return err
}

// stopUnowned records an attempt nothing owns stopped, under its task's lock: an advance holds that lock from
// recording an attempt until it has launched it, or handed it to the Director, and step finish holds it to publish
// a Director stage's result, so neither is part way through. An attempt finished meanwhile, or whose result step
// finish already published, which its advance takes, is left as it is; one a launcher took meanwhile is refused
// with store.ErrOwned, for its owner to stop (docs/director.md#stop).
func stopUnowned(ctx context.Context, at launch.Attempt) error {
	k := at.Key
	taskDir := filepath.Join(at.StateDir, k.Repository, k.TaskID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return err
	}
	if testHookStopWaitsForTask != nil {
		testHookStopWaitsForTask()
	}
	release, err := artisan.LockAttempt(ctx, taskDir)
	if err != nil {
		return fmt.Errorf("lock task %s/%s: %w", k.Repository, k.TaskID, err)
	}
	defer release()
	attempts, err := withStore(at.Store, true, func(s *store.Store) ([]store.StageAttempt, error) { return s.Attempts(k.Repository, k.TaskID) })
	if err != nil {
		return err
	}
	if slices.ContainsFunc(attempts, func(a store.StageAttempt) bool { return a.Step == k.Step && a.Number == k.Attempt && a.Outcome != "" }) {
		return nil
	}
	if _, err := os.Stat(filepath.Join(k.Dir(at.StateDir), artisan.ResultFile)); err == nil {
		return nil
	}
	_, err = withStore(at.Store, false, func(s *store.Store) (store.Receipt, error) {
		return s.StopUnownedAttempt(fmt.Sprintf("director-stop:%s:%s:%s", k.Repository, k.TaskID, k.AttemptID()), k.Repository, k.TaskID, k.Step, k.Attempt)
	})
	return err
}

// held reports whether the newest Director session holds new attempts: it does while paused or stopping.
func held(s *store.Store) (bool, error) {
	d, err := s.LatestDirectorSession()
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return d.State == store.DirectorPaused || d.State == store.DirectorStopping, err
}

// heldNow is held over the store file at path, opened read-only; a missing store holds nothing, and is left to the
// advance to refuse.
func heldNow(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		return false, nil
	}
	return withStore(path, true, held)
}

// pausedWait is what `task start` and `step retry` print when the Director holds new attempts.
const pausedWait = `{"wait":"paused"}`

// testHookStopWaitsForTask, when set, is called as director stop starts waiting for a task's lock to stop an attempt
// nothing owns.
var testHookStopWaitsForTask func()

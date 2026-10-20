package store

import (
	"errors"
	"fmt"
	"slices"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/artisan"
)

// bucketOwners holds one ownership record per owned stage attempt, keyed like the attempt.
var bucketOwners = []byte("owners")

var (
	// ErrOwnerLive refuses taking an attempt whose recorded launcher is live.
	ErrOwnerLive = errors.New("attempt is owned by a live launcher")
	// ErrOwnerUnknown refuses taking an attempt whose recorded launcher cannot be proven exited.
	ErrOwnerUnknown = errors.New("attempt owner cannot be proven exited")
)

// OwnershipState is where an owning launcher is in its attempt.
type OwnershipState string

const (
	// OwnerStarting is recorded when ownership is taken, before the worker runs.
	OwnerStarting OwnershipState = "starting"
	// OwnerRunning is recorded by the owner once its worker is contained and running.
	OwnerRunning OwnershipState = "running"
	// OwnerReleased is recorded by FinishAttempt; a released record is never owned again.
	OwnerReleased OwnershipState = "released"
)

// Containment names the unit an owner's worker runs in, such as a Windows job
// name or a Unix process group id. The store records it and does not interpret it.
type Containment struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Ownership records which launcher owns one running stage attempt (docs/store.md).
// The store's single-writer lock makes taking it a check-and-set, and its
// receipt is the proof of who took it.
type Ownership struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	Step       string `json:"step"`
	// Attempt is the stage attempt's number; StartOwnedAttempt assigns it.
	Attempt          int              `json:"attempt"`
	LauncherIdentity artisan.Identity `json:"launcher_identity"`
	// Containment is empty until the owner's worker has been placed in a unit.
	Containment Containment `json:"containment,omitzero"`
	// Endpoint is the owner's control endpoint, empty until it is bound.
	Endpoint string         `json:"endpoint,omitempty"`
	State    OwnershipState `json:"state"`
}

// OwnerProbe reports whether the launcher recorded in an ownership record is
// still live. Alive refuses a take-over with ErrOwnerLive; Gone with a nil
// error proves exit and allows it; anything else, including any error,
// refuses it with ErrOwnerUnknown. A caller whose owner contained processes
// reports survivors as Alive or Unverified, never Gone.
type OwnerProbe func(Ownership) (artisan.Liveness, error)

// ProbeLauncher probes the recorded launcher identity with artisan.Probe.
func ProbeLauncher(o Ownership) (artisan.Liveness, error) {
	return artisan.Probe(o.LauncherIdentity)
}

// MaxEndpointBytes bounds an ownership record's endpoint.
const MaxEndpointBytes = 1024

// StartOwnedAttempt records a new running attempt of o.Step owned by o's
// launcher in one mutation, as StartAttempt does, and returns the attempt
// number in the receipt. o.Attempt must be zero and o.State empty or
// OwnerStarting; the record is stored as OwnerStarting.
func (s *Store) StartOwnedAttempt(mutationID string, o Ownership) (Receipt, error) {
	if o.Attempt != 0 {
		return Receipt{}, invalid("a new owned attempt is numbered by the store, not %d", o.Attempt)
	}
	if err := validateOwnership(o, false, "", OwnerStarting); err != nil {
		return Receipt{}, err
	}
	o.State = OwnerStarting
	return s.mutate(mutationID, "start-owned-attempt", o, func(tx *bolt.Tx, r *Receipt) error {
		a, err := startAttempt(tx, r, o.Repository, o.TaskID, o.Step)
		if err != nil {
			return err
		}
		o.Attempt = a.Number
		return putOwnership(tx, o)
	})
}

// TakeOwnership records o's launcher as the owner of the running attempt
// o.Attempt; o.State must be empty or OwnerStarting and is stored as OwnerStarting. It takes an attempt with no owner, and
// takes over one whose recorded owner probe proves exited; it refuses with
// ErrOwnerLive or ErrOwnerUnknown otherwise and writes nothing. A nil probe
// means ProbeLauncher. The probe runs inside the mutation, under the store's
// single-writer lock, so two take-overs never both succeed. A replayed
// mutation id returns its original receipt without probing.
func (s *Store) TakeOwnership(mutationID string, o Ownership, probe OwnerProbe) (Receipt, error) {
	if err := validateOwnership(o, true, "", OwnerStarting); err != nil {
		return Receipt{}, err
	}
	if probe == nil {
		probe = ProbeLauncher
	}
	o.State = OwnerStarting
	return s.mutate(mutationID, "take-ownership", o, func(tx *bolt.Tx, r *Receipt) error {
		r.Repository, r.TaskID, r.Attempt, r.Record = o.Repository, o.TaskID, o.Attempt, ownerRecord(o.Step, o.Attempt)
		var a StageAttempt
		if err := getJSON(repoBucket(tx, o.Repository, bucketAttempts), attemptKey(o.TaskID, o.Step, o.Attempt), &a); err != nil {
			return fmt.Errorf("attempt %d of %s/%s step %q: %w", o.Attempt, o.Repository, o.TaskID, o.Step, err)
		}
		if a.Outcome != "" {
			return invalid("attempt %d of step %q already finished: %s", o.Attempt, o.Step, a.Outcome)
		}
		prev, err := getOwnership(tx, o.Repository, o.TaskID, o.Step, o.Attempt)
		switch {
		case errors.Is(err, ErrNotFound):
		case err != nil:
			return err
		default:
			lv, perr := probe(prev)
			switch {
			case lv == artisan.Alive:
				return fmt.Errorf("%w: launcher %d", ErrOwnerLive, prev.LauncherIdentity.PID)
			case perr != nil:
				return fmt.Errorf("%w: launcher %d: %w", ErrOwnerUnknown, prev.LauncherIdentity.PID, perr)
			case lv != artisan.Gone:
				return fmt.Errorf("%w: launcher %d is %s", ErrOwnerUnknown, prev.LauncherIdentity.PID, lv)
			}
		}
		return putOwnership(tx, o)
	})
}

// UpdateOwnership records the owner's progress: its containment, endpoint and
// a State of OwnerStarting or OwnerRunning, never back from running to
// starting. Only the recorded launcher identity may update; a record owned by
// another launcher is refused with ErrOwnerLive, and a released one with ErrInvalid.
func (s *Store) UpdateOwnership(mutationID string, o Ownership) (Receipt, error) {
	if err := validateOwnership(o, true, OwnerStarting, OwnerRunning); err != nil {
		return Receipt{}, err
	}
	return s.mutate(mutationID, "update-ownership", o, func(tx *bolt.Tx, r *Receipt) error {
		r.Repository, r.TaskID, r.Attempt, r.Record = o.Repository, o.TaskID, o.Attempt, ownerRecord(o.Step, o.Attempt)
		cur, err := getOwnership(tx, o.Repository, o.TaskID, o.Step, o.Attempt)
		switch {
		case err != nil:
			return err
		case cur.State == OwnerReleased:
			return invalid("ownership of attempt %d of step %q is released", o.Attempt, o.Step)
		case cur.LauncherIdentity != o.LauncherIdentity:
			return fmt.Errorf("%w: attempt %d of step %q is owned by launcher %d", ErrOwnerLive, o.Attempt, o.Step, cur.LauncherIdentity.PID)
		case cur.State == OwnerRunning && o.State == OwnerStarting:
			return invalid("ownership of attempt %d of step %q is running and cannot return to starting", o.Attempt, o.Step)
		}
		return putOwnership(tx, o)
	})
}

// Ownership returns the ownership record of one attempt, or ErrNotFound when it has none.
func (s *Store) Ownership(repository, taskID, step string, attempt int) (Ownership, error) {
	var o Ownership
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		o, err = getOwnership(tx, repository, taskID, step, attempt)
		return err
	})
	if err != nil {
		return Ownership{}, fmt.Errorf("store: %w", err)
	}
	return o, nil
}

// releaseOwnership marks an attempt's ownership record released, if it has one.
func releaseOwnership(tx *bolt.Tx, repository, taskID, step string, attempt int) error {
	o, err := getOwnership(tx, repository, taskID, step, attempt)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	o.State = OwnerReleased
	return putOwnership(tx, o)
}

func getOwnership(tx *bolt.Tx, repository, taskID, step string, attempt int) (Ownership, error) {
	var o Ownership
	if err := getJSON(repoBucket(tx, repository, bucketOwners), attemptKey(taskID, step, attempt), &o); err != nil {
		return Ownership{}, fmt.Errorf("ownership of attempt %d of %s/%s step %q: %w", attempt, repository, taskID, step, err)
	}
	return o, nil
}

func putOwnership(tx *bolt.Tx, o Ownership) error {
	owners, err := createRepoBucket(tx, o.Repository, bucketOwners)
	if err != nil {
		return err
	}
	return putJSON(owners, attemptKey(o.TaskID, o.Step, o.Attempt), o)
}

// ownerRecord is the receipt record name of the ownership of attempt number of step.
func ownerRecord(step string, number int) string { return fmt.Sprintf("owner/%s/%d", step, number) }

// validateOwnership checks a request's fields; numbered says whether it names an existing attempt,
// and states lists the states the request may ask for.
func validateOwnership(o Ownership, numbered bool, states ...OwnershipState) error {
	if err := validateIDs(o.Repository, o.TaskID, o.Step); err != nil {
		return err
	}
	if !slices.Contains(states, o.State) {
		return invalid("ownership state %q must be one of %q", o.State, states)
	}
	if numbered && o.Attempt < 1 {
		return invalid("attempt number %d must be positive", o.Attempt)
	}
	if o.LauncherIdentity.PID < 1 {
		return invalid("launcher pid %d must be positive", o.LauncherIdentity.PID)
	}
	if err := validateText("launcher token", o.LauncherIdentity.Token, 256); err != nil {
		return err
	}
	if o.Containment != (Containment{}) {
		for name, v := range map[string]string{"containment kind": o.Containment.Kind, "containment name": o.Containment.Name} {
			if err := validateText(name, v, 256); err != nil {
				return err
			}
		}
	}
	if o.Endpoint != "" {
		if err := validateText("endpoint", o.Endpoint, MaxEndpointBytes); err != nil {
			return err
		}
	}
	return nil
}

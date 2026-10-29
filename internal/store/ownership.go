package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

// bucketOwners holds one ownership record per owned stage attempt, keyed like the attempt.
var bucketOwners = []byte("owners")

var (
	// ErrOwnerLive refuses taking an attempt whose recorded launcher is live.
	ErrOwnerLive = errors.New("attempt is owned by a live launcher")
	// ErrOwnerUnknown refuses taking an attempt whose recorded launcher cannot be proven exited.
	ErrOwnerUnknown = errors.New("attempt owner cannot be proven exited")
	// ErrOwned refuses StopUnownedAttempt for an attempt with an unreleased ownership record: its owner stops it.
	ErrOwned = errors.New("attempt has an unreleased owner")
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

// ProcessIdentity names one process as a record value: its PID plus the OS
// creation token that differs for a later process reusing the PID. It has
// Artisan's identity fields, and the store never reads the OS to fill it.
type ProcessIdentity struct {
	PID   int    `json:"pid"`
	Token string `json:"token"`
}

// Liveness is a probe's verdict on a recorded process. Its values are the
// words Artisan's native probe reports; the zero value is no verdict and never
// proves exit.
type Liveness string

const (
	// ProbeGone proves no live process has the recorded identity.
	ProbeGone Liveness = "gone"
	// ProbeAlive means a live process has the recorded identity.
	ProbeAlive Liveness = "alive"
	// ProbeUnverified means the probe could neither prove exit nor find it live.
	ProbeUnverified Liveness = "unverified"
)

// Containment kinds a recovery description may name. They are the kinds
// artisan.Unit backends report, held here as record values.
const (
	// ContainmentJob is a Windows Job Object, found by its name.
	ContainmentJob = "job"
	// ContainmentCgroup is a Linux cgroup v2 child, found by its directory.
	ContainmentCgroup = "cgroup"
	// ContainmentSubreaper is a Linux process group with the launcher as
	// child subreaper, found by its leader, the worker.
	ContainmentSubreaper = "subreaper+pgroup"
	// ContainmentGroup is a process group alone (macOS), found by its leader.
	ContainmentGroup = "pgroup"
	// ContainmentNotStarted attests that completed supervision never invoked
	// worker startup. It has no name, reason or worker identity.
	ContainmentNotStarted = "not-started"
)

// ErrUnrecoverable reports a containment record that cannot identify its unit,
// so it never authorises a sweep or a take-over.
var ErrUnrecoverable = errors.New("containment record cannot identify its unit")

// MaxContainmentBytes bounds a containment record's name, reason and worker token.
const MaxContainmentBytes = 1024

// Containment is the recovery description of the unit an owner's worker runs
// in: an artisan.Containment field for field, plus the worker identity Start
// returned (docs/store.md#containment-record). A job or cgroup is found by its
// Name; a process group has no name and is found by its leader, the Worker.
type Containment struct {
	// Kind is one of the Containment kind constants.
	Kind string `json:"kind"`
	// Name is the job name or cgroup directory; it is empty for a process group.
	Name string `json:"name,omitempty"`
	// Reason is the Linux detection result that chose Kind.
	Reason string `json:"reason,omitempty"`
	// Worker is the contained worker; a process group requires it.
	Worker ProcessIdentity `json:"worker,omitzero"`
}

// Recoverable reports, with an error wrapping ErrUnrecoverable, a record that
// cannot identify its unit: an unknown kind, a job or cgroup without a name, a
// process group without a worker identity or with a name, an invalid worker
// identity, or an oversized or invalid name or reason.
func (c Containment) Recoverable() error {
	unrecoverable := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrUnrecoverable, fmt.Sprintf(format, args...))
	}
	text := func(name, v string) error {
		switch {
		case strings.TrimSpace(v) == "":
			return unrecoverable("%s %s must not be empty", c.Kind, name)
		case len(v) > MaxContainmentBytes:
			return unrecoverable("%s %s is %d bytes; the limit is %d", c.Kind, name, len(v), MaxContainmentBytes)
		case !utf8.ValidString(v):
			return unrecoverable("%s %s is not valid UTF-8", c.Kind, name)
		}
		return nil
	}
	switch c.Kind {
	case ContainmentNotStarted:
		if c != (Containment{Kind: ContainmentNotStarted}) {
			return unrecoverable("not-started cannot name a unit or worker")
		}
	case ContainmentJob, ContainmentCgroup:
		if err := text("name", c.Name); err != nil {
			return err
		}
	case ContainmentSubreaper, ContainmentGroup:
		if c.Name != "" {
			return unrecoverable("a %s has no name; it is found by its worker, not %q", c.Kind, c.Name)
		}
		if c.Worker == (ProcessIdentity{}) {
			return unrecoverable("a %s needs its worker identity", c.Kind)
		}
	default:
		return unrecoverable("unknown containment kind %q", c.Kind)
	}
	if c.Worker != (ProcessIdentity{}) {
		if c.Worker.PID < 1 {
			return unrecoverable("worker pid %d must be positive", c.Worker.PID)
		}
		if err := text("worker token", c.Worker.Token); err != nil {
			return err
		}
	}
	if c.Reason != "" {
		return text("reason", c.Reason)
	}
	return nil
}

// Ownership records which launcher owns one running stage attempt (docs/store.md).
// The store's single-writer lock makes taking it a check-and-set, and its
// receipt binds the request digest to its commit time without retaining a historical identity.
type Ownership struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	Step       string `json:"step"`
	// Attempt is the stage attempt's number; StartOwnedAttempt assigns it.
	Attempt          int             `json:"attempt"`
	LauncherIdentity ProcessIdentity `json:"launcher_identity"`
	// Containment is empty until the owner's unit is known: a named unit (job,
	// cgroup) is recorded when ownership is taken, its worker added by the
	// running update, and a process group once its worker runs. Once set it
	// must be Recoverable.
	Containment Containment `json:"containment,omitzero"`
	// Endpoint is the owner's control endpoint, empty until it is bound.
	Endpoint string         `json:"endpoint,omitempty"`
	State    OwnershipState `json:"state"`
}

// OwnerProbe reports whether the launcher recorded in an ownership record is
// still live. ProbeAlive refuses a take-over with ErrOwnerLive even with an
// error; ProbeGone with a nil error proves exit and allows it; all other results
// refuse it with ErrOwnerUnknown. A caller whose owner contained processes
// reports survivors as ProbeAlive or ProbeUnverified, never ProbeGone.
type OwnerProbe func(Ownership) (Liveness, error)

// IdentityProbe reports whether one recorded process is still live. The
// launcher composition supplies the operating system's probe; the store never
// probes processes itself.
type IdentityProbe func(ProcessIdentity) (Liveness, error)

// ProbeLauncher returns the conservative owner probe over identity: it reports
// ProbeUnverified without probing when containment is recorded, since launcher
// exit cannot prove absence of contained survivors, and otherwise probes the
// recorded launcher identity.
func ProbeLauncher(identity IdentityProbe) OwnerProbe {
	return func(o Ownership) (Liveness, error) {
		if o.Containment != (Containment{}) {
			return ProbeUnverified, nil
		}
		return identity(o.LauncherIdentity)
	}
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
		a, err := startAttempt(tx, r, o.Repository, o.TaskID, o.Step, false)
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
// ErrOwnerLive or ErrOwnerUnknown otherwise and writes nothing. A recorded
// containment that is not Recoverable refuses with ErrOwnerUnknown without
// probing, since its unit could not be swept. The probe is required, even
// for an attempt with no owner, and a nil probe is refused with ErrInvalid:
// launcher callers pass ProbeLauncher, which refuses recorded containment with
// ErrOwnerUnknown, and recovery passes a containment-aware probe proving no
// survivors. The probe runs inside the mutation, under the store's
// single-writer lock, so two take-overs never both succeed. A replayed
// mutation id returns its original receipt without probing.
func (s *Store) TakeOwnership(mutationID string, o Ownership, probe OwnerProbe) (Receipt, error) {
	if err := validateOwnership(o, true, "", OwnerStarting); err != nil {
		return Receipt{}, err
	}
	if probe == nil {
		return Receipt{}, invalid("a take-over needs an owner probe; the store does not probe processes")
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
			if prev.Containment != (Containment{}) {
				if err := prev.Containment.Recoverable(); err != nil {
					return fmt.Errorf("%w: launcher %d: %w", ErrOwnerUnknown, prev.LauncherIdentity.PID, err)
				}
			}
			lv, perr := probe(prev)
			switch {
			case lv == ProbeAlive:
				return fmt.Errorf("%w: launcher %d", ErrOwnerLive, prev.LauncherIdentity.PID)
			case perr != nil:
				return fmt.Errorf("%w: launcher %d: %w", ErrOwnerUnknown, prev.LauncherIdentity.PID, perr)
			case lv != ProbeGone:
				return fmt.Errorf("%w: launcher %d is %s", ErrOwnerUnknown, prev.LauncherIdentity.PID, lv)
			}
		}
		return putOwnership(tx, o)
	})
}

// StopUnownedAttempt records attempt number of step stopped when nothing owns it: it has no ownership record, or
// only a released one. The check and the outcome are one mutation under the store's single-writer lock, so a
// TakeOwnership that commits first refuses this with ErrOwned, and one that commits after it is refused because the
// attempt has finished; a launcher therefore never runs an attempt this stopped. An attempt that already finished,
// with any outcome, is refused with ErrInvalid as FinishAttempt refuses it, and a missing one with ErrNotFound; a
// refusal writes nothing. The outcome is stopped, as FinishAttempt records it, so the attempt no longer counts as
// running (AttemptUsage) and is retried like any stopped attempt. The receipt is a finish-attempt receipt with
// FinishAttempt's request for a stopped outcome, so a later FinishAttempt of the same stop stands as one would after
// FinishAttempt. Replaying returns the original receipt.
func (s *Store) StopUnownedAttempt(mutationID, repository, taskID, step string, number int) (Receipt, error) {
	if err := validateIDs(repository, taskID, step); err != nil {
		return Receipt{}, err
	}
	if number < 1 {
		return Receipt{}, invalid("attempt number %d must be positive", number)
	}
	req := StageAttempt{Repository: repository, TaskID: taskID, Step: step, Number: number, Outcome: AttemptStopped}
	return s.mutate(mutationID, "finish-attempt", req, func(tx *bolt.Tx, r *Receipt) error {
		r.Repository, r.TaskID, r.Attempt, r.Record = repository, taskID, number, attemptRecord(step, number)
		attempts := repoBucket(tx, repository, bucketAttempts)
		key := attemptKey(taskID, step, number)
		var a StageAttempt
		if err := getJSON(attempts, key, &a); err != nil {
			return fmt.Errorf("attempt %d of %s/%s step %q: %w", number, repository, taskID, step, err)
		}
		if a.Outcome != "" {
			return invalid("attempt %d of step %q already finished: %s", number, step, a.Outcome)
		}
		switch o, err := getOwnership(tx, repository, taskID, step, number); {
		case errors.Is(err, ErrNotFound):
		case err != nil:
			return err
		case o.State != OwnerReleased:
			return fmt.Errorf("%w: attempt %d of step %q is owned by launcher %d", ErrOwned, number, step, o.LauncherIdentity.PID)
		}
		a.Outcome = AttemptStopped
		return putJSON(attempts, key, a)
	})
}

// UpdateOwnership records the owner's progress: its containment, endpoint and
// a State of OwnerStarting or OwnerRunning, never back from running to
// starting. Only the recorded launcher identity may update; a record owned by
// another launcher is refused with ErrOwnerLive, and a released one with ErrInvalid.
// Recovery evidence is monotonic: a recorded containment or endpoint may be set
// from empty but never cleared or changed, except that a containment recorded
// without its worker may have the worker added once; an update that would
// otherwise change either is refused with ErrInvalid and writes nothing.
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
		case cur.Containment != (Containment{}) && o.Containment != cur.Containment && !addsWorker(cur.Containment, o.Containment):
			return invalid("ownership of attempt %d of step %q has recorded containment %+v, which an update must keep", o.Attempt, o.Step, cur.Containment)
		case cur.Endpoint != "" && o.Endpoint != cur.Endpoint:
			return invalid("ownership of attempt %d of step %q has recorded endpoint %q, which an update must keep", o.Attempt, o.Step, cur.Endpoint)
		}
		return putOwnership(tx, o)
	})
}

// addsWorker reports whether next is recorded unit cur with the worker
// identity added: a named unit is recorded when ownership is taken, before
// its worker exists, and the running update adds the worker once.
func addsWorker(cur, next Containment) bool {
	if cur.Worker != (ProcessIdentity{}) || next.Worker == (ProcessIdentity{}) {
		return false
	}
	next.Worker = ProcessIdentity{}
	return next == cur
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
		if err := o.Containment.Recoverable(); err != nil {
			return fmt.Errorf("store: %w: %w", ErrInvalid, err)
		}
	}
	if o.Endpoint != "" {
		if err := validateText("endpoint", o.Endpoint, MaxEndpointBytes); err != nil {
			return err
		}
	}
	return nil
}

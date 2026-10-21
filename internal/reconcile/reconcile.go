// Package reconcile classifies an owned stage attempt from what outlives its
// launcher: the attempt directory, the store's ownership record and the live
// state of the launcher, endpoint and containment that record names
// (docs/artisan.md#reconcile). It never signals, writes attempt state, takes
// over or relaunches anything; acquiring the lock may create attempt.lock.
//
// Classify decides in this order, the first that applies winning:
//
//  1. the launcher's outcome.json: its state, finished, cancelled or failed;
//  2. the cancel marker: cancelled;
//  3. a valid result: finished;
//  4. no ownership record: absent;
//  5. the owner, by its recorded launcher identity:
//     - gone: exited when no containment is recorded or artisan.Contained
//     proves the recorded unit empty (see artisan.Contained);
//     unknown when contained processes survive or cannot be checked;
//     - alive with a heartbeat older than the stale threshold: stuck,
//     whatever the endpoint answers;
//     - alive and the recorded endpoint answers status for this attempt and
//     launcher: running;
//     - anything else, such as a live launcher whose endpoint is silent or an
//     identity that cannot be verified: unknown.
//
// Classify reads outcome, marker and result under the same attempt lock as
// launcher decisions. An in-flight cancel or stop must publish its marker
// before Classify can accept a result. Lock contention is bounded by ctx and
// LockTimeout; failure to acquire yields unknown. The lock is released before
// checking the owner or calling its endpoint.
//
// The heartbeat is evidence, never proof: stuck is not exit and grants no
// relaunch, take-over or sweep, and a gone launcher's frozen heartbeat is
// reported while the identity decides. A heartbeat naming another attempt is
// ignored.
package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/store"
)

// State is an attempt's classification.
type State string

// States. Finished, Cancelled and Failed are decided by the attempt's files;
// the rest describe its owner.
const (
	Finished  State = "finished"
	Cancelled State = "cancelled"
	Failed    State = "failed"
	Absent    State = "absent"
	Running   State = "running"
	Stuck     State = "stuck"
	Exited    State = "exited"
	Unknown   State = "unknown"
)

// DefaultStaleAfter is the heartbeat age Classify calls stale when Options
// gives none: six missed default heartbeats.
const DefaultStaleAfter = 6 * artisan.DefaultHeartbeatInterval

// LockTimeout bounds Classify's wait for the attempt lock.
const LockTimeout = 2 * time.Second

// EndpointTimeout bounds Classify's status call to the owner's endpoint.
const EndpointTimeout = 2 * time.Second

// Attempt is what Classify reads about one attempt.
type Attempt struct {
	// Dir is the attempt directory, holding outcome.json and heartbeat.json.
	Dir string
	// Files are the attempt's result and cancel marker.
	Files artisan.AttemptFiles
	// Owner is the attempt's ownership record, nil when it has none.
	Owner *store.Ownership
}

// Options tune Classify.
type Options struct {
	// StaleAfter is the heartbeat age that is stale; zero means
	// DefaultStaleAfter.
	StaleAfter time.Duration
	// Now is the clock heartbeat ages are measured by; nil means time.Now.
	Now func() time.Time
}

// Classification is Classify's answer and the evidence behind it.
type Classification struct {
	State  State  `json:"state"`
	Reason string `json:"reason"`
	// Outcome is the launcher's decision when outcome.json decided.
	Outcome *artisan.Outcome `json:"outcome,omitempty"`
	// Launcher is the recorded launcher identity's liveness: gone, alive or
	// unverified; empty when the files decided.
	Launcher string `json:"launcher,omitempty"`
	// Endpoint is "answers" or why the endpoint did not; empty when not asked.
	Endpoint string `json:"endpoint,omitempty"`
	// Heartbeat is the last heartbeat read, and HeartbeatAge its age.
	Heartbeat    *artisan.Heartbeat `json:"heartbeat,omitempty"`
	HeartbeatAge time.Duration      `json:"heartbeat_age,omitempty"`
	// Survivors are contained processes alive after the launcher is gone.
	Survivors []artisan.Identity `json:"survivors,omitempty"`
}

// Classify classifies attempt a; ctx bounds lock acquisition and the endpoint
// call, also limited by LockTimeout and EndpointTimeout respectively.
func Classify(ctx context.Context, a Attempt, o Options) Classification {
	lockCtx, cancel := context.WithTimeout(ctx, LockTimeout)
	defer cancel()
	release, err := artisan.LockAttempt(lockCtx, a.Dir)
	if err != nil {
		return Classification{State: Unknown, Reason: "attempt lock: " + err.Error()}
	}
	c := func() Classification {
		defer release()
		return classifyFiles(a)
	}()
	if c.State != "" || c.Outcome != nil {
		return c
	}
	if a.Owner == nil {
		return Classification{State: Absent, Reason: "no ownership record"}
	}
	return classifyOwner(ctx, a, o)
}

func classifyFiles(a Attempt) Classification {
	unknown := func(format string, args ...any) Classification {
		return Classification{State: Unknown, Reason: fmt.Sprintf(format, args...)}
	}
	var out artisan.Outcome
	switch b, err := os.ReadFile(filepath.Join(a.Dir, artisan.OutcomeFile)); {
	case err == nil:
		if err := json.Unmarshal(b, &out); err != nil || out.Schema != artisan.OutcomeSchema {
			return unknown("unreadable outcome: %v", err)
		}
		return Classification{State: State(out.State), Reason: "launcher outcome: " + out.Detail, Outcome: &out}
	case !errors.Is(err, fs.ErrNotExist):
		return unknown("outcome: %v", err)
	}
	if m, err := a.Files.Marked(); err != nil || m {
		if err != nil {
			return unknown("cancel marker: %v", err)
		}
		return Classification{State: Cancelled, Reason: "cancel marker present; any result is late"}
	}
	if v, err := a.Files.ValidResult(); err != nil || v {
		if err != nil {
			return unknown("result: %v", err)
		}
		return Classification{State: Finished, Reason: "valid result"}
	}
	return Classification{}
}

func classifyOwner(ctx context.Context, a Attempt, o Options) Classification {
	own := *a.Owner
	k := artisan.AttemptKey{Repository: own.Repository, TaskID: own.TaskID, Step: own.Step, Attempt: own.Attempt}
	c := Classification{State: Unknown}
	if h, err := artisan.ReadHeartbeat(a.Dir); err == nil && h.Key() == k {
		now := time.Now
		if o.Now != nil {
			now = o.Now
		}
		c.Heartbeat, c.HeartbeatAge = &h, now().Sub(h.Time)
	}
	lv, perr := artisan.Probe(own.LauncherIdentity)
	c.Launcher = lv.String()
	switch lv {
	case artisan.Gone:
		if own.Containment == (store.Containment{}) {
			c.State, c.Reason = Exited, "launcher identity gone; no containment recorded"
			return c
		}
		cn := own.Containment
		left, err := artisan.Contained(artisan.Containment{Kind: cn.Kind, Name: cn.Name, Reason: cn.Reason}, cn.Worker)
		switch {
		case err != nil:
			c.Reason = "launcher identity gone; contained processes cannot be checked: " + err.Error()
		case len(left) > 0:
			c.Reason, c.Survivors = fmt.Sprintf("launcher identity gone; %d contained processes survive", len(left)), left
		default:
			c.State, c.Reason = Exited, "launcher identity gone; its unit is empty"
		}
		return c
	case artisan.Alive:
	default:
		c.Reason = fmt.Sprintf("launcher identity unverified: %v", perr)
		return c
	}
	stale := o.StaleAfter
	if stale <= 0 {
		stale = DefaultStaleAfter
	}
	c.Endpoint = "answers"
	ctx, cancel := context.WithTimeout(ctx, EndpointTimeout)
	defer cancel()
	if own.Endpoint == "" {
		c.Endpoint = "not recorded"
	} else if _, err := artisan.Call(ctx, own.Endpoint, k, own.LauncherIdentity, artisan.OpStatus); err != nil {
		c.Endpoint = err.Error()
	}
	switch {
	case c.Heartbeat != nil && c.HeartbeatAge > stale:
		c.State, c.Reason = Stuck, fmt.Sprintf("launcher identity live; heartbeat %d is %v old, over %v", c.Heartbeat.Sequence, c.HeartbeatAge, stale)
	case c.Endpoint == "answers":
		c.State, c.Reason = Running, "endpoint answers and launcher identity is live"
	default:
		c.Reason = "launcher identity live; endpoint silent: " + c.Endpoint
	}
	return c
}

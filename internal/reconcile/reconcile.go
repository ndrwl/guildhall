// Package reconcile classifies an owned stage attempt from what outlives its
// launcher: the attempt directory, the store's ownership record and the live
// state of the launcher, endpoint and containment that record names
// (docs/artisan.md#reconcile). It never signals, writes, takes over or
// relaunches anything.
//
// Classify decides in this order, the first that applies winning:
//
//  1. the launcher's outcome.json: its state, finished, cancelled or failed;
//  2. the cancel marker: cancelled;
//  3. a valid result: finished;
//  4. no ownership record: absent;
//  5. the owner, by its recorded launcher identity:
//     - gone: exited when no containment is recorded or artisan.Contained
//     proves the recorded unit empty (a job that no longer opens is empty);
//     unknown when contained processes survive or cannot be checked;
//     - alive with a heartbeat older than the stale threshold: stuck,
//     whatever the endpoint answers;
//     - alive and the recorded endpoint answers status for this attempt and
//     launcher: running;
//     - anything else, such as a live launcher whose endpoint is silent or an
//     identity that cannot be verified: unknown.
//
// The heartbeat is evidence, never proof: stuck is not exit and grants no
// relaunch, take-over or sweep, and a gone launcher's frozen heartbeat is
// reported while the identity decides. A heartbeat naming another attempt is
// ignored.
package reconcile

import (
	"context"
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

// Classify classifies attempt a; ctx bounds the endpoint call, which also
// gives up after EndpointTimeout.
func Classify(ctx context.Context, a Attempt, o Options) Classification {
	return Classification{}
}

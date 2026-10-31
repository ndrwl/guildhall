package store

import (
	"bytes"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/digest"
)

// bucketDirector is a top-level bucket holding the Director's own records: nested "sessions" keyed by
// zero-padded session number and "wakes" keyed by zero-padded receipt sequence.
// It is created by the first session or wake, so an existing schema 1 file accepts them unchanged.
var (
	bucketDirector = []byte("director")
	bucketSessions = []byte("sessions")
	bucketWakes    = []byte("wakes")
)

// Operations recorded on Director receipts.
const (
	opStartDirectorSession = "start-director-session"
	opSetDirectorState     = "set-director-state"
	opRecordWake           = "record-wake"
	opRequestDirectorStop  = "request-director-stop"
	opEndDirectorSession   = "end-director-session"
	opFinishDirectorStop   = "finish-director-stop"
	opRecordRuntime        = "record-director-runtime"
)

// MaxWakeReasonBytes bounds a wake's reason.
const MaxWakeReasonBytes = 256

// DirectorState is where a Director session is in its life (docs/store.md#director-sessions).
// Within one session it moves only forward, recovering, then running or paused, then stopping, then stopped,
// except that running and paused move to each other.
type DirectorState string

const (
	// DirectorRecovering is a started session that has not yet said it is ready; it starts no new work.
	DirectorRecovering DirectorState = "recovering"
	// DirectorRunning is a ready session.
	DirectorRunning DirectorState = "running"
	// DirectorPaused is a session that starts no new work; its running attempts finish.
	DirectorPaused DirectorState = "paused"
	// DirectorStopping is a session asked to stop, which ends once its runtime ends.
	DirectorStopping DirectorState = "stopping"
	// DirectorStopped is an ended session. It never changes again.
	DirectorStopped DirectorState = "stopped"
)

// directorRank orders states for the forward-only rule; running and paused share a rank.
var directorRank = map[DirectorState]int{
	DirectorRecovering: 0, DirectorRunning: 1, DirectorPaused: 1, DirectorStopping: 2, DirectorStopped: 3,
}

// DirectorPane is the Herdr pane a Director session runs in: one Guildhall created for it (R1.54), or, Adopted,
// the pane a plain director start found itself running in from its HERDR_* environment.
// The zero value means the session has no bound pane. It is presentation only (R1.52).
type DirectorPane struct {
	Session   string `json:"session"`
	Workspace string `json:"workspace"`
	Tab       string `json:"tab"`
	Pane      string `json:"pane"`
	// Adopted says Guildhall did not create the pane's tab, so it never retires it.
	Adopted bool `json:"adopted,omitempty"`
}

// DirectorSession is one run of the Director's runtime under its session supervisor.
// Sessions are numbered from 1; the newest is the Director's current session and supersedes every earlier one.
type DirectorSession struct {
	Number int           `json:"number"`
	State  DirectorState `json:"state"`
	// Identity is the session supervisor's process identity.
	Identity ProcessIdentity `json:"identity"`
	// Release is the Guildhall release the supervisor runs.
	Release string `json:"release"`
	// BinarySHA256 is the lowercase hex sha256 of the binary image the supervisor runs, hashed once when it
	// started; a session recorded before it was kept, or by a supervisor that could not hash its image, has none.
	BinarySHA256 string `json:"binary_sha256,omitempty"`
	// ConfigDigest is the "sha256:<hex>" digest of the configuration the session was started under.
	ConfigDigest string       `json:"config_digest"`
	Pane         DirectorPane `json:"pane,omitzero"`
	// Adapter is the runtime adapter the session runs, such as "claude"; it chooses how a wake reaches the
	// Director (TypedWakes). A session recorded before it was kept has none.
	Adapter string `json:"adapter,omitempty"`
	// Dir is the session's absolute directory, where its supervisor's endpoint listens; a wake that does not type
	// is handed over that endpoint.
	Dir string `json:"dir,omitempty"`
	// StartedAt, ReadyAt and EndedAt are set by the store, in UTC: when the session was recorded, when it first
	// became running and when it was recorded stopped.
	StartedAt time.Time `json:"started_at"`
	ReadyAt   time.Time `json:"ready_at,omitzero"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	// StateSequence is the sequence of the receipt that recorded State. SetDirectorState names it, so a move
	// decided on a stale read is refused rather than applied over a newer one.
	StateSequence uint64 `json:"state_sequence"`
	// StopNow is set by RequestDirectorStop for `director stop --now`: the supervisor ends the runtime at once.
	StopNow bool `json:"stop_now,omitempty"`
	// Sweeping is set by RequestDirectorStop and cleared by FinishDirectorStop once director stop has swept the
	// session's runtime and attempts as required by docs/director.md#stop; while it is set the session holds new
	// attempts (Holds), stopped or not.
	Sweeping bool `json:"sweeping,omitempty"`
	// Runtime is the containment unit the session's runtime runs in, with the runtime as its worker, once
	// RecordDirectorRuntime has recorded it; it is empty until then.
	Runtime Containment `json:"runtime,omitzero"`
	// EndReason is the reason EndDirectorSession recorded, such as "superseded" for a gone supervisor, or
	// EndSuperseded when StartDirectorSession recorded a newer session over a session not yet stopped.
	EndReason string `json:"end_reason,omitempty"`
	// Handoff is the automatic handoff policy the wakes of this session read; zero turns every trigger a wake
	// reads off.
	Handoff HandoffPolicy `json:"handoff,omitzero"`
	// Maintenance is the maintenance policy the status feed reads for this session; zero for a session recorded
	// before it was kept and for one with both parts off. The earliest session with a policy that is not zero is
	// where maintenance counting starts (docs/director.md#maintenance).
	Maintenance MaintenancePolicy `json:"maintenance,omitzero"`
}

// Holds reports whether the session holds new attempts: while it is paused or stopping, and while a stop's sweep
// of its attempts has not finished.
func (d DirectorSession) Holds() bool {
	return d.State == DirectorPaused || d.State == DirectorStopping || d.Sweeping
}

// TypedWakeAdapter is the one runtime adapter whose sessions are woken by typing a line into their pane: the fake,
// which is test-only and has no hooks.
const TypedWakeAdapter = "fake"

// TypedWakes reports whether a wake reaches the session by typing into its pane: only a fake runtime's session.
// Every other session, a Claude Code one among them and one recorded with no adapter, is woken through its session
// endpoint and never typed into (docs/decisions/director-wake-hook.md).
func (d DirectorSession) TypedWakes() bool { return d.Adapter == TypedWakeAdapter }

// EndSuperseded is the end reason of a session recorded stopped because its supervisor is gone, by
// StartDirectorSession when a newer session is recorded and by director stop.
const EndSuperseded = "superseded"

// WakeDecision is what a wake did.
type WakeDecision string

const (
	// WakeWoke means the notice line was typed into the Director's pane.
	WakeWoke WakeDecision = "woke"
	// WakeQueued means nothing was typed; the message waits for the Director's next turn.
	WakeQueued WakeDecision = "queued"
)

// Wake records one wake decision for one message to the Director (docs/messaging.md#store-records).
// A message has at most one wake.
type Wake struct {
	// Sequence is set by the store to the wake receipt's sequence.
	Sequence  uint64 `json:"sequence"`
	MessageID string `json:"message_id"`
	// SenderType is a lowercase id naming who sent the message, the input a per-sender wake policy reads.
	SenderType string       `json:"sender_type"`
	Decision   WakeDecision `json:"decision"`
	// Reason says why a queued wake typed nothing, such as "no pane" or "stopped"; a woke wake has none.
	Reason string `json:"reason,omitempty"`
	// Time is set by the store, in UTC.
	Time time.Time `json:"time"`
}

func directorSessionMutationID(n int) string { return "director-session:" + strconv.Itoa(n) }
func wakeMutationID(messageID string) string { return "wake:" + messageID }

func directorSessionRecord(n int) string { return "director/session/" + strconv.Itoa(n) }

func sessionKey(n int) []byte   { return fmt.Appendf(nil, "%010d", n) }
func wakeKey(seq uint64) []byte { return fmt.Appendf(nil, "%020d", seq) }

// StartDirectorSession records a new Director session with the mutation id "director-session:<number>".
// Its number must be one more than the newest session's, or 1 in a store with none; its state must be
// recovering; its times and state sequence are set by the store. Recording it records every earlier session
// that has not stopped as stopped, so exactly one session is current. Replaying returns the original receipt.
func (s *Store) StartDirectorSession(d DirectorSession) (Receipt, error) {
	if err := validateSession(d); err != nil {
		return Receipt{}, err
	}
	return s.mutate(directorSessionMutationID(d.Number), opStartDirectorSession, d, func(tx *bolt.Tx, r *Receipt) error {
		r.Record = directorSessionRecord(d.Number)
		sessions, err := createDirectorBucket(tx, bucketSessions)
		if err != nil {
			return err
		}
		newest := 0
		if k, _ := sessions.Cursor().Last(); k != nil {
			if newest, err = strconv.Atoi(string(k)); err != nil {
				return fmt.Errorf("%w: director session key %q", ErrNotStore, k)
			}
		}
		if d.Number != newest+1 {
			return invalid("director session %d must follow session %d", d.Number, newest)
		}
		now := s.now().UTC()
		// Every earlier session is stopped except possibly the newest, so only it can need superseding.
		if newest > 0 {
			var prev DirectorSession
			if err := getJSON(sessions, sessionKey(newest), &prev); err != nil {
				return err
			}
			if prev.State != DirectorStopped {
				prev.State, prev.EndedAt, prev.EndReason, prev.StateSequence = DirectorStopped, now, EndSuperseded, r.Sequence
				if err := putJSON(sessions, sessionKey(newest), prev); err != nil {
					return err
				}
			}
		}
		d.StartedAt, d.StateSequence = now, r.Sequence
		return putJSON(sessions, sessionKey(d.Number), d)
	})
}

// SetDirectorState moves the newest session, number n, to state to, with the mutation id
// "director-state:<n>:<to>:<from>", where from is the StateSequence the caller read.
// It is refused with ErrInvalid when n is not the newest session, from is not its StateSequence, the move
// goes backward or stays put, or the session is stopped; ErrNotFound when there is no session n.
// Reaching running for the first time sets ReadyAt, and reaching stopped sets EndedAt.
// Replaying returns the original receipt.
func (s *Store) SetDirectorState(n int, to DirectorState, from uint64) (Receipt, error) {
	if _, ok := directorRank[to]; !ok {
		return Receipt{}, invalid("director state %q is not one of recovering, running, paused, stopping or stopped", to)
	}
	req := struct {
		Number int           `json:"number"`
		To     DirectorState `json:"to"`
		From   uint64        `json:"from"`
	}{n, to, from}
	id := fmt.Sprintf("director-state:%d:%s:%d", n, to, from)
	return s.moveSession(id, opSetDirectorState, req, n, from, func(d *DirectorSession) error {
		if d.State == to || directorRank[to] < directorRank[d.State] {
			return invalid("director session %d cannot move from %s to %s", n, d.State, to)
		}
		d.State = to
		return nil
	})
}

// MaxEndReasonBytes bounds a session's end reason.
const MaxEndReasonBytes = 256

// RequestDirectorStop moves the newest session, number n, to stopping for `director stop`, with the mutation id
// "director-stop:<n>:<turn|now>:<from>", where from is the StateSequence the caller read; now sets StopNow, and
// every request sets Sweeping.
// A recovering, running or paused session moves to stopping; a stopping session without StopNow takes it when
// now is set and stays stopping. A stopped, non-sweeping session with a recorded runtime sets only Sweeping
// for recovery, preserving its end metadata. Every other move, a stale from and a superseded session are refused
// as SetDirectorState refuses them. Replaying returns the original receipt.
func (s *Store) RequestDirectorStop(n int, now bool, from uint64) (Receipt, error) {
	req := struct {
		Number int    `json:"number"`
		Now    bool   `json:"now"`
		From   uint64 `json:"from"`
	}{n, now, from}
	id := fmt.Sprintf("director-stop:%d:%s:%d", n, map[bool]string{false: "turn", true: "now"}[now], from)
	return s.moveSession(id, opRequestDirectorStop, req, n, from, func(d *DirectorSession) error {
		if d.State == DirectorStopped {
			if d.Sweeping || d.Runtime == (Containment{}) {
				return invalid("director session %d has no runtime recovery to request", n)
			}
			d.Sweeping = true
			return nil
		}
		if d.State == DirectorStopping && (d.StopNow || !now) {
			return invalid("director session %d is already stopping", n)
		}
		d.State, d.StopNow, d.Sweeping = DirectorStopping, now, true
		return nil
	})
}

// EndDirectorSession moves the newest session, number n, to stopped with reason as its EndReason, with the
// mutation id "director-end:<n>:<from>"; reason is a non-blank line of at most MaxEndReasonBytes. A stale from
// and a superseded or stopped session are refused as SetDirectorState refuses them. Replaying returns the
// original receipt.
func (s *Store) EndDirectorSession(n int, reason string, from uint64) (Receipt, error) {
	if err := validateText("director session end reason", reason, MaxEndReasonBytes); err != nil {
		return Receipt{}, err
	}
	if strings.ContainsAny(reason, "\r\n") {
		return Receipt{}, invalid("director session end reason must be one line")
	}
	req := struct {
		Number int    `json:"number"`
		Reason string `json:"reason"`
		From   uint64 `json:"from"`
	}{n, reason, from}
	return s.moveSession(fmt.Sprintf("director-end:%d:%d", n, from), opEndDirectorSession, req, n, from, func(d *DirectorSession) error {
		d.State, d.EndReason = DirectorStopped, reason
		return nil
	})
}

// FinishDirectorStop clears Sweeping on the newest session, number n, once director stop has completed the sweeps
// required by docs/director.md#stop,
// with the mutation id "director-swept:<n>:<from>", stopped or not. It is refused with ErrInvalid when Sweeping is
// not set, and a stale from and a superseded session as SetDirectorState refuses them. Replaying returns the
// original receipt.
func (s *Store) FinishDirectorStop(n int, from uint64) (Receipt, error) {
	req := struct {
		Number int    `json:"number"`
		From   uint64 `json:"from"`
	}{n, from}
	return s.moveSession(fmt.Sprintf("director-swept:%d:%d", n, from), opFinishDirectorStop, req, n, from, func(d *DirectorSession) error {
		if !d.Sweeping {
			return invalid("director session %d has no stop sweeping its attempts", n)
		}
		d.Sweeping = false
		return nil
	})
}

// RecordDirectorRuntime records c, the containment unit the runtime of the newest session, number n, runs in, with
// the runtime as its worker, on the session with the mutation id "director-runtime:<n>". c must be Recoverable and
// name its worker. It changes neither the session's state nor its StateSequence. It is refused with ErrInvalid when n
// is not the newest session or is stopped, and ErrNotFound when there is no session n; a different c for the same
// session conflicts with ErrMutationConflict. Replaying returns the original receipt.
func (s *Store) RecordDirectorRuntime(n int, c Containment) (Receipt, error) {
	if n < 1 {
		return Receipt{}, invalid("director session number %d must be positive", n)
	}
	if err := c.Recoverable(); err != nil {
		return Receipt{}, invalid("director session %d runtime: %v", n, err)
	}
	if c.Worker == (ProcessIdentity{}) {
		return Receipt{}, invalid("director session %d runtime: a %s needs the runtime as its worker", n, c.Kind)
	}
	req := struct {
		Number  int         `json:"number"`
		Runtime Containment `json:"runtime"`
	}{n, c}
	return s.mutate("director-runtime:"+strconv.Itoa(n), opRecordRuntime, req, func(tx *bolt.Tx, r *Receipt) error {
		r.Record = directorSessionRecord(n)
		sessions := directorBucket(tx, bucketSessions)
		var d DirectorSession
		if err := getJSON(sessions, sessionKey(n), &d); err != nil {
			return fmt.Errorf("director session %d: %w", n, err)
		}
		switch k, _ := sessions.Cursor().Last(); {
		case string(k) != string(sessionKey(n)):
			return invalid("director session %d is superseded by a newer session", n)
		case d.State == DirectorStopped:
			return invalid("director session %d is stopped", n)
		}
		d.Runtime = c
		return putJSON(sessions, sessionKey(n), d)
	})
}

// moveSession applies move to the newest session, number n, read at state sequence from, as mutation id: it
// refuses a missing session with ErrNotFound and a superseded, stopped or since-moved one with ErrInvalid, except
// that FinishDirectorStop finishes a stopped one and RequestDirectorStop may hold it for recovery.
// It sets ReadyAt on the first move to running and EndedAt on the move to stopped, and records the new StateSequence.
func (s *Store) moveSession(id, op string, req any, n int, from uint64, move func(*DirectorSession) error) (Receipt, error) {
	if n < 1 {
		return Receipt{}, invalid("director session number %d must be positive", n)
	}
	return s.mutate(id, op, req, func(tx *bolt.Tx, r *Receipt) error {
		r.Record = directorSessionRecord(n)
		sessions := directorBucket(tx, bucketSessions)
		var d DirectorSession
		if err := getJSON(sessions, sessionKey(n), &d); err != nil {
			return fmt.Errorf("director session %d: %w", n, err)
		}
		if k, _ := sessions.Cursor().Last(); string(k) != string(sessionKey(n)) {
			return invalid("director session %d is superseded by a newer session", n)
		}
		switch {
		case d.StateSequence != from:
			return invalid("director session %d changed at sequence %d, after the read at %d", n, d.StateSequence, from)
		case d.State == DirectorStopped && op != opFinishDirectorStop && op != opRequestDirectorStop:
			return invalid("director session %d is stopped", n)
		}
		if err := move(&d); err != nil {
			return err
		}
		now := s.now().UTC()
		if d.State == DirectorRunning && d.ReadyAt.IsZero() {
			d.ReadyAt = now
		}
		if d.State == DirectorStopped && d.EndedAt.IsZero() {
			d.EndedAt = now
		}
		d.StateSequence = r.Sequence
		return putJSON(sessions, sessionKey(n), d)
	})
}

// DirectorSession returns session n, or ErrNotFound.
func (s *Store) DirectorSession(n int) (DirectorSession, error) {
	var d DirectorSession
	err := s.view(func(tx *bolt.Tx) error { return getJSON(directorBucket(tx, bucketSessions), sessionKey(n), &d) })
	if err != nil {
		return DirectorSession{}, fmt.Errorf("store: director session %d: %w", n, err)
	}
	return d, nil
}

// LatestDirectorSession returns the newest session, or ErrNotFound when the store has none.
func (s *Store) LatestDirectorSession() (DirectorSession, error) {
	var d DirectorSession
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		d, err = latestSession(tx)
		return err
	})
	if err != nil {
		return DirectorSession{}, fmt.Errorf("store: latest director session: %w", err)
	}
	return d, nil
}

func latestSession(tx *bolt.Tx) (DirectorSession, error) {
	var d DirectorSession
	sessions := directorBucket(tx, bucketSessions)
	if sessions == nil {
		return d, ErrNotFound
	}
	k, v := sessions.Cursor().Last()
	if k == nil {
		return d, ErrNotFound
	}
	return d, decode(k, v, &d)
}

// RecordWake records the wake decision for a message to the Director with the mutation id
// "wake:<message id>", so a message is woken for at most once. The message must exist and be addressed to the
// Director; a queued wake needs a non-blank one-line reason of at most MaxWakeReasonBytes and a woke wake has
// none. Sequence and Time are set by the store. Replaying the same decision returns the original receipt, and a
// different decision for the same message returns ErrMutationConflict.
func (s *Store) RecordWake(w Wake) (Receipt, error) {
	if err := validateWake(w); err != nil {
		return Receipt{}, err
	}
	return s.mutate(wakeMutationID(w.MessageID), opRecordWake, w, func(tx *bolt.Tx, r *Receipt) error {
		m, err := getMessage(tx, w.MessageID)
		if err != nil {
			return err
		}
		r.Repository, r.TaskID, r.Record = m.Repository, m.TaskID, "wake/"+m.ID
		if m.Recipient != RecipientDirector {
			return invalid("message %q is addressed to %s, not the director", m.ID, describeRecipient(m.To()))
		}
		wakes, err := createDirectorBucket(tx, bucketWakes)
		if err != nil {
			return err
		}
		w.Sequence, w.Time = r.Sequence, s.now().UTC()
		return putJSON(wakes, wakeKey(w.Sequence), w)
	})
}

// WakeFor returns the wake recorded for a message, or ErrNotFound.
func (s *Store) WakeFor(messageID string) (Wake, error) {
	if err := ValidateMessageID(messageID); err != nil {
		return Wake{}, err
	}
	var w Wake
	err := s.view(func(tx *bolt.Tx) error {
		var r Receipt
		if err := getJSON(tx.Bucket(bucketReceipts), []byte(wakeMutationID(messageID)), &r); err != nil || r.Operation != opRecordWake {
			return ErrNotFound
		}
		return getJSON(directorBucket(tx, bucketWakes), wakeKey(r.Sequence), &w)
	})
	if err != nil {
		return Wake{}, fmt.Errorf("store: wake for message %q: %w", messageID, err)
	}
	return w, nil
}

// Wakes returns at most limit (1 to MaxInboxLimit) wakes with a sequence greater than after, in sequence
// order, and whether more remain.
func (s *Store) Wakes(after uint64, limit int) ([]Wake, bool, error) {
	if limit < 1 || limit > MaxInboxLimit {
		return nil, false, invalid("wake limit %d must be between 1 and %d", limit, MaxInboxLimit)
	}
	var out []Wake
	more := false
	err := s.view(func(tx *bolt.Tx) error {
		wakes := directorBucket(tx, bucketWakes)
		if wakes == nil || after == ^uint64(0) {
			return nil
		}
		c := wakes.Cursor()
		for k, v := c.Seek(wakeKey(after + 1)); k != nil; k, v = c.Next() {
			if len(out) == limit {
				more = true
				return nil
			}
			var w Wake
			if err := decode(k, v, &w); err != nil {
				return err
			}
			out = append(out, w)
		}
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("store: wakes: %w", err)
	}
	return out, more, nil
}

// The message types Guildhall itself sends the Director, and the documented kinds of a local message.
const (
	MessageAdvanceFailed    = "advance-failed"
	MessageAttemptFailed    = "attempt-failed"
	MessageAttemptRetried   = "attempt-retried"
	MessageBudgetRefused    = "budget-refused"
	MessageChangesRequested = "changes-requested"
	MessageChecksFailed     = "checks-failed"
	MessageChecksPassed     = "checks-passed"
	MessageChecksPending    = "checks-pending"
	MessagePRBlocked        = "pr-blocked"
	MessageDecision         = "decision"
	// MessageDependencyHeld: the dependency gate holds a task until its dependencies merge; it is not the
	// between-Directors dependency-notice.
	MessageDependencyHeld = "dependency-held"
	// MessageDependencyCancelled: a task this task depends on was cancelled (guildhall task cancel), so it can
	// never merge; the dependent stays open for the Director.
	MessageDependencyCancelled = "dependency-cancelled"
	// MessageBranchKept: an Artisan attempt runs on its untouched task branch, which could not be moved to the
	// current main.
	MessageBranchKept       = "branch-kept"
	MessageDirectorStage    = "director-stage"
	MessageHerdrLeftOpen    = "herdr-workspace-left-open"
	MessageJudgeFailed      = "judge-failed"
	MessageJudgement        = "judgement"
	MessageJudgementRefused = "judgement-refused"
	MessageLoopExitRejected = "loop-exit-rejected"
	MessageMergeHeld        = "merge-held"
	MessageMergeRefused     = "merge-refused"
	MessageMerged           = "merged"
	// MessageFinished: a task whose workflow has no deliver step, such as a hallmark, ran every step and is done,
	// with no pull request; it is never a merged message, so it counts toward no maintenance cadence.
	MessageFinished      = "finished"
	MessagePublished     = "published"
	MessageStall         = "stall"
	MessageWorkerStalled = "worker-stalled"
	MessageWorkspaceKept = "workspace-kept"
	// MessagePruneKept: the session supervisor's daily prune left a done task's worktree, because retiring it would
	// lose something or it is not a worktree.
	MessagePruneKept = "prune-workspace-kept"
	// The merge queue's messages (docs/interpreter.md#the-merge-queue): a task's place in the queue, a rebase of
	// its head onto the base tip, a rebase that became a build send-back, a front that rebased three times in one
	// stay, and a merge approval carried over to a rebased head.
	MessageMergeQueued     = "merge-queued"
	MessageRebased         = "rebased"
	MessageRebaseEscalated = "rebase-escalated"
	MessageRebaseStuck     = "rebase-stuck"
	MessageApprovalCarried = "approval-carried"
	MessageQuestion        = "question"
	MessageNote            = "note"
	// MessageHoldReleased is the session supervisor's report that a hold was released: a park, decision deferral or
	// follow-up whose named tasks merged or one of which was cancelled (docs/decisions/hold-until-merged.md).
	MessageHoldReleased = "hold-released"
)

// MessageTypes is the registry of message types DirectorUnread and the wake line count by name, each with its
// sender: "engine" for an advance (internal/interp), "launcher" for an attempt's launcher, "quality" for a judge,
// "supervisor" for a Director session's supervisor, "cancel" for `guildhall task cancel` and "local" for the
// documented kinds of `messages send`. Every other type, which any sender may choose, counts as "other", so the
// wake line carries no sender-chosen text. A type is added here by the change that sends it, never by a sender.
var MessageTypes = map[string]string{
	MessageAdvanceFailed: "launcher", MessageAttemptFailed: "engine", MessageAttemptRetried: "engine",
	MessageBudgetRefused: "engine", MessageChangesRequested: "engine", MessageChecksFailed: "engine",
	MessageChecksPassed: "engine", MessageChecksPending: "engine", MessagePRBlocked: "engine", MessageDecision: "engine",
	MessageDependencyHeld: "engine", MessageBranchKept: "engine",
	MessageDirectorStage: "engine", MessageLoopExitRejected: "engine", MessageMergeHeld: "engine",
	MessageMergeRefused: "engine", MessageMerged: "engine", MessageFinished: "engine", MessagePublished: "engine", MessageStall: "engine",
	MessageWorkerStalled: "engine and launcher", MessageWorkspaceKept: "engine",
	MessageMergeQueued: "engine", MessageRebased: "engine", MessageRebaseEscalated: "engine", MessageRebaseStuck: "engine",
	MessageApprovalCarried: "engine",
	MessageQuestion:        "local", MessageNote: "local",
	MessageJudgeFailed: "quality", MessageJudgement: "quality", MessageJudgementRefused: "quality",
	MessageHerdrLeftOpen: "supervisor", MessagePruneKept: "supervisor", MessageHoldReleased: "supervisor",
	MessageDependencyCancelled: "cancel",
}

// UnreadFrom is DirectorUnread's key for unread messages of type t from sender: "<t> from <sender>".
func UnreadFrom(t, sender string) string { return t + " from " + sender }

// DirectorUnread counts the Director's unacknowledged messages by message type, keyed only by the types in
// MessageTypes and "other", which counts every other type. Notes are counted by sender instead, keyed
// UnreadFrom(MessageNote, sender) with the sender from MessageSenders, so no key carries free text.
// Its cost grows with the number of unread messages and repositories, not with the store.
func (s *Store) DirectorUnread() (map[string]int, error) {
	counts := map[string]int{}
	err := s.view(func(tx *bolt.Tx) error {
		prefix := inboxPrefix(Recipient{Role: RecipientDirector})
		return tx.Bucket(bucketRepos).ForEachBucket(func(repo []byte) error {
			inbox := repoBucket(tx, string(repo), bucketInbox)
			if inbox == nil {
				return nil
			}
			c := inbox.Cursor()
			for k, id := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, id = c.Next() {
				var m Message
				if err := getJSON(repoBucket(tx, string(repo), bucketMessages), id, &m); err != nil {
					return fmt.Errorf("inbox message %q: %w", id, err)
				}
				if m.Type == MessageNote {
					counts[UnreadFrom(m.Type, unreadSender(m))]++
				} else if _, ok := MessageTypes[m.Type]; ok {
					counts[m.Type]++
				} else {
					counts["other"]++
				}
			}
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: director unread: %w", err)
	}
	return counts, nil
}

// unreadSender is m's sender as the wake line names it: one of MessageSenders, SenderUnknown for any other.
func unreadSender(m Message) string {
	if s := MessageSender(m); slices.Contains(MessageSenders, s) {
		return s
	}
	return SenderUnknown
}

func directorBucket(tx *bolt.Tx, name []byte) *bolt.Bucket {
	if d := tx.Bucket(bucketDirector); d != nil {
		return d.Bucket(name)
	}
	return nil
}

func createDirectorBucket(tx *bolt.Tx, name []byte) (*bolt.Bucket, error) {
	d, err := tx.CreateBucketIfNotExists(bucketDirector)
	if err != nil {
		return nil, err
	}
	return d.CreateBucketIfNotExists(name)
}

func validateSession(d DirectorSession) error {
	switch {
	case d.Number < 1:
		return invalid("director session number %d must be positive", d.Number)
	case d.State != DirectorRecovering:
		return invalid("a new director session is %s, not %q", DirectorRecovering, d.State)
	case d.Identity.PID < 1:
		return invalid("director session pid %d must be positive", d.Identity.PID)
	case !d.StartedAt.IsZero() || !d.ReadyAt.IsZero() || !d.EndedAt.IsZero() || d.StateSequence != 0 || d.EndReason != "":
		return invalid("director session times, end reason and state sequence are set by the store")
	case d.Runtime != (Containment{}):
		return invalid("a director session's runtime is recorded by RecordDirectorRuntime once it runs")
	}
	if err := validateText("director session token", d.Identity.Token, 256); err != nil {
		return err
	}
	if err := validateText("director session release", d.Release, 256); err != nil {
		return err
	}
	if _, err := digest.ParseHex(d.BinarySHA256); d.BinarySHA256 != "" && err != nil {
		return invalid("director session binary sha256 %q must be 64 lowercase hex characters", d.BinarySHA256)
	}
	if _, err := digest.Parse(d.ConfigDigest); err != nil {
		return invalid(`director session config digest %q must be "sha256:<64 lowercase hex>"`, d.ConfigDigest)
	}
	switch p := d.Handoff; {
	case p.AfterTasks < 0:
		return invalid("director session handoff after %d tasks must not be negative", p.AfterTasks)
	case p.ContextPercent < 0 || p.ContextPercent > 100:
		return invalid("director session handoff context percent %d must be between 0 and 100", p.ContextPercent)
	case p.ContextFile != "" && !filepath.IsAbs(p.ContextFile):
		return invalid("director session context report %q must be an absolute path", p.ContextFile)
	case len(p.ContextFile) > 4096:
		return invalid("director session context report path is %d bytes; the limit is 4096", len(p.ContextFile))
	}
	if p := d.Maintenance; p.NoteAuditEverySessions < 0 || p.HallmarkAfterMerges < 0 {
		return invalid("director session maintenance cadences %d sessions and %d merges must not be negative", p.NoteAuditEverySessions, p.HallmarkAfterMerges)
	}
	if d.Pane != (DirectorPane{}) {
		for name, v := range map[string]string{"session": d.Pane.Session, "workspace": d.Pane.Workspace, "tab": d.Pane.Tab, "pane": d.Pane.Pane} {
			if err := validateText("director pane "+name, v, 256); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateWake(w Wake) error {
	if err := ValidateMessageID(w.MessageID); err != nil {
		return err
	}
	if err := validateIDs(w.SenderType); err != nil {
		return err
	}
	switch {
	case w.Sequence != 0 || !w.Time.IsZero():
		return invalid("a wake's sequence and time are set by the store")
	case w.Decision == WakeWoke && w.Reason != "":
		return invalid("a woke wake has no reason")
	case w.Decision == WakeQueued:
		if err := validateText("wake reason", w.Reason, MaxWakeReasonBytes); err != nil {
			return err
		}
		if strings.ContainsAny(w.Reason, "\r\n") {
			return invalid("wake reason must be one line")
		}
	case w.Decision != WakeWoke:
		return invalid("wake decision %q must be %q or %q", w.Decision, WakeWoke, WakeQueued)
	}
	return nil
}

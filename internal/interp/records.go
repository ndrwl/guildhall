package interp

import (
	"context"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
)

// Records is the part of the Director store the engine reads and writes, declared here by the consumer
// (D9: the store has no general interface). In production cli.storeRecords implements it, opening the store
// for each operation; *store.Store implements it in process, and interptest.Records is its in-memory double.
// All three pass interptest.RunRecordsContract.
//
// Reads return a task's records in key order: attempts by step then number, documents by name and
// decisions by id, rulings by document then finding. Task returns store.ErrNotFound for an unknown task; list reads return empty slices.
// Every write names a mutation id: repeating it with the same request returns the original receipt and
// changes nothing, and reusing it for a different request fails with store.ErrMutationConflict.
// StartAttempt numbers attempts of a step from 1 and refuses, with store.ErrInvalid, while one is running;
// StartCappedAttempt is StartAttempt refused with a *store.CapRefusal (store.ErrAttemptCap), recording nothing,
// when the store is at one of its caps: caps.MaxRunning attempts started and not finished, or caps.MaxPerDay
// attempts started in the current UTC day, in any task; zero is no cap. The count and the start are one write, and
// a replayed mutation id returns its receipt whatever the caps are now.
// FinishAttempt finishes a running attempt once and the first finish stands: finishing it again with the same
// outcome under another mutation id returns the first finish's receipt, and another outcome is store.ErrInvalid.
// RejectAttempt records a result-validation failure with a nonempty reason, changing an unfinished or
// succeeded attempt to failed and preserving the launcher finish as a separate historical receipt.
// Failed or stopped attempts are refused with store.ErrInvalid; an unknown attempt is store.ErrNotFound.
// Repeating its mutation id is idempotent, including after a retry starts.
// A document or decision is recorded once per name or id and an existing one is refused with store.ErrExists. ContestRuling contests a recorded ruling once: an unknown
// one is store.ErrNotFound and a contested one store.ErrInvalid. SetTaskState closes an open task as done or
// cancelled: another state, or a task already closed, is store.ErrInvalid, and an unknown one store.ErrNotFound.
// Implementations are safe for concurrent use.
// Rulings are recorded by the Director (guildhall task finding rule), never by the engine.
//
// Workflow approvals are not read here: they are enforced at task creation (docs/decisions/0056-approval-enforcement-at-task-creation.md).
type Records interface {
	Task(repository, taskID string) (store.Task, error)
	Attempts(repository, taskID string) ([]store.StageAttempt, error)
	Documents(repository, taskID string) ([]store.DocumentRef, error)
	Decisions(repository, taskID string) ([]store.Decision, error)
	Rulings(repository, taskID string) ([]store.Ruling, error)
	StartAttempt(mutationID, repository, taskID, step string) (store.Receipt, error)
	StartCappedAttempt(mutationID, repository, taskID, step string, caps store.AttemptCaps) (store.Receipt, error)
	FinishAttempt(mutationID, repository, taskID, step string, number int, outcome store.AttemptOutcome) (store.Receipt, error)
	RejectAttempt(mutationID, repository, taskID, step string, number int, reason string) (store.Receipt, error)
	PutDocument(mutationID string, d store.DocumentRef) (store.Receipt, error)
	OpenDecision(mutationID string, d store.Decision) (store.Receipt, error)
	ContestRuling(mutationID, repository, taskID, document, finding, by string) (store.Receipt, error)
	FullReviews(repository, taskID string) ([]store.FullReview, error)
	ConsumeFullReview(mutationID, repository, taskID string, number int, by string) (store.Receipt, error)
	SetTaskState(mutationID, repository, taskID string, state store.TaskState) (store.Receipt, error)
	// DependencyOverride returns the Director's dependency override of a task, or store.ErrNotFound when it has
	// none; the Director records it (guildhall task start --ignore-deps), never the engine.
	DependencyOverride(repository, taskID string) (store.DependencyOverride, error)
}

var _ Records = (*store.Store)(nil)

// Outbox is how the engine tells the Director what changed: the send half of messaging.Mailbox.
// Resending the same envelope returns the original receipt, so a replayed advance sends nothing new.
type Outbox interface {
	Send(to store.Recipient, e messaging.Envelope) (store.Receipt, error)
}

var _ Outbox = messaging.Mailbox(nil)

// Prompter renders one Artisan attempt: render.Prompt with the task baseline and skill roots the caller chose.
// Its contract is render.Prompt's, including ErrNotEmpty for a directory that already holds files.
type Prompter interface {
	Prompt(stage render.Stage, inputs []render.Input) (render.Rendered, error)
}

// PromptFunc adapts a function, usually a closure over render.Prompt, to Prompter.
type PromptFunc func(stage render.Stage, inputs []render.Input) (render.Rendered, error)

// Prompt calls f.
func (f PromptFunc) Prompt(stage render.Stage, inputs []render.Input) (render.Rendered, error) {
	return f(stage, inputs)
}

// Forge is the pull-request boundary publish, validate and deliver call (docs/interpreter.md#publish-and-deliver).
// forge.GitHub implements it and forgetest.Fake is its double; both pass forgetest.RunForgeContract.
type Forge interface {
	Publish(ctx context.Context, c forge.Change) (forge.PullRequest, error)
	Merge(ctx context.Context, m forge.Merge) (forge.PullRequest, error)
	Checks(ctx context.Context, c forge.Commit) ([]forge.Check, error)
}

var _ Forge = forge.GitHub{}

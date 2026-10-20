package interp

import (
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
)

// Records is the part of the Director store the engine reads and writes, declared here by the consumer
// (D9: the store has no general interface). *store.Store implements it, and interptest.Records is its
// in-memory double; both pass interptest.RunRecordsContract.
//
// Reads return a task's records in key order: attempts by step then number, documents by name and
// decisions by id. Task returns store.ErrNotFound for an unknown task; list reads return empty slices.
// Every write names a mutation id: repeating it with the same request returns the original receipt and
// changes nothing, and reusing it for a different request fails with store.ErrMutationConflict.
// StartAttempt numbers attempts of a step from 1 and refuses, with store.ErrInvalid, while one is running;
// FinishAttempt finishes a running attempt once; a document or decision is recorded once per name or id
// and an existing one is refused with store.ErrExists. Implementations are safe for concurrent use.
//
// Workflow approvals are not read here: enforcing them at task creation is separate work (S10).
type Records interface {
	Task(repository, taskID string) (store.Task, error)
	Attempts(repository, taskID string) ([]store.StageAttempt, error)
	Documents(repository, taskID string) ([]store.DocumentRef, error)
	Decisions(repository, taskID string) ([]store.Decision, error)
	StartAttempt(mutationID, repository, taskID, step string) (store.Receipt, error)
	FinishAttempt(mutationID, repository, taskID, step string, number int, outcome store.AttemptOutcome) (store.Receipt, error)
	PutDocument(mutationID string, d store.DocumentRef) (store.Receipt, error)
	OpenDecision(mutationID string, d store.Decision) (store.Receipt, error)
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

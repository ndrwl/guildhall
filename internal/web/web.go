// Package web serves the Guildmaster's task pages: one read-only HTML page per task, and a reader page for each of
// its recorded documents, privately on the tailnet (docs/web.md, docs/contracts/web.md).
//
// The package renders and routes only. It never opens the store: a Source, which internal/cli fills from Clerk's
// readers, gives it each task as a Task, a page model of plain values, and the package reads a document's bytes
// from the copy's document directory only after checking them against the sha256 the Task records. Every page is
// one HTML document with inline CSS, no script and no external request, served with a Content-Security-Policy
// that allows neither.
package web

import (
	"errors"
	"time"
)

// ErrNotFound is a Source's answer for a task the copy does not hold.
var ErrNotFound = errors.New("no such task in this copy")

// ErrBusy is a Source's answer when a writer holds the store's lock past the Source's bounded wait.
var ErrBusy = errors.New("the store is busy")

// Source reads one task's page model. Each call opens the store, reads, and closes it before returning, so no page
// is rendered while the store is open. It returns ErrNotFound for an unknown task and ErrBusy for a store whose
// lock it could not take in time; any other error is shown as a refusal with no detail.
type Source interface {
	Task(repo, id string) (Task, error)
}

// StepState is the state of a step in the stepper and of a node on the story's spine; each has its own colour, icon
// and word on the page.
type StepState string

const (
	// StateDone is a finished step: its attempt succeeded or its decision was answered with a positive, carry-on or
	// neutral option of the option table, or in free text (✓, green).
	StateDone StepState = "ok"
	// StateBack is a step sent back: a review round's changes sent its work back, or its decision was answered with
	// an option the option table does not describe for its kind (↩, violet).
	StateBack StepState = "back"
	// StateRunning is a step whose attempt is at work (▸, blue).
	StateRunning StepState = "run"
	// StateYou is a step waiting on the Guildmaster: a decision of it is open (?, amber).
	StateYou StepState = "you"
	// StateLater is a step the workflow declares that has not started (hollow).
	StateLater StepState = "later"
	// StateFailed is an attempt that failed or was stopped (✗, red).
	StateFailed StepState = "bad"
	// StateVoid is a decision nobody waits for any more, such as a superseded acceptance (⊘, grey).
	StateVoid StepState = "void"
)

// Task is one task page's model: everything the page shows, as the store held it when ReadAt was taken.
type Task struct {
	Repository, ID, Title string
	// State is the task's state: open, done or cancelled.
	State string
	// Stands is where the task stands, Clerk's one phrase for it (docs/clerk.md).
	Stands string
	// Related are the tasks this one names: its dependencies, the task it fixes, the tasks whose documents are
	// linked to it.
	Related []Related
	// Steps are the bound workflow's steps in order, for the stepper; a multi-PR task's are its current slice's.
	Steps []Step
	// Slices is a multi-PR task's slices, nil for a one-slice task.
	Slices *Slices
	Status Status
	// Asks are the task's open decisions, the Guildmaster's asks, open ones first and then deferred ones.
	Asks []Ask
	// DirectorStage names the step of a Director stage the task waits on, such as grill, which the Director runs
	// with the Guildmaster in its session; "" when it waits on none.
	DirectorStage string
	// DirectorWaits are the task's other needs_me reasons, which wait on the Director, not the Guildmaster.
	DirectorWaits []string
	// Context is the Guildmaster's words the Director recorded on the task, oldest first.
	Context []Quote
	// Documents are the task's recorded documents and the documents of other tasks linked to it.
	Documents []Document
	// Story is the task's history, oldest first; the page shows it newest first.
	Story  []Node
	ReadAt time.Time
}

// Related is a task this task names, with how it is related, such as "depends on" or "fixes".
type Related struct {
	Label, Repository, ID string
}

// Step is one entry of the stepper.
type Step struct {
	Name  string
	State StepState
	// Note is a short line under the step's name, such as "round 2" or "PR #241".
	Note string
}

// Slices is a multi-PR task's slices and which is current.
type Slices struct {
	Current, Total int
	Items          []Slice
}

// Slice is one slice of a multi-PR task: its title, state (merged, current or planned) and pull request.
type Slice struct {
	Index int
	Title string
	State string
	PR    *PullRequest
}

// PullRequest is a recorded pull request; Merged says a merge record is held for it.
type PullRequest struct {
	Number int
	URL    string
	Merged bool
}

// CI states, as the store holds them; "" is no checks recorded.
const (
	CIPassed  = "passed"
	CIFailed  = "failed"
	CIPending = "pending"
)

// Status is the Status section: the current step and attempt, the pull request and its checks.
type Status struct {
	// Step is the step the task is at, Round its round, zero when unknown.
	Step  string
	Round int
	// Attempt is the latest attempt, nil when there is none.
	Attempt *Attempt
	// PR is the latest recorded pull request, the latest slice's for a multi-PR task.
	PR *PullRequest
	// CI is the PR's checks state: CIPassed, CIFailed, CIPending or "".
	CI string
}

// Attempt is one attempt of a step, its outcome ("" while it runs) and when it finished or started.
type Attempt struct {
	Step    string
	Number  int
	Outcome string
	At      *time.Time
}

// Ask is one open decision of the task.
type Ask struct {
	ID string
	// Kind is the decision's kind, such as approval or loop-exit; "" for an untyped decision.
	Kind     string
	Question string
	Options  []string
	// Subject is the digest the decision is bound to, "" when it has none, and SubjectDocument the name of the
	// task document whose digest it is, "" when no document matches.
	Subject, SubjectDocument string
	OpenedAt                 *time.Time
	// Deferred says the Director deferred it until other tasks merge; it is still answerable.
	Deferred bool
	// Findings are the subject review's open findings, for a review loop exit.
	Findings []Finding
}

// Finding is one review finding; Rule is its rule or severity label, and Fixed says a later round resolved it.
type Finding struct {
	ID, Rule, Summary string
	Fixed             bool
}

// Quote is one entry of the Guildmaster's words, with who recorded it and when.
type Quote struct {
	Text, Author string
	At           time.Time
}

// Document is one recorded document: the task it belongs to, its store name, its path relative to the document
// directory and the lowercase hex sha256 recorded for it, with the group the page lists it under (Review, Design,
// Spec, Research, Grill, Build or another), the step and attempt or round that wrote it, and when it was recorded.
type Document struct {
	Repository, TaskID, Name, Path, SHA256 string
	Kind                                   string
	Step                                   string
	Attempt                                int
	RecordedAt                             *time.Time
}

// Node is one entry of the story.
type Node struct {
	Title string
	State StepState
	// Word is the state as the node says it, such as "changes requested · sent back to build".
	Word       string
	Start, End *time.Time
	// Body is one quiet line under the title.
	Body     string
	Findings []Finding
	// Said is words quoted on the node: the Guildmaster's answer, or the feedback a send-back carried.
	Said []Said
	// Documents are names of the task's documents the node links to.
	Documents []string
	// Ask is the id of the node's open decision, linked to its ask on Now; "" for none.
	Ask string
}

// Said is quoted words on a node and where they were given; Back marks words that sent the work back.
type Said struct {
	Text, Where string
	Back        bool
}

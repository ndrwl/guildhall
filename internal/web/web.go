// Package web serves the Guildmaster's task pages: one read-only HTML page per task, and a reader page for each of
// its recorded documents, privately on the tailnet (docs/web.md, docs/contracts/web.md).
//
// The package renders and routes only. It never opens the store: a Source, which internal/cli fills from Clerk's
// readers, gives it each task as a Task, a page model of plain values, and the package reads a document's bytes
// from the copy's document directory only after checking them against the sha256 the Task records. Every page is
// one HTML document with inline CSS, only Guildhall's own scripts (embedded files under scripts/, pinned by hash)
// and no external request, served with a Content-Security-Policy that allows no other script and no request.
package web

import (
	"errors"
	"time"
)

// ErrNotFound is a Source's answer for a task the copy does not hold.
var ErrNotFound = errors.New("no such task in this copy")

// ErrBusy is a Source's answer when a writer holds the store's lock past the Source's bounded wait.
var ErrBusy = errors.New("the store is busy")

// Source reads one task's page model, and the task list's. Each call opens the store, reads, and closes it before
// returning, so no page is rendered while the store is open. Task returns ErrNotFound for an unknown task, and
// either returns ErrBusy for a store whose lock it could not take in time; any other error is shown as a refusal
// with no detail.
type Source interface {
	Task(repo, id string) (Task, error)
	Tasks() (TaskList, error)
	// Tail reads only the tail of repo/id's conversation, for the page's poll: it opens the store read-only and
	// builds none of Clerk's read. Its errors are Task's.
	Tail(repo, id string, after int) (Tail, error)
}

// Tail is the tail of a task's conversation: the number of its last message, 0 for none, and its messages numbered
// above the one the poll named, oldest first, of which the poll counts only the Director's.
type Tail struct {
	Last  int
	Newer []Message
}

// Group is the group of the task list a task is listed under: Needs you, one of the stage groups, or Recently
// finished.
type Group string

const (
	// GroupYou is a task waiting on the Guildmaster: an open or deferred ask, or a Director stage run with them. Needs
	// you lists it, pinned above the stage groups.
	GroupYou Group = "you"
	// GroupPlan is a task whose next step, of a grill, research, plan, refute, document review, plan approval or
	// design type, starts, runs or waits for a free slot.
	GroupPlan Group = "plan"
	// GroupBuild is a task whose next step, of a build, rebase, review, publish, validate, deliver, hallmark or
	// handoff type, starts, runs or waits for a free slot.
	GroupBuild Group = "build"
	// GroupCI is a task waiting for the checks of its pull request.
	GroupCI Group = "ci"
	// GroupMergeQueue is a task waiting in its repository's merge queue.
	GroupMergeQueue Group = "merge-queue"
	// GroupHeld is an open task in no running stage: it waits on its dependencies or the Director, or it is paused,
	// parked or not started.
	GroupHeld Group = "held"
	// GroupFinished is a task merged, done or cancelled in the last 24 hours.
	GroupFinished Group = "finished"
)

// TaskList is the task list's model: every task of the copy that is open or finished in the last 24 hours, in the
// Source's order (open ones by priority then id, finished ones newest first), as the store held them when ReadAt was
// taken. The page groups, sorts and filters them itself.
type TaskList struct {
	Tasks  []Listed
	ReadAt time.Time
}

// Listed is one row of the task list: the task, its group, its where-it-stands sentence (Stands, Clerk's phrase, is
// the fallback for an empty one) and since when it has stood so, nil when unknown.
type Listed struct {
	Repository, ID, Title string
	Group                 Group
	Where                 Where
	Stands                string
	Since                 *time.Time
	// Priority is the task's priority, 0 (p0, urgent) to 5 (p5, someday); 3 (p3), the default, is left off the row.
	Priority int
	// Step is the task's next step as the list names it, such as "build" or "plan approval", "" for a task with no
	// next step, and Attempt that step's latest attempt number, 0 when it has none yet.
	Step    string
	Attempt int
	// Slice and Slices are a multi-PR task's current slice and its number of slices, both 0 for a one-slice task.
	Slice, Slices int
	// PR is the task's pull request, nil before one is published, and CI its checks: CIPassed, CIFailed, CIPending
	// or "".
	PR *PullRequest
	CI string
	// Ask names what a task waiting on the Guildmaster is asked, such as "Approval of the plan"; "" for any other.
	Ask string
	// Queue is the task's place in its repository's merge queue, nil while it is not in it.
	Queue *QueuePlace
	// Finished is how a finished task ended: "merged", "done" or "cancelled"; "" for an open task.
	Finished string
}

// QueuePlace is a task's place in its repository's merge queue: its position from 1, whether it is the front,
// whether the Director holds its merge, and what it waits on: "queue" behind the front, and for the front "ci",
// "rebase" or "decision".
type QueuePlace struct {
	Position  int
	Front     bool
	Held      bool
	WaitingOn string
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
	// StateYou is a step waiting on the Guildmaster: a decision of it is open (●, amber).
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
	// Where is where the task stands as the page says it, one sentence; its zero value falls back to Stands.
	Where Where
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
	Story []Node
	// Conversation is the task's talk between the Guildmaster and the Director, oldest first.
	Conversation []Message
	// Tag is the current stage's attempt, such as grill-2, whose talk shows on Now; "" for a closed task or one with
	// no attempt yet.
	Tag string
	// TagStage is Tag's step, such as grill, for its chip; "" when it names none.
	TagStage string
	// TagRunning says Tag's attempt is still running, so a grill summary under it can be answered; one left
	// unanswered once it finished folds as not answered.
	TagRunning bool
	// Grill is the task's grill mode as task show prints it, such as "grill: unattended (director): <reason>" or
	// "grill: live", shown in the band under where it stands (docs/tasks.md#grill-mode); "" for a task whose
	// workflow has no grill.
	Grill string
	// Drafts are the Guildmaster's latest saved draft of each question of a round not sent yet; none on a closed
	// task, whose drafts were dropped.
	Drafts []Draft
	ReadAt time.Time
}

// Draft is the Guildmaster's latest saved draft of one question of a grill round: the round's tag and conversation
// entry number, the question's number from 1, its mark (DraftAccept, DraftWords or "" for words that are not an
// answer yet), the words in its box, when it was saved and its draft entry number, the highest of which in a round
// its form posts as drafts.
type Draft struct {
	Tag             string
	Round, Question int
	Number          int
	Mark, Text      string
	// Pick is the letter a draft marked DraftPick picks.
	Pick string
	At   time.Time
}

// The marks of a Draft.
const (
	DraftAccept = "accept"
	DraftWords  = "words"
	DraftPick   = "pick"
)

// Where is a where-it-stands sentence: Lead, shown in bold, says who or what the task waits on, such as "Waiting for
// you on 2 asks:", and Rest what for; You says it waits on the Guildmaster.
type Where struct {
	Lead, Rest string
	You        bool
}

// The authors of a Message, the store's fixed vocabulary.
const (
	AuthorGuildmasterPage = "guildmaster-page"
	AuthorGuildmasterChat = "guildmaster-chat"
	AuthorDirector        = "director"
)

// The kinds of a Message the page treats apart: KindRecommendation recommends an answer to its ask, which the page
// offers to Confirm, and KindConfirmed records that the Guildmaster confirmed one.
const (
	KindRecommendation = "recommendation"
	KindConfirmed      = "confirmed"
	// KindRound is a grill round the Director put on the page (ParseRound's text), which the page shows as one form,
	// and KindRoundAnswers the Guildmaster's answers to it (RoundAnswersText's), folded into the round's row.
	KindRound        = "round"
	KindRoundAnswers = "round-answers"
	// KindSummary is the grill's closing summary the Director put on the page, which the page asks with Confirm and
	// Request changes, and KindSummaryAnswer the Guildmaster's answer (SummaryAnswerText's), its outcome in Option,
	// folded into the summary's row.
	KindSummary       = "summary"
	KindSummaryAnswer = "summary-answer"
)

// Message is one entry of the task's conversation: its number, who wrote it (an Author constant), the attempt or
// decision it is about (its tag) and that tag's step (Stage, "" when the tag names none), its kind, its Markdown
// text and when it was recorded. A recommendation's Option is the answer it recommends and Words the Guildmaster's
// words the ask's Your words box is pre-filled with; a confirmation's Option is the answer it gave and Confirms the
// number of the recommendation it confirmed. A round's answers name in Round the number of the round they answer,
// and a summary's answer the number of the summary it answers, with its outcome as its Option.
type Message struct {
	Number           int
	Author           string
	Tag, Stage, Kind string
	Text             string
	At               time.Time
	Option, Words    string
	Confirms         int
	Round            int
}

// Related is a task this task names, with how it is related, such as "depends on" or "fixes".
type Related struct {
	Label, Repository, ID string
}

// Step is one entry of the stepper.
type Step struct {
	Name  string
	State StepState
	// Note is a short line under the step's name, such as "round 2" or "plan approved", and Raw the same said with
	// its record id, such as "accept-2 approved", shown as the note's title; "" when the note names no record.
	Note, Raw string
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
	// Step is the step the task is at as the page names it, such as "Slice 2 review", Round its round, zero when
	// unknown.
	Step  string
	Round int
	// Attempt is the latest attempt, nil when there is none.
	Attempt *Attempt
	// PR is the latest recorded pull request, the latest slice's for a multi-PR task.
	PR *PullRequest
	// CI is the PR's checks state: CIPassed, CIFailed, CIPending or "".
	CI string
}

// Attempt is one attempt of a step, its outcome ("" while it runs) and when it finished or started; Name is how the
// page names it, such as "Slice 2 review, attempt 2", "" to name it by its record id.
type Attempt struct {
	Step    string
	Name    string
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
	// Mockup is the name of the task document holding a design ask's mock-up, the asked designer attempt's mockup
	// part, designer-<k>-mockup, or for a mock-up recorded before that part, its legacy name designer-<k>-design-2;
	// "" for any other ask and for a design with no mock-up.
	Mockup string
	// Recommendation is the Director's one recommendation on the ask, which marks its tile and, from task say, is
	// its Confirm; nil for none.
	Recommendation *Recommendation
	// Stage is the step the decision belongs to, such as plan, for its tag chip; "" when its id names none.
	Stage string
	// Headline is the ask's readable first line, a fixed sentence by kind such as "The plan for slice 2 is ready
	// for your approval.", shown in place of the question's own first line, which moves into the rest of the
	// question; "" keeps the question's first line. Phrase is the same ask's short form, such as "approval of the
	// plan", which the Needs you index and the where-it-stands line name it by.
	Headline, Phrase string
	// Explanation is the Director's explanation of the ask (decide explain), nil when it has none.
	Explanation *Explanation
}

// Explanation is the Director's plain-English explanation of an ask: Markdown Text, rendered only through
// RenderMarkdown; Recommend, the option it recommends, "" for none, which marks no tile itself (the ask's one
// Recommendation does); when it was written; and Stale, which says it was
// written against an earlier subject than the ask's, so the page hides it.
type Explanation struct {
	Text, Recommend string
	At              time.Time
	Stale           bool
}

// Recommendation is the Director's one recommended answer to an ask (docs/store.md#decision-recommendations):
// Option, one of the ask's options when the page marks it; Words, which pre-fill the Your words box; Number, the
// conversation entry of the task say recommendation that made it, which Confirm names, 0 for one made otherwise;
// when it was made; and Stale, which says it was made against an earlier subject than the ask's, so the page marks
// no tile and offers no Confirm, and says so.
type Recommendation struct {
	Option, Words string
	Number        int
	At            time.Time
	Stale         bool
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
// Label is the name the task page shows for it, such as "Grill notes, round 3", "" to show its store name.
type Document struct {
	Repository, TaskID, Name, Path, SHA256 string
	Label                                  string
	Kind                                   string
	Step                                   string
	Attempt                                int
	RecordedAt                             *time.Time
}

// Node is one entry of the story.
type Node struct {
	// Title says what happened as a sentence, such as "You approved the plan", and Raw is the record id it is about,
	// shown as the title's title, "" for none.
	Title, Raw string
	State      StepState
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
	// Events are the raw records the node was built from, oldest first, shown folded under it.
	Events []Event
	// Tag is the attempt (such as grill-2) or decision (such as accept-1) the node is, whose talk folds under it;
	// "" for a node that is neither.
	Tag string
}

// Event is one raw record behind a story node, such as "attempt review--s2-2 started · review", and when it was
// committed, nil when unknown.
type Event struct {
	At   *time.Time
	Text string
}

// Said is quoted words on a node and where they were given; Back marks words that sent the work back.
type Said struct {
	Text, Where string
	Back        bool
}

package interp

import (
	"cmp"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// hallmarkKindType is the step type of a read-only report stage (docs/decisions/report-only-workflows.md).
const hallmarkKindType = "hallmark"

// hallmarkReadOnly is the start of every refusal of a hallmark result that changed the project.
const hallmarkReadOnly = "hallmarks must not change the project"

// preparedName is the document holding the commit the worktree was at when attempt n of hallmark step was launched,
// <step>-<n>-prepared.
func preparedName(step string, n int) string {
	return documentName(place{step: step, round: n, part: "prepared"})
}

// recordPrepared records, once, the HEAD of the task's worktree as attempt n of hallmark step s is launched, after
// the task branch was fast-forwarded; a relaunch keeps the first record, so intake compares the result with the
// commit the attempt started at. Without Workspaces nothing is recorded.
func (r *run) recordPrepared(s workflow.Step, n int) error {
	if r.e.Workspaces == nil || s.Type != hallmarkKindType {
		return nil
	}
	name := preparedName(s.ID, n)
	if r.document(name) != nil {
		return nil
	}
	head, err := (git.Program{}).Head(r.ctx, r.workPath())
	if err != nil {
		return fmt.Errorf("prepared head of %s attempt %d: %w", s.ID, n, err)
	}
	data := []byte(head + "\n")
	return r.put(store.DocumentRef{
		Repository: r.task.Repository, TaskID: r.task.ID, Name: name,
		Path:   path.Join("tasks", r.task.Repository, r.task.ID, r.key(s.ID, n).AttemptID(), "prepared"),
		SHA256: digest.Of(data).Hex(),
	}, data)
}

// checkReadOnly refuses, as the result's fault, a result of attempt n of hallmark step s that leaves a staged or
// unstaged tracked change in the worktree, or whose HEAD is not the commit recorded when the attempt was prepared.
// Untracked files, such as the report and test caches, are allowed. Without Workspaces nothing is checked, and
// without a prepared record only the tracked changes are. A git failure is an operational error, never a rejection.
func (r *run) checkReadOnly(s workflow.Step, n int) error {
	if r.e.Workspaces == nil {
		return nil
	}
	work := r.workPath()
	changes, err := (git.Program{}).TrackedChanges(r.ctx, work)
	if err != nil {
		return err
	}
	if len(changes) > 0 {
		return rejectOutput(fmt.Errorf("%s: uncommitted tracked changes: %q; a hallmark is read-only, so it fails", hallmarkReadOnly, changes))
	}
	d := r.document(preparedName(s.ID, n))
	if d == nil {
		return nil
	}
	data, err := r.read(*d)
	if err != nil {
		return err
	}
	prepared := strings.TrimSpace(string(data))
	head, err := (git.Program{}).Head(r.ctx, work)
	if err != nil {
		return err
	}
	if head != prepared {
		return rejectOutput(fmt.Errorf("%s: HEAD moved from %s, where the attempt was prepared, to %s; a hallmark is read-only, so it fails", hallmarkReadOnly, short(prepared), short(head)))
	}
	return nil
}

// previousName is the document of task's copy of the report its hallmark step was given as previous.
func previousName(step string) string { return "previous-" + step }

// previousReport returns the previous input of attempt n of hallmark step s, nil when s is no hallmark step, wires
// previous itself, or has no previous report. The first attempt copies the report of the repository's last hallmark
// (LastHallmark) into the task's documents once, as previous-<step>; later attempts reuse that copy, so the report an attempt is given never moves
// with a hallmark that finishes later (docs/decisions/report-only-workflows.md).
func (r *run) previousReport(s workflow.Step, n int) (*render.Input, error) {
	if s.Type != hallmarkKindType || len(s.Inputs[PreviousPort]) > 0 {
		return nil, nil
	}
	name := previousName(s.ID)
	if r.document(name) == nil && n == 1 {
		prev, err := r.newestHallmark()
		if err != nil {
			return nil, fmt.Errorf("previous hallmark report: %w", err)
		}
		if prev == nil {
			return nil, nil
		}
		src, from := prev.report, prev.task.ID
		data, err := r.read(src)
		if err != nil {
			return nil, fmt.Errorf("previous hallmark report of %s: %w", from, err)
		}
		d := store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name,
			Path:   path.Join("tasks", r.task.Repository, r.task.ID, name+"-"+from+path.Ext(src.Path)),
			SHA256: digest.Of(data).Hex()}
		if err := r.put(d, data); err != nil {
			return nil, err
		}
	}
	d := r.document(name)
	if d == nil {
		return nil, nil
	}
	return &render.Input{Port: PreviousPort, Name: d.Path, SHA256: d.SHA256, Path: filepath.Join(r.e.DocumentDir, filepath.FromSlash(d.Path))}, nil
}

// hallmarkTask is the repository's previous hallmark as newestHallmark chose it: the task, its workflow's hallmark
// steps, its documents and its latest report.
type hallmarkTask struct {
	task   store.Task
	steps  []string
	docs   []store.DocumentRef
	report store.DocumentRef
}

// doneTasks returns the repository's done tasks other than this one, newest first by close receipt, then by id, with
// every task's receipts.
func (r *run) doneTasks() ([]store.Task, map[store.TaskRef]store.TaskClose, error) {
	tasks, err := r.e.Records.Tasks(r.task.Repository)
	if err != nil {
		return nil, nil, err
	}
	closes, err := r.e.Records.TaskCloses()
	if err != nil {
		return nil, nil, err
	}
	tasks = slices.DeleteFunc(tasks, func(t store.Task) bool { return t.State != store.TaskDone || t.ID == r.task.ID })
	ref := func(t store.Task) store.TaskRef { return store.TaskRef{Repository: t.Repository, ID: t.ID} }
	slices.SortFunc(tasks, func(a, b store.Task) int {
		if c := cmp.Compare(closes[ref(b)].Closed, closes[ref(a)].Closed); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	return tasks, closes, nil
}

// newestHallmark returns the repository's last hallmark other than this task (LastHallmark), with its workflow's
// hallmark steps, its documents and its latest report; nil when there is none. It is the one choice of previous
// hallmark: the previous input and the baseline both read it.
func (r *run) newestHallmark() (*hallmarkTask, error) {
	tasks, err := r.e.Records.Tasks(r.task.Repository)
	if err != nil {
		return nil, err
	}
	closes, err := r.e.Records.TaskCloses()
	if err != nil {
		return nil, err
	}
	h, err := LastHallmark(tasks, closes, r.e.Records.Documents, r.e.Workflow, r.task.ID)
	if err != nil || h == nil {
		return nil, err
	}
	docs, err := r.e.Records.Documents(h.Task.Repository, h.Task.ID)
	if err != nil {
		return nil, err
	}
	return &hallmarkTask{task: h.Task, steps: hallmarkSteps(h.Task, r.e.Workflow), docs: docs, report: h.Report}, nil
}

// Hallmark is a repository's last hallmark: its task, the task's create and close receipts, and the report of its
// hallmark step's newest round.
type Hallmark struct {
	Task   store.Task
	Close  store.TaskClose
	Report store.DocumentRef
}

// hallmarkSteps returns the ids of the hallmark steps of the workflow task binds, none when it does not resolve.
func hallmarkSteps(t store.Task, resolve func(workflow.Revision) (*workflow.Definition, error)) []string {
	def, err := resolve(t.Workflow)
	if err != nil {
		return nil
	}
	return definitionHallmarkSteps(def)
}

// definitionHallmarkSteps returns the ids of def's hallmark steps, in workflow order: a task bound to def is a
// hallmark task when there is one. The last-hallmark selector and the follow-ups decision of a finished task use it.
func definitionHallmarkSteps(def *workflow.Definition) []string {
	var steps []string
	for _, s := range def.Steps {
		if s.Type == hallmarkKindType {
			steps = append(steps, s.ID)
		}
	}
	return steps
}

// LastHallmark is the one rule for a repository's last hallmark, which both the hallmark step's previous report and
// the Director's hallmark cadence read (docs/decisions/engine-owned-hallmark-cadence.md): the newest done task of
// tasks, one repository's, other than except, whose bound workflow resolves by resolve and has a hallmark step, that
// recorded the finished document and a report of a hallmark step. Tasks are newest by close receipt, then by id; a
// cancelled task, an open one and one whose workflow does not resolve are passed over. closes holds each task's
// receipts (store.TaskCloses) and docs reads a task's documents. It is nil when no task qualifies.
func LastHallmark(tasks []store.Task, closes map[store.TaskRef]store.TaskClose, docs func(repository, taskID string) ([]store.DocumentRef, error),
	resolve func(workflow.Revision) (*workflow.Definition, error), except string) (*Hallmark, error) {
	tasks = slices.DeleteFunc(slices.Clone(tasks), func(t store.Task) bool { return t.State != store.TaskDone || t.ID == except })
	ref := func(t store.Task) store.TaskRef { return store.TaskRef{Repository: t.Repository, ID: t.ID} }
	slices.SortFunc(tasks, func(a, b store.Task) int {
		if c := cmp.Compare(closes[ref(b)].Closed, closes[ref(a)].Closed); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	for _, t := range tasks {
		steps := hallmarkSteps(t, resolve)
		if len(steps) == 0 {
			continue
		}
		list, err := docs(t.Repository, t.ID)
		if err != nil {
			return nil, err
		}
		var best *store.DocumentRef
		round, finished := 0, false
		for i, d := range list {
			finished = finished || d.Name == FinishedDocument
			p, ok := parseDocument(d.Name)
			if ok && !p.exit && p.index == 0 && p.part == "report" && slices.Contains(steps, p.step) && p.round > round {
				best, round = &list[i], p.round
			}
		}
		if best != nil && finished {
			return &Hallmark{Task: t, Close: closes[ref(t)], Report: *best}, nil
		}
	}
	return nil, nil
}

// OpenHallmark returns the newest open task of tasks, one repository's, by create receipt, then by id, whose bound
// workflow resolves by resolve and has a hallmark step: a hallmark that is still running. It is nil when there is none.
func OpenHallmark(tasks []store.Task, closes map[store.TaskRef]store.TaskClose, resolve func(workflow.Revision) (*workflow.Definition, error)) *store.Task {
	var out *store.Task
	var created uint64
	for i, t := range tasks {
		if t.State != store.TaskOpen || len(hallmarkSteps(t, resolve)) == 0 {
			continue
		}
		c := closes[store.TaskRef{Repository: t.Repository, ID: t.ID}].Created
		if out == nil || c > created || c == created && t.ID > out.ID {
			out, created = &tasks[i], c
		}
	}
	return out
}

// FinishedDocument is the document a task whose workflow has no deliver step records as it finishes; prune retires
// its worktree at the head it names, as at a merge record's.
const FinishedDocument = "finished"

// finishedSchema identifies the finished document.
const finishedSchema = "guildhall.finished/1"

// finishedDoc records that a task whose workflow has no deliver step ran every step. Head, when set, is the commit
// its work is safe at, as a merge record's head is: the prepared commit of the latest attempt of its last step,
// which the read-only check confirmed unchanged; it is set only when every step that runs attempts is a hallmark
// step, so prune retires the worktree of a report-only task and keeps any other's.
type finishedDoc struct {
	Schema string `json:"schema"`
	Head   string `json:"head,omitempty"`
}

// finish closes a task whose workflow has no deliver step once every step is done: it records the finished document,
// opens the follow-ups decision when the last step is a hallmark step that recorded a report, and tells the Director
// the task finished with its last step's output. While the follow-ups decision is open, deferred or not, the task
// stays open and waits on it; once it is answered, or when there is none, the task closes through closeTask. The
// close is the advance that follows the answer (task start --decision follow-ups), which runs finish again: the
// document is already recorded and the notifications are resends, so only the close is new. It sends no merged message, which would count toward the
// repository's hallmark cadence, and opens no audit, which is bound to a merged head
// (docs/decisions/report-only-workflows.md). Every write is replay safe.
func (r *run) finish() (Next, error) {
	last := r.def.Steps[len(r.def.Steps)-1]
	if r.document(FinishedDocument) == nil {
		doc := finishedDoc{Schema: finishedSchema}
		if !slices.ContainsFunc(r.def.Steps, func(s workflow.Step) bool { return runsAttempts(s) && s.Type != hallmarkKindType }) && last.Type == hallmarkKindType {
			if a := r.latestAttempt(last.ID); a != nil {
				if d := r.document(preparedName(last.ID, a.Number)); d != nil {
					data, err := r.read(*d)
					if err != nil {
						return Next{}, err
					}
					doc.Head = strings.TrimSpace(string(data))
				}
			}
		}
		data, err := json.Marshal(doc)
		if err != nil {
			return Next{}, err
		}
		if err := r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: FinishedDocument,
			Path: path.Join("tasks", r.task.Repository, r.task.ID, "finished.json"), SHA256: digest.Of(data).Hex()}, data); err != nil {
			return Next{}, err
		}
	}
	followUps, err := r.openFollowUps(last)
	if err != nil {
		return Next{}, err
	}
	waiting := false
	if followUps {
		d := r.decision(FollowUpsDecision)
		waiting = d != nil && d.Open()
	}
	text := fmt.Sprintf("Workflow %s has no deliver step and ran every step; the task is done, with no pull request.", r.def.Name)
	if waiting {
		text = fmt.Sprintf("Workflow %s has no deliver step and ran every step, with no pull request.", r.def.Name)
	}
	kind, _ := workflow.LookupKind(last.Type, last.Version)
	for _, port := range kind.Outputs {
		if doc, _, err := r.output(last.ID, port.Name); err == nil {
			text += fmt.Sprintf("\nStep %s's %s: %s", last.ID, port.Name, filepath.Join(r.e.DocumentDir, filepath.FromSlash(doc.Path)))
		}
	}
	if waiting {
		text += fmt.Sprintf("\nDecision %s is open for the Guildmaster to pick the report's follow-ups. The task stays open until it is answered; "+
			"guildhall task start --decision %s --repo %s %s then closes it.", FollowUpsDecision, FollowUpsDecision, r.task.Repository, r.task.ID)
	}
	if err := r.notify(store.MessageFinished, store.MessageFinished, text); err != nil {
		return Next{}, err
	}
	if waiting {
		return Next{Wait: WaitDecision, Step: last.ID, Decision: FollowUpsDecision}, nil
	}
	if err := r.closeTask("Task done: workflow " + r.def.Name + " finished"); err != nil {
		return Next{}, err
	}
	return Next{Wait: WaitDone, Step: last.ID}, nil
}

// FollowUpsDecision is the id of a finished hallmark task's follow-ups decision; a step's decisions are always
// <step>-<round>, so it is never one of them. followUpsOptions are its answers: pick, with the picked ids on the
// following lines, or none.
const FollowUpsDecision = "follow-ups"

var followUpsOptions = []string{"pick", "none"}

// followUpHeading matches a ranked fix of the hallmark template, "### H<n>. <title>".
var followUpHeading = regexp.MustCompile(`^###[ \t]+(H[0-9]+)\.[ \t]+(.+?)[ \t]*$`)

// followUpsCut ends a list of follow-ups cut to fit the question.
const followUpsCut = "… see the report\n"

// openFollowUps opens, once, the Guildmaster's follow-ups decision of a task whose last step, last, is a hallmark
// step that recorded a report, bound to the report its latest attempt recorded, and tells the Director; it reports
// whether the decision is open. Answering it records the answer only: the Director creates a task for each id the
// Guildmaster picks, and Guildhall creates none (docs/decisions/report-only-workflows.md).
func (r *run) openFollowUps(last workflow.Step) (bool, error) {
	if !slices.Contains(definitionHallmarkSteps(r.def), last.ID) {
		return false, nil
	}
	d := r.decision(FollowUpsDecision)
	if d == nil {
		doc, _, err := r.output(last.ID, "report")
		if err != nil {
			return false, nil
		}
		// The headings are best effort: a report that cannot be read here is named by its path only.
		data, _ := r.read(doc)
		nd := store.Decision{Repository: r.task.Repository, TaskID: r.task.ID, ID: FollowUpsDecision, Kind: store.DecisionFollowUps,
			Subject: &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + doc.SHA256}, Options: followUpsOptions,
			Question: followUpsQuestion(r.task.Repository, r.task.ID, filepath.Join(r.e.DocumentDir, filepath.FromSlash(doc.Path)), data)}
		if _, err := r.e.Records.OpenDecision(r.mutation("decision", FollowUpsDecision), nd); err != nil {
			return false, err
		}
		d = r.addDecision(nd)
	}
	return true, r.notifyDecision(*d, last.ID)
}

// followUpsQuestion is the follow-ups decision's question for task repo/task's report at file, with content report:
// the report's ranked "### H<n>. <title>" headings in report order, when any parse, and the answer format. The list
// is cut, ending with followUpsCut, so the question fits store.MaxTextBytes.
func followUpsQuestion(repo, task, file string, report []byte) string {
	head := fmt.Sprintf("Follow-ups of hallmark task %s/%s.\nReport: %s\n", repo, task, file)
	tail := "\nAnswer pick, then the ids of the follow-ups to make tasks of on the following lines, one per line or comma-separated, for example:\n" +
		"pick\nH1, H3\n" +
		"or answer none to make no task. Guildhall creates no task; the Director creates one for each id picked.\n"
	const listHead = "\nRanked follow-ups:\n"
	room := store.MaxTextBytes - len(head) - len(tail) - len(listHead) - len(followUpsCut)
	var list strings.Builder
	cut := false
	for _, line := range strings.Split(strings.ToValidUTF8(string(report), "�"), "\n") {
		m := followUpHeading.FindStringSubmatch(strings.TrimSuffix(line, "\r"))
		if m == nil {
			continue
		}
		item := m[1] + ". " + m[2] + "\n"
		if list.Len()+len(item) > room {
			cut = true
			break
		}
		list.WriteString(item)
	}
	if list.Len() == 0 && !cut {
		return head + tail
	}
	text := head + listHead + list.String()
	if cut {
		text += followUpsCut
	}
	return text + tail
}

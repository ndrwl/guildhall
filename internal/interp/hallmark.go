package interp

import (
	"cmp"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
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
// previous itself, or has no previous report. The first attempt looks for the newest done task of the repository
// whose bound workflow has a hallmark step and that recorded a report, and copies that report into the task's
// documents once, as previous-<step>; later attempts reuse that copy, so the report an attempt is given never moves
// with a hallmark that finishes later (docs/decisions/report-only-workflows.md).
func (r *run) previousReport(s workflow.Step, n int) (*render.Input, error) {
	if s.Type != hallmarkKindType || len(s.Inputs[PreviousPort]) > 0 {
		return nil, nil
	}
	name := previousName(s.ID)
	if r.document(name) == nil && n == 1 {
		src, from, err := r.newestHallmarkReport()
		if err != nil {
			return nil, fmt.Errorf("previous hallmark report: %w", err)
		}
		if src == nil {
			return nil, nil
		}
		data, err := r.read(*src)
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

// newestHallmarkReport returns the latest report of the newest done task of the repository, other than this one,
// whose bound workflow has a hallmark step, with that task's id; nil when there is none. Tasks are newest by the time
// they closed, then by id. A task whose bound workflow no longer resolves is passed over.
func (r *run) newestHallmarkReport() (*store.DocumentRef, string, error) {
	tasks, err := r.e.Records.Tasks(r.task.Repository)
	if err != nil {
		return nil, "", err
	}
	closed, err := r.e.Records.CloseTimes()
	if err != nil {
		return nil, "", err
	}
	tasks = slices.DeleteFunc(tasks, func(t store.Task) bool { return t.State != store.TaskDone || t.ID == r.task.ID })
	slices.SortFunc(tasks, func(a, b store.Task) int {
		ra, rb := store.TaskRef{Repository: a.Repository, ID: a.ID}, store.TaskRef{Repository: b.Repository, ID: b.ID}
		if c := closed[rb].Compare(closed[ra]); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	for _, t := range tasks {
		def, err := r.e.Workflow(t.Workflow)
		if err != nil {
			continue
		}
		var steps []string
		for _, s := range def.Steps {
			if s.Type == hallmarkKindType {
				steps = append(steps, s.ID)
			}
		}
		if len(steps) == 0 {
			continue
		}
		docs, err := r.e.Records.Documents(t.Repository, t.ID)
		if err != nil {
			return nil, "", err
		}
		var best *store.DocumentRef
		round := 0
		for i, d := range docs {
			p, ok := parseDocument(d.Name)
			if ok && !p.exit && p.index == 0 && p.part == "report" && slices.Contains(steps, p.step) && p.round > round {
				best, round = &docs[i], p.round
			}
		}
		if best != nil {
			return best, t.ID, nil
		}
	}
	return nil, "", nil
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
// tells the Director the task finished with its last step's output, and marks the task done. It sends no merged
// message, which would count toward the repository's hallmark cadence, and opens no audit, which is bound to a merged
// head (docs/decisions/report-only-workflows.md). Every write is replay safe.
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
	text := fmt.Sprintf("Workflow %s has no deliver step and ran every step; the task is done, with no pull request.", r.def.Name)
	kind, _ := workflow.LookupKind(last.Type, last.Version)
	for _, port := range kind.Outputs {
		if doc, _, err := r.output(last.ID, port.Name); err == nil {
			text += fmt.Sprintf("\nStep %s's %s: %s", last.ID, port.Name, filepath.Join(r.e.DocumentDir, filepath.FromSlash(doc.Path)))
		}
	}
	if err := r.notify(store.MessageFinished, store.MessageFinished, text); err != nil {
		return Next{}, err
	}
	if _, err := r.e.Records.SetTaskState(r.mutation("task-state", string(store.TaskDone)), r.task.Repository, r.task.ID, store.TaskDone); err != nil {
		return Next{}, err
	}
	return Next{Wait: WaitDone, Step: last.ID}, nil
}

package interp

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// baselineName is the document of the baseline a task's hallmark step is given, baseline-<step>.
func baselineName(step string) string { return "baseline-" + step }

// IsBaselineDocument reports whether d is a hallmark task's baseline, baseline-<step> at
// tasks/<repo>/<task>/baseline-<step>.md, whose failed jobs guildhall ci log serves (BaselineJobs).
func IsBaselineDocument(d store.DocumentRef) bool {
	return strings.HasPrefix(d.Name, "baseline-") && d.Path == path.Join("tasks", d.Repository, d.TaskID, d.Name+".md")
}

// baselineLimit is how many merges a baseline lists, the newest by landing; it says how many more in the range it
// dropped.
const baselineLimit = 20

// BaselineJobsSchema identifies the failed-jobs block of a baseline.
const BaselineJobsSchema = "guildhall.hallmark-baseline-jobs/1"

// BaselineJob is one failed GitHub Actions job a baseline lists: check Check of merged task Task's merge commit
// Head on Remote, whose log guildhall ci log serves.
type BaselineJob struct {
	Task   string `json:"task"`
	Remote string `json:"remote"`
	Head   string `json:"head"`
	Check  string `json:"check"`
	Job    string `json:"job"`
}

// baselineJobs is the JSON object a baseline's failed-jobs block holds.
type baselineJobs struct {
	Schema string        `json:"schema"`
	Jobs   []BaselineJob `json:"jobs"`
}

// baselineJobsHeading starts a baseline's failed-jobs block, a baselineJobs object fenced as json.
const baselineJobsHeading = "## Failed jobs\n\n```json\n"

// BaselineJobs returns the failed jobs a baseline document lists in its failed-jobs block; none when it has no
// block, as a baseline rendered without a forge has none. A block that does not hold a baselineJobs object of
// BaselineJobsSchema is an error.
func BaselineJobs(data []byte) ([]BaselineJob, error) {
	_, block, ok := strings.Cut(string(data), "\n"+baselineJobsHeading)
	if !ok {
		return nil, nil
	}
	block, _, ok = strings.Cut(block, "\n```")
	var v baselineJobs
	if !ok || json.Unmarshal([]byte(block), &v) != nil || v.Schema != BaselineJobsSchema {
		return nil, fmt.Errorf("the failed-jobs block is not a %s object", BaselineJobsSchema)
	}
	return v.Jobs, nil
}

// baselineInput returns the baseline input of attempt n of hallmark step s, nil when s is no hallmark step or
// wires baseline itself. Whichever attempt is bound first renders the baseline and records it once as
// baseline-<step>; every later attempt reuses it, so the input never moves and a replay reads no forge or git
// (docs/interpreter.md#hallmark). A forge error is recorded in the document, never returned; only the advance's
// context ending is, so a cancelled advance records nothing.
func (r *run) baselineInput(s workflow.Step, n int) (*render.Input, error) {
	if s.Type != hallmarkKindType || len(s.Inputs[BaselinePort]) > 0 {
		return nil, nil
	}
	name := baselineName(s.ID)
	if r.document(name) == nil {
		p := path.Join("tasks", r.task.Repository, r.task.ID, name+".md")
		// A file a crash left unrecorded after it was written is the baseline rendered then.
		data, err := os.ReadFile(filepath.Join(r.e.DocumentDir, filepath.FromSlash(p)))
		if errors.Is(err, os.ErrNotExist) {
			data, err = r.renderBaseline(s, n)
		}
		if err != nil {
			return nil, err
		}
		d := store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name, Path: p, SHA256: digest.Of(data).Hex()}
		if err := r.put(d, data); err != nil {
			return nil, err
		}
	}
	d := r.document(name)
	return &render.Input{Port: BaselinePort, Name: d.Path, SHA256: d.SHA256, Path: filepath.Join(r.e.DocumentDir, filepath.FromSlash(d.Path))}, nil
}

// baselineMerge is a merge record a baseline may list: the task that recorded it, its slice (DocumentSlice), the
// label its row, checks heading and failed jobs name it by (<task>, or <task> · slice <k>/<n> for a slice of a
// multi-PR task), the record and the commit it landed.
type baselineMerge struct {
	task   string
	slice  int
	label  string
	pr     RecordedMerge
	commit string
	// unreadable is why the merge record could not be read (MergeRecord); such a record has no commit.
	unreadable string
}

// renderBaseline renders the baseline of attempt n of hallmark step s (baselineInput).
func (r *run) renderBaseline(s workflow.Step, n int) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Hallmark baseline for %s/%s\n\nRendered %s when attempt %d of step %s was bound.\n\n", r.task.Repository, r.task.ID, r.now().UTC().Format(time.RFC3339), n, s.ID)
	prev, err := r.newestHallmark()
	if err != nil {
		return nil, fmt.Errorf("baseline: previous hallmark: %w", err)
	}
	previous := ""
	if prev != nil {
		if previous, err = r.hallmarkHead(prev); err != nil {
			return nil, fmt.Errorf("baseline: head of %s: %w", prev.task.ID, err)
		}
	}
	if previous == "" {
		b.WriteString("- Previous hallmark: none\n")
	} else {
		fmt.Fprintf(&b, "- Previous hallmark: %s, head `%s`\n", prev.task.ID, previous)
	}
	repo, err := r.repository()
	if err != nil {
		return nil, err
	}
	current, unavailable := "", "no workspaces"
	if r.e.Workspaces != nil {
		if _, err := r.workDir(); err != nil {
			return nil, err
		}
		if current, err = r.e.Workspaces.Tip(r.ctx, repo.Path, git.DefaultRef(r.branch())); err != nil {
			if r.ctx.Err() != nil {
				return nil, r.ctx.Err()
			}
			current, unavailable = "", err.Error()
		}
	}
	if current == "" {
		b.WriteString("- Current: unknown\n")
	} else {
		fmt.Fprintf(&b, "- Current: `%s`, the remote's default branch when this was rendered; the attempt's prepared head (`%s`) is the authority and may be newer on a later attempt\n", current, preparedName(s.ID, n))
	}
	var listed []baselineMerge
	if current == "" {
		fmt.Fprintf(&b, "\n## Merged tasks\n\nmerged tasks unavailable: %s\n", cell(unavailable))
	} else {
		merges, err := r.baselineMerges()
		if err != nil {
			return nil, fmt.Errorf("baseline: merged tasks: %w", err)
		}
		var placeless []string
		var placed, unreadable []baselineMerge
		for _, m := range merges {
			if m.unreadable != "" {
				unreadable = append(unreadable, m)
				placeless = append(placeless, fmt.Sprintf("%s: merge record unreadable: %s", m.label, cell(m.unreadable)))
				continue
			}
			in, err := r.between(repo.Path, m.commit, previous, current)
			if err != nil {
				if r.ctx.Err() != nil {
					return nil, r.ctx.Err()
				}
				placeless = append(placeless, fmt.Sprintf("%s (`%s`): %s", m.label, m.commit, cell(err.Error())))
				continue
			}
			if in {
				placed = append(placed, m)
			}
		}
		if placed, err = r.byLanding(repo.Path, placed); err != nil {
			if r.ctx.Err() != nil {
				return nil, r.ctx.Err()
			}
			return nil, fmt.Errorf("baseline: order merges: %w", err)
		}
		dropped := max(0, len(placed)-baselineLimit)
		listed = placed[:len(placed)-dropped]
		fmt.Fprintf(&b, "\n## Merged tasks (%d, newest first)\n\n", len(listed)+len(unreadable))
		if len(listed)+len(unreadable) == 0 {
			b.WriteString("No merged task is between the two heads.\n")
		} else {
			b.WriteString("| Task | Pull request | Merge commit | Head |\n|---|---|---|---|\n")
			for _, m := range listed {
				fmt.Fprintf(&b, "| %s | [#%d](%s) | `%s` | `%s` |\n", m.label, m.pr.Number, cell(m.pr.URL), m.pr.MergeCommit, m.pr.Head)
			}
			for _, m := range unreadable {
				fmt.Fprintf(&b, "| %s | unreadable: %s | — | — |\n", m.label, cell(m.unreadable))
			}
		}
		if dropped > 0 {
			fmt.Fprintf(&b, "\n%d older merges in the range are not listed (limit %d).\n", dropped, baselineLimit)
		}
		if len(placeless) > 0 {
			fmt.Fprintf(&b, "\nNot placed: %s.\n", strings.Join(placeless, "; "))
		}
	}
	if r.e.Forge == nil {
		return []byte(b.String()), nil
	}
	b.WriteString("\n## Checks\n")
	if len(listed) == 0 {
		b.WriteString("\nNo merged task to check.\n")
	}
	jobs := baselineJobs{Schema: BaselineJobsSchema, Jobs: []BaselineJob{}}
	for _, m := range listed {
		fmt.Fprintf(&b, "\n### %s `%s`\n\n", m.label, m.commit)
		checks, err := r.e.Forge.Checks(r.ctx, forge.Commit{Repository: repo.Path, Remote: m.pr.Remote, Head: m.commit})
		if err != nil {
			if r.ctx.Err() != nil {
				return nil, r.ctx.Err()
			}
			fmt.Fprintf(&b, "checks unavailable: %s\n", cell(err.Error()))
			continue
		}
		if len(checks) == 0 {
			b.WriteString("No checks reported.\n")
			continue
		}
		b.WriteString("| Check | State | URL | Log |\n|---|---|---|---|\n")
		for _, c := range checks {
			command := ""
			if c.State == forge.CheckFailed && c.Job != "" {
				command = "`" + ciLogCommand(r.e.Copy, r.task.Repository, r.task.ID, c.Job) + "`"
				jobs.Jobs = append(jobs.Jobs, BaselineJob{Task: m.label, Remote: m.pr.Remote, Head: m.commit, Check: c.Name, Job: c.Job})
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", cell(c.Name), c.State, cell(c.URL), command)
		}
	}
	block, err := json.Marshal(jobs)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(&b, "\n%s%s\n```\n", baselineJobsHeading, block)
	return []byte(b.String()), nil
}

// cell is text made safe for one Markdown table cell or line: no pipe ends the cell and no newline the row.
func cell(text string) string {
	return strings.NewReplacer("|", `\|`, "\r", " ", "\n", " ").Replace(text)
}

// hallmarkHead is the commit the previous hallmark's work stood at: its finished document's head, or else its
// newest prepared record; "" with neither.
func (r *run) hallmarkHead(h *hallmarkTask) (string, error) {
	var prepared *store.DocumentRef
	round := 0
	for i, d := range h.docs {
		if d.Name == FinishedDocument {
			data, err := r.read(d)
			if err != nil {
				return "", err
			}
			var f finishedDoc
			if err := json.Unmarshal(data, &f); err != nil {
				return "", fmt.Errorf("document %s: %w", d.Name, err)
			}
			if f.Head != "" {
				return f.Head, nil
			}
		}
		if p, ok := parseDocument(d.Name); ok && !p.exit && p.index == 0 && p.part == "prepared" && slices.Contains(h.steps, p.step) && p.round > round {
			prepared, round = &h.docs[i], p.round
		}
	}
	if prepared == nil {
		return "", nil
	}
	data, err := r.read(*prepared)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// baselineMerges returns every merge record, by deliver or outside Guildhall, of every task in the repository other
// than this one, open, done or cancelled, each read on its own as SliceHeads reads them, with the commit it landed:
// its merge commit, or its pull request's head when the forge gave none. A record that cannot be read is returned
// with why. Two readable records of one task that landed the same commit are one, the higher slice's or else the
// later name's. They are in candidate order: open tasks first by id descending, then done and cancelled tasks
// together by close time descending and then id descending, and within a task by slice descending and then name
// descending.
func (r *run) baselineMerges() ([]baselineMerge, error) {
	tasks, err := r.e.Records.Tasks(r.task.Repository)
	if err != nil {
		return nil, err
	}
	closes, err := r.e.Records.TaskCloses()
	if err != nil {
		return nil, err
	}
	tasks = slices.DeleteFunc(tasks, func(t store.Task) bool { return t.ID == r.task.ID })
	ref := func(t store.Task) store.TaskRef { return store.TaskRef{Repository: t.Repository, ID: t.ID} }
	closed := func(t store.Task) bool { return t.State == store.TaskDone || t.State == store.TaskCancelled }
	slices.SortFunc(tasks, func(a, b store.Task) int {
		ca, cb := closed(a), closed(b)
		if ca != cb {
			if ca {
				return 1
			}
			return -1
		}
		if ca {
			if c := cmp.Compare(closes[ref(b)].Closed, closes[ref(a)].Closed); c != 0 {
				return c
			}
		}
		return cmp.Compare(b.ID, a.ID)
	})
	var out []baselineMerge
	for _, t := range tasks {
		docs, err := r.e.Records.Documents(t.Repository, t.ID)
		if err != nil {
			return nil, err
		}
		var records []int
		for k := range docs {
			if IsMergeRecord(docs[k].Name) {
				records = append(records, k)
			}
		}
		if len(records) == 0 {
			continue
		}
		slices.SortFunc(records, func(a, b int) int {
			if c := cmp.Compare(DocumentSlice(docs[b].Name), DocumentSlice(docs[a].Name)); c != 0 {
				return c
			}
			return cmp.Compare(docs[b].Name, docs[a].Name)
		})
		total, err := r.sliceTotal(t, docs)
		if err != nil {
			return nil, err
		}
		landed := map[string]bool{}
		for _, k := range records {
			slice := DocumentSlice(docs[k].Name)
			bm := baselineMerge{task: t.ID, slice: slice, label: sliceLabel(t.ID, slice, total)}
			m, err := MergeRecord(docs[k:k+1], r.read)
			if err != nil {
				bm.unreadable = err.Error()
				out = append(out, bm)
				continue
			}
			bm.pr, bm.commit = *m, cmp.Or(m.MergeCommit, m.Head)
			// Records come highest slice, then latest name, first: the first to land a commit is its row.
			if landed[bm.commit] {
				continue
			}
			landed[bm.commit] = true
			out = append(out, bm)
		}
	}
	return out, nil
}

// sliceTotal is the number of slices task t's accepted spec lists, read as TaskSlices reads it, from its documents
// docs and its bound definition: 1 for a one-slice task, and 0 when it cannot be read (no definition, or an
// unreadable spec). Only the store failing to give the task's decisions is an error.
func (r *run) sliceTotal(t store.Task, docs []store.DocumentRef) (int, error) {
	def, err := r.e.Workflow(t.Workflow)
	if err != nil || def == nil {
		return 0, nil
	}
	if def.Slices == nil {
		return 1, nil
	}
	decisions, err := r.e.Records.Decisions(t.Repository, t.ID)
	if err != nil {
		return 0, err
	}
	st, err := slicesOf(def, newHistory(t.Repository, t.ID, def, r.read, nil, docs, decisions))
	if err != nil {
		return 0, nil
	}
	return st.total, nil
}

// sliceLabel names slice k of task, whose spec lists total slices (0 when unknown): "<task> · slice <k>/<total>" for
// a multi-PR task, "<task> · slice <k>" for a later slice of a task whose total is unknown, and the task alone
// otherwise.
func sliceLabel(task string, k, total int) string {
	switch {
	case total > 1:
		return fmt.Sprintf("%s · slice %d/%d", task, k, total)
	case k >= 2:
		return fmt.Sprintf("%s · slice %d", task, k)
	}
	return task
}

// byLanding sorts merges, each with a commit on the current head, newest first by landing: a commit that is an
// ancestor of another merge's commit sorts after it. It is a stable binary insertion by Workspaces.IsAncestor, so
// merges whose commits are equal or neither of which is an ancestor of the other keep their order.
func (r *run) byLanding(repository string, merges []baselineMerge) ([]baselineMerge, error) {
	out := make([]baselineMerge, 0, len(merges))
	for _, m := range merges {
		lo, hi := 0, len(out)
		for lo < hi {
			mid := (lo + hi) / 2
			newer := false
			if m.commit != out[mid].commit {
				var err error
				if newer, err = r.e.Workspaces.IsAncestor(r.ctx, repository, out[mid].commit, m.commit); err != nil {
					return nil, err
				}
			}
			if newer {
				hi = mid
			} else {
				lo = mid + 1
			}
		}
		out = slices.Insert(out, lo, m)
	}
	return out, nil
}

// between reports whether commit is an ancestor of current and, with a previous head, not one of previous.
func (r *run) between(repository, commit, previous, current string) (bool, error) {
	if !digest.ValidHead(commit) {
		return false, errors.New("no merge commit is recorded")
	}
	in, err := r.e.Workspaces.IsAncestor(r.ctx, repository, commit, current)
	if err != nil || !in || previous == "" {
		return in, err
	}
	before, err := r.e.Workspaces.IsAncestor(r.ctx, repository, commit, previous)
	return !before, err
}

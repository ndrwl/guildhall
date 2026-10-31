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

// baselineLimit is how many merged tasks a baseline lists; it says how many more in the range it dropped.
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

// baselineMerge is a merged task a baseline may list: its merge record and the commit it landed.
type baselineMerge struct {
	task   string
	pr     RecordedMerge
	commit string
	// unreadable is why the task's merge record could not be read (MergeRecord); such a task has no commit.
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
		merges, err := r.mergedTasks()
		if err != nil {
			return nil, fmt.Errorf("baseline: merged tasks: %w", err)
		}
		var placeless []string
		dropped := 0
		for _, m := range merges {
			if m.unreadable != "" {
				placeless = append(placeless, fmt.Sprintf("%s: merge record unreadable: %s", m.task, cell(m.unreadable)))
				continue
			}
			in, err := r.between(repo.Path, m.commit, previous, current)
			if err != nil {
				if r.ctx.Err() != nil {
					return nil, r.ctx.Err()
				}
				placeless = append(placeless, fmt.Sprintf("%s (`%s`): %s", m.task, m.commit, cell(err.Error())))
				continue
			}
			switch {
			case !in:
			case len(listed) < baselineLimit:
				listed = append(listed, m)
			default:
				dropped++
			}
		}
		fmt.Fprintf(&b, "\n## Merged tasks (%d, newest first)\n\n", len(listed))
		if len(listed) == 0 {
			b.WriteString("No merged task is between the two heads.\n")
		} else {
			b.WriteString("| Task | Pull request | Merge commit | Head |\n|---|---|---|---|\n")
			for _, m := range listed {
				fmt.Fprintf(&b, "| %s | [#%d](%s) | `%s` | `%s` |\n", m.task, m.pr.Number, cell(m.pr.URL), m.pr.MergeCommit, m.pr.Head)
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
		fmt.Fprintf(&b, "\n### %s `%s`\n\n", m.task, m.commit)
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
				jobs.Jobs = append(jobs.Jobs, BaselineJob{Task: m.task, Remote: m.pr.Remote, Head: m.commit, Check: c.Name, Job: c.Job})
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

// mergedTasks returns the repository's done tasks, other than this one, that recorded a merge, by deliver or
// outside Guildhall, newest first by close time then id, each with the commit it landed: its merge commit, or its
// pull request's head when the forge gave none.
func (r *run) mergedTasks() ([]baselineMerge, error) {
	tasks, _, err := r.doneTasks()
	if err != nil {
		return nil, err
	}
	var out []baselineMerge
	for _, t := range tasks {
		docs, err := r.e.Records.Documents(t.Repository, t.ID)
		if err != nil {
			return nil, err
		}
		m, err := MergeRecord(docs, r.read)
		switch {
		case err != nil:
			out = append(out, baselineMerge{task: t.ID, unreadable: err.Error()})
		case m != nil:
			out = append(out, baselineMerge{task: t.ID, pr: *m, commit: cmp.Or(m.MergeCommit, m.Head)})
		}
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

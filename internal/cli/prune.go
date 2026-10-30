package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/quality"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const pruneHelp = `Usage: guildhall prune [--dry-run] [--repo <name>] [--task <id>] [--copy <dir>]
  --copy <dir>   Director copy (or GUILDHALL_COPY)
  --dry-run      list what would be pruned and skipped, removing nothing
  --repo <name>  repository filter (default: all)
  --task <id>    one task (default: every done task)
  --help         show this help
Removes the worktree and the bulky attempt files of each done task closed longer ago than prune_after
(default 336h, 14 days), keeping its store records, recorded documents, result.json, outcome.json and
prompt/manifest.json (docs/running.md#pruning).
`

// pruneKept are the files a prune leaves in an attempt directory, besides prompt/manifest.json and the pane files of
// an attempt whose Herdr workspace is not closed yet: the result with its usage file, the outcome and the attempt
// lock, enough for spend report, the quality judge's builder lookup and artisan status to read the same as before.
var pruneKept = map[string]bool{artisan.ResultFile: true, adapter.UsageFileName: true, artisan.OutcomeFile: true, artisan.LockFile: true}

// maxPruneKept bounds the kept reason a prune record and message hold, well inside the store's text limits.
const maxPruneKept = 4096

// boundKept is reason cut to maxPruneKept bytes: a worktree with many untracked files keeps a long reason, and the
// record and message hold its start.
func boundKept(reason string) string {
	if len(reason) <= maxPruneKept {
		return reason
	}
	return strings.ToValidUTF8(reason[:maxPruneKept], "") + " …"
}

// selfReviewFile is a self-review document's name in the copy's quality directory.
var selfReviewFile = regexp.MustCompile(`^self-review-([1-9][0-9]*)\.json$`)

// pruner selects the done tasks of a copy that are due a prune and removes their worktrees and bulky attempt files
// (docs/running.md#pruning). The command and the session supervisor's daily prune share it.
type pruner struct {
	storePath, state, quality string
	// repositories are the config's repositories by id: a prune retires a worktree only of a listed repository.
	repositories map[string]config.Repository
	// after is prune_after and wait lock_wait.
	after, wait time.Duration
	workspaces  interp.Workspaces
	now         func() time.Time
	// remove removes one file or directory tree; nil is os.RemoveAll.
	remove func(string) error
}

// pruner is the prune of the copy at copyDir under cfg.
func (a *App) pruner(cfg *config.Config, copyDir string) (*pruner, error) {
	after, err := cfg.PruneAfter()
	if err != nil {
		return nil, err
	}
	wait, err := cfg.LockWait()
	if err != nil {
		return nil, err
	}
	p := &pruner{storePath: filepath.Join(copyDir, "director.store"), state: filepath.Join(copyDir, "state"),
		quality: filepath.Join(copyDir, "quality"), repositories: map[string]config.Repository{},
		after: after, wait: wait, workspaces: a.Workspaces, now: a.Now}
	if p.workspaces == nil {
		p.workspaces = git.Program{}
	}
	for _, r := range cfg.Repositories {
		p.repositories[r.ID] = r
	}
	return p, nil
}

// pruneResult is what a prune did, or would do, with one task.
type pruneResult struct {
	Task store.TaskRef
	// Skip is why the task was left alone; empty when it was pruned.
	Skip string
	// Bytes is what was freed, or would be.
	Bytes int64
	// Worktree is a store.Worktree* outcome, and Kept the reason for a kept one or one that is not a worktree.
	Worktree, Kept string
	// Failed lists what could not be removed, and Panes the attempts whose pane files were kept.
	Failed, Panes []string
}

// complete reports whether nothing prunable was left.
func (r pruneResult) complete() bool {
	return r.Skip == "" && r.Worktree == store.WorktreeRemoved && len(r.Failed) == 0 && len(r.Panes) == 0
}

// pruneTask is a done task the store does not record as pruned completely.
type pruneTask struct {
	ref    store.TaskRef
	closed time.Time
	// skip is why it is not prunable, empty when it is.
	skip string
}

func (p *pruner) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// selectTasks lists, in repository then id order, every done task of repository (all when empty) and taskID (all
// when empty) that is not pruned completely, each with why it is not prunable now, if it is not. A task named by
// taskID that is open or pruned completely is listed with that as its reason.
func (p *pruner) selectTasks(repository, taskID string) ([]pruneTask, error) {
	return withRecords(storeRecords{path: p.storePath, wait: p.wait}, true, nil, func(s *store.Store) ([]pruneTask, error) {
		repos, err := s.Repositories()
		if err != nil {
			return nil, err
		}
		closed, err := s.CloseTimes()
		if err != nil {
			return nil, err
		}
		// Every open task's dependencies, across repositories: a done task one of them names is kept.
		dependants := map[store.TaskRef][]store.TaskRef{}
		all := map[string][]store.Task{}
		for _, repo := range repos {
			tasks, err := s.Tasks(repo)
			if err != nil {
				return nil, err
			}
			all[repo] = tasks
			for _, t := range tasks {
				if t.State != store.TaskOpen {
					continue
				}
				for _, d := range t.DependsOn {
					dependants[d] = append(dependants[d], store.TaskRef{Repository: t.Repository, ID: t.ID})
				}
			}
		}
		now := p.clock()
		var out []pruneTask
		for _, repo := range repos {
			if repository != "" && repo != repository {
				continue
			}
			for _, t := range all[repo] {
				if taskID != "" && t.ID != taskID {
					continue
				}
				ref := store.TaskRef{Repository: repo, ID: t.ID}
				named := taskID != ""
				if t.State != store.TaskDone {
					if named {
						out = append(out, pruneTask{ref: ref, skip: "task is " + string(t.State)})
					}
					continue
				}
				switch prior, err := s.TaskPrune(repo, t.ID); {
				case err == nil && prior.Complete:
					if named {
						out = append(out, pruneTask{ref: ref, skip: "already pruned on " + prior.PrunedAt.UTC().Format(time.DateOnly)})
					}
					continue
				case err != nil && !errors.Is(err, store.ErrNotFound):
					return nil, err
				}
				pt := pruneTask{ref: ref, closed: closed[ref]}
				if pt.skip, err = p.skipReason(s, pt, dependants[ref], now); err != nil {
					return nil, err
				}
				out = append(out, pt)
			}
		}
		return out, nil
	})
}

// skipReason is why the done task t is not prunable now, or empty when it is: too recent, an open dependant, an
// unanswered audit, a pending self-review, an unfinished attempt, an unreleased ownership record or a repository the
// config does not list.
func (p *pruner) skipReason(s *store.Store, t pruneTask, dependants []store.TaskRef, now time.Time) (string, error) {
	repo, id := t.ref.Repository, t.ref.ID
	switch {
	case t.closed.IsZero():
		return "no close time recorded", nil
	case now.Sub(t.closed) < p.after:
		return fmt.Sprintf("too recent: closed %s, prunable from %s", t.closed.UTC().Format(time.DateOnly), t.closed.Add(p.after).UTC().Format(time.DateOnly)), nil
	case len(dependants) > 0:
		return fmt.Sprintf("open dependant %s/%s", dependants[0].Repository, dependants[0].ID), nil
	}
	if _, ok := p.repositories[repo]; !ok {
		return "repository " + repo + " is not in the config", nil
	}
	decisions, err := s.Decisions(repo, id)
	if err != nil {
		return "", err
	}
	for _, d := range decisions {
		if d.Kind == store.DecisionAudit && !d.Answered {
			return "audit " + d.ID + " open", nil
		}
	}
	if pending, err := p.pendingSelfReview(repo, id); err != nil || pending != "" {
		return pending, err
	}
	attempts, err := s.Attempts(repo, id)
	if err != nil {
		return "", err
	}
	for _, at := range attempts {
		k := artisan.AttemptKey{Repository: repo, TaskID: id, Step: at.Step, Attempt: at.Number}
		if at.Outcome == "" {
			return "attempt " + k.AttemptID() + " unfinished", nil
		}
		o, err := s.Ownership(repo, id, at.Step, at.Number)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return "", err
		case o.State != store.OwnerReleased:
			return "attempt " + k.AttemptID() + " owned", nil
		}
	}
	return "", nil
}

// pendingSelfReview names a self-review of the task with a criterion still pending its judge, or one that cannot be
// read, or is empty when there is none.
func (p *pruner) pendingSelfReview(repo, id string) (string, error) {
	dir := filepath.Join(p.quality, repo, id)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	for _, e := range entries {
		m := selfReviewFile.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return "", err
		}
		if sr, err := quality.ReadSelfReview(data); err != nil {
			return fmt.Sprintf("self-review %s unreadable: %v", m[1], err), nil
		} else if len(sr.Pending) > 0 {
			return "self-review " + m[1] + " pending", nil
		}
	}
	return "", nil
}

// run prunes, or with dry lists what it would prune, the tasks selectTasks lists for repository and taskID.
// kept, when not nil, is told of each task whose worktree a real prune left.
func (p *pruner) run(ctx context.Context, repository, taskID string, dry bool, kept func(pruneResult) error) ([]pruneResult, error) {
	tasks, err := p.selectTasks(repository, taskID)
	if err != nil {
		return nil, err
	}
	var out []pruneResult
	for _, t := range tasks {
		if t.skip != "" {
			out = append(out, pruneResult{Task: t.ref, Skip: t.skip})
			continue
		}
		var r pruneResult
		if dry {
			r, err = p.inspect(t)
		} else {
			r, err = p.pruneLocked(ctx, t)
		}
		if err != nil {
			return out, fmt.Errorf("prune %s/%s: %w", t.ref.Repository, t.ref.ID, err)
		}
		out = append(out, r)
		if kept != nil && !dry && r.Skip == "" && r.Worktree != store.WorktreeRemoved {
			if err := kept(r); err != nil {
				return out, fmt.Errorf("prune %s/%s: %w", t.ref.Repository, t.ref.ID, err)
			}
		}
	}
	return out, nil
}

// taskDir is the task's directory under the state directory.
func (p *pruner) taskDir(t store.TaskRef) string {
	return filepath.Join(p.state, t.Repository, t.ID)
}

// inspect is what pruning t would do, removing nothing and taking no lock: a work directory with a .git file is
// taken for a worktree Retire removes, so a dry run cannot foresee a worktree a real run keeps.
func (p *pruner) inspect(t pruneTask) (pruneResult, error) {
	r := pruneResult{Task: t.ref, Worktree: store.WorktreeRemoved}
	h, err := withRecords(storeRecords{path: p.storePath, wait: p.wait}, true, nil, func(s *store.Store) (store.TaskHistory, error) {
		return s.TaskHistory(t.ref.Repository, t.ref.ID)
	})
	if err != nil {
		return r, err
	}
	work := filepath.Join(p.taskDir(t.ref), "work")
	if _, err := os.Lstat(work); err == nil {
		if fi, err := os.Lstat(filepath.Join(work, ".git")); err != nil || !fi.Mode().IsRegular() {
			r.Worktree, r.Kept = store.WorktreeNotWorkspace, work+" is not a worktree of "+p.repositories[t.ref.Repository].Path
		} else {
			r.Bytes += treeSize(work)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return r, err
	}
	for _, at := range h.Attempts {
		if err := p.pruneAttempt(&r, artisan.AttemptKey{Repository: t.ref.Repository, TaskID: t.ref.ID, Step: at.Step, Attempt: at.Number}, true); err != nil {
			return r, err
		}
	}
	return r, nil
}

// pruneLocked prunes t under its task lock, checking again under the lock that it is prunable, and records the
// prune once removal has finished.
func (p *pruner) pruneLocked(ctx context.Context, t pruneTask) (pruneResult, error) {
	r := pruneResult{Task: t.ref}
	dir := p.taskDir(t.ref)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return r, err
	}
	lockCtx, cancel := context.WithTimeout(ctx, p.wait)
	release, err := artisan.LockAttempt(lockCtx, dir)
	cancel()
	if err != nil {
		return r, fmt.Errorf("lock task: %w", err)
	}
	defer release()
	again, err := p.selectTasks(t.ref.Repository, t.ref.ID)
	if err != nil {
		return r, err
	}
	if len(again) != 1 || again[0].skip != "" {
		r.Skip = "no longer prunable"
		if len(again) == 1 {
			r.Skip = again[0].skip
		}
		return r, nil
	}
	h, err := withRecords(storeRecords{path: p.storePath, wait: p.wait}, true, nil, func(s *store.Store) (store.TaskHistory, error) {
		return s.TaskHistory(t.ref.Repository, t.ref.ID)
	})
	if err != nil {
		return r, err
	}
	if err := p.retire(ctx, &r, h); err != nil {
		return r, err
	}
	for _, at := range h.Attempts {
		if err := p.pruneAttempt(&r, artisan.AttemptKey{Repository: t.ref.Repository, TaskID: t.ref.ID, Step: at.Step, Attempt: at.Number}, false); err != nil {
			return r, err
		}
	}
	return r, p.record(r, h.Pruned)
}

// retire removes the task's worktree with git.Program.Retire, at the head its merge record names, when nothing in
// it would be lost; a kept worktree, one Retire cannot remove and a work directory that is not a worktree are left
// as they are, with the reason.
func (p *pruner) retire(ctx context.Context, r *pruneResult, h store.TaskHistory) error {
	repo := p.repositories[r.Task.Repository]
	work := filepath.Join(p.taskDir(r.Task), "work")
	_, statErr := os.Lstat(work)
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return statErr
	}
	exists := statErr == nil
	head, err := p.mergedHead(h.Documents)
	if err != nil {
		return err
	}
	r.Worktree = store.WorktreeRemoved
	if head == "" {
		if exists {
			r.Worktree, r.Kept = store.WorktreeKept, "no merge record names the head the task merged at"
		}
		return nil
	}
	size := int64(0)
	if exists {
		size = treeSize(work)
	}
	w := git.Workspace{Repository: repo.Path, Remote: repo.Remote, Branch: interp.TaskBranch(r.Task.ID), Dir: work}
	kept, err := p.workspaces.Retire(ctx, git.Retirement{Workspace: w, Head: head, Outputs: interp.RetirementOutputs(r.Task.Repository, r.Task.ID, h.Documents)})
	switch {
	case errors.Is(err, git.ErrNotWorkspace):
		r.Worktree, r.Kept = store.WorktreeNotWorkspace, work+" is not a worktree of "+repo.Path
		return nil
	case err != nil && ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		kept = []string{"it could not be removed: " + err.Error()}
	}
	if len(kept) > 0 {
		r.Worktree, r.Kept = store.WorktreeKept, strings.Join(kept, "; ")
		return nil
	}
	if _, err := os.Lstat(work); errors.Is(err, fs.ErrNotExist) {
		r.Bytes += size
	}
	return nil
}

// mergedHead is the head the task's newest merge record, the <id>-merge document, names, or empty when it has none.
func (p *pruner) mergedHead(docs []store.DocumentRef) (string, error) {
	head := ""
	for _, d := range docs {
		if !strings.HasSuffix(d.Name, "-merge") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(p.state, "documents", filepath.FromSlash(d.Path)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return "", err
		}
		var doc struct {
			Head string `json:"head"`
		}
		if json.Unmarshal(data, &doc) == nil && digest.ValidHead(doc.Head) {
			head = doc.Head
		}
	}
	return head, nil
}

// pruneAttempt removes, or with dry measures, everything in k's attempt directory but the files a prune keeps:
// pruneKept, prompt/manifest.json, and the pane files of an attempt whose Herdr workspace is not closed yet. What
// cannot be removed is added to r.Failed and the rest goes on.
func (p *pruner) pruneAttempt(r *pruneResult, k artisan.AttemptKey, dry bool) error {
	dir := k.Dir(p.state)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	_, closedErr := os.Lstat(filepath.Join(dir, paneClosed))
	if closedErr != nil && !errors.Is(closedErr, fs.ErrNotExist) {
		return closedErr
	}
	var doomed []string
	for _, e := range entries {
		name := e.Name()
		switch {
		case pruneKept[name]:
		case name == launch.PaneClaim && closedErr != nil:
			r.Panes = append(r.Panes, k.AttemptID())
		case name == "prompt" && e.IsDir():
			inner, err := os.ReadDir(filepath.Join(dir, name))
			if err != nil {
				return err
			}
			for _, f := range inner {
				if f.Name() != render.ManifestFile {
					doomed = append(doomed, filepath.Join(dir, name, f.Name()))
				}
			}
		default:
			doomed = append(doomed, filepath.Join(dir, name))
		}
	}
	remove := p.remove
	if remove == nil {
		remove = os.RemoveAll
	}
	for _, path := range doomed {
		size := treeSize(path)
		if dry {
			r.Bytes += size
			continue
		}
		if err := remove(path); err != nil {
			rel, _ := filepath.Rel(p.taskDir(r.Task), path)
			r.Failed = append(r.Failed, filepath.ToSlash(rel))
			if _, err := os.Lstat(path); err == nil {
				size -= treeSize(path)
			}
		}
		r.Bytes += size
	}
	return nil
}

// treeSize is the size of the regular files at or below path, counting what can be read.
func treeSize(path string) int64 {
	var n int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// record writes r as the task's prune record when it differs from prior, the record already stored, if any: the
// first prune, one that freed something and one whose outcome changed are recorded, and a rerun that found nothing
// new writes nothing.
func (p *pruner) record(r pruneResult, prior *store.TaskPrune) error {
	rec := store.TaskPrune{Repository: r.Task.Repository, TaskID: r.Task.ID, BytesFreed: r.Bytes, Worktree: r.Worktree, Kept: boundKept(r.Kept), Complete: r.complete()}
	id := mutationID("prune", r.Task.Repository, r.Task.ID)
	if prior != nil {
		if r.Bytes == 0 && prior.Worktree == rec.Worktree && prior.Kept == rec.Kept && prior.Complete == rec.Complete {
			return nil
		}
		rec.Runs = prior.Runs
		id = mutationID("prune", r.Task.Repository, r.Task.ID, strconv.Itoa(prior.Runs+1))
	}
	_, err := withRecords(storeRecords{path: p.storePath, wait: p.wait}, false, p.now, func(s *store.Store) (store.Receipt, error) {
		return s.PruneTask(id, rec)
	})
	return err
}

// prune runs `guildhall prune` (docs/running.md#pruning).
func (a *App) prune(args []string) int {
	fl := flag.NewFlagSet("prune", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	dry := fl.Bool("dry-run", false, "remove nothing")
	repo := fl.String("repo", "", "repository")
	task := fl.String("task", "", "task")
	if err := fl.Parse(args); errors.Is(err, flag.ErrHelp) {
		if _, err := io.WriteString(a.Stdout, pruneHelp); err != nil {
			return 1
		}
		return 0
	} else if err != nil {
		return a.usageError("prune: " + err.Error() + "\n" + pruneHelp)
	}
	switch {
	case fl.NArg() != 0:
		return a.usageError("prune: unexpected arguments\n" + pruneHelp)
	case *repo != "" && !workflow.ValidID(*repo), *task != "" && !workflow.ValidID(*task):
		return a.usageError("prune: --repo and --task take ids")
	}
	var storePath string
	if err := cp.apply(a.Getenv, &storePath, nil, nil); err != nil {
		return a.usageError("prune: " + err.Error())
	}
	if cp.dir == "" {
		return a.usageError("prune: --copy or GUILDHALL_COPY required\n" + pruneHelp)
	}
	cfg, err := config.Load(filepath.Join(cp.dir, "config.toml"))
	if err != nil {
		return a.fail(fmt.Errorf("prune: %w", err))
	}
	p, err := a.pruner(cfg, cp.dir)
	if err != nil {
		return a.fail(fmt.Errorf("prune: %w", err))
	}
	results, err := p.run(context.Background(), *repo, *task, *dry, nil)
	if werr := writePruneReport(a.Stdout, results, *dry); werr != nil {
		return 1
	}
	if err != nil {
		return a.fail(err)
	}
	return 0
}

// writePruneReport writes one line per task, pruned (or with dry, that would be) with the space freed, or skipped
// with why, and a total.
func writePruneReport(w io.Writer, results []pruneResult, dry bool) error {
	var b strings.Builder
	pruned, skipped := 0, 0
	var total int64
	for _, r := range results {
		name := r.Task.Repository + "/" + r.Task.ID
		total += r.Bytes
		if r.Skip != "" {
			skipped++
			fmt.Fprintf(&b, "skipped %s: %s\n", name, r.Skip)
			continue
		}
		var notes []string
		switch r.Worktree {
		case store.WorktreeKept:
			notes = append(notes, "worktree kept: "+r.Kept)
		case store.WorktreeNotWorkspace:
			notes = append(notes, "not a worktree: "+r.Kept)
		}
		if len(r.Panes) > 0 {
			notes = append(notes, "pane files kept for "+strings.Join(r.Panes, ", ")+", whose Herdr workspace is not closed yet")
		}
		if len(r.Failed) > 0 {
			notes = append(notes, "partial: could not remove "+strings.Join(r.Failed, ", "))
		}
		size := humanBytes(r.Bytes)
		if r.Worktree != store.WorktreeRemoved {
			// The worktree is what a prune is mostly for, so a task that keeps it is a skip, with the attempt files
			// it freed.
			skipped++
			verb := "freed"
			if dry {
				verb = "would be freed"
			}
			fmt.Fprintf(&b, "skipped %s: %s; %s of attempt files %s\n", name, strings.Join(notes, "; "), size, verb)
			continue
		}
		pruned++
		line := "pruned " + name + ": " + size + " freed"
		if dry {
			line = "would prune " + name + ": " + size
		}
		if len(notes) > 0 {
			line += " (" + strings.Join(notes, "; ") + ")"
		}
		b.WriteString(line + "\n")
	}
	if dry {
		fmt.Fprintf(&b, "total: %d would be pruned, %d skipped, %s would be freed\n", pruned, skipped, humanBytes(total))
	} else {
		fmt.Fprintf(&b, "total: %d pruned, %d skipped, %s freed\n", pruned, skipped, humanBytes(total))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// humanBytes is n in bytes, KiB, MiB or GiB.
func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	v, i := float64(n)/1024, 0
	for v >= 1024 && i < len(units)-1 {
		v, i = v/1024, i+1
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// pruneDailyEvery is how often the session supervisor's daily prune checks whether a day has passed since the
// copy's last prune; pruneDailyGap is that day.
const (
	pruneDailyEvery = time.Hour
	pruneDailyGap   = 24 * time.Hour
)

// pruneStamp is the presentation-only file in the state directory that records when the daily prune last ran, so
// a new session prunes at its start only when the last run was more than a day ago. It is never read as task state.
const pruneStamp = "prune-last.json"

// dailyPrune is the session supervisor's prune of its copy, run off the decision path once a day when prune_daily
// is on (docs/running.md#pruning). It writes what it did to stderr, the supervisor's log, and tells the Director
// once per task whose worktree it left.
type dailyPrune struct {
	p      *pruner
	stderr io.Writer
}

// dailyPrune is the daily prune of session's supervisor over copyDir with cfg, cfg being read when the supervisor
// admits it, or nil when prune_daily is off.
func (a *App) dailyPrune(cfg *config.Config, copyDir string) (*dailyPrune, error) {
	on, err := cfg.PruneDaily()
	if err != nil || !on {
		return nil, err
	}
	p, err := a.pruner(cfg, copyDir)
	if err != nil {
		return nil, err
	}
	return &dailyPrune{p: p, stderr: a.Stderr}, nil
}

// due reports whether a day has passed since the stamp's last run, or no run is stamped.
func (d *dailyPrune) due(now time.Time) bool {
	data, err := os.ReadFile(filepath.Join(d.p.state, pruneStamp))
	if err != nil {
		return true
	}
	var stamp struct {
		RanAt time.Time `json:"ran_at"`
	}
	return json.Unmarshal(data, &stamp) != nil || now.Sub(stamp.RanAt) >= pruneDailyGap
}

// run is one round: a prune when one is due, its report written to stderr and its time stamped.
func (d *dailyPrune) run() {
	now := d.p.clock()
	if !d.due(now) {
		return
	}
	results, err := d.p.run(context.Background(), "", "", false, d.report)
	var b strings.Builder
	_ = writePruneReport(&b, results, false)
	for line := range strings.Lines(b.String()) {
		d.warn("%s", strings.TrimSuffix(line, "\n"))
	}
	if err != nil {
		d.warn("%v", err)
		return
	}
	stamp, err := json.Marshal(struct {
		RanAt time.Time `json:"ran_at"`
	}{now.UTC()})
	if err == nil {
		err = os.MkdirAll(d.p.state, 0o755)
	}
	if err == nil {
		err = atomicfile.Write(filepath.Join(d.p.state, pruneStamp), append(stamp, '\n'))
	}
	if err != nil {
		d.warn("stamp: %v", err)
	}
}

// pruneKeptMessage is the Director message reporting that the daily prune left r's worktree. Its id depends only on
// the task, so the Director is told once per task, not once a day.
func pruneKeptMessage(r pruneResult) store.Message {
	id := fmt.Sprintf("%s.%s.%s", r.Task.Repository, r.Task.ID, store.MessagePruneKept)
	if len(id) > 100 {
		id = store.MessagePruneKept + "." + digest.Of([]byte(id)).Hex()
	}
	text := fmt.Sprintf("The daily prune left the working directory of done task %s/%s, because %s. Its attempt files were pruned; the Director decides what happens to the working directory.",
		r.Task.Repository, r.Task.ID, boundKept(r.Kept))
	return store.Message{ID: id, Recipient: store.RecipientDirector, Repository: r.Task.Repository, TaskID: r.Task.ID,
		Type: store.MessagePruneKept, Text: text}
}

// report sends r's kept message once: a message already sent under its id is not sent again.
func (d *dailyPrune) report(r pruneResult) error {
	m := pruneKeptMessage(r)
	_, err := withRecords(storeRecords{path: d.p.storePath, wait: d.p.wait}, false, nil, func(s *store.Store) (store.Receipt, error) {
		if _, err := s.Message(m.ID); err == nil {
			return store.Receipt{}, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return store.Receipt{}, err
		}
		return s.SendMessage(m)
	})
	return err
}

func (d *dailyPrune) warn(format string, args ...any) {
	if d.stderr != nil {
		_, _ = fmt.Fprintf(d.stderr, "guildhall: daily prune: "+format+"\n", args...)
	}
}

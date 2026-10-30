package cli

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// taskCancel runs `guildhall task cancel`: it ends an open task for good without delivering it, with the Director's
// reason, and prints one line per effect (docs/tasks.md#task-cancel). Under the task lock it refuses a done task, and
// a task whose work merged or is merging; stops its running attempts; closes its open pull requests with the reason;
// voids its open decisions; takes it out of the start and merge queues and clears its merge hold; tells the Director
// about each open task that depends on it; and last records it cancelled. Every step is idempotent under a mutation
// id derived from the task, so a cancel that fails part-way is finished by running the same command again, and
// cancelling a cancelled task prints that and changes nothing.
func (a *App) taskCancel(args []string) int {
	fl := flag.NewFlagSet("task cancel", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var f advanceFlags
	f.register(fl)
	reason := fl.String("reason", "", "the Director's reason for cancelling the task")
	if err := parseInterspersed(fl, args); err != nil {
		return a.taskUsage("task cancel: " + err.Error())
	}
	if err := f.resolve(a); err != nil {
		return a.taskUsage("task cancel: " + err.Error())
	}
	switch {
	case fl.NArg() != 1 || !workflow.ValidID(fl.Arg(0)):
		return a.taskUsage("task cancel takes one task id")
	case strings.TrimSpace(*reason) == "":
		return a.taskUsage("task cancel: --reason is required")
	case len(*reason) > store.MaxTextBytes:
		return a.taskUsage(fmt.Sprintf("task cancel: --reason is %d bytes; the limit is %d", len(*reason), store.MaxTextBytes))
	}
	if err := f.listed(); err != nil {
		return a.fail(fmt.Errorf("task cancel: %w", err))
	}
	if _, err := os.Stat(f.store); errors.Is(err, fs.ErrNotExist) {
		return a.fail(fmt.Errorf("task cancel: %w", noStore(f.store)))
	}
	if err := a.cancelTask(f, fl.Arg(0), *reason); err != nil {
		return a.fail(fmt.Errorf("task cancel: %w", err))
	}
	return 0
}

// cancelTask is task cancel's work over f's copy, under the task lock, printing each effect as it is made.
func (a *App) cancelTask(f advanceFlags, task, reason string) error {
	cfg, repo, state, release, err := lockTask(f, task)
	if err != nil {
		return err
	}
	defer release()

	records := storeRecords{path: f.store, wait: f.wait}
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(a.Stdout, format+"\n", args...) }
	ref := store.TaskRef{Repository: f.repo, ID: task}
	t, err := records.Task(f.repo, task)
	if err != nil {
		return err
	}
	switch t.State {
	case store.TaskCancelled:
		say("task %s/%s is already cancelled: %s", f.repo, task, t.CancelReason)
		return nil
	case store.TaskDone:
		return fmt.Errorf("task %s/%s is done: its work merged, so it is not cancelled", f.repo, task)
	}
	docs, err := records.Documents(f.repo, task)
	if err != nil {
		return err
	}
	prs, merging, err := interp.TaskPullRequests(docs, filepath.Join(state, "documents"))
	if err != nil {
		return err
	}
	if merging {
		return fmt.Errorf("task %s/%s is merging: deliver recorded its merge, so it is not cancelled; advance it with guildhall task start", f.repo, task)
	}

	// Running attempts first: a cancelled task never has a live Artisan, so a stop that cannot be proven refuses
	// the cancel before anything else changes.
	attempts, err := records.Attempts(f.repo, task)
	if err != nil {
		return err
	}
	for _, at := range attempts {
		if at.Outcome != "" {
			continue
		}
		if err := a.stopForCancel(records, state, at); err != nil {
			return err
		}
		say("attempt %d of step %s stopped", at.Number, at.Step)
	}

	// The pull requests next, before any record of the task changes, so a pull request merged on the forge refuses
	// the cancel with only the stopped attempts above. Its work landed outside Guildhall, so the outside-merge rule
	// records the task done instead (docs/tasks.md#merged-outside-guildhall).
	fg := a.forge()
	for _, pr := range prs {
		closed, err := fg.Close(context.Background(), forge.Close{Repository: repo.Path, Remote: pr.Remote, Number: pr.Number, Comment: "Cancelled by Guildhall: " + reason})
		if errors.Is(err, forge.ErrRefused) {
			got, rerr := a.reconcileEngine(f, cfg, repo, state).Reconcile(context.Background(), f.repo, task)
			if rerr != nil {
				return fmt.Errorf("pull request %s of task %s/%s: %w; its work landed, so the task is not cancelled: %w", pr.URL, f.repo, task, err, rerr)
			}
			if got.Done {
				return fmt.Errorf("pull request %s of task %s/%s is merged; its work landed, so the task is recorded done, not cancelled", got.URL, f.repo, task)
			}
			return fmt.Errorf("pull request %s of task %s/%s: %w; its work landed, so the task is not cancelled", pr.URL, f.repo, task, err)
		}
		if err != nil {
			return fmt.Errorf("close pull request %s: %w", pr.URL, err)
		}
		say("pull request %s closed; branch %s kept", closed.URL, cmp.Or(closed.Branch, pr.Branch))
	}

	decisions, err := records.Decisions(f.repo, task)
	if err != nil {
		return err
	}
	for _, d := range decisions {
		if !d.Open() {
			continue
		}
		if _, err := withRecords(records, false, nil, func(s *store.Store) (store.Receipt, error) {
			return s.VoidDecision(cancelMutation(f.repo, task, "void", d.ID), f.repo, task, d.ID, "Task cancelled: "+reason)
		}); err != nil {
			return err
		}
		say("decision %s voided", d.ID)
	}

	if err := leaveQueues(records, f.repo, task, say); err != nil {
		return err
	}

	if err := tellDependents(records, cfg, ref, reason, say); err != nil {
		return err
	}

	if _, err := withRecords(records, false, nil, func(s *store.Store) (store.Receipt, error) {
		return s.CancelTask(cancelMutation(f.repo, task, "cancel"), f.repo, task, reason)
	}); err != nil {
		return err
	}
	say("task %s/%s cancelled: %s", f.repo, task, reason)
	return nil
}

// stopForCancel ends a running attempt of a task being cancelled and records it stopped. An Artisan attempt is
// stopped through its owner as `artisan stop` does; one whose stop is not proven refuses the cancel. A Director
// stage's attempt has no owner: it was handed to the Director, so recording it stopped is all there is to do.
func (a *App) stopForCancel(records storeRecords, state string, at store.StageAttempt) error {
	if !at.Director {
		stop := launch.Stop
		if a.StopAttempt != nil {
			stop = a.StopAttempt
		}
		ctx, cancel := context.WithTimeout(context.Background(), defaultStopTimeout)
		defer cancel()
		k := artisan.AttemptKey{Repository: at.Repository, TaskID: at.TaskID, Step: at.Step, Attempt: at.Number}
		if _, err := stop(ctx, launch.Attempt{Store: records.path, StateDir: state, Key: k}); err != nil {
			return fmt.Errorf("stop attempt %d of step %s: %w; the task is not cancelled", at.Number, at.Step, err)
		}
	}
	// The stopped launcher may have recorded the attempt's end already: the first finish stands.
	_, err := records.FinishAttempt(cancelMutation(at.Repository, at.TaskID, "stop", at.Step, fmt.Sprint(at.Number)), at.Repository, at.TaskID, at.Step, at.Number, store.AttemptStopped)
	if errors.Is(err, store.ErrInvalid) {
		attempts, aerr := records.Attempts(at.Repository, at.TaskID)
		if aerr == nil && slices.ContainsFunc(attempts, func(x store.StageAttempt) bool {
			return x.Step == at.Step && x.Number == at.Number && x.Outcome != ""
		}) {
			return nil
		}
	}
	return err
}

// leaveQueues takes a task being cancelled out of the start queue and the merge queue, releasing the merge front if
// it holds it, and clears its merge hold.
func leaveQueues(records storeRecords, repo, task string, say func(string, ...any)) error {
	_, err := withRecords(records, false, nil, func(s *store.Store) (struct{}, error) {
		starts, err := s.StartQueue()
		if err != nil {
			return struct{}{}, err
		}
		for _, e := range starts {
			if e.Repository != repo || e.TaskID != task {
				continue
			}
			if _, err := s.LeaveStartQueue(store.LeaveStartQueueMutation(e.StartQueueEntry), e.StartQueueEntry); err != nil {
				return struct{}{}, err
			}
			say("left the start queue: attempt %d of step %s", e.Attempt, e.Step)
		}
		q, err := s.MergeQueue(repo)
		if err != nil {
			return struct{}{}, err
		}
		queued := q.Position(task) > 0 || slices.ContainsFunc(q.Out, func(e store.MergeQueueEntry) bool { return e.TaskID == task })
		if _, err := s.LeaveMergeQueue(cancelMutation(repo, task, "leave-merge-queue"), repo, task, false); err != nil {
			return struct{}{}, err
		}
		if queued {
			say("left the merge queue")
		}
		switch h, err := s.MergeHold(repo, task); {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return struct{}{}, err
		case h.Held:
			if _, err := s.SetMergeHold(cancelMutation(repo, task, "release-merge-hold", fmt.Sprint(len(h.History))), repo, task, false); err != nil {
				return struct{}{}, err
			}
			say("merge hold cleared")
		}
		return struct{}{}, nil
	})
	return err
}

// tellDependents sends the Director one dependency-cancelled message for each open task of a listed repository that
// depends on the cancelled task ref; dependents are not cancelled. A message already sent by an earlier run of the
// cancel stands, so a re-run sends nothing new.
func tellDependents(records storeRecords, cfg *config.Config, ref store.TaskRef, reason string, say func(string, ...any)) error {
	for _, r := range cfg.Repositories {
		tasks, err := withRecords(records, true, nil, func(s *store.Store) ([]store.Task, error) { return s.Tasks(r.ID) })
		if err != nil {
			return err
		}
		for _, dep := range tasks {
			if dep.State != store.TaskOpen || !slices.Contains(dep.DependsOn, ref) {
				continue
			}
			m, err := interp.DependencyCancelledMessage(store.TaskRef{Repository: dep.Repository, ID: dep.ID}, ref, reason)
			if err != nil {
				return err
			}
			if _, err := records.Send(store.Recipient{Role: store.RecipientDirector}, m); err != nil &&
				!errors.Is(err, store.ErrMutationConflict) && !errors.Is(err, store.ErrExists) {
				return err
			}
			say("dependent task %s/%s told", dep.Repository, dep.ID)
		}
	}
	return nil
}

// cancelMutation is the mutation id of one of a task's cancel writes, cancel.<repo>.<task>[.<part>...], hashed to
// fit the store's 128 bytes, so a cancel run again replays what an earlier run wrote.
func cancelMutation(repo, task string, parts ...string) string {
	id := strings.Join(append([]string{"cancel", repo, task}, parts...), ".")
	if len(id) > 128 {
		id = "cancel." + digest.Of([]byte(strings.Join(append([]string{repo, task}, parts...), "/"))).Hex()
	}
	return id
}

// advancedOrCancelled prints where an advance of task left it, as advanced does, except that an advance of a
// cancelled task, such as a stopped attempt's launcher runs after task cancel stopped it, prints that the task is
// cancelled and succeeds: there is nothing to advance and nothing for the Director to recover (docs/tasks.md#task-cancel).
// A done task's advance fails as before.
func (a *App) advancedOrCancelled(f advanceFlags, task string, n interp.Next, err error) int {
	if errors.Is(err, interp.ErrTaskClosed) {
		if t, terr := (storeRecords{path: f.store, wait: f.wait}).Task(f.repo, task); terr == nil && t.State == store.TaskCancelled {
			_, _ = fmt.Fprintf(a.Stdout, "task %s/%s is cancelled; nothing to advance\n", f.repo, task)
			return 0
		}
	}
	return a.advanced(n, err)
}

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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Holds are Director records kept until named tasks merge: a park with --until-merged, a decision deferral and a
// follow-up (docs/decisions/0096-hold-until-merged.md). Status derives each one's release from the task states it
// reads; the session supervisor's holds watcher wakes the Director once for each release.

// untilMerged collects repeated --until-merged flags in the order given.
type untilMerged []store.TaskRef

func (u *untilMerged) String() string { return store.TaskRefList(*u) }

func (u *untilMerged) Set(v string) error {
	repo, task, ok := strings.Cut(v, "/")
	if !ok || !workflow.ValidID(repo) || !workflow.ValidID(task) {
		return fmt.Errorf("--until-merged %q must be <repository>/<task>", v)
	}
	*u = append(*u, store.TaskRef{Repository: repo, ID: task})
	return nil
}

// refStrings is refs as "<repository>/<id>" strings.
func refStrings(refs []store.TaskRef) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.String()
	}
	return out
}

// holdRelease says what released a hold judged j: "dependency cancelled: <repo>/<task>, …", or "released:
// <repo>/<task> merged <time>, …" with each task's merge time from closedAt, its state receipt's commit time.
func holdRelease(j store.HoldJudgement, until []store.TaskRef, closedAt map[store.TaskRef]time.Time) string {
	if j.State == store.HoldDependencyCancelled {
		return "dependency cancelled: " + store.TaskRefList(j.Cancelled)
	}
	parts := make([]string, len(until))
	for i, ref := range until {
		parts[i] = ref.String() + " merged"
		if at, ok := closedAt[ref]; ok {
			parts[i] += " " + at.UTC().Format(time.RFC3339)
		}
	}
	return "released: " + strings.Join(parts, ", ")
}

// releasedHold is a released hold that still matters: a park still counting but for its release, a deferred
// decision still open, or a follow-up not done. Repository and TaskID are the held item's task, or a follow-up's
// first named task; Step and Attempt name a park's attempt.
type releasedHold struct {
	Kind       string
	Repository string
	TaskID     string
	Step       string
	Attempt    int
	Item       string
	Cycle      int
	Release    string
	Reason     string
	Command    string
}

// messageID is the id of the hold-released message for h: hold-released.<kind>.<repo>.<task>.<item>.<cycle>, or
// hold-released.follow-up.<id> for a follow-up, which is done once and has no cycle; one too long for a message id
// is hashed.
func (h releasedHold) messageID() string {
	id := fmt.Sprintf("%s.%s.%s.%s.%s.%d", store.MessageHoldReleased, h.Kind, h.Repository, h.TaskID, h.Item, h.Cycle)
	if h.Kind == followUpKind {
		id = fmt.Sprintf("%s.%s.%s", store.MessageHoldReleased, h.Kind, h.Item)
	}
	if len(id) > 100 {
		id = store.MessageHoldReleased + "." + digest.Of([]byte(id)).Hex()
	}
	return id
}

// message is the Director message reporting h's release.
func (h releasedHold) message() store.Message {
	var what string
	switch h.Kind {
	case "park":
		what = fmt.Sprintf("The park of attempt %d of %s of %s/%s", h.Attempt, h.Step, h.Repository, h.TaskID)
	case decisionKind:
		what = fmt.Sprintf("The deferral of decision %s of %s/%s", h.Item, h.Repository, h.TaskID)
	default:
		what = "Follow-up " + h.Item
	}
	text := fmt.Sprintf("%s is released (%s). Its reason: %s. Nothing was done automatically; act on it with `%s`.", what, h.Release, oneLine(h.Reason), h.Command)
	return store.Message{ID: h.messageID(), Recipient: store.RecipientDirector, Repository: h.Repository, TaskID: h.TaskID, Step: h.Step,
		Attempt: h.Attempt, Type: store.MessageHoldReleased, Text: text, Sender: store.SenderGuildhall}
}

// holdsRound is one round of the holds watcher over the store at path and the state directory, while its newest
// session is running or recovering: it reads status and, for each released hold that still matters, sends the
// Director one hold-released message and wakes it with wake. A message already sent is not sent or woken for again,
// so each release wakes the Director once however many rounds see it.
func holdsRound(path, state string, wake func(id string) error, logf func(format string, args ...any)) {
	live, err := withStore(path, true, func(s *store.Store) (bool, error) {
		d, err := s.LatestDirectorSession()
		switch {
		case errors.Is(err, store.ErrNotFound):
			return true, nil
		case err != nil:
			return false, err
		}
		return d.State == store.DirectorRunning || d.State == store.DirectorRecovering, nil
	})
	if err != nil {
		logf("holds: %v", err)
		return
	}
	if !live {
		return
	}
	status, err := readDirectorStatus(context.Background(), path, state, nil)
	if err != nil {
		logf("holds: %v", err)
		return
	}
	for _, h := range status.released {
		m := h.message()
		sent, err := withStore(path, false, func(s *store.Store) (bool, error) {
			if _, err := s.Message(m.ID); err == nil {
				return false, nil
			} else if !errors.Is(err, store.ErrNotFound) {
				return false, err
			}
			_, err := s.SendMessage(m)
			return err == nil, err
		})
		if err != nil {
			logf("%s/%s: hold-released %s %s: %v", h.Repository, h.TaskID, h.Kind, h.Item, err)
			continue
		}
		if !sent {
			continue
		}
		logf("%s/%s: %s %s released: %s", h.Repository, h.TaskID, h.Kind, h.Item, h.Release)
		if err := wake(m.ID); err != nil {
			logf("%s/%s: wake for %s: %v", h.Repository, h.TaskID, m.ID, err)
		}
	}
}

// watchHolds is session n's holds watcher: a fifth checks watcher, on the same interval, that wakes the Director
// once for each hold released since (docs/decisions/0096-hold-until-merged.md); it logs to holds.log in dir. Like
// the others it runs only while the session is running or recovering.
func (a *App) watchHolds(p startPlan, dir, copyDir, storePath, state string) (*checksWatch, error) {
	w, _, err := a.watcher(p, dir, copyDir, storePath, state, holdsLog)
	if err != nil {
		return nil, err
	}
	logf := watchLog(filepath.Join(dir, holdsLog))
	wake := func(id string) error {
		_, err := a.wake(storePath, id, store.SenderGuildhall)
		return err
	}
	w.drain = func() { holdsRound(storePath, state, wake, logf) }
	return w, nil
}

// holdsLog is the file in a session's directory its holds watcher logs to.
const holdsLog = "holds.log"

// decideDefer runs `guildhall decide defer|undefer`: the Director holds an open decision out of needs_me until named
// tasks merge, or lifts that hold (docs/decisions-cli.md#deferring-a-decision). It prints the deferral record.
func (a *App) decideDefer(sub string, args []string) int {
	name := "decide " + sub
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	path := fl.String("store", "", "store file")
	var cp copyFlags
	cp.register(fl)
	repo := fl.String("repo", "", "repository id")
	task := fl.String("task", "", "task id")
	id := fl.String("decision", "", "decision id")
	var reason *string
	var until untilMerged
	if sub == "defer" {
		reason = fl.String("reason", "", "the Director's reason")
		fl.Var(&until, "until-merged", "a task the decision waits to merge, <repo>/<task>")
	}
	if err := fl.Parse(args); err != nil {
		return a.decideUsage(err.Error())
	}
	switch err := cp.apply(a, path, nil, nil); {
	case fl.NArg() > 0:
		return a.decideUsage(fmt.Sprintf("%s takes no arguments", name))
	case err != nil:
		return a.decideUsage(err.Error())
	case cp.searched:
		return a.decideUsage(cp.missing(a).Error())
	case *path == "" || *repo == "" || *task == "" || *id == "":
		return a.decideUsage(name + " requires --store, --repo, --task and --decision")
	case !workflow.ValidID(*repo) || !workflow.ValidID(*task) || !workflow.ValidID(*id):
		return a.decideUsage("--repo, --task and --decision must be ids")
	case sub == "defer" && strings.TrimSpace(*reason) == "":
		return a.decideUsage(name + " requires --reason")
	case sub == "defer" && len(until) == 0:
		return a.decideUsage(name + " requires --until-merged <repo>/<task>")
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s: %v\n", name, err)
		return 1
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(*path))
	}
	d, err := withStore(*path, false, func(s *store.Store) (store.DecisionDeferral, error) {
		if sub == "defer" {
			return deferDecision(s, *repo, *task, *id, *reason, until)
		}
		return undeferDecision(s, *repo, *task, *id)
	})
	if err != nil {
		return fail(err)
	}
	if err := json.NewEncoder(a.Stdout).Encode(d); err != nil {
		return 1
	}
	return 0
}

// deferDecision defers a decision under the mutation id of its deferral record's current cycle, so repeating an
// identical deferral replays it, and refuses another reason or until-merged set while the decision is deferred.
func deferDecision(s *store.Store, repo, task, id, reason string, until []store.TaskRef) (store.DecisionDeferral, error) {
	prior, err := s.DecisionDeferral(repo, task, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.DecisionDeferral{}, err
	}
	already := func(d store.DecisionDeferral) error {
		return fmt.Errorf("decision %s of %s/%s is already deferred, with reason %q until %s merges; undefer it first", id, repo, task, d.Reason,
			store.TaskRefList(d.UntilMerged))
	}
	if prior.Deferred && (prior.Reason != reason || !slices.Equal(prior.UntilMerged, until)) {
		return store.DecisionDeferral{}, already(prior)
	}
	_, err = s.DeferDecision(mutationID("decision-defer", repo, task, id, strconv.Itoa(prior.Cycle)), repo, task, id, reason, until, prior.Cycle)
	if errors.Is(err, store.ErrMutationConflict) || errors.Is(err, store.ErrExists) {
		// A deferral or undefer between the read and the write: report the record as it now stands.
		if now, rerr := s.DecisionDeferral(repo, task, id); rerr == nil && now.Deferred {
			return store.DecisionDeferral{}, already(now)
		}
	}
	if err != nil {
		return store.DecisionDeferral{}, err
	}
	return s.DecisionDeferral(repo, task, id)
}

// undeferDecision lifts a decision's deferral under the mutation id of its deferral record's current cycle.
func undeferDecision(s *store.Store, repo, task, id string) (store.DecisionDeferral, error) {
	prior, err := s.DecisionDeferral(repo, task, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.DecisionDeferral{}, err
	}
	if !prior.Deferred {
		return store.DecisionDeferral{}, fmt.Errorf("decision %s of %s/%s is not deferred", id, repo, task)
	}
	if _, err := s.UndeferDecision(mutationID("decision-undefer", repo, task, id, strconv.Itoa(prior.Cycle)), repo, task, id, prior.Cycle); err != nil {
		return store.DecisionDeferral{}, err
	}
	return s.DecisionDeferral(repo, task, id)
}

// directorFollowUp runs `guildhall director follow-up add|list|done` (docs/director.md#follow-ups). A change prints a
// one-line receipt, such as "f3 added", and list prints the follow-ups not done.
func (a *App) directorFollowUp(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "add":
			return a.directorFollowUpAdd(args[1:])
		case "list":
			return a.directorFollowUpList(args[1:])
		case "done":
			return a.directorFollowUpDone(args[1:])
		}
	}
	return a.usageError("director follow-up: the commands are add, list and done")
}

// directorFollowUpAdd runs `guildhall director follow-up add --until-merged <repo>/<task>... <text> [--copy <dir> |
// --store <file>]`.
func (a *App) directorFollowUpAdd(args []string) int {
	var until untilMerged
	path, _, ops, code := a.sessionOperands("director follow-up add", args, false, []string{"<text>"}, func(fl *flag.FlagSet) {
		fl.Var(&until, "until-merged", "a task the follow-up waits to merge, <repo>/<task>")
	})
	if code >= 0 {
		return code
	}
	if len(until) == 0 {
		return a.usageError("director follow-up add: --until-merged <repo>/<task> is required")
	}
	return a.followUpChange("director follow-up add", path, "added", func(s *store.Store, n int) (store.FollowUp, error) {
		f, _, err := s.AddFollowUp(n, ops[0], until)
		return f, err
	})
}

// directorFollowUpDone runs `guildhall director follow-up done <id> [--copy <dir> | --store <file>]`.
func (a *App) directorFollowUpDone(args []string) int {
	path, _, ops, code := a.sessionOperands("director follow-up done", args, false, []string{"<id>"}, nil)
	if code >= 0 {
		return code
	}
	return a.followUpChange("director follow-up done", path, "done", func(s *store.Store, n int) (store.FollowUp, error) {
		f, _, err := s.DoneFollowUp(n, ops[0])
		return f, err
	})
}

// followUpChange makes one follow-up change as the newest session, as noteChange does a note's, and prints
// "<id> <done>".
func (a *App) followUpChange(name, path, done string, change func(*store.Store, int) (store.FollowUp, error)) int {
	f, err := withStoreClock(path, false, a.Now, func(s *store.Store) (store.FollowUp, error) {
		d, err := s.LatestDirectorSession()
		if errors.Is(err, store.ErrNotFound) {
			return store.FollowUp{}, errors.New("no Director session; start one with guildhall director start")
		} else if err != nil {
			return store.FollowUp{}, err
		}
		return change(s, d.Number)
	})
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	if _, err := fmt.Fprintf(a.Stdout, "%s %s\n", f.ID, done); err != nil {
		return 1
	}
	return 0
}

// directorFollowUpList runs `guildhall director follow-up list [--copy <dir> | --store <file>]`: the follow-ups not
// done, one line each, "- f3 until app/d45: text", with "(waiting for …)" or "[released: …]", or "No follow-ups.".
func (a *App) directorFollowUpList(args []string) int {
	path, _, code := a.sessionArgs("director follow-up list", args, false, nil)
	if code >= 0 {
		return code
	}
	lines, err := withStore(path, true, func(s *store.Store) ([]string, error) {
		follows, err := s.FollowUps(false)
		if err != nil {
			return nil, err
		}
		tasks, err := listTasks(s, "")
		if err != nil {
			return nil, err
		}
		var lines []string
		for _, f := range follows {
			j := store.JudgeHold(f.UntilMerged, tasks)
			state := fmt.Sprintf("(waiting for %s)", store.TaskRefList(j.Waiting))
			if j.State != store.HoldWaiting {
				state = "[" + holdRelease(j, f.UntilMerged, nil) + "]"
			}
			lines = append(lines, fmt.Sprintf("- %s until %s %s: %s", f.ID, store.TaskRefList(f.UntilMerged), state, f.Text))
		}
		return lines, nil
	})
	if err != nil {
		return a.fail(fmt.Errorf("director follow-up list: %w", err))
	}
	out := "No follow-ups.\n"
	if len(lines) > 0 {
		out = strings.Join(lines, "\n") + "\n"
	}
	if _, err := fmt.Fprint(a.Stdout, out); err != nil {
		return 1
	}
	return 0
}

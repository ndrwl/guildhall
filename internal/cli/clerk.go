package cli

import (
	"cmp"
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
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/reconcile"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const clerkUsage = `usage:
  guildhall clerk guild [--all] [--copy <dir>]
  guildhall clerk decisions [--copy <dir>]
  guildhall clerk task [--copy <dir>] <task>
  guildhall clerk inspect [--copy <dir>] <task>
  guildhall clerk queue [--copy <dir>]
<task> is a task id, or <repo>/<task> when more than one repository has that id.
Clerk only reads the copy found from the current directory, or --copy, and prints plain text for the Guildmaster;
it changes nothing (docs/clerk.md).`

// clerkWidth is the width, in columns, Clerk lays its answers out at.
const clerkWidth = 100

// clerkLockWait bounds how long Clerk waits for a writer's store lock before it says the store is busy.
var clerkLockWait = storeWait

// errStoreBusy is Clerk's answer when a writer holds the store's lock.
var errStoreBusy = errors.New("the store is busy (the Director is writing); try again in a moment")

// clerkWindow is how far back `clerk guild --all` shows closed tasks.
const clerkWindow = 7 * 24 * time.Hour

// clerk runs `guildhall clerk`, the read-only text answers behind the Clerk plugin's slash commands
// (docs/contracts/clerk.md). It exits 0 on success, 1 when the copy or the store refuses and 2 on a usage error.
func (a *App) clerk(args []string) int {
	if len(args) == 0 {
		return a.clerkUsage("missing clerk subcommand")
	}
	sub := args[0]
	if sub == "-h" || sub == "--help" || sub == "help" {
		if _, err := fmt.Fprintln(a.Stdout, clerkUsage); err != nil {
			return 1
		}
		return 0
	}
	want := 0
	switch sub {
	case "guild", "decisions", "queue":
	case "task", "inspect":
		want = 1
	default:
		return a.clerkUsage(fmt.Sprintf("unknown clerk subcommand %q", sub))
	}
	fl := flag.NewFlagSet("clerk "+sub, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	dir := fl.String("copy", "", "Director copy directory")
	all := fl.Bool("all", false, "also show tasks closed in the last 7 days")
	var positional []string
	for rest := args[1:]; ; {
		if err := fl.Parse(rest); errors.Is(err, flag.ErrHelp) {
			if _, err := fmt.Fprintln(a.Stdout, clerkUsage); err != nil {
				return 1
			}
			return 0
		} else if err != nil {
			return a.clerkUsage("clerk " + sub + ": " + err.Error())
		}
		if fl.NArg() == 0 {
			break
		}
		positional, rest = append(positional, fl.Arg(0)), fl.Args()[1:]
	}
	switch {
	case *all && sub != "guild":
		return a.clerkUsage(fmt.Sprintf("--all does not apply to clerk %s", sub))
	case len(positional) != want && want == 1:
		return a.clerkUsage(fmt.Sprintf("clerk %s takes one task id", sub))
	case len(positional) != want:
		return a.clerkUsage(fmt.Sprintf("clerk %s takes no arguments", sub))
	}
	fail := func(err error) int {
		if errors.Is(err, store.ErrLocked) {
			err = errStoreBusy
		}
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: clerk %s: %v\n", sub, err)
		return 1
	}
	c, err := a.clerkCopy(*dir)
	if err != nil {
		return fail(err)
	}
	d, err := a.clerkRead(context.Background(), c)
	if err != nil {
		return fail(err)
	}
	var out string
	switch sub {
	case "guild":
		out = d.guild(*all)
	case "decisions":
		out = d.decisions()
	case "queue":
		out = d.queue()
	case "task", "inspect":
		var ref store.TaskRef
		if ref, err = d.resolve(positional[0]); err != nil {
			return fail(err)
		}
		if sub == "task" {
			out = d.task(ref)
		} else if out, err = d.inspect(c, ref); err != nil {
			return fail(err)
		}
	}
	if _, err := io.WriteString(a.Stdout, out); err != nil {
		return 1
	}
	if d.refusal != "" {
		if _, err := fmt.Fprintf(a.Stderr, "guildhall: clerk %s: answered without the copy's config, which is refused: %s\n", sub, d.refusal); err != nil {
			return 1
		}
	}
	return 0
}

func (a *App) clerkUsage(msg string) int {
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s\n%s\n", msg, clerkUsage)
	return 2
}

// clerkPaths are the files of the copy Clerk reads.
type clerkPaths struct {
	dir, store, config, state string
}

// clerkCopy selects the copy Clerk reads: --copy, which must already be a marked copy since Clerk never writes the
// marker, or else the copy found from the working directory, with copy discovery's one refusal when there is none.
func (a *App) clerkCopy(dir string) (clerkPaths, error) {
	if dir == "" {
		found, ok, err := discoverCopy(a.Getwd)
		if err != nil {
			return clerkPaths{}, err
		}
		if !ok {
			c := copyFlags{searched: true, from: found}
			return clerkPaths{}, c.missing(a)
		}
		dir = found
	} else {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return clerkPaths{}, err
		}
		dir = abs
		if err := readCopyMarker(filepath.Join(dir, copyMarkerFile)); errors.Is(err, fs.ErrNotExist) {
			return clerkPaths{}, fmt.Errorf("%s is not a Guildhall copy: it has no %s marker, and Clerk never writes one", dir, copyMarkerFile)
		} else if err != nil {
			return clerkPaths{}, err
		}
	}
	p := clerkPaths{dir: dir, store: filepath.Join(dir, "director.store"), config: filepath.Join(dir, "config.toml"), state: filepath.Join(dir, "state")}
	if _, err := os.Stat(p.store); errors.Is(err, fs.ErrNotExist) {
		return clerkPaths{}, noStore(p.store)
	}
	return p, nil
}

// clerkData is everything Clerk's answers are made from: the status feed, each task's view, and the facts the feed
// does not carry, read in one more read-only transaction.
type clerkData struct {
	now    time.Time
	status directorStatus
	// tasks are every task's view, in repository then task id order, and byKey the same with their status by
	// "<repo>/<task>".
	tasks []taskView
	byKey map[string]clerkTask
	// multi says more than one repository has tasks, so tasks are named <repo>/<task>.
	multi bool
	// cfg is the copy's config, nil when it cannot be read or the configuration-admission boundary refuses it; refusal
	// is that refusal.
	cfg     *config.Config
	refusal string
	// resolver returns repository repo's bound-workflow resolver, made once per repository and memoised per
	// revision, so each revision resolves at most once per Clerk read; nil before clerkRead sets it.
	resolver func(repo string) func(workflow.Revision) (*workflow.Definition, error)
}

// clerkResolvers wraps byRepo so each repository's resolver, with its per-revision memo, is made once.
func clerkResolvers(byRepo func(repo string) func(workflow.Revision) (*workflow.Definition, error)) func(repo string) func(workflow.Revision) (*workflow.Definition, error) {
	made := map[string]func(workflow.Revision) (*workflow.Definition, error){}
	return func(repo string) func(workflow.Revision) (*workflow.Definition, error) {
		if r, ok := made[repo]; ok {
			return r
		}
		r := byRepo(repo)
		made[repo] = r
		return r
	}
}

// clerkTask is one task's facts: its view and status, its latest recorded pull request, whether it merged or is
// merging, the type of its newest checks message and when it closed.
type clerkTask struct {
	view     taskView
	status   statusTask
	pr       *interp.RecordedPullRequest
	merging  bool
	checks   string
	closedAt time.Time
}

func (a *App) clerkRead(ctx context.Context, c clerkPaths) (clerkData, error) {
	return a.clerkReadWait(ctx, c, clerkLockWait)
}

// clerkReadWait is clerkRead waiting up to wait for a writer's store lock, as the task pages read with their own
// bounded wait.
func (a *App) clerkReadWait(ctx context.Context, c clerkPaths, wait time.Duration) (clerkData, error) {
	records := storeRecords{path: c.store, wait: wait}
	st, err := readDirectorStatusRecords(ctx, records, c.state, c.config, a.Now)
	if err != nil {
		return clerkData{}, err
	}
	// The version block is read with no fetch, as director status reads it.
	if it, ok := a.versionInfo(ctx, c.config, c.state, false).installItem(); ok {
		st.NeedsMe = append(st.NeedsMe, it)
	}
	d := clerkData{now: time.Now().UTC(), status: st, byKey: map[string]clerkTask{}}
	if a.Now != nil {
		d.now = a.Now().UTC()
	}
	// A config the boundary refuses is not applied: Clerk answers without it, as for one that does not load, and
	// says so.
	var refused refusalError
	if cfg, err := admitDirector(c.config); err == nil {
		d.cfg = cfg
	} else if errors.As(err, &refused) {
		d.refusal = refused.Text
	}
	d.resolver = clerkResolvers(statusWorkflows(c.config))
	documents := filepath.Join(c.state, "documents")
	_, err = withRecords(records, true, a.Now, func(s *store.Store) (struct{}, error) {
		tasks, err := listTasks(s, "")
		if err != nil {
			return struct{}{}, err
		}
		if d.tasks, err = taskViews(s, tasks); err != nil {
			return struct{}{}, err
		}
		receipts, err := s.Receipts()
		if err != nil {
			return struct{}{}, err
		}
		for _, v := range d.tasks {
			key := v.Repository + "/" + v.ID
			ct := clerkTask{view: v, closedAt: v.CancelledAt}
			docs, err := s.Documents(v.Repository, v.ID)
			if err != nil {
				return struct{}{}, err
			}
			// A pull request document that cannot be read leaves the task with no pull request shown.
			if prs, merged, err := interp.TaskPullRequests(docs, documents); err == nil {
				// The pull request shown is the latest slice's, and merging is that slice's.
				ct.merging = merged[1]
				if len(prs) > 0 {
					ct.pr = &prs[len(prs)-1]
					for i := range prs {
						if prs[i].Slice >= ct.pr.Slice {
							ct.pr = &prs[i]
						}
					}
					ct.merging = merged[ct.pr.Slice]
				}
			}
			d.byKey[key] = ct
		}
		for _, r := range receipts {
			key := r.Repository + "/" + r.TaskID
			ct, ok := d.byKey[key]
			if !ok {
				continue
			}
			switch r.Operation {
			case "set-task-state":
				ct.closedAt = r.CommittedAt
			case "send-message":
				id, _ := strings.CutPrefix(r.Record, "message/")
				if ct.pr == nil || !strings.Contains(id, ".checks-") && !strings.HasPrefix(id, "advance.") {
					continue
				}
				m, err := s.Message(id)
				if err != nil {
					return struct{}{}, err
				}
				if m.Type == store.MessageChecksPassed || m.Type == store.MessageChecksFailed || m.Type == store.MessageChecksPending {
					ct.checks = m.Type
				}
			}
			d.byKey[key] = ct
		}
		return struct{}{}, nil
	})
	if err != nil {
		return clerkData{}, err
	}
	for _, t := range st.Tasks {
		key := t.Repository + "/" + t.ID
		if ct, ok := d.byKey[key]; ok {
			ct.status = t
			d.byKey[key] = ct
		}
	}
	for _, v := range d.tasks {
		d.multi = d.multi || v.Repository != d.tasks[0].Repository
	}
	return d, nil
}

// heading is an answer's first line, saying the view is local: "<what>, as of <time>, local state".
func (d clerkData) heading(what string) string {
	return fmt.Sprintf("%s, as of %s, local state\n", what, clerkTime(d.now))
}

// name is how a task is named: its id, or <repo>/<id> when more than one repository has tasks.
func (d clerkData) name(repo, id string) string {
	if d.multi {
		return repo + "/" + id
	}
	return id
}

// resolve finds the task arg names: <repo>/<id>, or an id exactly one repository has.
func (d clerkData) resolve(arg string) (store.TaskRef, error) {
	if repo, id, ok := strings.Cut(arg, "/"); ok {
		if _, found := d.byKey[repo+"/"+id]; !found {
			return store.TaskRef{}, fmt.Errorf("no task %s in this copy", arg)
		}
		return store.TaskRef{Repository: repo, ID: id}, nil
	}
	var found []string
	for _, v := range d.tasks {
		if v.ID == arg {
			found = append(found, v.Repository+"/"+v.ID)
		}
	}
	switch len(found) {
	case 0:
		return store.TaskRef{}, fmt.Errorf("no task %s in this copy", arg)
	case 1:
		repo, id, _ := strings.Cut(found[0], "/")
		return store.TaskRef{Repository: repo, ID: id}, nil
	}
	return store.TaskRef{}, fmt.Errorf("task %s is in more than one repository; name one of %s", arg, strings.Join(found, ", "))
}

// kind is the task kind t was created as, read by interp.TaskKindOf (docs/decisions/task-kinds.md) with the copy's
// repository, a zero one when the config does not list it.
func (d clerkData) kind(t store.Task) string {
	var r config.Repository
	if d.cfg != nil {
		if found, ok := d.cfg.Repository(t.Repository); ok {
			r = found
		}
	}
	var resolve func(workflow.Revision) (*workflow.Definition, error)
	if d.resolver != nil {
		resolve = d.resolver(t.Repository)
	}
	return interp.TaskKindOf(t, r, resolve)
}

// next is a task's next.json as Clerk reads it.
type clerkNext struct {
	Wait     string `json:"wait"`
	Step     string `json:"step"`
	Attempt  int    `json:"attempt"`
	Decision string `json:"decision"`
	Position int    `json:"position"`
}

// stands is where the task stands, the one plain phrase docs/clerk.md's table gives for it; an unknown wait is
// shown raw rather than refused.
func (d clerkData) stands(ct clerkTask) string {
	t := ct.view
	switch t.State {
	case store.TaskCancelled:
		return "cancelled"
	case store.TaskDone:
		if ct.merging {
			return "merged"
		}
		return "done"
	}
	if ct.status.Next == nil {
		if ct.status.NextError != "" {
			return "waiting (next.json unreadable)"
		}
		return "not started"
	}
	var n clerkNext
	if err := json.Unmarshal(ct.status.Next, &n); err != nil {
		return "waiting (next.json unreadable)"
	}
	switch interp.Wait(n.Wait) {
	case interp.WaitAttempt:
		for _, s := range ct.status.Steps {
			if s.Step == n.Step && s.Attempt == n.Attempt && s.Outcome == "" && s.Live != nil && s.Live.State == reconcile.Running {
				return n.Step + " running"
			}
		}
		return n.Step + " starting"
	case interp.WaitDecision:
		// next.json keeps naming the decision after it is answered, until the Director continues the task.
		for _, dec := range d.status.Decisions {
			if dec.Repository == t.Repository && dec.TaskID == t.ID && dec.ID == n.Decision {
				if dec.Kind == store.DecisionQuestion {
					return "waiting for your answer"
				}
				return "waiting for your approval"
			}
		}
		return "waiting for the Director"
	case interp.WaitDirector, interp.WaitDirectorStage:
		return "waiting for the Director"
	case interp.WaitChecks:
		if ct.pr != nil {
			return fmt.Sprintf("CI pending, PR #%d", ct.pr.Number)
		}
		return "CI pending"
	case interp.WaitMergeQueue:
		e := t.MergeQueue
		if e == nil {
			return "in merge queue"
		}
		out := fmt.Sprintf("in merge queue %d/%d", e.Position, d.queueLength(t.Repository))
		if e.WaitingOn != "queue" {
			out += ", " + clerkWaitingOn(e.WaitingOn)
		}
		return out
	case interp.WaitQueued:
		return "queued " + strconv.Itoa(n.Position)
	case interp.WaitPaused:
		return "paused"
	case interp.WaitDependencies:
		var waiting []string
		for _, dep := range t.DependsOn {
			if ct, ok := d.byKey[dep.Repository+"/"+dep.ID]; ok && ct.view.State == store.TaskDone {
				continue
			}
			if dep.Repository == t.Repository && !d.multi {
				waiting = append(waiting, dep.ID)
			} else {
				waiting = append(waiting, dep.Repository+"/"+dep.ID)
			}
		}
		if len(waiting) == 0 {
			return "waiting on its dependencies"
		}
		return "waiting on " + strings.Join(waiting, ", ")
	case interp.WaitDone:
		if ct.merging {
			return "merged"
		}
		return "done"
	}
	return "waiting (" + n.Wait + ")"
}

// queueLength is how many tasks repository repo's merge queue holds.
func (d clerkData) queueLength(repo string) int {
	for _, q := range d.status.MergeQueues {
		if q.Repository == repo {
			return len(q.Entries)
		}
	}
	return 0
}

// clerkWaitingOn says a merge-queue entry's waiting_on in plain words.
func clerkWaitingOn(w string) string {
	switch w {
	case "rebase":
		return "rebasing"
	case "ci":
		return "CI pending"
	case "decision":
		return "waiting for your answer"
	case "queue":
		return "waiting its turn"
	}
	return "waiting (" + w + ")"
}

// checks is the pull request's checks state as the store holds it.
func (ct clerkTask) checksState() string {
	var n clerkNext
	if ct.status.Next != nil && json.Unmarshal(ct.status.Next, &n) == nil && interp.Wait(n.Wait) == interp.WaitChecks {
		return "CI pending"
	}
	switch ct.checks {
	case store.MessageChecksPassed:
		return "CI passed"
	case store.MessageChecksFailed:
		return "CI failed"
	case store.MessageChecksPending:
		return "CI pending"
	}
	return "no checks recorded"
}

// guild is `clerk guild`: the tasks by priority, what needs the Guildmaster, the open pull requests and the merge
// queue's summary.
func (d clerkData) guild(all bool) string {
	var b strings.Builder
	b.WriteString(d.heading("Guild"))

	var shown []clerkTask
	for _, v := range d.tasks {
		ct := d.byKey[v.Repository+"/"+v.ID]
		closed := v.State != store.TaskOpen
		if closed && (!all || ct.closedAt.IsZero() || d.now.Sub(ct.closedAt) > clerkWindow) {
			continue
		}
		shown = append(shown, ct)
	}
	slices.SortStableFunc(shown, func(a, b clerkTask) int {
		return cmp.Or(cmp.Compare(a.view.Priority, b.view.Priority), strings.Compare(a.view.ID, b.view.ID), strings.Compare(a.view.Repository, b.view.Repository))
	})
	if all {
		b.WriteString("\nTasks (open, and closed in the last 7 days)\n")
	} else {
		b.WriteString("\nTasks\n")
	}
	if len(shown) == 0 {
		if all {
			b.WriteString("No open tasks, and none closed in the last 7 days.\n")
		} else {
			b.WriteString("No open tasks.\n")
		}
	} else {
		rows := [][]string{}
		for _, ct := range shown {
			stands := d.stands(ct)
			if sl := ct.status.Slices; sl != nil {
				stands = fmt.Sprintf("slice %d of %d, %s", sl.Current, sl.Total, stands)
			}
			rows = append(rows, []string{d.name(ct.view.Repository, ct.view.ID), ct.view.Priority.String(), d.kind(ct.view.Task), oneLine(ct.view.Title), stands})
		}
		clerkTable(&b, []string{"TASK", "PRI", "KIND", "TITLE", "WHERE IT STANDS"}, rows, 3)
	}

	// Needs you holds only the Guildmaster's asks: open decisions, a pending install, and the open tasks handed to a
	// Director stage, which interviews the Guildmaster. Every other needs_me item is the Director's.
	var mine, director []needsMeItem
	for _, it := range d.status.NeedsMe {
		if it.Kind == decisionKind || it.Kind == installKind {
			mine = append(mine, it)
		} else {
			director = append(director, it)
		}
	}
	for _, ct := range shown {
		if ct.view.State != store.TaskOpen || ct.status.Next == nil {
			continue
		}
		var n clerkNext
		if err := json.Unmarshal(ct.status.Next, &n); err != nil || interp.Wait(n.Wait) != interp.WaitDirectorStage {
			continue
		}
		mine = append(mine, needsMeItem{Kind: clerkDirectorStageKind, Repository: ct.view.Repository, TaskID: ct.view.ID, Step: n.Step,
			Reason: "the Director runs " + n.Step + " with you in its session"})
	}
	b.WriteString("\nNeeds you\n")
	if len(mine) == 0 {
		b.WriteString("Nothing needs you.\n")
	}
	d.needsMeLines(&b, mine)
	b.WriteString("\nWaiting on the Director\n")
	if len(director) == 0 {
		b.WriteString("Nothing waits on the Director.\n")
	}
	d.needsMeLines(&b, director)

	b.WriteString("\nPull requests\n")
	var prs [][]string
	for _, v := range d.tasks {
		ct := d.byKey[v.Repository+"/"+v.ID]
		if v.State != store.TaskOpen || ct.pr == nil || ct.merging {
			continue
		}
		prs = append(prs, []string{"#" + strconv.Itoa(ct.pr.Number), d.name(v.Repository, v.ID), ct.checksState()})
	}
	if len(prs) == 0 {
		b.WriteString("No open pull requests.\n")
	} else {
		clerkTable(&b, []string{"PR", "TASK", "CHECKS"}, prs, -1)
	}

	b.WriteString("\nMerge queue\n")
	if len(d.status.MergeQueues) == 0 {
		b.WriteString("The merge queue is empty.\n")
	}
	for _, q := range d.status.MergeQueues {
		f := q.Entries[0]
		where := "front"
		if !f.Front {
			where = "next"
		}
		line := fmt.Sprintf("%s: %d queued, %s %s %s", q.Repository, len(q.Entries), where, f.Task, clerkWaitingOn(f.WaitingOn))
		if f.Held {
			line += ", held"
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// decisions is `clerk decisions`: every open decision, its question and numbered options, and whether it is
// deferred.
// clerkDirectorStageKind is the kind of Clerk's Needs you line for an open task handed to a Director stage, such as
// the grill; it is derived from the task's next.json and is never a needs_me item.
const clerkDirectorStageKind = "director-stage"

// needsMeLines writes each item as `- <kind>[ <task>][ <step>][ <decision>]: <reason>`.
func (d clerkData) needsMeLines(b *strings.Builder, items []needsMeItem) {
	for _, it := range items {
		head := it.Kind
		if it.TaskID != "" {
			head += " " + d.name(it.Repository, it.TaskID)
		} else if it.Repository != "" {
			head += " " + it.Repository
		}
		if it.Step != "" {
			head += " " + it.Step
		}
		if it.Decision != "" {
			head += " " + it.Decision
		}
		b.WriteString(clerkWrap("- "+head+": "+oneLine(it.Reason), "  "))
	}
}

func (d clerkData) decisions() string {
	var b strings.Builder
	b.WriteString(d.heading("Decisions"))
	b.WriteString("\n")
	if len(d.status.Decisions) == 0 {
		b.WriteString("No open decisions.\n")
	}
	for i, dec := range d.status.Decisions {
		if i > 0 {
			b.WriteString("\n")
		}
		line := fmt.Sprintf("%s %s", d.name(dec.Repository, dec.TaskID), dec.ID)
		if dec.Kind != "" {
			line += " (" + string(dec.Kind) + ")"
		}
		if dec.Deferred {
			line += ", deferred"
		}
		b.WriteString(line + "\n")
		for _, l := range strings.Split(strings.TrimRight(dec.Question, "\n"), "\n") {
			b.WriteString(clerkWrap("  "+l, "  "))
		}
		for n, o := range dec.Options {
			b.WriteString(clerkWrap(fmt.Sprintf("  %d. %s", n+1, oneLine(o)), "     "))
		}
	}
	return b.String()
}

// queue is `clerk queue`: each repository's merge queue in full, front first and then in queue order.
func (d clerkData) queue() string {
	var b strings.Builder
	b.WriteString(d.heading("Merge queue"))
	if len(d.status.MergeQueues) == 0 {
		b.WriteString("\nThe merge queue is empty.\n")
	}
	for _, q := range d.status.MergeQueues {
		b.WriteString("\n" + q.Repository + "\n")
		rows := [][]string{}
		for _, e := range q.Entries {
			ct := d.byKey[q.Repository+"/"+e.Task]
			pr := clerkPRNumber(e.PR)
			if ct.pr != nil {
				pr = "#" + strconv.Itoa(ct.pr.Number)
			}
			held := ""
			if e.Held {
				held = "held"
			}
			rows = append(rows, []string{strconv.Itoa(e.Position), e.Task, e.Priority.String(), oneLine(ct.view.Title), pr,
				clerkSince(d.now, e.ReadyAt), held, clerkWaitingOn(e.WaitingOn)})
		}
		clerkTable(&b, []string{"POS", "TASK", "PRI", "TITLE", "PR", "READY FOR", "HELD", "WAITING ON"}, rows, 3)
	}
	return b.String()
}

// task is `clerk task`: the task's state, priority, kind and title, where it stands, its pull request and its steps.
func (d clerkData) task(ref store.TaskRef) string {
	var b strings.Builder
	b.WriteString(d.heading("Task " + ref.Repository + "/" + ref.ID))
	d.taskSummary(&b, d.byKey[ref.Repository+"/"+ref.ID])
	return b.String()
}

func (d clerkData) taskSummary(b *strings.Builder, ct clerkTask) {
	v := ct.view
	b.WriteString("\n")
	b.WriteString(clerkWrap(v.ID+": "+oneLine(v.Title), "  "))
	pr := "none"
	if ct.pr != nil {
		pr = fmt.Sprintf("#%d %s", ct.pr.Number, ct.pr.URL)
		if v.State == store.TaskOpen && !ct.merging {
			pr += " (" + ct.checksState() + ")"
		}
	}
	rows := [][]string{
		{"State:", string(v.State)},
		{"Priority:", v.Priority.String()},
		{"Kind:", d.kind(v.Task)},
		{"Where it stands:", d.stands(ct)},
	}
	// A multi-PR task names its current slice, and lists every slice with its pull request in place of the one.
	sl := ct.status.Slices
	if sl != nil {
		rows = append(rows, []string{"Slice:", clerkSliceOf(sl)})
	} else {
		rows = append(rows, []string{"Pull request:", pr})
	}
	clerkTable(b, nil, rows, -1)
	if sl != nil {
		b.WriteString("\nSlices\n")
		var items [][]string
		for _, it := range sl.Items {
			pr := "none"
			if it.PullRequest != nil {
				pr = fmt.Sprintf("#%d %s", it.PullRequest.Number, it.PullRequest.URL)
				if it.State == interp.SliceCurrent && v.State == store.TaskOpen && !ct.merging && ct.pr != nil && ct.pr.Number == it.PullRequest.Number {
					pr += " (" + ct.checksState() + ")"
				}
			}
			items = append(items, []string{strconv.Itoa(it.Index), oneLine(it.Title), it.State, pr})
		}
		clerkTable(b, []string{"SLICE", "TITLE", "STATE", "PULL REQUEST"}, items, 1)
	}
	b.WriteString("\nSteps\n")
	if len(ct.status.Steps) == 0 {
		b.WriteString("No steps yet.\n")
		return
	}
	rows = [][]string{}
	for _, s := range ct.status.Steps {
		outcome := string(s.Outcome)
		if s.Outcome == "" {
			outcome = "running"
			if s.Live != nil && s.Live.State != reconcile.Running {
				outcome = "no outcome yet (" + string(s.Live.State) + ")"
			}
		}
		if s.Parked {
			outcome += ", parked"
		}
		rows = append(rows, []string{s.Step, strconv.Itoa(s.Attempt), outcome})
	}
	clerkTable(b, []string{"STEP", "ATTEMPT", "OUTCOME"}, rows, -1)
}

// clerkSliceOf names a multi-PR task's current slice: "<k> of <n>: <title>".
func clerkSliceOf(sl *statusSlices) string {
	text := fmt.Sprintf("%d of %d", sl.Current, sl.Total)
	if sl.Current >= 1 && sl.Current <= len(sl.Items) {
		text += ": " + oneLine(sl.Items[sl.Current-1].Title)
	}
	return text
}

// inspect is `clerk inspect`: everything task shows, then the task's timeline, attempt summaries, documents and
// spend.
func (d clerkData) inspect(c clerkPaths, ref store.TaskRef) (string, error) {
	var tl taskTimeline
	var fl taskFiles
	_, err := withRecords(storeRecords{path: c.store, wait: clerkLockWait}, true, nil, func(s *store.Store) (struct{}, error) {
		h, err := s.TaskHistory(ref.Repository, ref.ID)
		if err != nil {
			return struct{}{}, err
		}
		tl, fl = timeline(h), files(h)
		return struct{}{}, nil
	})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(d.heading("Inspect " + ref.Repository + "/" + ref.ID))
	ct := d.byKey[ref.Repository+"/"+ref.ID]
	d.taskSummary(&b, ct)

	b.WriteString("\nAttempts\n")
	if len(tl.Attempts) == 0 {
		b.WriteString("No attempts yet.\n")
	} else {
		rows := [][]string{}
		for _, at := range tl.Attempts {
			outcome := string(at.Outcome)
			if outcome == "" {
				outcome = "no outcome yet"
			}
			rows = append(rows, []string{at.Step, strconv.Itoa(at.Number), outcome, clerkStamp(at.StartedAt), clerkStamp(at.FinishedAt), clerkSeconds(at.DurationSeconds)})
		}
		clerkTable(&b, []string{"STEP", "ATTEMPT", "OUTCOME", "STARTED", "FINISHED", "TOOK"}, rows, -1)
	}

	b.WriteString("\nAttempt summaries\n")
	summaries := 0
	for _, at := range tl.Attempts {
		key := artisan.AttemptKey{Repository: ref.Repository, TaskID: ref.ID, Step: at.Step, Attempt: at.Number}
		if s := clerkSummary(filepath.Join(key.Dir(c.state), artisan.ResultFile)); s != "" {
			b.WriteString(clerkWrap(fmt.Sprintf("- %s %d: %s", at.Step, at.Number, oneLine(s)), "  "))
			summaries++
		}
	}
	if summaries == 0 {
		b.WriteString("No attempt summaries.\n")
	}

	b.WriteString("\nDecisions\n")
	if len(tl.Decisions) == 0 {
		b.WriteString("No decisions.\n")
	} else {
		rows := [][]string{}
		for _, dec := range tl.Decisions {
			state := "open"
			switch {
			case dec.Void:
				state = "void"
			case dec.Answered:
				state = "answered"
			}
			rows = append(rows, []string{dec.ID, string(dec.Kind), state, clerkStamp(dec.OpenedAt), clerkStamp(dec.AnsweredAt), clerkSeconds(dec.WaitSeconds)})
		}
		clerkTable(&b, []string{"DECISION", "KIND", "STATE", "OPENED", "ANSWERED", "WAITED"}, rows, -1)
	}

	b.WriteString("\nFindings\n")
	if len(tl.Findings) == 0 {
		b.WriteString("No review findings.\n")
	}
	for _, f := range tl.Findings {
		line := fmt.Sprintf("- %s: %d findings, %d suggestions", f.Document, len(f.Findings), len(f.Suggestions))
		if f.Summary != "" {
			line += "; " + oneLine(f.Summary)
		}
		b.WriteString(clerkWrap(line, "  "))
		for _, x := range f.Findings {
			b.WriteString(clerkWrap("  "+x.ID+": "+oneLine(x.Summary), "    "))
		}
	}

	b.WriteString("\nRulings\n")
	if len(tl.Rulings) == 0 {
		b.WriteString("No rulings.\n")
	}
	for _, r := range tl.Rulings {
		line := fmt.Sprintf("- %s %s: %s, %s", r.Document, r.Finding, r.Move, oneLine(r.Reason))
		if r.Contested {
			line += " (contested by " + r.ContestedBy + ")"
		}
		b.WriteString(clerkWrap(line, "  "))
	}

	b.WriteString("\nPriority changes\n")
	if len(tl.PriorityChanges) == 0 {
		b.WriteString("None; " + tl.Priority.String() + " by default.\n")
	}
	for _, p := range tl.PriorityChanges {
		fmt.Fprintf(&b, "- %s set %s\n", p.Priority, clerkStamp(p.SetAt))
	}

	b.WriteString("\nMerge holds\n")
	if len(tl.MergeHolds) == 0 {
		b.WriteString("None.\n")
	}
	for _, h := range tl.MergeHolds {
		what := "released"
		if h.Held {
			what = "held"
		}
		fmt.Fprintf(&b, "- %s %s\n", what, clerkStamp(h.SetAt))
	}

	b.WriteString("\nDocuments\n")
	if len(fl.Documents) == 0 {
		b.WriteString("No documents.\n")
	} else {
		rows := [][]string{}
		for _, doc := range fl.Documents {
			rows = append(rows, []string{doc.Name, clerkStamp(doc.RecordedAt), doc.Path})
		}
		clerkTable(&b, []string{"DOCUMENT", "RECORDED", "PATH"}, rows, -1)
	}

	b.WriteString("\nSpend\n")
	b.WriteString(clerkSpend(ct.status.Spend))
	return b.String(), nil
}

// clerkSpend says a task's spend in one line.
func clerkSpend(s store.SpendTotal) string {
	if s.Attempts == 0 {
		return "No spend recorded.\n"
	}
	plural := map[bool]string{true: "s"}[s.Attempts != 1]
	out := fmt.Sprintf("%d attempt%s: ", s.Attempts, plural)
	// A known measure some attempts did not report is partial, and says how many.
	partial := func(unknown int) string {
		if unknown == 0 {
			return ""
		}
		return fmt.Sprintf(", %d unknown", unknown)
	}
	if s.Tokens == store.SpendUnknown {
		out += "tokens unknown"
	} else {
		out += fmt.Sprintf("%d input, %d output, %d cache-write, %d cache-read tokens (%s%s)", s.InputTokens, s.OutputTokens,
			s.CacheCreationInputTokens, s.CacheReadInputTokens, s.Tokens, partial(s.TokensUnknown))
	}
	if s.Cost == store.SpendUnknown {
		out += "; cost unknown"
	} else {
		out += fmt.Sprintf("; $%.2f (%s%s)", s.CostUSD, s.Cost, partial(s.CostUnknown))
	}
	return clerkWrap(out, "  ")
}

// clerkSummary is the summary in an attempt's result file, "" when it has none that can be read.
func clerkSummary(path string) string {
	b, err := atomicfile.Read(path)
	if err != nil {
		return ""
	}
	var r struct {
		Summary string `json:"summary"`
	}
	if json.Unmarshal(b, &r) != nil {
		return ""
	}
	return strings.TrimSpace(r.Summary)
}

// clerkTable writes rows under header, which may be nil, aligned with tabwriter; the column fit, -1 for none, is
// cut with … so the table fits clerkWidth.
func clerkTable(b *strings.Builder, header []string, rows [][]string, fit int) {
	all := rows
	if header != nil {
		all = append([][]string{header}, rows...)
	}
	if fit >= 0 {
		others := 0
		for col := range all[0] {
			if col == fit {
				continue
			}
			w := 0
			for _, r := range all {
				w = max(w, utf8.RuneCountInString(r[col]))
			}
			others += w + 2
		}
		budget := max(clerkWidth-others-2, 12)
		for _, r := range rows {
			r[fit] = clerkCut(r[fit], budget)
		}
	}
	w := tabwriter.NewWriter(b, 0, 4, 2, ' ', 0)
	for _, r := range all {
		_, _ = fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	_ = w.Flush()
}

// clerkCut cuts s to n runes, ending a cut with ….
func clerkCut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// clerkWrap wraps one line of prose at clerkWidth, indenting each continuation line with indent.
func clerkWrap(s, indent string) string {
	var b strings.Builder
	rest := strings.TrimLeft(s, " ")
	lead := s[:len(s)-len(rest)]
	line := ""
	for _, word := range strings.Fields(rest) {
		switch {
		case line == "":
			line = lead + word
		case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) > clerkWidth:
			b.WriteString(line + "\n")
			line = indent + word
		default:
			line += " " + word
		}
	}
	b.WriteString(line + "\n")
	return b.String()
}

// clerkTime is a time as Clerk prints it, to the minute in UTC.
func clerkTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// clerkStamp is clerkTime of t, or - when it is not known.
func clerkStamp(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04")
}

// clerkSeconds is a duration in seconds as clerkDuration says it, or - when it is not known.
func clerkSeconds(s *float64) string {
	if s == nil {
		return "-"
	}
	return clerkDuration(time.Duration(*s * float64(time.Second)))
}

// clerkSince is how long ago t was, by now.
func clerkSince(now, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return clerkDuration(now.Sub(t))
}

// clerkDuration says d in its two largest units: 3d 4h, 2h 5m, 12m, or under 1m.
func clerkDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under 1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	}
	return fmt.Sprintf("%dd %dh", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
}

// clerkPRNumber is the pull request number at the end of a pull request URL, as #<n>, or - when it has none.
func clerkPRNumber(url string) string {
	last := url[strings.LastIndex(url, "/")+1:]
	if n, err := strconv.Atoi(last); err == nil && n > 0 {
		return "#" + last
	}
	return "-"
}

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
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/quality"
	"github.com/ndrwl/guildhall/internal/reconcile"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// statusLimit bounds the message headers and recent receipts the status feed prints.
const statusLimit = 20

// directorStatus is what `director status` prints (docs/director.md#status).
type directorStatus struct {
	AsOf    statusLabel   `json:"as_of"`
	Session statusSession `json:"session"`
	Mode    statusMode    `json:"mode"`
	// Quality is the config's quality settings (docs/configuration.md#quality-settings), when the copy has a config.
	Quality *config.Quality `json:"quality,omitempty"`
	Tasks   []statusTask    `json:"tasks"`
	// MergeQueues is each repository's merge queue that has a task in it (docs/interpreter.md#the-merge-queue).
	MergeQueues []mergeQueue     `json:"merge_queues"`
	Decisions   []statusDecision `json:"decisions"`
	Messages    statusMessages   `json:"messages"`
	Recent      []recentReceipt  `json:"recent"`
	NeedsMe     []needsMeItem    `json:"needs_me"`
	// Parked are the attempts the Director parked, held on purpose and left out of needs_me (docs/director.md#status).
	Parked []statusParked `json:"parked"`
	// Version is the binary printing the feed, from its build info and local git reads only
	// (docs/contracts/cli.md#version); the session supervisor's own snapshot leaves it out.
	Version *versionInfo `json:"version,omitempty"`
}

// recentReceipt is a receipt without its request digest, which only replay checks need.
type recentReceipt struct {
	MutationID  string    `json:"mutation_id"`
	Operation   string    `json:"operation"`
	Repository  string    `json:"repository"`
	TaskID      string    `json:"task_id"`
	Attempt     int       `json:"attempt,omitempty"`
	Record      string    `json:"record,omitempty"`
	Sequence    uint64    `json:"sequence"`
	CommittedAt time.Time `json:"committed_at"`
}

// statusLabel is the inspection label without a workflow, which the feed gives per task.
type statusLabel struct {
	Partial     bool      `json:"partial"`
	Excluded    []string  `json:"excluded"`
	Sequence    uint64    `json:"sequence"`
	CommittedAt time.Time `json:"committed_at"`
}

// statusSession is the newest Director session, or only the state "no session" when there is none.
type statusSession struct {
	State     string                 `json:"state"`
	Number    int                    `json:"number,omitempty"`
	Identity  *store.ProcessIdentity `json:"identity,omitempty"`
	Release   string                 `json:"release,omitempty"`
	Pane      store.DirectorPane     `json:"pane,omitzero"`
	StartedAt time.Time              `json:"started_at,omitzero"`
	ReadyAt   time.Time              `json:"ready_at,omitzero"`
	EndedAt   time.Time              `json:"ended_at,omitzero"`
	StopNow   bool                   `json:"stop_now,omitempty"`
	Sweeping  bool                   `json:"sweeping,omitempty"`
	EndReason string                 `json:"end_reason,omitempty"`
	// LastWake is the newest wake of any message to the Director, so whether it woke anyone and why not.
	LastWake *store.Wake `json:"last_wake,omitempty"`
	// GuardRails are the copy's guard rails and what the attempt caps count today (docs/director.md#guard-rails).
	GuardRails statusGuardRails `json:"guard_rails"`
	// StartQueue is every start the running cap queued, in queue order (docs/director.md#the-start-queue); the
	// supervisor's start-queue watcher drains it, so it is not in needs_me.
	StartQueue []statusQueuedStart `json:"start_queue"`
	// HandoffNote and Reload are the newest handoff note and reload request, whichever session recorded them.
	HandoffNote *store.HandoffNote    `json:"handoff_note,omitempty"`
	Reload      *store.DirectorReload `json:"reload,omitempty"`
	// Context is the newest context use the session's runtime reported, absent when it reported none.
	Context *session.Context `json:"context,omitempty"`
}

// statusQueuedStart is one start-queue entry, with its task's priority now and its 1-based place.
type statusQueuedStart struct {
	Repository string                 `json:"repository"`
	Task       string                 `json:"task"`
	Step       string                 `json:"step"`
	Attempt    int                    `json:"attempt"`
	Request    store.StartRequestKind `json:"request"`
	Priority   store.Priority         `json:"priority"`
	Position   int                    `json:"position"`
	QueuedAt   time.Time              `json:"queued_at"`
}

// statusGuardRails is the config's guard rails, when the copy has a config, beside the attempt caps' counts.
// ConfigError says why a config that exists gives no rails.
type statusGuardRails struct {
	*config.GuardRails
	ConfigError string `json:"config_error,omitempty"`
	// RunningAttempts counts what the running cap counts: running attempts, not Director stages.
	RunningAttempts int `json:"running_attempts"`
	AttemptsToday   int `json:"attempts_today"`
	// Day is the UTC day AttemptsToday counts.
	Day string `json:"day"`
	// SpendToday sums what the attempts started on Day reported they spent (docs/director.md#spend).
	SpendToday store.SpendTotal `json:"spend_today"`
}

// statusMode is the copy's mode; standalone until managed mode exists.
type statusMode struct {
	Name string `json:"name"`
}

type statusTask struct {
	Repository string            `json:"repository"`
	ID         string            `json:"id"`
	Title      string            `json:"title"`
	State      store.TaskState   `json:"state"`
	Workflow   workflow.Revision `json:"workflow"`
	Next       json.RawMessage   `json:"next,omitempty"`
	NextError  string            `json:"next_error,omitempty"`
	Steps      []statusStep      `json:"steps"`
	// Spend sums what the task's finished attempts reported they spent (docs/director.md#spend).
	Spend store.SpendTotal `json:"spend"`
}

// statusStep is a step's latest attempt: its outcome, or while it runs its live classification.
type statusStep struct {
	Step    string               `json:"step"`
	Attempt int                  `json:"attempt"`
	Outcome store.AttemptOutcome `json:"outcome,omitempty"`
	Live    *statusLive          `json:"live,omitempty"`
	// Parked says the latest attempt is parked, which is why a failed or stopped step is not in needs_me.
	Parked bool `json:"parked,omitempty"`
}

// statusParked is a counting park: a parked failed or stopped attempt that is its open task's step's latest, with the
// command to run when the hold ends.
type statusParked struct {
	Repository string               `json:"repository"`
	TaskID     string               `json:"task_id"`
	Step       string               `json:"step"`
	Attempt    int                  `json:"attempt"`
	Outcome    store.AttemptOutcome `json:"outcome"`
	Reason     string               `json:"reason"`
	ParkedAt   *time.Time           `json:"parked_at,omitempty"`
	Command    string               `json:"command"`
}

type statusLive struct {
	State  reconcile.State `json:"state"`
	Reason string          `json:"reason"`
}

type statusDecision struct {
	Repository string             `json:"repository"`
	TaskID     string             `json:"task_id"`
	ID         string             `json:"id"`
	Kind       store.DecisionKind `json:"kind,omitempty"`
	Subject    *store.Subject     `json:"subject,omitempty"`
	Options    []string           `json:"options,omitempty"`
	Question   string             `json:"question"`
	OpenedAt   *time.Time         `json:"opened_at,omitempty"`
}

type statusMessages struct {
	Unread  int             `json:"unread"`
	ByType  map[string]int  `json:"by_type"`
	Headers []messageHeader `json:"headers"`
}

type messageHeader struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	Step       string `json:"step,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
	Sequence   uint64 `json:"sequence"`
}

// needsMeItem is one thing waiting on the Director or the Guildmaster, with the command the Director runs next.
type needsMeItem struct {
	Kind       string `json:"kind"`
	Repository string `json:"repository,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	Step       string `json:"step,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
	Decision   string `json:"decision,omitempty"`
	Message    string `json:"message,omitempty"`
	Count      int    `json:"count,omitempty"`
	Retryable  bool   `json:"retryable,omitempty"`
	Resume     bool   `json:"resume,omitempty"`
	Reason     string `json:"reason"`
	Command    string `json:"command"`
}

// decisionKind is the needs_me kind of an open decision, which waits on the Guildmaster.
const decisionKind = "decision"

// checksKind is the needs_me kind of a task waiting for its checks, which the session supervisor advances
// (docs/director.md#checks).
const checksKind = "checks"

// dependenciesKind is the needs_me kind of a task the dependency gate holds, which the session supervisor advances
// (docs/director.md#dependencies).
const dependenciesKind = "dependencies"

// mergeQueueKind is the needs_me kind of a task waiting in its repository's merge queue, which the session
// supervisor advances (docs/interpreter.md#the-merge-queue).
const mergeQueueKind = "merge-queue"

// directorStatusCmd runs `guildhall director status` (docs/director.md#status).
func (a *App) directorStatusCmd(args []string) int {
	fl := flag.NewFlagSet("director status", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	path := fl.String("store", "", "Director store file")
	state := fl.String("state", "", "state directory")
	cfg := fl.String("config", "", "Director config file")
	asJSON := fl.Bool("json", false, "print JSON")
	if err := fl.Parse(args); err != nil {
		return a.usageError("director status: " + err.Error())
	}
	named := false
	fl.Visit(func(f *flag.Flag) { named = named || f.Name == "config" })
	switch err := cp.apply(a.Getenv, path, cfg, state); {
	case fl.NArg() > 0:
		return a.usageError(fmt.Sprintf("director status: unexpected argument %q", fl.Arg(0)))
	case !*asJSON:
		return a.usageError("director status prints JSON only; pass --json")
	case err != nil:
		return a.usageError("director status: " + err.Error())
	case *path == "" || *state == "":
		return a.usageError("director status: --store and --state are required, or --copy")
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return a.fail(fmt.Errorf("director status: %w", noStore(*path)))
	}
	out, err := readDirectorStatus(context.Background(), *path, *state, a.Now)
	if err != nil {
		return a.fail(fmt.Errorf("director status: %w", err))
	}
	out.Session.GuardRails.GuardRails, out.Quality, out.Session.GuardRails.ConfigError = statusConfig(*cfg, named)
	v := a.versionInfo(context.Background(), *cfg, *state, false)
	out.Version = &v
	return a.printJSON(out)
}

// statusConfig reads the guard rails and quality settings of the config at path: none for no path or a copy's
// config that does not exist, and the reason for a config that cannot give them.
func statusConfig(path string, named bool) (*config.GuardRails, *config.Quality, string) {
	if path == "" {
		return nil, nil, ""
	}
	cfg, err := config.Load(path)
	if errors.Is(err, fs.ErrNotExist) && !named {
		return nil, nil, ""
	}
	if err != nil {
		return nil, nil, err.Error()
	}
	g, err := cfg.GuardRails()
	if err != nil {
		return nil, nil, err.Error()
	}
	q, err := cfg.Quality()
	if err != nil {
		return nil, nil, err.Error()
	}
	return &g, &q, ""
}

// readDirectorStatus builds the status feed: one read-only store transaction, by the clock now (nil is wall time),
// then each task's next.json and the live classification of every running attempt under the state directory.
func readDirectorStatus(ctx context.Context, path, stateDir string, now func() time.Time) (directorStatus, error) {
	stateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return directorStatus{}, err
	}
	var queues []mergeQueue
	var starts []store.QueuedStart
	v, err := withStoreClock(path, true, now, func(s *store.Store) (store.DirectorView, error) {
		v, err := s.DirectorView()
		if err != nil {
			return v, err
		}
		if starts, err = s.StartQueue(); err != nil {
			return v, err
		}
		var repos []string
		for _, t := range v.Tasks {
			if !slices.Contains(repos, t.Repository) {
				repos = append(repos, t.Repository)
			}
		}
		queues, err = mergeQueues(s, repos)
		return v, err
	})
	if err != nil {
		return directorStatus{}, err
	}
	out := directorStatus{
		AsOf:        statusLabel{Partial: true, Excluded: []string{"worker files", "transcripts", "forge state"}, Sequence: v.AsOf.Sequence, CommittedAt: v.AsOf.CommittedAt},
		Session:     statusSession{State: "no session"},
		Mode:        statusMode{Name: "standalone"},
		Tasks:       []statusTask{},
		MergeQueues: queues,
		Decisions:   []statusDecision{},
		Messages:    statusMessages{ByType: map[string]int{}, Headers: []messageHeader{}},
		Recent:      []recentReceipt{},
		NeedsMe:     []needsMeItem{},
		Parked:      []statusParked{},
	}

	usage := statusGuardRails{RunningAttempts: v.Usage.Running, AttemptsToday: v.Usage.Today, Day: v.Usage.Day.Format(time.DateOnly), SpendToday: v.Usage.Spend}
	out.Session.GuardRails, out.Session.LastWake = usage, v.LastWake
	if d := v.Session; d != nil {
		out.Session = statusSession{GuardRails: usage, LastWake: v.LastWake, State: string(d.State), Number: d.Number, Identity: &d.Identity, Release: d.Release,
			Pane: d.Pane, StartedAt: d.StartedAt, ReadyAt: d.ReadyAt, EndedAt: d.EndedAt, StopNow: d.StopNow, Sweeping: d.Sweeping, EndReason: d.EndReason,
			HandoffNote: v.Handoff, Reload: v.Reload}
		// A missing or unreadable report is no report; context refresh belongs to the runtime's reporter.
		if f := d.Handoff.ContextFile; f != "" {
			if c, err := session.ReadContext(f); err == nil {
				out.Session.Context = &c
			}
		}
		if d.State == store.DirectorRecovering {
			out.NeedsMe = append(out.NeedsMe, needsMeItem{Kind: "recovering",
				Reason: fmt.Sprintf("Director session %d is recovering and starts no new work until it is ready", d.Number), Command: "guildhall director ready"})
		}
	}
	out.Session.StartQueue = []statusQueuedStart{}
	for i, e := range starts {
		out.Session.StartQueue = append(out.Session.StartQueue, statusQueuedStart{Repository: e.Repository, Task: e.TaskID, Step: e.Step,
			Attempt: e.Attempt, Request: e.Kind, Priority: e.Priority, Position: i + 1, QueuedAt: e.QueuedAt})
	}
	for _, r := range v.Receipts[max(0, len(v.Receipts)-statusLimit):] {
		out.Recent = append(out.Recent, recentReceipt{MutationID: r.MutationID, Operation: r.Operation, Repository: r.Repository, TaskID: r.TaskID,
			Attempt: r.Attempt, Record: r.Record, Sequence: r.Sequence, CommittedAt: r.CommittedAt})
	}
	// Steps are ordered by their first attempt's start; decisions are timed by their opening and parks by their newest park.
	firstStart, openedAt, parkedAt := map[string]uint64{}, map[string]time.Time{}, map[string]time.Time{}
	for _, r := range v.Receipts {
		key := r.Repository + "/" + r.TaskID + "/"
		if step, _, ok := strings.Cut(strings.TrimPrefix(r.Record, "attempt/"), "/"); ok && strings.HasPrefix(r.Record, "attempt/") {
			if _, seen := firstStart[key+step]; !seen {
				firstStart[key+step] = r.Sequence
			}
		}
		if id, ok := strings.CutPrefix(r.Record, "decision/"); ok && r.Operation == "open-decision" {
			openedAt[key+id] = r.CommittedAt
		}
		if r.Operation == "park-attempt" {
			parkedAt[key+r.Record] = r.CommittedAt
		}
	}
	owners := map[artisan.AttemptKey]*store.Ownership{}
	for i, o := range v.Owners {
		owners[artisan.AttemptKey{Repository: o.Repository, TaskID: o.TaskID, Step: o.Step, Attempt: o.Attempt}] = &v.Owners[i]
	}
	latest, spend := map[string]store.StageAttempt{}, map[string]store.SpendTotal{}
	for _, at := range v.Attempts {
		latest[at.Repository+"/"+at.TaskID+"/"+at.Step] = at // attempts come in number order
		task, ok := spend[at.Repository+"/"+at.TaskID]
		if !ok {
			task = store.NoSpend
		}
		task.AddAttempt(at)
		spend[at.Repository+"/"+at.TaskID] = task
	}

	parks := map[string]store.AttemptPark{}
	for _, p := range v.Parks {
		if p.Parked {
			parks[fmt.Sprintf("%s/%s/%s/%d", p.Repository, p.TaskID, p.Step, p.Attempt)] = p
		}
	}

	sent := map[string]bool{}
	for _, r := range v.Receipts {
		if r.Operation == "send-message" {
			sent[r.Record] = true
		}
	}
	var advance, retry, resume, wait, held, checks, deps, queued []needsMeItem
	for _, t := range v.Tasks {
		st := statusTask{Repository: t.Repository, ID: t.ID, Title: t.Title, State: t.State, Workflow: t.Workflow, Steps: []statusStep{}, Spend: store.NoSpend}
		if total, ok := spend[t.Repository+"/"+t.ID]; ok {
			st.Spend = total
		}
		st.Next, st.NextError = readNext(filepath.Join(stateDir, t.Repository, t.ID, "next.json"))
		prefix := t.Repository + "/" + t.ID + "/"
		var steps []string
		for k := range latest {
			if step, ok := strings.CutPrefix(k, prefix); ok {
				steps = append(steps, step)
			}
		}
		slices.SortFunc(steps, func(a, b string) int {
			if order := byStart(firstStart[prefix+a], firstStart[prefix+b]); order != 0 {
				return order
			}
			return strings.Compare(a, b)
		})
		open := t.State == store.TaskOpen
		var waits struct {
			Wait     interp.Wait
			Step     string
			Attempt  int
			Decision string
		}
		if st.Next != nil {
			_ = json.Unmarshal(st.Next, &waits)
		}
		// A park counts only while its attempt is its open task's step's latest and failed or stopped; a retry clears
		// it by starting the next attempt (docs/decisions/0092-park-attempt.md).
		counting := map[string]store.AttemptPark{}
		for _, step := range steps {
			at := latest[prefix+step]
			p, ok := parks[fmt.Sprintf("%s%s/%d", prefix, step, at.Number)]
			if !ok || !open || (at.Outcome != store.AttemptFailed && at.Outcome != store.AttemptStopped) {
				continue
			}
			counting[step] = p
			var when *time.Time
			if c, ok := parkedAt[prefix+store.AttemptParkRecord(step, at.Number)]; ok {
				when = &c
			}
			out.Parked = append(out.Parked, statusParked{Repository: t.Repository, TaskID: t.ID, Step: step, Attempt: at.Number, Outcome: at.Outcome,
				Reason: p.Reason, ParkedAt: when, Command: fmt.Sprintf("guildhall step retry --repo %s --task %s --step %s --attempt %d", t.Repository, t.ID, step, at.Number)})
		}
		var attempts []store.StageAttempt
		for _, step := range steps {
			if step != workflow.JudgeStep {
				attempts = append(attempts, latest[prefix+step])
				continue
			}
			for _, at := range v.Attempts {
				if at.Repository == t.Repository && at.TaskID == t.ID && at.Step == workflow.JudgeStep {
					attempts = append(attempts, at)
				}
			}
		}
		for _, at := range attempts {
			judge := at.Step == workflow.JudgeStep
			_, parked := counting[at.Step]
			ss := statusStep{Step: at.Step, Attempt: at.Number, Outcome: at.Outcome, Parked: parked}
			item := needsMeItem{Repository: t.Repository, TaskID: t.ID, Step: at.Step, Attempt: at.Number}
			flags := fmt.Sprintf("--repo %s --task %s --step %s --attempt %d", t.Repository, t.ID, at.Step, at.Number)
			key := artisan.AttemptKey{Repository: t.Repository, TaskID: t.ID, Step: at.Step, Attempt: at.Number}
			switch at.Outcome {
			case "":
				c := launch.Classify(ctx, launch.Attempt{Store: path, StateDir: stateDir, Key: key}, owners[key])
				ss.Live = &statusLive{State: c.State, Reason: c.Reason}
				_, launchedErr := os.Stat(filepath.Join(key.Dir(stateDir), "launched.json"))
				if judge && owners[key] == nil && errors.Is(launchedErr, fs.ErrNotExist) {
					item.Kind, item.Retryable = "retry", true
					item.Command = fmt.Sprintf("guildhall quality judge --repo %s %s", t.Repository, t.ID)
					item.Reason = fmt.Sprintf("judge attempt %d was not launched; supply its self-review number with --review to retry", at.Number)
					if review, err := quality.PreparedReview(key.Dir(stateDir)); err == nil && review > 0 {
						item.Command = fmt.Sprintf("guildhall quality judge --repo %s --review %d %s", t.Repository, review, t.ID)
						item.Reason = fmt.Sprintf("judge attempt %d of self-review %d was prepared but not launched", at.Number, review)
					}
					retry = append(retry, item)
				} else if (open || judge) && (c.State == reconcile.Stuck || c.State == reconcile.Exited || c.State == reconcile.Unknown ||
					(judge && (c.State == reconcile.Finished || c.State == reconcile.Failed || c.State == reconcile.Cancelled))) {
					item.Kind, item.Resume, item.Reason, item.Command = "resume", true, string(c.State)+": "+c.Reason, "guildhall artisan resume "+flags
					resume = append(resume, item)
				}
				// No launcher announces a Director stage's result, so one published by a step finish that stopped
				// before its advance waits here until it is taken.
				result := filepath.Join(key.Dir(stateDir), artisan.ResultFile)
				if handed, _ := interp.HandedToDirector(key.Dir(stateDir)); open && handed {
					if _, err := os.Stat(result); err == nil {
						it := item
						it.Kind, it.Reason = "advance", fmt.Sprintf("attempt %d of Director stage %s has a published result that was not taken", at.Number, at.Step)
						it.Command = "guildhall step done " + flags + " --result " + result
						advance = append(advance, it)
					}
				}
			case store.AttemptFailed, store.AttemptStopped:
				if open && !judge && !parked {
					it := item
					it.Kind, it.Retryable, it.Reason, it.Command = "retry", true, fmt.Sprintf("attempt %d of %s %s", at.Number, at.Step, at.Outcome), "guildhall step retry "+flags
					if at.IntakeRejection != "" {
						it.Reason += "; result rejected: " + at.IntakeRejection
					}
					retry = append(retry, it)
				}
			}
			if judge && at.Outcome != "" {
				taken := false
				for _, typ := range []string{store.MessageJudgement, store.MessageJudgeFailed, store.MessageJudgementRefused} {
					taken = taken || sent["message/"+quality.MessageID(t.Repository, t.ID, at.Number, typ)]
				}
				if !taken {
					item.Kind, item.Reason = "advance", fmt.Sprintf("judge attempt %d ended %s and awaits intake", at.Number, at.Outcome)
					item.Command = fmt.Sprintf("guildhall quality judged --repo %s --task %s --attempt %d", t.Repository, t.ID, at.Number)
					advance = append(advance, item)
				}
			}
			// A finished attempt the task's last advance still waits on was not taken, as when its watcher's advance failed.
			if open && !judge && at.Outcome != "" && waits.Wait == interp.WaitAttempt && waits.Step == at.Step && waits.Attempt == at.Number {
				item.Kind, item.Reason = "advance", fmt.Sprintf("attempt %d of %s %s and the task's last advance still waits on it", at.Number, at.Step, at.Outcome)
				item.Command = fmt.Sprintf("guildhall task start --repo %s %s", t.Repository, t.ID)
				result := filepath.Join(key.Dir(stateDir), artisan.ResultFile)
				if _, err := os.Stat(result); err == nil && at.Outcome == store.AttemptSucceeded {
					item.Command = "guildhall step done " + flags + " --result " + result
				}
				advance = append(advance, item)
			}
			if at.Number == latest[prefix+at.Step].Number {
				st.Steps = append(st.Steps, ss)
			}
		}
		if open {
			switch waits.Wait {
			case interp.WaitDirector:
				// A parked attempt's failure is held on purpose; a wait for a decision or another attempt still needs the Director.
				if p, ok := counting[waits.Step]; ok && waits.Attempt == p.Attempt && waits.Decision == "" {
					break
				}
				wait = append(wait, needsMeItem{Kind: "director", Repository: t.Repository, TaskID: t.ID, Reason: "the last advance waits for the Director",
					Command: fmt.Sprintf("guildhall task timeline --repo %s --json %s", t.Repository, t.ID)})
			case interp.WaitPaused:
				item := needsMeItem{Kind: "paused", Repository: t.Repository, TaskID: t.ID, Reason: "the last advance was held while the Director was paused",
					Command: fmt.Sprintf("guildhall task start --repo %s %s", t.Repository, t.ID)}
				if at, ok := latest[prefix+waits.Step]; ok && (at.Outcome == store.AttemptFailed || at.Outcome == store.AttemptStopped) {
					item.Command = fmt.Sprintf("guildhall step retry --repo %s --task %s --step %s --attempt %d", t.Repository, t.ID, at.Step, at.Number)
				}
				held = append(held, item)
			case interp.WaitChecks:
				checks = append(checks, needsMeItem{Kind: checksKind, Repository: t.Repository, TaskID: t.ID, Step: waits.Step,
					Reason:  fmt.Sprintf("step %s waits for the checks of its published head; the session supervisor reads them again every checks_refresh, never more often than every 30 seconds, and wakes you when they finish", waits.Step),
					Command: fmt.Sprintf("guildhall task start --repo %s %s", t.Repository, t.ID)})
			case interp.WaitDependencies:
				deps = append(deps, needsMeItem{Kind: dependenciesKind, Repository: t.Repository, TaskID: t.ID, Step: waits.Step,
					Reason:  fmt.Sprintf("step %s is held until the task's dependencies merge; the session supervisor advances it again every checks_refresh, or start it anyway with --ignore-deps --reason", waits.Step),
					Command: fmt.Sprintf("guildhall task start --repo %s %s", t.Repository, t.ID)})
			case interp.WaitMergeQueue:
				queued = append(queued, needsMeItem{Kind: mergeQueueKind, Repository: t.Repository, TaskID: t.ID, Step: waits.Step,
					Reason:  fmt.Sprintf("step %s is authorized and waits in the merge queue, or its merge is held; the session supervisor advances it again every checks_refresh, and guildhall task hold-merge toggles its hold", waits.Step),
					Command: fmt.Sprintf("guildhall task start --repo %s %s", t.Repository, t.ID)})
			case interp.WaitQueued:
				// A queued start is the start-queue watcher's to drain; only one that left the queue, as when the
				// watcher's advance of it failed, needs the Director.
				if !slices.ContainsFunc(starts, func(e store.QueuedStart) bool { return e.Is(t.Repository, t.ID, waits.Step) }) {
					advance = append(advance, needsMeItem{Kind: "advance", Repository: t.Repository, TaskID: t.ID, Step: waits.Step,
						Reason:  fmt.Sprintf("the last advance queued attempt %d of %s, which is no longer in the start queue; see start-queue.log in the session directory", waits.Attempt, waits.Step),
						Command: fmt.Sprintf("guildhall task start --repo %s %s", t.Repository, t.ID)})
				}
			}
		}
		out.Tasks = append(out.Tasks, st)
	}

	slices.SortFunc(out.Parked, func(a, b statusParked) int {
		return cmp.Or(strings.Compare(a.Repository, b.Repository), strings.Compare(a.TaskID, b.TaskID), strings.Compare(a.Step, b.Step),
			cmp.Compare(a.Attempt, b.Attempt))
	})
	var stalled, decisions []needsMeItem
	for _, d := range v.OpenDecisions {
		var opened *time.Time
		if at, ok := openedAt[d.Repository+"/"+d.TaskID+"/"+d.ID]; ok {
			opened = &at
		}
		out.Decisions = append(out.Decisions, statusDecision{Repository: d.Repository, TaskID: d.TaskID, ID: d.ID, Kind: d.Kind, Subject: d.Subject,
			Options: d.Options, Question: d.Question, OpenedAt: opened})
		decisions = append(decisions, needsMeItem{Kind: decisionKind, Repository: d.Repository, TaskID: d.TaskID, Decision: d.ID, Reason: d.Question,
			Command: fmt.Sprintf("guildhall decide show --repo %s --task %s --decision %s", d.Repository, d.TaskID, d.ID)})
	}
	others := 0
	for _, m := range v.Unread {
		out.Messages.Unread++
		out.Messages.ByType[m.Type]++
		if len(out.Messages.Headers) < statusLimit {
			out.Messages.Headers = append(out.Messages.Headers, messageHeader{ID: m.ID, Type: m.Type, Repository: m.Repository, TaskID: m.TaskID,
				Step: m.Step, Attempt: m.Attempt, Sequence: m.Sequence})
		}
		if m.Type != store.MessageWorkerStalled {
			others++
			continue
		}
		stalled = append(stalled, needsMeItem{Kind: "stalled", Repository: m.Repository, TaskID: m.TaskID, Step: m.Step, Attempt: m.Attempt, Message: m.ID,
			Reason: m.Text, Command: "guildhall messages read " + m.ID})
	}
	slices.SortStableFunc(stalled, func(a, b needsMeItem) int {
		return strings.Compare(a.Repository+"/"+a.TaskID, b.Repository+"/"+b.TaskID)
	})
	out.NeedsMe = slices.Concat(out.NeedsMe, advance, retry, resume, stalled, decisions)
	if others > 0 {
		plural := map[bool]string{true: "s"}[others > 1]
		out.NeedsMe = append(out.NeedsMe, needsMeItem{Kind: "messages", Count: others, Reason: fmt.Sprintf("%d unread Director message%s", others, plural),
			Command: "guildhall messages list director"})
	}
	if out.NeedsMe = slices.Concat(out.NeedsMe, wait, held, checks, deps, queued); out.NeedsMe == nil {
		out.NeedsMe = []needsMeItem{}
	}
	return out, nil
}

// readNext reads a task's next.json as written, or says why it cannot; a missing file is neither.
func readNext(path string) (json.RawMessage, string) {
	b, err := atomicfile.Read(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, ""
	case err != nil:
		return nil, err.Error()
	case !json.Valid(b):
		return nil, "next.json is not JSON"
	}
	return json.RawMessage(b), ""
}

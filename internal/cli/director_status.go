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
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/reconcile"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// statusLimit bounds the message headers and recent receipts the status feed prints.
const statusLimit = 20

// directorStatus is what `director status` prints (docs/director.md#status).
type directorStatus struct {
	AsOf      statusLabel      `json:"as_of"`
	Session   statusSession    `json:"session"`
	Mode      statusMode       `json:"mode"`
	Tasks     []statusTask     `json:"tasks"`
	Decisions []statusDecision `json:"decisions"`
	Messages  statusMessages   `json:"messages"`
	Recent    []recentReceipt  `json:"recent"`
	NeedsMe   []needsMeItem    `json:"needs_me"`
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
	EndReason string                 `json:"end_reason,omitempty"`
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
}

// statusStep is a step's latest attempt: its outcome, or while it runs its live classification.
type statusStep struct {
	Step    string               `json:"step"`
	Attempt int                  `json:"attempt"`
	Outcome store.AttemptOutcome `json:"outcome,omitempty"`
	Live    *statusLive          `json:"live,omitempty"`
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

// directorStatusCmd runs `guildhall director status` (docs/director.md#status).
func (a *App) directorStatusCmd(args []string) int {
	fl := flag.NewFlagSet("director status", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	path := fl.String("store", "", "Director store file")
	state := fl.String("state", "", "state directory")
	asJSON := fl.Bool("json", false, "print JSON")
	if err := fl.Parse(args); err != nil {
		return a.usageError("director status: " + err.Error())
	}
	switch err := cp.apply(a.Getenv, path, nil, state); {
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
	out, err := readDirectorStatus(context.Background(), *path, *state)
	if err != nil {
		return a.fail(fmt.Errorf("director status: %w", err))
	}
	return a.printJSON(out)
}

// readDirectorStatus builds the status feed: one read-only store transaction, then each task's next.json and
// the live classification of every running attempt under the state directory.
func readDirectorStatus(ctx context.Context, path, stateDir string) (directorStatus, error) {
	stateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return directorStatus{}, err
	}
	v, err := withStore(path, true, func(s *store.Store) (store.DirectorView, error) { return s.DirectorView() })
	if err != nil {
		return directorStatus{}, err
	}
	out := directorStatus{
		AsOf:      statusLabel{Partial: true, Excluded: []string{"worker files", "transcripts", "forge state"}, Sequence: v.AsOf.Sequence, CommittedAt: v.AsOf.CommittedAt},
		Session:   statusSession{State: "no session"},
		Mode:      statusMode{Name: "standalone"},
		Tasks:     []statusTask{},
		Decisions: []statusDecision{},
		Messages:  statusMessages{ByType: map[string]int{}, Headers: []messageHeader{}},
		Recent:    []recentReceipt{},
		NeedsMe:   []needsMeItem{},
	}

	if d := v.Session; d != nil {
		out.Session = statusSession{State: string(d.State), Number: d.Number, Identity: &d.Identity, Release: d.Release, Pane: d.Pane,
			StartedAt: d.StartedAt, ReadyAt: d.ReadyAt, EndedAt: d.EndedAt, StopNow: d.StopNow, EndReason: d.EndReason}
		if d.State == store.DirectorRecovering {
			out.NeedsMe = append(out.NeedsMe, needsMeItem{Kind: "recovering",
				Reason: fmt.Sprintf("Director session %d is recovering and starts no new work until it is ready", d.Number), Command: "guildhall director ready"})
		}
	}
	for _, r := range v.Receipts[max(0, len(v.Receipts)-statusLimit):] {
		out.Recent = append(out.Recent, recentReceipt{MutationID: r.MutationID, Operation: r.Operation, Repository: r.Repository, TaskID: r.TaskID,
			Attempt: r.Attempt, Record: r.Record, Sequence: r.Sequence, CommittedAt: r.CommittedAt})
	}
	// Steps are ordered by their first attempt's start; decisions are timed by their opening.
	firstStart, openedAt := map[string]uint64{}, map[string]time.Time{}
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
	}
	owners := map[artisan.AttemptKey]*store.Ownership{}
	for i, o := range v.Owners {
		owners[artisan.AttemptKey{Repository: o.Repository, TaskID: o.TaskID, Step: o.Step, Attempt: o.Attempt}] = &v.Owners[i]
	}
	latest := map[string]store.StageAttempt{}
	for _, at := range v.Attempts {
		latest[at.Repository+"/"+at.TaskID+"/"+at.Step] = at // attempts come in number order
	}

	var retry, resume, wait, held []needsMeItem
	for _, t := range v.Tasks {
		st := statusTask{Repository: t.Repository, ID: t.ID, Title: t.Title, State: t.State, Workflow: t.Workflow, Steps: []statusStep{}}
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
		for _, step := range steps {
			at := latest[prefix+step]
			ss := statusStep{Step: at.Step, Attempt: at.Number, Outcome: at.Outcome}
			item := needsMeItem{Repository: t.Repository, TaskID: t.ID, Step: at.Step, Attempt: at.Number}
			flags := fmt.Sprintf("--repo %s --task %s --step %s --attempt %d", t.Repository, t.ID, at.Step, at.Number)
			switch at.Outcome {
			case "":
				key := artisan.AttemptKey{Repository: t.Repository, TaskID: t.ID, Step: at.Step, Attempt: at.Number}
				c := launch.Classify(ctx, launch.Attempt{Store: path, StateDir: stateDir, Key: key}, owners[key])
				ss.Live = &statusLive{State: c.State, Reason: c.Reason}
				if open && (c.State == reconcile.Stuck || c.State == reconcile.Exited || c.State == reconcile.Unknown) {
					item.Kind, item.Resume, item.Reason, item.Command = "resume", true, string(c.State)+": "+c.Reason, "guildhall artisan resume "+flags
					resume = append(resume, item)
				}
			case store.AttemptFailed, store.AttemptStopped:
				if open {
					item.Kind, item.Retryable, item.Reason, item.Command = "retry", true, fmt.Sprintf("attempt %d of %s %s", at.Number, at.Step, at.Outcome), "guildhall step retry "+flags
					retry = append(retry, item)
				}
			}
			st.Steps = append(st.Steps, ss)
		}
		if open && st.Next != nil {
			var n struct{ Wait interp.Wait }
			switch _ = json.Unmarshal(st.Next, &n); n.Wait {
			case interp.WaitDirector:
				wait = append(wait, needsMeItem{Kind: "director", Repository: t.Repository, TaskID: t.ID, Reason: "the last advance waits for the Director",
					Command: fmt.Sprintf("guildhall task timeline --repo %s --json %s", t.Repository, t.ID)})
			case interp.WaitPaused:
				held = append(held, needsMeItem{Kind: "paused", Repository: t.Repository, TaskID: t.ID, Reason: "the last advance was held while the Director was paused",
					Command: fmt.Sprintf("guildhall task start --repo %s %s", t.Repository, t.ID)})
			}
		}
		out.Tasks = append(out.Tasks, st)
	}

	var stalled, decisions []needsMeItem
	for _, d := range v.OpenDecisions {
		var opened *time.Time
		if at, ok := openedAt[d.Repository+"/"+d.TaskID+"/"+d.ID]; ok {
			opened = &at
		}
		out.Decisions = append(out.Decisions, statusDecision{Repository: d.Repository, TaskID: d.TaskID, ID: d.ID, Kind: d.Kind, Subject: d.Subject,
			Options: d.Options, Question: d.Question, OpenedAt: opened})
		decisions = append(decisions, needsMeItem{Kind: "decision", Repository: d.Repository, TaskID: d.TaskID, Decision: d.ID, Reason: d.Question,
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
		if m.Type != interp.WorkerStalled {
			others++
			continue
		}
		stalled = append(stalled, needsMeItem{Kind: "stalled", Repository: m.Repository, TaskID: m.TaskID, Step: m.Step, Attempt: m.Attempt, Message: m.ID,
			Reason: m.Text, Command: "guildhall messages read " + m.ID})
	}
	slices.SortStableFunc(stalled, func(a, b needsMeItem) int {
		return strings.Compare(a.Repository+"/"+a.TaskID, b.Repository+"/"+b.TaskID)
	})
	out.NeedsMe = slices.Concat(out.NeedsMe, retry, resume, stalled, decisions)
	if others > 0 {
		plural := map[bool]string{true: "s"}[others > 1]
		out.NeedsMe = append(out.NeedsMe, needsMeItem{Kind: "messages", Count: others, Reason: fmt.Sprintf("%d unread Director message%s", others, plural),
			Command: "guildhall messages list director"})
	}
	if out.NeedsMe = slices.Concat(out.NeedsMe, wait, held); out.NeedsMe == nil {
		out.NeedsMe = []needsMeItem{}
	}
	return out, nil
}

// readNext reads a task's next.json as written, or says why it cannot; a missing file is neither.
func readNext(path string) (json.RawMessage, string) {
	b, err := os.ReadFile(path)
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

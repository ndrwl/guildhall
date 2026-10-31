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
	"github.com/ndrwl/guildhall/internal/backup"
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
	AsOf statusLabel `json:"as_of"`
	// Copy is the absolute copy directory, when the feed was read from a copy rather than explicit paths.
	Copy    string        `json:"copy,omitempty"`
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
	// Held are the decision deferrals and follow-ups still waiting for the tasks they name to merge, left out of
	// needs_me until they are released (docs/decisions/hold-until-merged.md).
	Held []statusHeld `json:"held"`
	// Running are the attempts with no store outcome yet and how each classifies, for information and not work: one
	// that is not healthy is also a needs_me item (docs/director.md#status).
	Running []statusRunning `json:"running"`
	// Grants are the active task grants, extra allowed tools the Guildmaster approved for one task
	// (docs/decisions/task-grants.md); one leaves the list once it expires or is revoked, with no write.
	Grants []statusGrant `json:"grants"`
	// Maintenance is when Director maintenance was last run and is next due (docs/director.md#maintenance), counts
	// and times only; absent until a session started with a maintenance policy.
	Maintenance *statusMaintenance `json:"maintenance,omitempty"`
	// RepositoryPolicies is each configured repository's policies as last fetched, read with no fetch
	// (docs/configuration.md#repository-policies); absent when no repository has a policy file and none has an
	// error. They are guidance, not guard rails: nothing enforces them. The session supervisor's own snapshot
	// leaves them out.
	RepositoryPolicies []statusPolicies `json:"repository_policies,omitempty"`
	// Version is the binary printing the feed, from its build info and local git reads only
	// (docs/contracts/cli.md#version); the session supervisor's own snapshot leaves it out.
	Version *versionInfo `json:"version,omitempty"`
	// SupervisorBinary compares the newest session's supervisor binary with the guildhall on PATH, for a feed
	// with a session; the supervisor's own snapshot leaves it out.
	SupervisorBinary *supervisorBinaryStatus `json:"supervisor_binary,omitempty"`
	// Web is where the task pages are served and whether the running session serves them: "not configured" with no
	// [web] table, else its url and "serving", "failed to bind" with the reason, or "not serving" (docs/web.md);
	// absent when status reads no config.
	Web *webStatus `json:"web,omitempty"`

	// released are the released holds that still matter, which the holds watcher wakes the Director for once each.
	released []releasedHold
}

// supervisorBinaryStatus is the newest session's supervisor binary beside the guildhall binary on PATH: their
// sha256 ("" when not recorded or not hashed), whether a reload would leave the supervisor on another binary than
// the one on PATH, and a line saying so.
type supervisorBinaryStatus struct {
	SHA256     string `json:"sha256"`
	Path       string `json:"path"`
	PathSHA256 string `json:"path_sha256"`
	Differs    bool   `json:"differs"`
	Summary    string `json:"summary"`
}

// supervisorBinaryStatus compares session d's supervisor binary with the binary a reload would move it onto: the
// copy's pinned release once copyDir has adopted releases, else the guildhall on PATH (targetBinary).
func (a *App) supervisorBinaryStatus(d statusSession, copyDir string) *supervisorBinaryStatus {
	if d.Number == 0 {
		return nil
	}
	out := &supervisorBinaryStatus{SHA256: d.BinarySHA256}
	bin, pinned, err := a.targetBinary(copyDir)
	out.Path, out.PathSHA256 = bin.Path, bin.SHA256
	target := targetName(pinned)
	switch {
	case err != nil:
		out.Summary = fmt.Sprintf("cannot compare session %d's supervisor binary with %s: %s", d.Number, target, oneLine(err.Error()))
	case d.BinarySHA256 == "":
		// A supervisor from before reloads moved binaries records no sha256, and a reload never moves it.
		out.Differs = true
		out.Summary = fmt.Sprintf("session %d's supervisor predates moving binaries at a reload, so a director reload keeps it on its own binary; "+
			"run director stop, then director start, to move it onto %s", d.Number, target)
	case d.BinarySHA256 != bin.SHA256:
		out.Differs = true
		out.Summary = fmt.Sprintf("session %d's supervisor runs guildhall %s, not %s %s %s; a director reload moves it onto that one",
			d.Number, shortSHA(d.BinarySHA256), target, bin.Path, shortSHA(bin.SHA256))
	default:
		out.Summary = fmt.Sprintf("session %d's supervisor runs %s (%s)", d.Number, target, shortSHA(bin.SHA256))
	}
	return out
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
	State    string                 `json:"state"`
	Number   int                    `json:"number,omitempty"`
	Identity *store.ProcessIdentity `json:"identity,omitempty"`
	Release  string                 `json:"release,omitempty"`
	// BinarySHA256 is the sha256 of the binary the session's supervisor runs, as it recorded it.
	BinarySHA256 string             `json:"binary_sha256,omitempty"`
	Pane         store.DirectorPane `json:"pane,omitzero"`
	StartedAt    time.Time          `json:"started_at,omitzero"`
	ReadyAt      time.Time          `json:"ready_at,omitzero"`
	EndedAt      time.Time          `json:"ended_at,omitzero"`
	StopNow      bool               `json:"stop_now,omitempty"`
	Sweeping     bool               `json:"sweeping,omitempty"`
	EndReason    string             `json:"end_reason,omitempty"`
	// LastWake is the newest wake of any message to the Director, so whether it woke anyone and why not.
	LastWake *store.Wake `json:"last_wake,omitempty"`
	// GuardRails are the copy's guard rails and what the attempt caps count today (docs/director.md#guard-rails).
	GuardRails statusGuardRails `json:"guard_rails"`
	// StartQueue is every start the running cap queued, in queue order (docs/director.md#the-start-queue); the
	// supervisor's start-queue watcher drains it, so it is not in needs_me.
	StartQueue []statusQueuedStart `json:"start_queue"`
	// Reload is the newest reload request, whichever session recorded it.
	Reload *store.DirectorReload `json:"reload,omitempty"`
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

// statusMode is the copy's mode (docs/director-modes.md#mode).
type statusMode struct {
	// Name is the mode the configuration-admission boundary finds: standalone, managed or refused.
	Name string `json:"name"`
}

// statusModeName is the mode name of the copy whose config is at cfgPath, or whose store is at storePath when no
// config is named, with managed facts v read from its store. A config that cannot be read leaves the mode to the
// store and managed.toml alone.
func statusModeName(cfgPath, storePath string, v store.ManagedView) string {
	if cfgPath == "" {
		cfgPath = filepath.Join(filepath.Dir(storePath), "config.toml")
	}
	f, err := readCopyFiles(cfgPath)
	if err != nil {
		f = copyFiles{CfgPath: cfgPath}
		if b, err := os.ReadFile(filepath.Join(f.Dir(), managedFile)); err == nil {
			f.Managed = b
		}
	}
	return judge(f, v).Mode
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
	// Overrides marks a task with a task override in effect; task show lists them (docs/tasks.md#task-overrides).
	Overrides bool `json:"overrides,omitempty"`
	// Slices is a multi-PR task's progress through its accepted spec's slices; omitted for a one-slice task
	// (docs/decisions/multi-pr-tasks.md).
	Slices *statusSlices `json:"slices,omitempty"`
}

// statusSlices is where a multi-PR task stands: the slice being delivered, the total and each slice.
type statusSlices struct {
	Current int           `json:"current"`
	Total   int           `json:"total"`
	Items   []statusSlice `json:"items"`
}

// statusSlice is one slice: its title, whether it merged, is current or is still to come, and its pull request.
type statusSlice struct {
	Index       int                `json:"index"`
	Title       string             `json:"title"`
	State       string             `json:"state"`
	PullRequest *statusPullRequest `json:"pull_request,omitempty"`
}

// statusPullRequest is a slice's pull request as Guildhall last recorded it.
type statusPullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// readTaskSlices reads the slice progress of each task of v whose bound workflow declares a slice range and whose
// accepted spec lists more than one slice, by "<repo>/<task>"; a task whose workflow, records or documents do not read
// is left out, as a one-slice task is.
func readTaskSlices(s *store.Store, v store.DirectorView, documents string, workflows func(repo string) func(workflow.Revision) (*workflow.Definition, error)) map[string]*statusSlices {
	out := map[string]*statusSlices{}
	for _, t := range v.Tasks {
		def, err := workflows(t.Repository)(t.Workflow)
		if err != nil || def.Slices == nil {
			continue
		}
		docs, err := s.Documents(t.Repository, t.ID)
		if err != nil {
			continue
		}
		decisions, err := s.Decisions(t.Repository, t.ID)
		if err != nil {
			continue
		}
		p, err := interp.TaskSlices(def, t.Repository, t.ID, docs, decisions, documents)
		if err != nil || p.Total < 2 {
			continue
		}
		sl := &statusSlices{Current: p.Current, Total: p.Total, Items: []statusSlice{}}
		for _, it := range p.Items {
			item := statusSlice{Index: it.Index, Title: it.Title, State: it.State}
			if pr := it.PullRequest; pr != nil {
				item.PullRequest = &statusPullRequest{Number: pr.Number, URL: pr.URL}
			}
			sl.Items = append(sl.Items, item)
		}
		out[t.Repository+"/"+t.ID] = sl
	}
	return out
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
	// UntilMerged are the tasks a held park waits to merge, and WaitingFor those of them not merged yet.
	UntilMerged []string `json:"until_merged,omitempty"`
	WaitingFor  []string `json:"waiting_for,omitempty"`
}

// statusHeld is a waiting hold other than a park: a deferred decision or a follow-up, with what it waits for and
// the command that lifts it.
type statusHeld struct {
	Kind       string `json:"kind"`
	Repository string `json:"repository,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	// Item is the decision id or the follow-up id.
	Item string `json:"item"`
	// Reason is a deferral's reason or a follow-up's text.
	Reason      string   `json:"reason"`
	UntilMerged []string `json:"until_merged"`
	WaitingFor  []string `json:"waiting_for"`
	Command     string   `json:"command"`
}

// statusGrant is an active task grant: the rules its task's attempts of its step are given, why and for how long,
// the attempts bound with it so far, and when the Guildmaster's approval was recorded.
type statusGrant struct {
	Repository string           `json:"repository"`
	TaskID     string           `json:"task_id"`
	Grant      string           `json:"grant"`
	Step       string           `json:"step"`
	Rules      []string         `json:"rules"`
	Reason     string           `json:"reason"`
	Until      store.GrantUntil `json:"until"`
	Bound      []store.GrantUse `json:"bound"`
	ApprovedAt *time.Time       `json:"approved_at,omitempty"`
}

// statusRunning is an attempt with no store outcome yet, other than a Director stage, with the classification
// `director status` made of it: what `guildhall artisan status` would say, and the command to ask again.
type statusRunning struct {
	Repository string          `json:"repository"`
	TaskID     string          `json:"task_id"`
	Step       string          `json:"step"`
	Attempt    int             `json:"attempt"`
	State      reconcile.State `json:"state"`
	Reason     string          `json:"reason"`
	Launcher   string          `json:"launcher,omitempty"`
	Endpoint   string          `json:"endpoint,omitempty"`
	// WorkerState, HeartbeatAt and LastOutputAt are from the attempt's last heartbeat, absent when it has none.
	WorkerState  string    `json:"worker_state,omitempty"`
	HeartbeatAt  time.Time `json:"heartbeat_at,omitzero"`
	LastOutputAt time.Time `json:"last_output_at,omitzero"`
	Command      string    `json:"command"`
}

// statusMaintenance is the maintenance counts under the newest session's policy, from the baseline session on.
type statusMaintenance struct {
	BaselineSession int              `json:"baseline_session"`
	NoteAudit       statusNoteAudit  `json:"note_audit"`
	Hallmarks       []statusHallmark `json:"hallmarks"`
}

// statusNoteAudit is the last note audit and the sessions until the next; SessionsUntilDue is absent when the note
// audit is off.
type statusNoteAudit struct {
	EverySessions    int       `json:"every_sessions"`
	LastSession      int       `json:"last_session,omitempty"`
	LastAt           time.Time `json:"last_at,omitzero"`
	SessionsSince    int       `json:"sessions_since"`
	SessionsUntilDue *int      `json:"sessions_until_due,omitempty"`
}

// statusHallmark is one repository's last hallmark and the merges since; MergesUntilDue is absent when hallmarks
// are off, and RunningTask names the repository's open hallmark task, absent when none is open. A hallmark waiting
// on its follow-ups decision is still open, so it is the RunningTask.
type statusHallmark struct {
	Repository     string    `json:"repository"`
	AfterMerges    int       `json:"after_merges"`
	LastTask       string    `json:"last_task,omitempty"`
	LastAt         time.Time `json:"last_at,omitzero"`
	MergedSince    int       `json:"merged_since"`
	MergesUntilDue *int      `json:"merges_until_due,omitempty"`
	RunningTask    string    `json:"running_task,omitempty"`
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
	// Deferred says the Director deferred the decision until tasks merge and they have not, which is why it is not in
	// needs_me; the held section says what it waits for.
	Deferred bool `json:"deferred,omitempty"`
}

type statusMessages struct {
	Unread  int             `json:"unread"`
	ByType  map[string]int  `json:"by_type"`
	Headers []messageHeader `json:"headers"`
}

type messageHeader struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Sender     string `json:"sender"`
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
	FollowUp   string `json:"follow_up,omitempty"`
	Message    string `json:"message,omitempty"`
	Count      int    `json:"count,omitempty"`
	Retryable  bool   `json:"retryable,omitempty"`
	Resume     bool   `json:"resume,omitempty"`
	Reason     string `json:"reason"`
	Command    string `json:"command"`
}

// noPaneKind is the needs_me kind of a recovering or running Director session with no Herdr pane, which no wake
// reaches (docs/director.md#start).
const noPaneKind = "no_pane"

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

// followUpKind is the needs_me kind and held kind of a follow-up (docs/decisions/hold-until-merged.md).
const followUpKind = "follow-up"

// maintenanceKind is the needs_me kind of a due note audit or hallmark, which the Director works after recovery
// (docs/director.md#maintenance).
const maintenanceKind = "maintenance"

// installKind is the needs_me kind of a binary behind its repository's default branch, which waits on the
// Guildmaster to install it; director status adds it from its own version block, so the supervisor's snapshot
// leaves it out.
const installKind = "install"

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
	switch err := cp.apply(a, path, cfg, state); {
	case fl.NArg() > 0:
		return a.usageError(fmt.Sprintf("director status: unexpected argument %q", fl.Arg(0)))
	case !*asJSON:
		return a.usageError("director status prints JSON only; pass --json")
	case err != nil:
		return a.usageError("director status: " + err.Error())
	case *path == "" && *state == "" && *cfg == "":
		return a.usageError("director status: " + cp.missing(a).Error())
	case *path == "" || *state == "":
		return a.usageError("director status: --store and --state are required, or --copy")
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return a.fail(fmt.Errorf("director status: %w", noStore(*path)))
	}
	out, err := readDirectorStatusConfig(context.Background(), *path, *state, *cfg, a.Now)
	if err != nil {
		return a.fail(fmt.Errorf("director status: %w", err))
	}
	if cp.dir != "" {
		if out.Copy, err = filepath.Abs(cp.dir); err != nil {
			return a.fail(fmt.Errorf("director status: %w", err))
		}
	}
	out.Session.GuardRails.GuardRails, out.Quality, out.Session.GuardRails.ConfigError = statusConfig(*cfg, named)
	if *cfg != "" {
		// A config that cannot be read gives no policies; the guard rails already say why.
		if c, err := reportedConfig(*cfg); err == nil {
			out.RepositoryPolicies = feedPolicies(a.readPolicies(context.Background(), c.Repositories...))
		}
	}
	v := a.versionInfo(context.Background(), *cfg, *state, false)
	out.Version = &v
	if it, ok := v.installItem(); ok {
		out.NeedsMe = append(out.NeedsMe, it)
	}
	out.SupervisorBinary = a.supervisorBinaryStatus(out.Session, out.Copy)
	out.Web = readWebStatus(*cfg, *state, out.Session)
	return a.printJSON(out)
}

// statusConfig reads the guard rails and quality settings of the config at path: none for no path or a copy's
// config that does not exist, and the reason for a config that cannot give them.
func statusConfig(path string, named bool) (*config.GuardRails, *config.Quality, string) {
	if path == "" {
		return nil, nil, ""
	}
	cfg, err := reportedConfig(path)
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

// readDirectorStatus is readDirectorStatusConfig with no config, so hallmark tasks resolve against the shipped
// workflows only.
func readDirectorStatus(ctx context.Context, path, stateDir string, now func() time.Time) (directorStatus, error) {
	return readDirectorStatusConfig(ctx, path, stateDir, "", now)
}

// readDirectorStatusConfig builds the status feed: one read-only store transaction, by the clock now (nil is wall
// time), then each task's next.json and the live classification of every running attempt under the state directory.
// Each repository's hallmark tasks resolve their workflows under the config at cfgPath (statusWorkflows).
func readDirectorStatusConfig(ctx context.Context, path, stateDir, cfgPath string, now func() time.Time) (directorStatus, error) {
	return readDirectorStatusRecords(ctx, storeRecords{path: path}, stateDir, cfgPath, now)
}

// readDirectorStatusRecords is readDirectorStatusConfig waiting p's wait for the store's lock.
func readDirectorStatusRecords(ctx context.Context, p storeRecords, stateDir, cfgPath string, now func() time.Time) (directorStatus, error) {
	path := p.path
	stateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return directorStatus{}, err
	}
	var queues []mergeQueue
	var starts []store.QueuedStart
	var hallmarks map[string]repoHallmarks
	var taskSlices map[string]*statusSlices
	var managed store.ManagedView
	v, err := withRecords(p, true, now, func(s *store.Store) (store.DirectorView, error) {
		v, err := s.DirectorView()
		if err != nil {
			return v, err
		}
		if managed, err = s.ManagedView(); err != nil {
			return v, err
		}
		if starts, err = s.StartQueue(); err != nil {
			return v, err
		}
		workflows := statusWorkflows(cfgPath)
		if hallmarks, err = readRepoHallmarks(s, v, workflows); err != nil {
			return v, err
		}
		taskSlices = readTaskSlices(s, v, filepath.Join(stateDir, "documents"), workflows)
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
		Mode:        statusMode{Name: statusModeName(cfgPath, path, managed)},
		Tasks:       []statusTask{},
		MergeQueues: queues,
		Decisions:   []statusDecision{},
		Messages:    statusMessages{ByType: map[string]int{}, Headers: []messageHeader{}},
		Recent:      []recentReceipt{},
		NeedsMe:     []needsMeItem{},
		Parked:      []statusParked{},
		Held:        []statusHeld{},
		Running:     []statusRunning{},
		Grants:      []statusGrant{},
	}
	for _, g := range v.Grants {
		out.Grants = append(out.Grants, statusGrant{Repository: g.Repository, TaskID: g.TaskID, Grant: g.ID, Step: g.Step, Rules: g.Rules,
			Reason: g.Reason, Until: g.Until, Bound: append([]store.GrantUse{}, g.Bound...), ApprovedAt: g.ApprovedAt})
	}

	usage := statusGuardRails{RunningAttempts: v.Usage.Running, AttemptsToday: v.Usage.Today, Day: v.Usage.Day.Format(time.DateOnly), SpendToday: v.Usage.Spend}
	out.Session.GuardRails, out.Session.LastWake = usage, v.LastWake
	if d := v.Session; d != nil {
		out.Session = statusSession{GuardRails: usage, LastWake: v.LastWake, State: string(d.State), Number: d.Number, Identity: &d.Identity, Release: d.Release,
			BinarySHA256: d.BinarySHA256, Pane: d.Pane, StartedAt: d.StartedAt, ReadyAt: d.ReadyAt, EndedAt: d.EndedAt, StopNow: d.StopNow, Sweeping: d.Sweeping, EndReason: d.EndReason,
			Reload: v.Reload}
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
		// Every wake to a session with no pane stops at "no pane" before any waiter is tried.
		if (d.State == store.DirectorRecovering || d.State == store.DirectorRunning) && d.Pane == (store.DirectorPane{}) {
			out.NeedsMe = append(out.NeedsMe, needsMeItem{Kind: noPaneKind,
				Reason:  fmt.Sprintf("Director session %d has no Herdr pane, so no wake can reach it; messages wait until its next turn; stop this session first, then start it in a Herdr tab", d.Number),
				Command: "guildhall director start --tab"})
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
	// A hold's release names when each task it waited for merged, by the task's state receipt.
	closedAt := map[store.TaskRef]time.Time{}
	for _, r := range v.Receipts {
		if r.Operation == "set-task-state" {
			closedAt[store.TaskRef{Repository: r.Repository, ID: r.TaskID}] = r.CommittedAt
		}
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
		st := statusTask{Repository: t.Repository, ID: t.ID, Title: t.Title, State: t.State, Workflow: t.Workflow, Steps: []statusStep{}, Spend: store.NoSpend,
			Slices: taskSlices[t.Repository+"/"+t.ID]}
		if total, ok := spend[t.Repository+"/"+t.ID]; ok {
			st.Spend = total
		}
		st.Overrides = slices.ContainsFunc(v.Overrides, func(o store.TaskOverride) bool { return o.Repository == t.Repository && o.TaskID == t.ID })
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
		// it by starting the next attempt (docs/decisions/park-attempt.md).
		// A park held until tasks merge stops counting once they have, so its retry item returns with the release
		// (docs/decisions/hold-until-merged.md).
		counting, releasedPark := map[string]store.AttemptPark{}, map[string]string{}
		for _, step := range steps {
			at := latest[prefix+step]
			p, ok := parks[fmt.Sprintf("%s%s/%d", prefix, step, at.Number)]
			if !ok || !open || (at.Outcome != store.AttemptFailed && at.Outcome != store.AttemptStopped) {
				continue
			}
			sp := statusParked{Repository: t.Repository, TaskID: t.ID, Step: step, Attempt: at.Number, Outcome: at.Outcome,
				Reason: p.Reason, Command: fmt.Sprintf("guildhall step retry --repo %s --task %s --step %s --attempt %d", t.Repository, t.ID, step, at.Number)}
			if len(p.UntilMerged) > 0 {
				j := store.JudgeHold(p.UntilMerged, v.Tasks)
				if j.State != store.HoldWaiting {
					release := holdRelease(j, p.UntilMerged, closedAt)
					releasedPark[step] = release + "; park reason: " + p.Reason
					out.released = append(out.released, releasedHold{Kind: "park", Repository: t.Repository, TaskID: t.ID, Step: step, Attempt: at.Number,
						Item: fmt.Sprintf("%s.%d", step, at.Number), Cycle: p.Cycle, Release: release, Reason: p.Reason, Command: sp.Command})
					continue
				}
				sp.UntilMerged, sp.WaitingFor = refStrings(p.UntilMerged), refStrings(j.Waiting)
			}
			counting[step] = p
			if c, ok := parkedAt[prefix+store.AttemptParkRecord(step, at.Number)]; ok {
				sp.ParkedAt = &c
			}
			out.Parked = append(out.Parked, sp)
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
				c := launch.ClassifyAt(ctx, launch.Attempt{Store: path, StateDir: stateDir, Key: key}, owners[key], now)
				ss.Live = &statusLive{State: c.State, Reason: c.Reason}
				handed, _ := interp.HandedToDirector(key.Dir(stateDir))
				// A Director stage runs in the Director's own session, so it stays quiet here as in needs_me.
				if c.State != reconcile.Handed && !handed {
					r := statusRunning{Repository: t.Repository, TaskID: t.ID, Step: at.Step, Attempt: at.Number, State: c.State, Reason: c.Reason,
						Launcher: c.Launcher, Endpoint: c.Endpoint, Command: "guildhall artisan status " + flags}
					if h := c.Heartbeat; h != nil {
						r.WorkerState, r.HeartbeatAt, r.LastOutputAt = h.WorkerState, h.Time, h.LastOutputAt
					}
					out.Running = append(out.Running, r)
				}
				settled := c.State == reconcile.Finished || c.State == reconcile.Failed || c.State == reconcile.Cancelled
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
					// Files that settled an attempt the store did not, as when its launcher died before recording
					// the outcome, are settled by artisan resume; a Director stage's published result is an advance.
					(settled && !handed)) {
					item.Kind, item.Resume, item.Reason, item.Command = "resume", true, string(c.State)+": "+c.Reason, "guildhall artisan resume "+flags
					resume = append(resume, item)
				}
				// No launcher announces a Director stage's result, so one published by a step finish that stopped
				// before its advance waits here until it is taken.
				result := filepath.Join(key.Dir(stateDir), artisan.ResultFile)
				if open && handed {
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
					if release, ok := releasedPark[at.Step]; ok {
						it.Reason += "; " + release
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
	slices.SortFunc(out.Running, func(a, b statusRunning) int {
		return cmp.Or(strings.Compare(a.Repository, b.Repository), strings.Compare(a.TaskID, b.TaskID), strings.Compare(a.Step, b.Step),
			cmp.Compare(a.Attempt, b.Attempt))
	})
	var stalled, decisions, follows []needsMeItem
	deferrals := map[string]store.DecisionDeferral{}
	for _, d := range v.Deferrals {
		if d.Deferred {
			deferrals[d.Repository+"/"+d.TaskID+"/"+d.Decision] = d
		}
	}
	for _, d := range v.OpenDecisions {
		var opened *time.Time
		if at, ok := openedAt[d.Repository+"/"+d.TaskID+"/"+d.ID]; ok {
			opened = &at
		}
		sd := statusDecision{Repository: d.Repository, TaskID: d.TaskID, ID: d.ID, Kind: d.Kind, Subject: d.Subject,
			Options: d.Options, Question: d.Question, OpenedAt: opened}
		item := needsMeItem{Kind: decisionKind, Repository: d.Repository, TaskID: d.TaskID, Decision: d.ID, Reason: d.Question,
			Command: fmt.Sprintf("guildhall decide show --repo %s --task %s --decision %s", d.Repository, d.TaskID, d.ID)}
		// A deferred decision stays open but leaves needs_me until the tasks it waits for merge.
		if df, ok := deferrals[d.Repository+"/"+d.TaskID+"/"+d.ID]; ok {
			j := store.JudgeHold(df.UntilMerged, v.Tasks)
			if j.State == store.HoldWaiting {
				sd.Deferred = true
				out.Decisions = append(out.Decisions, sd)
				out.Held = append(out.Held, statusHeld{Kind: decisionKind, Repository: d.Repository, TaskID: d.TaskID, Item: d.ID, Reason: df.Reason,
					UntilMerged: refStrings(df.UntilMerged), WaitingFor: refStrings(j.Waiting),
					Command: fmt.Sprintf("guildhall decide undefer --repo %s --task %s --decision %s", d.Repository, d.TaskID, d.ID)})
				continue
			}
			release := holdRelease(j, df.UntilMerged, closedAt)
			item.Reason += "; " + release + "; deferral reason: " + df.Reason
			out.released = append(out.released, releasedHold{Kind: decisionKind, Repository: d.Repository, TaskID: d.TaskID, Item: d.ID, Cycle: df.Cycle,
				Release: release, Reason: df.Reason, Command: item.Command})
		}
		out.Decisions = append(out.Decisions, sd)
		decisions = append(decisions, item)
	}
	for _, f := range v.FollowUps {
		j := store.JudgeHold(f.UntilMerged, v.Tasks)
		command := "guildhall director follow-up done " + f.ID
		if j.State == store.HoldWaiting {
			out.Held = append(out.Held, statusHeld{Kind: followUpKind, Item: f.ID, Reason: f.Text, UntilMerged: refStrings(f.UntilMerged),
				WaitingFor: refStrings(j.Waiting), Command: command})
			continue
		}
		release := holdRelease(j, f.UntilMerged, closedAt)
		follows = append(follows, needsMeItem{Kind: followUpKind, FollowUp: f.ID, Reason: f.Text + "; " + release, Command: command})
		// A follow-up names no task of its own, so its wake is filed under the first task it waited for.
		first := f.UntilMerged[0]
		out.released = append(out.released, releasedHold{Kind: followUpKind, Repository: first.Repository, TaskID: first.ID, Item: f.ID,
			Release: release, Reason: f.Text, Command: command})
	}
	slices.SortStableFunc(out.Held, func(a, b statusHeld) int {
		order := cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.Repository, b.Repository), strings.Compare(a.TaskID, b.TaskID))
		if order != 0 || a.Kind == followUpKind {
			// Follow-ups keep their id order.
			return order
		}
		return strings.Compare(a.Item, b.Item)
	})
	others := 0
	for _, m := range v.Unread {
		out.Messages.Unread++
		out.Messages.ByType[m.Type]++
		if len(out.Messages.Headers) < statusLimit {
			out.Messages.Headers = append(out.Messages.Headers, messageHeader{ID: m.ID, Type: m.Type, Sender: store.MessageSender(m), Repository: m.Repository, TaskID: m.TaskID,
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
	out.NeedsMe = slices.Concat(out.NeedsMe, advance, retry, resume, stalled, decisions, follows)
	if others > 0 {
		plural := map[bool]string{true: "s"}[others > 1]
		out.NeedsMe = append(out.NeedsMe, needsMeItem{Kind: "messages", Count: others, Reason: fmt.Sprintf("%d unread Director message%s", others, plural),
			Command: "guildhall messages list director"})
	}
	var due []needsMeItem
	out.Maintenance, due = maintenanceStatus(v, hallmarks)
	if out.NeedsMe = slices.Concat(out.NeedsMe, wait, held, checks, deps, queued, due, backupNeeds(stateDir)); out.NeedsMe == nil {
		out.NeedsMe = []needsMeItem{}
	}
	return out, nil
}

// backupKind is the needs_me kind of the copy's last backup, when it failed or left a file out (docs/running.md#backups).
const backupKind = "backup"

// backupNeeds is the needs_me item of the copy's last backup, read from its stamp in stateDir: one when the last run
// failed, naming its error, or when its size guard left files out, naming them; none for a copy that never backed up
// or whose last backup was whole. A store message names a task, and a backup belongs to no task, so the copy's
// status carries it instead.
func backupNeeds(stateDir string) []needsMeItem {
	stamp, err := backup.ReadStamp(stateDir)
	if err != nil || stamp.RanAt.IsZero() {
		return nil
	}
	var reasons []string
	if !stamp.OK {
		reasons = append(reasons, fmt.Sprintf("the backup to %s at %s failed: %s", stamp.Branch, stamp.RanAt.Format(time.RFC3339), oneLine(stamp.Error)))
	}
	if len(stamp.LeftOut) > 0 {
		var names []string
		for _, l := range stamp.LeftOut {
			names = append(names, l.Path)
		}
		reasons = append(reasons, fmt.Sprintf("the backup left out %s, over the size limit", strings.Join(names, ", ")))
	}
	if len(reasons) == 0 {
		return nil
	}
	return []needsMeItem{{Kind: backupKind, Reason: strings.Join(reasons, "; "), Command: "guildhall backup status"}}
}

// repoHallmarks is a repository's last hallmark and its running one, each nil when there is none; followUpsAnswered
// is the sequence of the last hallmark's follow-ups answer receipt, 0 when it was not answered.
type repoHallmarks struct {
	last              *interp.Hallmark
	running           *store.Task
	followUpsAnswered uint64
}

// readRepoHallmarks reads, in s, the last and running hallmark of each repository v counts maintenance for, with
// the workflow resolver workflows gives for the repository; none before a session started with a policy.
func readRepoHallmarks(s *store.Store, v store.DirectorView, workflows func(repo string) func(workflow.Revision) (*workflow.Definition, error)) (map[string]repoHallmarks, error) {
	if v.Maintenance.Baseline == 0 || v.Session == nil {
		return nil, nil
	}
	closes, err := s.TaskCloses()
	if err != nil {
		return nil, err
	}
	type taskKey struct{ repo, task string }
	answered := map[taskKey]uint64{}
	for _, rc := range v.Receipts {
		if rc.Operation == "answer-decision" && rc.Record == "decision/"+interp.FollowUpsDecision {
			answered[taskKey{rc.Repository, rc.TaskID}] = rc.Sequence
		}
	}
	out := map[string]repoHallmarks{}
	for _, r := range v.Maintenance.Repositories {
		var tasks []store.Task
		for _, t := range v.Tasks {
			if t.Repository == r.Repository {
				tasks = append(tasks, t)
			}
		}
		resolve := workflows(r.Repository)
		last, err := interp.LastHallmark(tasks, closes, s.Documents, resolve, "")
		if err != nil {
			return nil, err
		}
		h := repoHallmarks{last: last, running: interp.OpenHallmark(tasks, closes, resolve)}
		if last != nil {
			h.followUpsAnswered = answered[taskKey{last.Task.Repository, last.Task.ID}]
		}
		out[r.Repository] = h
	}
	return out, nil
}

// statusWorkflows returns, for a repository, the resolver of the workflows its tasks bound, which remembers each
// revision it resolved: boundWorkflow with the repository's settings in the config at cfgPath when that reads and
// names it, and otherwise shippedWorkflow (docs/decisions/engine-owned-hallmark-cadence.md).
func statusWorkflows(cfgPath string) func(repo string) func(workflow.Revision) (*workflow.Definition, error) {
	var cfg *config.Config
	if cfgPath != "" {
		if c, err := reportedConfig(cfgPath); err == nil {
			cfg = c
		}
	}
	return func(repo string) func(workflow.Revision) (*workflow.Definition, error) {
		resolve := shippedWorkflow
		if cfg != nil {
			if r, ok := cfg.Repository(repo); ok {
				dir := filepath.Join(filepath.Dir(cfgPath), "workflows")
				resolve = func(rev workflow.Revision) (*workflow.Definition, error) { return boundWorkflow(dir, cfg, r, rev) }
			}
		}
		type resolved struct {
			def *workflow.Definition
			err error
		}
		seen := map[workflow.Revision]resolved{}
		return func(rev workflow.Revision) (*workflow.Definition, error) {
			if r, ok := seen[rev]; ok {
				return r.def, r.err
			}
			def, err := resolve(rev)
			seen[rev] = resolved{def, err}
			return def, err
		}
	}
}

// shippedWorkflow resolves rev, with no config, to the definition this or an earlier release shipped whose digest,
// or legacy defaulted digest, is rev's; a revision of a Director-local workflow, or of a shipped one with workflow
// overrides, does not resolve.
func shippedWorkflow(rev workflow.Revision) (*workflow.Definition, error) {
	shipped, err := shippedDefinitions()
	if err != nil {
		return nil, err
	}
	for _, d := range shipped {
		if d.Name != rev.Name {
			continue
		}
		if def := matchBound(d, rev); def != nil {
			return def, nil
		}
		retired, err := workflow.Retired(d.Name)
		if err != nil {
			return nil, err
		}
		for _, old := range retired {
			if def := matchBound(old, rev); def != nil {
				return def, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: workflow %q %s is not a shipped revision", interp.ErrRevision, rev.Name, rev.Digest)
}

// maintenanceStatus is the feed's maintenance object and its due maintenance items under the newest session's
// policy, counted from the baseline session: none before a session started with a policy
// (docs/decisions/director-maintenance.md). A repository's hallmark counts from its last hallmark task in hallmarks,
// or from its newest recorded hallmark run when it has none, and is not due while a hallmark task of it is open or
// while its last hallmark task's follow-ups decision is unanswered. Merges count after that decision's answer, or
// after the task's creation when it has none (docs/decisions/engine-owned-hallmark-cadence.md).
func maintenanceStatus(v store.DirectorView, hallmarks map[string]repoHallmarks) (*statusMaintenance, []needsMeItem) {
	m := v.Maintenance
	if m.Baseline == 0 || v.Session == nil {
		return nil, nil
	}
	p := v.Session.Maintenance
	out := &statusMaintenance{BaselineSession: m.Baseline, NoteAudit: statusNoteAudit{EverySessions: p.NoteAuditEverySessions}, Hallmarks: []statusHallmark{}}
	var items []needsMeItem
	since, last := m.Baseline, "the upgrade"
	if r := m.LastNotes; r != nil {
		out.NoteAudit.LastSession, out.NoteAudit.LastAt = r.Session, r.Time
		since, last = max(since, r.Session), "the last"
	}
	out.NoteAudit.SessionsSince = max(0, v.Session.Number-since)
	if n := p.NoteAuditEverySessions; n > 0 {
		until := max(0, n-out.NoteAudit.SessionsSince)
		out.NoteAudit.SessionsUntilDue = &until
		if until == 0 {
			items = append(items, needsMeItem{Kind: maintenanceKind,
				Reason:  fmt.Sprintf("note audit due: %d sessions since %s (session %d)", out.NoteAudit.SessionsSince, last, since),
				Command: "guildhall director maintenance record --notes --summary <text>"})
		}
	}
	for _, r := range m.Repositories {
		h := statusHallmark{Repository: r.Repository, AfterMerges: p.HallmarkAfterMerges, MergedSince: r.Merged}
		last := "the upgrade"
		found := hallmarks[r.Repository]
		switch l := r.LastHallmark; {
		case found.last != nil:
			// Merges count after the follow-ups answer, so the cadence is paced by the Guildmaster's feedback. A
			// hallmark closes only once its follow-ups decision is answered, so the last one has an answer; one with no
			// follow-ups decision counts after the task's creation.
			after := found.last.Close.Created
			if found.followUpsAnswered != 0 {
				after = found.followUpsAnswered
			}
			h.MergedSince = 0
			for _, seq := range r.MergedSequences {
				if seq > after {
					h.MergedSince++
				}
			}
			h.LastTask, h.LastAt, last = found.last.Task.ID, found.last.Close.At, "the last"
		case l != nil:
			h.LastTask, h.LastAt, last = l.Task, l.Time, "the last"
		}
		if found.running != nil {
			h.RunningTask = found.running.ID
		}
		if n := p.HallmarkAfterMerges; n > 0 {
			until := max(0, n-h.MergedSince)
			h.MergesUntilDue = &until
			if until == 0 && h.RunningTask == "" {
				items = append(items, needsMeItem{Kind: maintenanceKind, Repository: r.Repository, Count: h.MergedSince,
					Reason:  fmt.Sprintf("hallmark due: %d merged since %s", h.MergedSince, last),
					Command: fmt.Sprintf("guildhall task new --repo %s --kind hallmark --title <text> <id>", r.Repository)})
			}
		}
		out.Hallmarks = append(out.Hallmarks, h)
	}
	return out, items
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

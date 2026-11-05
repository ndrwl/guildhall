// Package config parses and validates a Guildhall copy's configuration file.
//
// The file is TOML and versioned by schema_version.
// It never holds credentials; sign-in stays in each runtime's own store.
// Workflow definitions are separate JSON files (package workflow); repositories select them by name.
package config

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/workbench"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// SchemaVersion is the newest config schema this Guildhall reads.
const SchemaVersion = 1

// ErrNewerSchema reports a config written for a newer Guildhall; it is refused rather than guessed at.
var ErrNewerSchema = errors.New("config schema is newer than this Guildhall supports")

// Mode is the role a Guildhall copy runs as.
type Mode string

const (
	ModeSteward  Mode = "steward"
	ModeDirector Mode = "director"
)

// ManagedAlertRepository is the repository id reserved for a Director store's own managed-mode records: the task
// holding the Guildmaster's decisions on observed-untrusted managed.toml digests (docs/director-modes.md#guard-rails).
// A real repository may not use it.
const ManagedAlertRepository = "managed-mode"

// MessagesRepository is the repository id reserved for a Director store's own messaging records: the tasks holding
// the Guildmaster's decisions on a closed inbox (docs/messaging.md#closing-the-inbox-to-a-sender). A real repository
// may not use it.
const MessagesRepository = "messages"

// MergeOptions are a repository's merge settings, [repository.merge] (docs/configuration.md#merge-requirements).
type MergeOptions struct {
	Requires MergeRequires `toml:"requires"`
	// ReapproveAfterRebase is the rebase kind from which a rebased head needs a new merge decision; empty is
	// RebaseMajorConflicts. Read it through Reapprove.
	ReapproveAfterRebase RebaseKind `toml:"reapprove_after_rebase" json:"reapprove_after_rebase,omitempty"`
	// MinorConflictFiles and MinorConflictLines bound a minor_conflicts rebase; nil is the default, 3 files and 50
	// lines. Read them through MinorFiles and MinorLines.
	MinorConflictFiles *int `toml:"minor_conflict_files" json:"minor_conflict_files,omitempty"`
	MinorConflictLines *int `toml:"minor_conflict_lines" json:"minor_conflict_lines,omitempty"`
	// Regenerate declares the repository's generated files and the commands that rewrite them,
	// [[repository.merge.regenerate]]: a rebase whose only conflicts lie under these paths is resolved by running the
	// commands, with no model (docs/configuration.md#rebase-settings). Read it through RegenerateFor.
	Regenerate []Regenerate `toml:"regenerate" json:"regenerate,omitempty"`
	// MergeBatchSize is the most queued tasks the merge queue tests together on one combined head and lands at once
	// (docs/configuration.md#merge-batches); nil, like 1, is today's serial queue. Read it through BatchSize.
	MergeBatchSize *int `toml:"merge_batch_size" json:"merge_batch_size,omitempty"`
}

// DefaultMergeBatchSize is the effective merge_batch_size when unset: one task at a time, the serial queue.
const DefaultMergeBatchSize = 1

// BatchSize is the effective merge_batch_size, at least 1; 1 is the serial queue.
func (m MergeOptions) BatchSize() int { return intOr(m.MergeBatchSize, DefaultMergeBatchSize) }

// Batching reports whether the merge queue batches tasks: merge_batch_size is above 1.
func (m MergeOptions) Batching() bool { return m.BatchSize() > 1 }

// Regenerate is one [[repository.merge.regenerate]] entry: directory prefixes of generated files, each relative,
// clean and ending in /, and the argv, run with no shell from the worktree root, that rewrites them.
type Regenerate struct {
	Paths   []string `toml:"paths" json:"paths"`
	Command []string `toml:"command" json:"command"`
}

// Covers reports whether path lies under one of the entry's prefixes.
func (r Regenerate) Covers(path string) bool {
	return slices.ContainsFunc(r.Paths, func(prefix string) bool { return strings.HasPrefix(path, prefix) })
}

// RegenerateFor returns the regenerate entries that cover paths, in declared order and without duplicates. ok is true
// only when at least one entry is declared, paths is not empty, and every path lies under some entry's prefix.
func (m MergeOptions) RegenerateFor(paths []string) (cmds []Regenerate, ok bool) {
	if len(m.Regenerate) == 0 || len(paths) == 0 {
		return nil, false
	}
	used := make([]bool, len(m.Regenerate))
	for _, p := range paths {
		hit := false
		for i, r := range m.Regenerate {
			if r.Covers(p) {
				used[i], hit = true, true
			}
		}
		if !hit {
			return nil, false
		}
	}
	for i, r := range m.Regenerate {
		if used[i] {
			cmds = append(cmds, r)
		}
	}
	return cmds, true
}

// regeneratePathProblem says what is wrong with a regenerate path prefix, or "" when it is relative, clean, free of
// .. and ends in /.
func regeneratePathProblem(p string) string {
	switch trimmed := strings.TrimSuffix(p, "/"); {
	case p == "" || trimmed == "":
		return "must not be empty"
	case !strings.HasSuffix(p, "/"):
		return "must end in /"
	case path.IsAbs(p) || filepath.IsAbs(p) || filepath.VolumeName(p) != "" || strings.Contains(p, `\`):
		return "must be a relative path with / separators"
	case slices.Contains(strings.Split(trimmed, "/"), ".."):
		return "must not contain .."
	case path.Clean(trimmed) != trimmed:
		return "must be a clean path"
	}
	return ""
}

// RebaseKind is how hard a rebase onto the base tip was (docs/interpreter.md#tiered-rebase), and, as
// reapprove_after_rebase, the kind from which a rebased head needs a new merge decision.
type RebaseKind string

// The rebase kinds, in order: a later kind is harder. RebaseNever is a threshold only: no rebase reaches it.
const (
	RebaseNoConflicts    RebaseKind = "no_conflicts"
	RebaseMinorConflicts RebaseKind = "minor_conflicts"
	RebaseMajorConflicts RebaseKind = "major_conflicts"
	RebaseNever          RebaseKind = "never"
)

// rebaseKinds are the kinds in order.
var rebaseKinds = []RebaseKind{RebaseNoConflicts, RebaseMinorConflicts, RebaseMajorConflicts, RebaseNever}

// Rank orders the kinds: no_conflicts is 0 and never 3; an unknown kind is -1.
func (k RebaseKind) Rank() int { return slices.Index(rebaseKinds, k) }

// The defaults of [repository.merge].
const (
	DefaultReapproveAfterRebase = RebaseMajorConflicts
	DefaultMinorConflictFiles   = 3
	DefaultMinorConflictLines   = 50
)

// Reapprove is the effective reapprove_after_rebase.
func (m MergeOptions) Reapprove() RebaseKind {
	return cmp.Or(m.ReapproveAfterRebase, DefaultReapproveAfterRebase)
}

// MinorFiles and MinorLines are the effective minor_conflict_files and minor_conflict_lines.
func (m MergeOptions) MinorFiles() int { return intOr(m.MinorConflictFiles, DefaultMinorConflictFiles) }
func (m MergeOptions) MinorLines() int { return intOr(m.MinorConflictLines, DefaultMinorConflictLines) }

// NeedsReapproval reports whether a rebase of kind k needs a new merge decision: k is at or above the threshold. An
// unknown kind, or an unknown threshold, always needs one, so a carry-over fails closed.
func (m MergeOptions) NeedsReapproval(k RebaseKind) bool {
	if k.Rank() < 0 || m.Reapprove().Rank() < 0 {
		return true
	}
	return k.Rank() >= m.Reapprove().Rank()
}

func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// MergeRequires is what a task's pull request needs before deliver merges it, [repository.merge.requires].
// A nil field is a key the config does not set, which is required, so the zero value requires everything, as a
// config without the section does. Read a requirement through its method, never its field.
type MergeRequires struct {
	// GuildmasterApproval requires the Guildmaster's merge answer at the deliver decision; when false, deliver
	// merges by itself once every other requirement holds.
	GuildmasterApproval *bool `toml:"guildmaster_approval" json:"guildmaster_approval"`
	// ArtisanApproval requires every earlier review step's latest outcome, from the independent review Artisan,
	// to approve the delivered head, the accepted spec and the current standards; no answer merges over it.
	ArtisanApproval *bool `toml:"artisan_approval" json:"artisan_approval"`
	// CIGreen requires the validation input to be a passed result for the delivered head; no answer merges over it.
	CIGreen *bool `toml:"ci_green" json:"ci_green"`
	// NothingOpen requires those review outcomes to have no open finding (unresolved, or disputed without a standing
	// Director ruling; suggestions never count) and the task to have no unanswered decision but deliver's own;
	// the Guildmaster's merge answer overrides it.
	NothingOpen *bool `toml:"nothing_open" json:"nothing_open"`
}

func required(b *bool) bool { return b == nil || *b }

// Guildmaster reports whether guildmaster_approval is required; Artisan, CI and Open (nothing_open) report the others.
func (m MergeRequires) Guildmaster() bool { return required(m.GuildmasterApproval) }
func (m MergeRequires) Artisan() bool     { return required(m.ArtisanApproval) }
func (m MergeRequires) CI() bool          { return required(m.CIGreen) }
func (m MergeRequires) Open() bool        { return required(m.NothingOpen) }

// Effective returns m with every requirement set to its effective value.
func (m MergeRequires) Effective() MergeRequires {
	b := func(v bool) *bool { return &v }
	return MergeRequires{GuildmasterApproval: b(m.Guildmaster()), ArtisanApproval: b(m.Artisan()), CIGreen: b(m.CI()), NothingOpen: b(m.Open())}
}

// DefaultWorkflow is the workflow a repository runs when it names none.
const DefaultWorkflow = "default"

// Task kinds: a task is created as one, which chooses its workflow (docs/tasks.md#task-kinds).
// A feature task runs the repository's Workflow; a bug or hallmark task runs Kinds[<kind>], or the shipped workflow
// named for its kind. A task kind is not a step kind (workflow.Kind).
const (
	TaskKindFeature = "feature"
	TaskKindBug     = "bug"
	// TaskKindHallmark is a report-only review of the whole repository, which finishes without a pull request.
	TaskKindHallmark = "hallmark"
)

// TaskKindRow is one task kind: its name, its description for task new's help and the Director's tasks skill,
// and the workflow it runs when the repository names none.
type TaskKindRow struct {
	Name            string
	Description     string
	DefaultWorkflow string
	FeatureWorkflow bool // runs Repository.Workflow; kinds.<name> is refused
}

// TaskKindTable is the one table of task kinds, the default first. The descriptions live here rather than in a
// workflow's description so that rewording them never changes a digest.
var TaskKindTable = []TaskKindRow{
	{TaskKindFeature, "New or changed behaviour, and the default: it runs the repository's workflow setting, the " +
		"shipped default when it names none, with grill, research, plan, the Guildmaster's acceptance, build, review, " +
		"publish, validate and deliver.", DefaultWorkflow, true},
	{TaskKindBug, "A fix for a defect in behaviour that should already work: it runs the repository's " +
		"[repository.kinds] bug workflow, the shipped bug workflow when it names none, which has the default's steps " +
		"without grill and the Guildmaster's acceptance, so research, plan, build, review, publish, validate and " +
		"deliver, and merges under the repository's merge requirements: research reproduces the defect from the title " +
		"and detail and finds its cause, plan's spec goes straight to build, build writes a regression test that fails " +
		"before the fix and passes after it, and review checks that test.", "bug", false},
	{TaskKindHallmark, "A report-only review of the whole repository, launched when guildhall director status " +
		"lists a hallmark due: it runs the repository's [repository.kinds] hallmark workflow, the shipped hallmark " +
		"workflow when it names none, whose one read-only hallmark stage uses the guildhall-hallmark skill, starts from " +
		"the repository's previous hallmark report and writes ranked, PR-sized follow-ups; Guildhall fails the stage if " +
		"it changes the project, and the task finishes as done with no pull request.", "hallmark", false},
}

// TaskKinds lists every task kind's name, in TaskKindTable's order, the default first.
var TaskKinds = func() []string {
	names := make([]string, len(TaskKindTable))
	for i, row := range TaskKindTable {
		names[i] = row.Name
	}
	return names
}()

// TaskKindRowOf returns TaskKindTable's row for the kind named name, and whether there is one.
func TaskKindRowOf(name string) (TaskKindRow, bool) {
	for _, row := range TaskKindTable {
		if row.Name == name {
			return row, true
		}
	}
	return TaskKindRow{}, false
}

// TaskKindWorkflow returns the workflow name repository r runs for a task of kind taskKind, and whether the kind is
// known.
func (r Repository) TaskKindWorkflow(taskKind string) (string, bool) {
	row, ok := TaskKindRowOf(taskKind)
	switch {
	case !ok:
		return "", false
	case row.FeatureWorkflow:
		return r.Workflow, true
	case r.Kinds[taskKind] != "":
		return r.Kinds[taskKind], true
	}
	return row.DefaultWorkflow, true
}

// kindsChoice lists the task kinds a [repository.kinds] table accepts, joined as "a, b and c".
func kindsChoice() string {
	var names []string
	for _, row := range TaskKindTable {
		if !row.FeatureWorkflow {
			names = append(names, row.Name)
		}
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// StallIntervalKey is the setting that sets the interpreter's worker-stalled interval (docs/configuration.md#settings).
const StallIntervalKey = "stall_interval"

// DefaultStallInterval is the stall interval of a config that does not set StallIntervalKey.
const DefaultStallInterval = 15 * time.Minute

// WakeQuietKey is the setting that sets how long the Director session's supervisor holds a burst's message wakes
// after the newest before it wakes the Director once for them (docs/configuration.md#settings).
const WakeQuietKey = "wake_quiet"

// DefaultWakeQuiet is the wake quiet period of a config that does not set WakeQuietKey.
const DefaultWakeQuiet = 3 * time.Second

// WakeMaxDelayKey is the setting that bounds how long after the first held message wake the supervisor wakes the
// Director, however long the burst goes on (docs/configuration.md#settings).
const WakeMaxDelayKey = "wake_max_delay"

// DefaultWakeMaxDelay is the wake delay bound of a config that does not set WakeMaxDelayKey.
const DefaultWakeMaxDelay = 15 * time.Second

// MailboxMaxKey is the setting that caps a Director's unacknowledged external messages, such as Steward messages:
// at the cap, `messages receive` refuses a new one with "mailbox full" (docs/messaging.md#a-full-mailbox).
const MailboxMaxKey = "mailbox_max"

// DefaultMailboxMax is the mailbox cap of a config that does not set MailboxMaxKey.
const DefaultMailboxMax = 200

// ChecksRefreshKey is the setting that sets how long a waiting validate step waits between reads of the same
// head's checks (docs/configuration.md#settings).
const ChecksRefreshKey = "checks_refresh"

// DefaultChecksRefresh is the checks refresh of a config that does not set ChecksRefreshKey.
const DefaultChecksRefresh = time.Minute

// CIStartGraceKey is the setting that sets how long a validate step or merge-queue front waits on a head its forge
// reports no check for, from the first read that saw none, before it tells the Director no CI run exists for it and
// re-triggers the pull request's CI where the forge can (docs/configuration.md#settings).
const CIStartGraceKey = "ci_start_grace"

// DefaultCIStartGrace is the CI start grace of a config that does not set CIStartGraceKey.
const DefaultCIStartGrace = 10 * time.Minute

// CILogLimitKey is the setting that caps the bytes of one failed job log Guildhall fetches and caches
// (docs/configuration.md#settings).
const CILogLimitKey = "ci_log_limit"

// DefaultCILogLimit is the failed job log cap of a config that does not set CILogLimitKey.
const DefaultCILogLimit int64 = 5 << 20

// ArtisanWorkspaceCloseAfterKey is the setting that sets how long after an attempt finishes the supervisor's
// workspace close sweep closes the attempt's Herdr workspace (docs/configuration.md#settings).
const ArtisanWorkspaceCloseAfterKey = "artisan_workspace_close_after"

// DefaultArtisanWorkspaceCloseAfter is the close delay of a config that does not set
// ArtisanWorkspaceCloseAfterKey.
const DefaultArtisanWorkspaceCloseAfter = 5 * time.Minute

// ArtisanPaneOutputKey is the setting that chooses what an Artisan's Herdr pane shows: the Workbench, the
// default, or launcher.log plain (docs/configuration.md#settings).
const ArtisanPaneOutputKey = "artisan_pane_output"

// PruneAfterKey is the setting that sets how long after a task closes `guildhall prune` may reclaim its working
// directory and attempt directories (docs/configuration.md#settings).
const PruneAfterKey = "prune_after"

// DefaultPruneAfter is the prune delay of a config that does not set PruneAfterKey: 14 days.
const DefaultPruneAfter = 14 * 24 * time.Hour

// PruneDailyKey is the setting that makes the session supervisor run a prune once a day
// (docs/configuration.md#settings).
const PruneDailyKey = "prune_daily"

// LockWaitKey is the setting that bounds each advance's waits for its task lock and for the store's lock
// (docs/configuration.md#settings).
const LockWaitKey = "lock_wait"

// DefaultLockWait is the lock wait of a config that does not set LockWaitKey.
const DefaultLockWait = 30 * time.Second

// HerdrSessionKey is the setting that names the Herdr session the Director's tab opens in
// (docs/configuration.md#settings).
const HerdrSessionKey = "herdr_session"

// Config is one copy's parsed configuration.
type Config struct {
	SchemaVersion int  `toml:"schema_version"`
	Mode          Mode `toml:"mode"`
	// Name is the Director's name; required in director mode.
	Name string `toml:"name"`
	// PrivateConfig points at the private preferences repository; Steward only.
	PrivateConfig *PrivateConfig `toml:"private_config"`
	// Managed is written only by Steward rollouts.
	Managed map[string]ManagedSetting `toml:"managed"`
	// Local is owned by this copy and may override managed keys marked overridable.
	Local        map[string]string `toml:"local"`
	Repositories []Repository      `toml:"repository"`
	// Profiles are the Director's named Artisan profiles, keyed by the name workflow steps give in "profile".
	Profiles map[string]Profile `toml:"profile"`
	// Director chooses who runs the Director's own session (docs/director.md#start); Director mode only.
	// It is omitted from JSON when unset, so the config digest of a config without it is unchanged.
	Director *Director `toml:"director" json:",omitempty"`
	// Steward chooses who runs the Steward session (docs/steward.md#the-steward-session); Steward mode only.
	// It is omitted from JSON when unset, so the config digest of a config without it is unchanged.
	Steward *Steward `toml:"steward" json:",omitempty"`
	// Web serves the Guildmaster's task pages on the tailnet (docs/web.md); Director mode only. It is omitted from JSON
	// when unset, so the config digest of a config without it is unchanged.
	Web *Web `toml:"web" json:",omitempty"`
	// Backup backs the copy up to its own branch of a private repository (docs/running.md#backups); Director mode
	// only. It is never in JSON, so adding or editing it changes no config digest.
	Backup *Backup `toml:"backup" json:"-"`
	// Peers are the other Directors this one exchanges Director messages with (docs/messaging.md#director-to-director-messages);
	// Director mode only. They are never in JSON, so adding or editing one changes no config digest.
	Peers []Peer `toml:"peer" json:"-"`
}

// Web is the [web] table: where a Director's session supervisor, or a hand-run `guildhall web`, serves the task pages,
// and the Guildmaster's Tailscale login (docs/web.md).
type Web struct {
	// Address is the host and port to listen on, such as "andrew-lap-wsl.tail5da40a.ts.net:4388"; required, with no
	// default port, so each copy on a machine names its own. Its host must resolve only to Tailscale addresses when
	// the server binds, or nothing is served; page links are built from it.
	Address string `toml:"address"`
	// Guildmaster is the Guildmaster's Tailscale login, such as "andrew@example.com"; optional. Only requests Tailscale
	// identifies as this login can answer on the page; with none, every page is read-only.
	Guildmaster string `toml:"guildmaster" json:",omitempty"`
}

// Steward names the Steward session's Herdr workspace and chooses its runtime adapter, model, effort and role
// prompt, as [director] does for a Director. Like a profile it names no credential. Name and Adapter are required to
// start a session, not to parse a config, so a config that only runs rollouts needs no [steward] table.
type Steward struct {
	// Name labels the Steward's Herdr workspace and tab; an id, like a Director's name.
	Name string `toml:"name"`
	// Adapter names the runtime adapter, such as "claude"; a positional runtime overrides it for one command.
	Adapter string `toml:"adapter"`
	// Model is passed as given; empty means the adapter's default.
	// Effort is explicit when set; otherwise EffectiveEffort supplies the config fallback.
	Model  string `toml:"model"`
	Effort string `toml:"effort"`
	// Role is a slash-separated path relative to the config file's directory; empty keeps the copy's
	// baseline/roles/steward.md, else the release's.
	Role string `toml:"role"`
	// CollectEvery is how often the Steward session collects lessons and incidents from the enrolled copies, as a Go
	// duration of at least a minute; empty, the default, means never on a schedule (docs/steward.md#collection).
	CollectEvery string `toml:"collect_every" json:",omitempty"`
	// ReportUnreachableAfter is how long an enrolled copy must have been unreachable before the scheduled run, once it
	// reaches the copy again, sends it an unreachable-recovered Steward message, as a Go duration of at least a minute;
	// empty means DefaultReportUnreachableAfter (docs/steward.md#steward-messages).
	ReportUnreachableAfter string `toml:"report_unreachable_after" json:",omitempty"`
	// ReportDirectorAfter is how long an enrolled Director must have taken no turn while messages waited for it, or
	// have run a release behind its copy's pin, or stayed recovering, before the scheduled run sends it a
	// director-stuck, release-behind or director-unhealthy Steward message, as a Go duration of at least a minute;
	// empty means DefaultReportDirectorAfter (docs/steward.md#director-health).
	ReportDirectorAfter string `toml:"report_director_after" json:",omitempty"`
}

// DefaultReportDirectorAfter is the [steward] report_director_after of a config that does not set it.
const DefaultReportDirectorAfter = time.Hour

// ReportDirectorAfterDuration is the table's report_director_after, DefaultReportDirectorAfter when unset.
func (s Steward) ReportDirectorAfterDuration() (time.Duration, error) {
	if s.ReportDirectorAfter == "" {
		return DefaultReportDirectorAfter, nil
	}
	d, err := time.ParseDuration(s.ReportDirectorAfter)
	if err != nil || d < time.Minute {
		return 0, fmt.Errorf("steward: report_director_after %q must be a duration of at least a minute, such as \"1h\"", s.ReportDirectorAfter)
	}
	return d, nil
}

// DefaultReportUnreachableAfter is the [steward] report_unreachable_after of a config that does not set it.
const DefaultReportUnreachableAfter = time.Hour

// ReportUnreachableAfterDuration is the table's report_unreachable_after, DefaultReportUnreachableAfter when unset.
func (s Steward) ReportUnreachableAfterDuration() (time.Duration, error) {
	if s.ReportUnreachableAfter == "" {
		return DefaultReportUnreachableAfter, nil
	}
	d, err := time.ParseDuration(s.ReportUnreachableAfter)
	if err != nil || d < time.Minute {
		return 0, fmt.Errorf("steward: report_unreachable_after %q must be a duration of at least a minute, such as \"1h\"", s.ReportUnreachableAfter)
	}
	return d, nil
}

// CollectEveryDuration is the table's collect_every, and false when it is unset, so no round is scheduled.
func (s Steward) CollectEveryDuration() (time.Duration, bool, error) {
	if s.CollectEvery == "" {
		return 0, false, nil
	}
	d, err := time.ParseDuration(s.CollectEvery)
	if err != nil || d < time.Minute {
		return 0, false, fmt.Errorf("steward: collect_every %q must be a duration of at least a minute, such as \"1h\"", s.CollectEvery)
	}
	return d, true, nil
}

// Director chooses the runtime adapter, model and effort of the Director's session and optionally its role
// prompt. Like a profile it names no credential. Adapter is required to start a session, not to parse.
type Director struct {
	// Adapter names the runtime adapter, such as "claude"; a positional runtime overrides it for one command.
	Adapter string `toml:"adapter"`
	// Model is passed as given; empty means the adapter's default.
	// Effort is explicit when set; otherwise EffectiveEffort supplies the config fallback.
	Model  string `toml:"model"`
	Effort string `toml:"effort"`
	// Role is a slash-separated path relative to the config file's directory; empty keeps the copy's
	// baseline/roles/director.md, else the release's.
	Role string `toml:"role"`
	// IdleAfter is how long a Director session with turn hooks may go without a turn signal or a wake before it
	// counts as idle, as a Go duration; empty means DefaultIdleAfter.
	IdleAfter string `toml:"idle_after" json:",omitempty"`
	// HandoffAfterTasks, HandoffContextPercent and HandoffIdle are the automatic handoff triggers
	// (docs/director.md#automatic-handoff): finished tasks per crossing, 0 for off; the used context percent, 0 to
	// 100 with 0 for off, DefaultHandoffContextPercent when unset; and a quiet period as a Go duration, "0s" for off
	// and when unset.
	HandoffAfterTasks     int    `toml:"handoff_after_tasks" json:",omitempty"`
	HandoffContextPercent *int   `toml:"handoff_context_percent" json:",omitempty"`
	HandoffIdle           string `toml:"handoff_idle" json:",omitempty"`
	// NoteAuditEverySessions and HallmarkAfterMerges are the maintenance cadences
	// (docs/director.md#maintenance): sessions between note audits and merged pull requests per repository between
	// hallmarks, 0 for off, DefaultNoteAuditEverySessions and DefaultHallmarkAfterMerges when unset.
	NoteAuditEverySessions *int `toml:"note_audit_every_sessions" json:",omitempty"`
	HallmarkAfterMerges    *int `toml:"hallmark_after_merges" json:",omitempty"`
}

// DefaultHandoffContextPercent is the [director] handoff_context_percent a config that does not set it gets.
const DefaultHandoffContextPercent = 70

// Handoff is a config's effective automatic handoff triggers; zero turns a trigger off.
type Handoff struct {
	AfterTasks     int
	ContextPercent int
	Idle           time.Duration
}

// DefaultNoteAuditEverySessions and DefaultHallmarkAfterMerges are the [director] note_audit_every_sessions and
// hallmark_after_merges a config that does not set them gets.
const (
	DefaultNoteAuditEverySessions = 3
	DefaultHallmarkAfterMerges    = 4
)

// Maintenance is a config's effective maintenance cadences; zero turns a part off.
type Maintenance struct {
	NoteAuditEverySessions int
	HallmarkAfterMerges    int
}

// DefaultIdleAfter is the [director] idle_after a config that does not set it gets.
const DefaultIdleAfter = 5 * time.Minute

// ErrUnknownProfile reports a profile name that the Director config does not declare.
var ErrUnknownProfile = errors.New("unknown profile")

// Profile chooses who runs an Artisan stage that names it: the runtime adapter, model and effort,
// and optionally a role prompt replacing the release's Artisan role.
// It names no credential; sign-in stays in each runtime's own store.
type Profile struct {
	// Adapter names the runtime adapter, such as "claude"; required.
	Adapter string `toml:"adapter"`
	// Model is passed as given; empty means the adapter's default.
	// Effort is explicit when set; otherwise EffectiveEffort supplies the config fallback.
	Model  string `toml:"model"`
	Effort string `toml:"effort"`
	// Role is a slash-separated path relative to the config file's directory; empty keeps the release role.
	Role string `toml:"role"`
	// AllowedTools are the tool permission rules this Director grants the profile's Artisan beyond its runtime's
	// baseline, in the runtime's own syntax, such as "Bash(go test:*)" for Claude Code
	// (docs/adapters.md#artisan-permissions). They come only from this copy's own config file, never a managed one.
	// Nil means the key is not set, so a claude profile with a well-known name gets its shipped default grant; a set
	// list, even an empty one, replaces that default. A pointer keeps the difference through JSON while leaving the
	// config digest of a config that sets no empty list unchanged.
	AllowedTools *[]string `toml:"allowed_tools" json:",omitempty"`
	// InheritEnv are the names of this machine's injected secrets (docs/secrets.md) the profile's Artisans are
	// given; every other injected name is withheld from them. Its entries are environment variable names, never
	// values.
	InheritEnv []string `toml:"inherit_env" json:",omitempty"`
}

// AllowedToolsSet reports whether the profile sets allowed_tools, even to an empty list.
func (p Profile) AllowedToolsSet() bool { return p.AllowedTools != nil }

// AllowedToolsList returns the profile's allowed_tools as set, or nil when it sets none.
func (p Profile) AllowedToolsList() []string {
	if p.AllowedTools == nil {
		return nil
	}
	return *p.AllowedTools
}

// CheckAllowedTool reports why rule cannot be a profile's allowed_tools entry, or nil if it can: a tool name,
// optionally with a parenthesised specifier, with no control characters, and never a rule that allows every shell
// command (Bash alone, Bash(*) or Bash(:*)).
func CheckAllowedTool(rule string) error {
	m := allowedToolPattern.FindStringSubmatch(rule)
	switch {
	case m == nil || strings.ContainsFunc(rule, unicode.IsControl):
		return fmt.Errorf("%q is not a permission rule: a tool name, optionally with a specifier in parentheses, such as Bash(go test:*)", rule)
	case m[1] == "Bash" && strings.Trim(m[3], "*: ") == "":
		return fmt.Errorf("%q allows every shell command; name the commands, such as Bash(go test:*)", rule)
	}
	return nil
}

// PrivateConfig is a reference to the private config repository, never a credential.
type PrivateConfig struct {
	Location string `toml:"location"`
}

// ManagedSetting is one Steward-managed value and whether a local value may replace it.
type ManagedSetting struct {
	Value       string `toml:"value"`
	Overridable bool   `toml:"overridable"`
}

// Repository is one repository a Director owns.
type Repository struct {
	ID           string `toml:"id"`
	Path         string `toml:"path"`
	Remote       string `toml:"remote"`
	OwnerMachine string `toml:"owner_machine"`
	// TaskPrefix is the 1 to 3 lowercase letters every task id of this repository that task new assigns starts with,
	// such as "d" for d42 (docs/configuration.md#repositories). It is an identity key, never overridable per task, and
	// unique across a copy's repositories. Read it through TaskIDPrefix. It is nil when the config does not set it,
	// and omitted from JSON then, so the config digest of a config without it is unchanged.
	TaskPrefix *string `toml:"task_prefix" json:",omitempty"`
	// Merge holds what deliver requires before it merges; a missing section requires everything.
	Merge MergeOptions `toml:"merge"`
	// Workflow names the Director's workflow definition this repository runs for a feature task.
	Workflow string `toml:"workflow"`
	// Kinds names the workflow this repository runs for each other task kind, keyed by a TaskKindTable kind that does not run Workflow; a kind
	// it does not name runs the shipped workflow of that name. It is omitted from JSON when unset, so the config
	// digest of a config without it is unchanged.
	Kinds map[string]string `toml:"kinds" json:",omitempty"`
	// WorkflowOverrides chooses step actors for this repository only, keyed by step id (workflow.ApplyOverrides).
	WorkflowOverrides map[string]workflow.Override `toml:"workflow_overrides"`
	// NoCI declares that the repository runs no checks, so validate passes a head its forge reports none for.
	NoCI bool `toml:"no_ci" json:",omitempty"`
	// FlakyChecks is what validate does with a failed GitHub Actions check: off, rerun or rerun-and-file
	// (docs/configuration.md#repositories); empty is DefaultFlakyChecks. Read it through FlakyChecksPolicy. It is
	// omitted from JSON when unset, so the config digest of a config without it is unchanged.
	FlakyChecks FlakyChecks `toml:"flaky_checks" json:",omitempty"`
	// CIRetrigger is how Guildhall re-triggers the pull request CI of a published head that still reports no check
	// once ci_start_grace has passed (docs/configuration.md#repositories): off, the default, starts nothing, and
	// reopen closes and reopens the pull request once per head. Read it through CIRetriggerPolicy. It is omitted
	// from JSON when unset, so the config digest of a config without it is unchanged.
	CIRetrigger CIRetrigger `toml:"ci_retrigger" json:",omitempty"`
	// Profiles are this repository's grants, keyed by a declared claude profile's name: each adds its rules to that
	// profile's effective grant for this repository's tasks only (docs/adapters.md#artisan-permissions).
	// It is omitted from JSON when unset, so the config digest of a config without it is unchanged.
	Profiles map[string]RepositoryProfile `toml:"profiles" json:",omitempty"`
}

// taskPrefixPattern is the form of a repository's task_prefix: 1 to 3 lowercase letters.
var taskPrefixPattern = regexp.MustCompile(`^[a-z]{1,3}$`)

// ValidTaskPrefix reports whether p has the form of a task_prefix, 1 to 3 lowercase letters.
func ValidTaskPrefix(p string) bool { return taskPrefixPattern.MatchString(p) }

// TaskIDPrefix is the repository's task_prefix, and false when the config does not set one.
func (r Repository) TaskIDPrefix() (string, bool) {
	if r.TaskPrefix == nil {
		return "", false
	}
	return *r.TaskPrefix, true
}

// FlakyChecks is a repository's flaky-check policy, flaky_checks.
type FlakyChecks string

// The flaky-check policies: off records a failed check as a failed round at once; rerun re-runs each failed
// GitHub Actions job once on the same head first; rerun-and-file also tells the Director of a flake, a failure
// that passed on its rerun, so the Director files or updates a bug.
const (
	FlakyChecksOff          FlakyChecks = "off"
	FlakyChecksRerun        FlakyChecks = "rerun"
	FlakyChecksRerunAndFile FlakyChecks = "rerun-and-file"
	DefaultFlakyChecks                  = FlakyChecksRerunAndFile
)

// Valid reports whether p is empty or one of the policies.
func (p FlakyChecks) Valid() bool {
	return p == "" || p == FlakyChecksOff || p == FlakyChecksRerun || p == FlakyChecksRerunAndFile
}

// FlakyChecksPolicy is the effective flaky_checks.
func (r Repository) FlakyChecksPolicy() FlakyChecks {
	return cmp.Or(r.FlakyChecks, DefaultFlakyChecks)
}

// CIRetrigger is a repository's missing-run policy, ci_retrigger.
type CIRetrigger string

// The missing-run policies: off starts no run, so a head with no check is only reported; reopen closes and reopens
// the pull request once per head, whose pull_request reopened event runs its CI on the same head.
const (
	CIRetriggerOff     CIRetrigger = "off"
	CIRetriggerReopen  CIRetrigger = "reopen"
	DefaultCIRetrigger             = CIRetriggerOff
)

// Valid reports whether p is empty or one of the policies.
func (p CIRetrigger) Valid() bool {
	return p == "" || p == CIRetriggerOff || p == CIRetriggerReopen
}

// CIRetriggerPolicy is the effective ci_retrigger.
func (r Repository) CIRetriggerPolicy() CIRetrigger {
	return cmp.Or(r.CIRetrigger, DefaultCIRetrigger)
}

// RepositoryProfile is a repository grant: the rules one repository adds to one profile.
type RepositoryProfile struct {
	// AllowedTools are added after the profile's copy list or shipped default, in the same syntax.
	AllowedTools []string `toml:"allowed_tools"`
}

var (
	keyPattern        = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	credentialPattern = regexp.MustCompile(`(?i)(token|passw|secret|api_?key|credential|private_?key)`)
	drivePathPattern  = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
	// allowedToolPattern is a permission rule: a tool name and an optional non-empty parenthesised specifier.
	allowedToolPattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*)(\((.+)\))?$`)
)

// ownerAbsolute reports whether path is absolute on its owner machine, which may run another OS than this host:
// a POSIX path from the root or a Windows path from a drive letter.
func ownerAbsolute(path string) bool {
	return strings.HasPrefix(path, "/") || drivePathPattern.MatchString(path)
}

// configRelative reports whether p names a file below the config directory on every platform:
// slash-separated, relative, without dot segments, backslashes or a drive letter.
func configRelative(p string) bool {
	return fs.ValidPath(p) && p != "." && !strings.Contains(p, `\`) && !strings.Contains(p, ":")
}

func ownerPathKey(p string) string {
	if drivePathPattern.MatchString(p) {
		return strings.ToLower(p[:2]) + path.Clean(strings.ReplaceAll(p[2:], `\`, "/"))
	}
	return path.Clean(p)
}

// Parse decodes and validates a config, applying defaults.
func Parse(data []byte) (*Config, error) {
	var head struct {
		SchemaVersion int `toml:"schema_version"`
	}
	if _, err := toml.Decode(string(data), &head); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	switch {
	case head.SchemaVersion > SchemaVersion:
		return nil, fmt.Errorf("%w: schema_version %d (supported: %d)", ErrNewerSchema, head.SchemaVersion, SchemaVersion)
	case head.SchemaVersion < 1:
		return nil, errors.New("config: schema_version must be set to a positive version")
	}
	var cfg Config
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		keys := make([]string, len(extra))
		for i, k := range extra {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("config: unknown keys: %s", strings.Join(keys, ", "))
	}
	for name, p := range cfg.Profiles {
		if p.AllowedTools == nil && md.IsDefined("profile", name, "allowed_tools") {
			p.AllowedTools = &[]string{}
			cfg.Profiles[name] = p
		}
	}
	cfg.applyDefaults()
	if cfg.Mode == ModeDirector {
		cfg.addShippedProfiles()
	}
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, errors.New("invalid config: " + strings.Join(problems, "; "))
	}
	return &cfg, nil
}

// ShippedProfiles are the role profiles this release gives every Director copy, one per role
// (workflow.RoleProfile), so a role step always resolves a profile. Each runs the claude adapter with no model, role
// or allowed_tools, so it gets the adapter's default model and its shipped default grant
// (docs/adapters.md#artisan-permissions), and with no effort but designer's, which is high. A copy's own
// [profile.<name>] replaces the shipped one as a whole, so a copy designer without effort runs at default_effort.
var ShippedProfiles = map[string]Profile{
	"builder":    {Adapter: "claude"},
	"designer":   {Adapter: "claude", Effort: "high"},
	"planner":    {Adapter: "claude"},
	"rebaser":    {Adapter: "claude"},
	"researcher": {Adapter: "claude"},
	"reviewer":   {Adapter: "claude"},
}

// addShippedProfiles adds each shipped profile the copy does not declare to c.Profiles.
func (c *Config) addShippedProfiles() {
	for name, p := range ShippedProfiles {
		if _, ok := c.Profiles[name]; ok {
			continue
		}
		if c.Profiles == nil {
			c.Profiles = map[string]Profile{}
		}
		c.Profiles[name] = p
	}
}

func (c *Config) applyDefaults() {
	for i := range c.Repositories {
		r := &c.Repositories[i]
		if r.Workflow == "" {
			r.Workflow = DefaultWorkflow
		}
	}
}

func (c *Config) validate() []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	switch c.Mode {
	case ModeDirector:
		if c.Name == "" {
			add("a director needs a name")
		} else if !workflow.ValidID(c.Name) {
			add("invalid director name %q", c.Name)
		}
		if c.PrivateConfig != nil {
			add("private_config is for the steward only")
		}
		if len(c.Repositories) == 0 {
			add("a director needs at least one repository")
		}
		if c.Steward != nil {
			add("the steward table belongs to the steward, not a director")
		}
	case ModeSteward:
		if c.PrivateConfig == nil || strings.TrimSpace(c.PrivateConfig.Location) == "" {
			add("the steward needs private_config.location")
		} else if hasCredential(c.PrivateConfig.Location) {
			add("private_config.location must not contain credentials")
		}
		if len(c.Repositories) > 0 {
			add("repositories belong to a director, not the steward")
		}
		if len(c.Profiles) > 0 {
			add("profiles belong to a director, not the steward")
		}
		if c.Director != nil {
			add("the director table belongs to a director, not the steward")
		}
		if c.Web != nil {
			add("the web table belongs to a director, not the steward")
		}
		if c.Backup != nil {
			add("the backup table belongs to a director, not the steward")
		}
		// The Steward's session runs the default wake_quiet, as it does idle_after and the handoff triggers.
		_, local := c.Local[WakeQuietKey]
		if _, managed := c.Managed[WakeQuietKey]; local || managed {
			add("%s is a director setting, not the steward's", WakeQuietKey)
		}
	default:
		add(`mode must be "steward" or "director", not %q`, c.Mode)
	}

	for _, key := range slices.Sorted(maps.Keys(c.Managed)) {
		problems = append(problems, checkKey("managed", key)...)
	}
	for _, key := range slices.Sorted(maps.Keys(c.Local)) {
		problems = append(problems, checkKey("local", key)...)
		if m, managed := c.Managed[key]; managed && !m.Overridable {
			add("local %q overrides a managed key that is not overridable", key)
		}
	}

	// Each registered setting's own check, in registry order; the Claude skill policies need the declared profiles,
	// so checkClaudeSkills checks them.
	for _, s := range settingRegistry {
		if s.check == nil {
			continue
		}
		if m, ok := c.Managed[s.key]; ok {
			if err := s.check(m.Value); err != nil {
				add("managed %v", err)
			}
		}
		if v, ok := c.Local[s.key]; ok {
			if err := s.check(v); err != nil {
				add("local %v", err)
			}
		}
	}
	if quiet, err := c.WakeQuiet(); err == nil {
		if most, err := c.WakeMaxDelay(); err == nil && most < quiet {
			add("%s %s must be at least %s %s", WakeMaxDelayKey, most, WakeQuietKey, quiet)
		}
	}
	problems = append(problems, c.checkQuality()...)
	problems = append(problems, c.checkClaudeSkills()...)
	problems = append(problems, c.checkRelease()...)

	for _, name := range slices.Sorted(maps.Keys(c.Profiles)) {
		p := c.Profiles[name]
		if !workflow.ValidID(name) {
			add("invalid profile name %q", name)
			continue
		}
		switch {
		case p.Adapter == "":
			add("profile %q: adapter is required", name)
		case !workflow.ValidID(p.Adapter):
			add("profile %q: invalid adapter %q", name, p.Adapter)
		}
		if p.Role != "" && !configRelative(p.Role) {
			add("profile %q: role must be a relative slash-separated path inside the config directory", name)
		}
		if p.Adapter == adapter.CodexName && p.AllowedToolsSet() {
			add("profile %q: runs the %s adapter, which runs read-only and grants no allowed_tools; remove allowed_tools", name, p.Adapter)
		}
		problems = append(problems, checkAllowedTools(fmt.Sprintf("profile %q", name), p.AllowedToolsList())...)
		problems = append(problems, checkInheritEnv(fmt.Sprintf("profile %q", name), p.InheritEnv)...)
	}

	if st := c.Steward; st != nil {
		if st.Name != "" && !workflow.ValidID(st.Name) {
			add("steward: invalid name %q", st.Name)
		}
		if st.Adapter != "" && !workflow.ValidID(st.Adapter) {
			add("steward: invalid adapter %q", st.Adapter)
		}
		if st.Role != "" && !configRelative(st.Role) {
			add("steward: role must be a relative slash-separated path inside the config directory")
		}
		if _, _, err := st.CollectEveryDuration(); err != nil {
			add("%v", err)
		}
		if _, err := st.ReportUnreachableAfterDuration(); err != nil {
			add("%v", err)
		}
		if _, err := st.ReportDirectorAfterDuration(); err != nil {
			add("%v", err)
		}
	}
	if w := c.Web; w != nil {
		problems = append(problems, checkWeb(*w)...)
	}
	if b := c.Backup; b != nil && c.Mode == ModeDirector {
		problems = append(problems, checkBackup(*b, c.Name)...)
	}
	problems = append(problems, checkPeers(c.Peers, c.Mode, c.Name)...)
	if d := c.Director; d != nil {
		if d.Adapter != "" && !workflow.ValidID(d.Adapter) {
			add("director: invalid adapter %q", d.Adapter)
		}
		if d.Role != "" && !configRelative(d.Role) {
			add("director: role must be a relative slash-separated path inside the config directory")
		}
		if d.IdleAfter != "" {
			if _, err := parseIdleAfter(d.IdleAfter); err != nil {
				add("director: %v", err)
			}
		}
		if d.HandoffAfterTasks < 0 {
			add("director: handoff_after_tasks %d must not be negative", d.HandoffAfterTasks)
		}
		if p := d.HandoffContextPercent; p != nil && (*p < 0 || *p > 100) {
			add("director: handoff_context_percent %d must be between 0 and 100", *p)
		}
		if d.HandoffIdle != "" {
			if _, err := parseHandoffIdle(d.HandoffIdle); err != nil {
				add("director: %v", err)
			}
		}
		if p := d.NoteAuditEverySessions; p != nil && *p < 0 {
			add("director: note_audit_every_sessions %d must not be negative", *p)
		}
		if p := d.HallmarkAfterMerges; p != nil && *p < 0 {
			add("director: hallmark_after_merges %d must not be negative", *p)
		}
	}

	seenID, seenPath := map[string]bool{}, map[string]bool{}
	prefixOwner := map[string]string{}
	var machines []string
	for i, r := range c.Repositories {
		name := fmt.Sprintf("repository %d", i+1)
		if !workflow.ValidID(r.ID) {
			add("%s: invalid repository id %q", name, r.ID)
		} else if r.ID == ManagedAlertRepository {
			add("repository id %q is reserved for the Director store's managed-mode records; choose another id", r.ID)
		} else if r.ID == MessagesRepository {
			add("repository id %q is reserved for the Director store's messaging records; choose another id", r.ID)
		} else {
			name = fmt.Sprintf("repository %q", r.ID)
		}
		if seenID[r.ID] {
			add("duplicate repository id %q", r.ID)
		}
		seenID[r.ID] = true
		if p, set := r.TaskIDPrefix(); set {
			if other, dup := prefixOwner[p]; !ValidTaskPrefix(p) {
				add("%s: task_prefix %q must be 1 to 3 lowercase letters", name, p)
			} else if dup {
				add("duplicate task_prefix %q: repositories %q and %q both use it", p, other, r.ID)
			} else {
				prefixOwner[p] = r.ID
			}
		}
		if !ownerAbsolute(r.Path) {
			add("%s: path must be absolute", name)
		} else if clean := ownerPathKey(r.Path); seenPath[clean] {
			add("%s: path is already used by another repository", name)
		} else {
			seenPath[clean] = true
		}
		if strings.TrimSpace(r.Remote) == "" {
			add("%s: remote is required", name)
		} else if hasCredential(r.Remote) {
			add("%s: remote must not contain credentials", name)
		}
		if r.OwnerMachine == "" {
			add("%s: owner_machine is required", name)
		} else if !slices.Contains(machines, r.OwnerMachine) {
			machines = append(machines, r.OwnerMachine)
		}
		if !workflow.ValidID(r.Workflow) {
			add("%s: invalid workflow name %q", name, r.Workflow)
		}
		for _, k := range slices.Sorted(maps.Keys(r.Kinds)) {
			row, known := TaskKindRowOf(k)
			switch {
			case known && row.FeatureWorkflow:
				add("%s: kinds.%s is not accepted; the repository's workflow setting is the %s kind's workflow", name, k, k)
			case !known:
				add("%s: unknown task kind %q in kinds; the only ones are %s", name, k, kindsChoice())
			case !workflow.ValidID(r.Kinds[k]):
				add("%s: kinds.%s: invalid workflow name %q", name, k, r.Kinds[k])
			}
		}
		if !r.FlakyChecks.Valid() {
			add("%s: flaky_checks %q must be off, rerun or rerun-and-file", name, r.FlakyChecks)
		}
		if !r.CIRetrigger.Valid() {
			add("%s: ci_retrigger %q must be off or reopen", name, r.CIRetrigger)
		}
		m := r.Merge
		switch k := m.ReapproveAfterRebase; {
		case k != "" && k.Rank() < 0:
			add("%s: merge.reapprove_after_rebase %q must be no_conflicts, minor_conflicts, major_conflicts or never", name, k)
		case k == RebaseNever && !m.Requires.Artisan():
			add("%s: merge.reapprove_after_rebase = \"never\" needs merge.requires.artisan_approval", name)
		}
		if p := m.MinorConflictFiles; p != nil && *p < 1 {
			add("%s: merge.minor_conflict_files must be at least 1", name)
		}
		if p := m.MinorConflictLines; p != nil && *p < 1 {
			add("%s: merge.minor_conflict_lines must be at least 1", name)
		}
		if p := m.MergeBatchSize; p != nil && *p < 1 {
			add("%s: merge.merge_batch_size must be at least 1", name)
		}
		for i, g := range m.Regenerate {
			where := fmt.Sprintf("%s: merge.regenerate[%d]", name, i)
			if len(g.Paths) == 0 {
				add("%s: paths must name at least one directory prefix", where)
			}
			for _, p := range g.Paths {
				if problem := regeneratePathProblem(p); problem != "" {
					add("%s: path %q %s", where, p, problem)
				}
			}
			if len(g.Command) == 0 || g.Command[0] == "" {
				add("%s: command must be a non-empty argv", where)
			}
		}
		for _, profile := range slices.Sorted(maps.Keys(r.Profiles)) {
			where := fmt.Sprintf("%s: profile %q", name, profile)
			if p, ok := c.Profiles[profile]; !ok {
				add("%s: the copy declares no such profile", where)
			} else if p.Adapter != "claude" {
				add("%s: runs the %s adapter, which grants no allowed_tools; only the claude adapter does", where, p.Adapter)
			}
			problems = append(problems, checkAllowedTools(where, r.Profiles[profile].AllowedTools)...)
		}
	}
	if len(machines) > 1 {
		add("a director has one owner machine, but repositories name %s", strings.Join(machines, ", "))
	}
	return problems
}

// checkAllowedTools reports each rule of the allowed_tools list for where that CheckAllowedTool refuses or that the
// list repeats.
func checkAllowedTools(where string, rules []string) []string {
	var problems []string
	seen := map[string]bool{}
	for _, rule := range rules {
		if err := CheckAllowedTool(rule); err != nil {
			problems = append(problems, fmt.Sprintf("%s: allowed_tools: %v", where, err))
		} else if seen[rule] {
			problems = append(problems, fmt.Sprintf("%s: allowed_tools lists %q twice", where, rule))
		}
		seen[rule] = true
	}
	return problems
}

// envNamePattern is an environment variable name in the portable shell form.
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkInheritEnv reports each inherit_env entry for where that is not an environment variable name or that the
// list repeats. A name such as API_KEY is allowed: it names a secret and holds none.
func checkInheritEnv(where string, names []string) []string {
	var problems []string
	seen := map[string]bool{}
	for _, n := range names {
		if !envNamePattern.MatchString(n) {
			problems = append(problems, fmt.Sprintf("%s: inherit_env: %q is not an environment variable name", where, n))
		} else if seen[n] {
			problems = append(problems, fmt.Sprintf("%s: inherit_env lists %q twice", where, n))
		}
		seen[n] = true
	}
	return problems
}

// CheckKey reports why key cannot name a managed or local setting, or nil if it can.
func CheckKey(key string) error {
	if problems := checkKey("setting", key); len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func checkKey(section, key string) []string {
	var problems []string
	if !keyPattern.MatchString(key) {
		problems = append(problems, fmt.Sprintf("%s: invalid key %q", section, key))
	}
	if credentialPattern.MatchString(key) {
		problems = append(problems, fmt.Sprintf("%s: key %q looks like a credential; credentials never belong in config", section, key))
	}
	return problems
}

// hasCredential reports whether a location embeds a password or, for web URLs, any user info.
// SSH user names such as git@host are not credentials.
func hasCredential(location string) bool {
	u, err := url.Parse(location)
	if err != nil || u.User == nil {
		return false
	}
	if _, set := u.User.Password(); set {
		return true
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// Setting returns the effective value of a preference:
// the local value where the managed key allows it or no managed key exists, otherwise the managed value.
func (c *Config) Setting(key string) (string, bool) {
	m, managed := c.Managed[key]
	if v, local := c.Local[key]; local && (!managed || m.Overridable) {
		return v, true
	}
	return m.Value, managed
}

// StallInterval returns the effective StallIntervalKey setting as a duration: DefaultStallInterval when
// it is not set, and zero, which disables worker-stalled observations, for "0s".
// A value that is not a nonnegative Go duration is an error; Parse refuses one already.
func (c *Config) StallInterval() (time.Duration, error) {
	v, ok := c.Setting(StallIntervalKey)
	if !ok {
		return DefaultStallInterval, nil
	}
	return parseStallInterval(v)
}

func parseStallInterval(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s %q must be a nonnegative duration such as \"15m\", or \"0s\" to disable", StallIntervalKey, v)
	}
	return d, nil
}

// WakeQuiet returns the effective WakeQuietKey setting as a duration: DefaultWakeQuiet when it is not set, and
// zero, which hands every message wake at once, for "0s".
// A value that is not a nonnegative Go duration is an error; Parse refuses one already.
func (c *Config) WakeQuiet() (time.Duration, error) {
	v, ok := c.Setting(WakeQuietKey)
	if !ok {
		return DefaultWakeQuiet, nil
	}
	return parseWakeQuiet(v)
}

func parseWakeQuiet(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s %q must be a nonnegative duration such as \"3s\", or \"0s\" to wake at once", WakeQuietKey, v)
	}
	return d, nil
}

// WakeMaxDelay returns the effective WakeMaxDelayKey setting as a duration: DefaultWakeMaxDelay when it is not
// set. A value that is not a positive Go duration is an error; Parse refuses one already, and one less than
// WakeQuiet.
func (c *Config) WakeMaxDelay() (time.Duration, error) {
	v, ok := c.Setting(WakeMaxDelayKey)
	if !ok {
		return DefaultWakeMaxDelay, nil
	}
	return parseWakeMaxDelay(v)
}

// MailboxMax returns the effective MailboxMaxKey setting: DefaultMailboxMax when it is not set. A value that is not a
// whole number from 1 to 10000 is an error; Parse refuses one already.
func (c *Config) MailboxMax() (int, error) {
	v, ok := c.Setting(MailboxMaxKey)
	if !ok {
		return DefaultMailboxMax, nil
	}
	return parseMailboxMax(v)
}

func parseMailboxMax(v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 10000 || strconv.Itoa(n) != v {
		return 0, fmt.Errorf("%s %q must be a whole number from 1 to 10000, such as \"200\"", MailboxMaxKey, v)
	}
	return n, nil
}

func parseWakeMaxDelay(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s %q must be a positive duration such as \"15s\"", WakeMaxDelayKey, v)
	}
	return d, nil
}

// ChecksRefresh returns the effective ChecksRefreshKey setting as a duration: DefaultChecksRefresh when it is
// not set, and zero, which reads the checks at every advance, for "0s".
// A value that is not a nonnegative Go duration is an error; Parse refuses one already.
func (c *Config) ChecksRefresh() (time.Duration, error) {
	v, ok := c.Setting(ChecksRefreshKey)
	if !ok {
		return DefaultChecksRefresh, nil
	}
	return parseChecksRefresh(v)
}

func parseChecksRefresh(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s %q must be a nonnegative duration such as \"1m\", or \"0s\" to read at every advance", ChecksRefreshKey, v)
	}
	return d, nil
}

// CIStartGrace returns the effective CIStartGraceKey setting as a duration: DefaultCIStartGrace when it is not
// set, and zero, which reports a head with no checks at the first read that sees none, for "0s". A value that is
// not a nonnegative Go duration is an error; Parse refuses one already.
func (c *Config) CIStartGrace() (time.Duration, error) {
	v, ok := c.Setting(CIStartGraceKey)
	if !ok {
		return DefaultCIStartGrace, nil
	}
	return parseCIStartGrace(v)
}

func parseCIStartGrace(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s %q must be a nonnegative duration such as \"10m\"", CIStartGraceKey, v)
	}
	return d, nil
}

// CILogLimit returns the effective CILogLimitKey setting in bytes: DefaultCILogLimit when it is not set, and
// zero, which fetches no failed job log, for "0". A value that is not whole bytes, with an optional KiB, MiB, KB
// or MB suffix (all 1024-based), from 64 KiB to 100 MiB is an error; Parse refuses one already.
func (c *Config) CILogLimit() (int64, error) {
	v, ok := c.Setting(CILogLimitKey)
	if !ok {
		return DefaultCILogLimit, nil
	}
	return parseCILogLimit(v)
}

// The bounds of a nonzero CILogLimitKey.
const (
	minCILogLimit int64 = 64 << 10
	maxCILogLimit int64 = 100 << 20
)

func parseCILogLimit(v string) (int64, error) {
	bad := fmt.Errorf("%s %q must be whole bytes from 64KiB to 100MiB, such as \"5MiB\", \"512KiB\" or \"1048576\", or \"0\" to fetch no logs", CILogLimitKey, v)
	digits, unit := v, int64(1)
	for _, s := range []struct {
		suffix string
		unit   int64
	}{{"KiB", 1 << 10}, {"KB", 1 << 10}, {"MiB", 1 << 20}, {"MB", 1 << 20}} {
		if d, ok := strings.CutSuffix(v, s.suffix); ok {
			digits, unit = d, s.unit
			break
		}
	}
	// At most 12 digits keeps the product with the unit inside int64.
	if digits == "" || len(digits) > 12 || strings.Trim(digits, "0123456789") != "" {
		return 0, bad
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, bad
	}
	n *= unit
	if n != 0 && (n < minCILogLimit || n > maxCILogLimit) {
		return 0, bad
	}
	return n, nil
}

// ArtisanWorkspaceCloseAfter returns the effective ArtisanWorkspaceCloseAfterKey setting as a duration:
// DefaultArtisanWorkspaceCloseAfter when it is not set, and zero, which never closes a Herdr workspace
// automatically, for "0" or "0s". A value that is not a nonnegative Go duration is an error; Parse refuses one
// already.
func (c *Config) ArtisanWorkspaceCloseAfter() (time.Duration, error) {
	v, ok := c.Setting(ArtisanWorkspaceCloseAfterKey)
	if !ok {
		return DefaultArtisanWorkspaceCloseAfter, nil
	}
	return parseArtisanWorkspaceCloseAfter(v)
}

func parseArtisanWorkspaceCloseAfter(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s %q must be a nonnegative duration such as \"5m\", or \"0\" never to close automatically", ArtisanWorkspaceCloseAfterKey, v)
	}
	return d, nil
}

// ArtisanPaneOutput returns the effective ArtisanPaneOutputKey setting: workbench.OutputWorkbench when it is not
// set, or workbench.OutputPlain. Any other value is an error; Parse refuses one already.
func (c *Config) ArtisanPaneOutput() (string, error) {
	v, ok := c.Setting(ArtisanPaneOutputKey)
	if !ok {
		return workbench.OutputWorkbench, nil
	}
	return v, checkArtisanPaneOutput(v)
}

func checkArtisanPaneOutput(v string) error {
	if v != workbench.OutputWorkbench && v != workbench.OutputPlain {
		return fmt.Errorf("%s %q must be %q or %q", ArtisanPaneOutputKey, v, workbench.OutputWorkbench, workbench.OutputPlain)
	}
	return nil
}

// PruneAfter returns the effective PruneAfterKey setting as a duration: DefaultPruneAfter when it is not set, and
// zero, which makes a task prunable as soon as it closes, for "0" or "0s". A value that is not a nonnegative Go
// duration is an error; Parse refuses one already.
func (c *Config) PruneAfter() (time.Duration, error) {
	v, ok := c.Setting(PruneAfterKey)
	if !ok {
		return DefaultPruneAfter, nil
	}
	return parsePruneAfter(v)
}

func parsePruneAfter(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s %q must be a nonnegative duration such as \"336h\" (14 days), or \"0\" to prune as soon as a task closes", PruneAfterKey, v)
	}
	return d, nil
}

// PruneDaily returns the effective PruneDailyKey setting: false when it is not set.
// A value other than "true" or "false" is an error; Parse refuses one already.
func (c *Config) PruneDaily() (bool, error) {
	v, ok := c.Setting(PruneDailyKey)
	if !ok {
		return false, nil
	}
	return parsePruneDaily(v)
}

func parsePruneDaily(v string) (bool, error) {
	switch v {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("%s %q must be \"true\" or \"false\"", PruneDailyKey, v)
}

// LockWait returns the effective LockWaitKey setting as a duration: DefaultLockWait when it is not set.
// A value that is not a positive Go duration is an error; Parse refuses one already.
func (c *Config) LockWait() (time.Duration, error) {
	v, ok := c.Setting(LockWaitKey)
	if !ok {
		return DefaultLockWait, nil
	}
	return parseLockWait(v)
}

func parseLockWait(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s %q must be a positive duration such as \"30s\"", LockWaitKey, v)
	}
	return d, nil
}

// HerdrSession returns the effective HerdrSessionKey setting: the named Herdr session the Director's tab opens
// in, or "" when it is not set.
// A value Herdr would not take as a session name, or Herdr's default session, is an error; Parse refuses one
// already.
func (c *Config) HerdrSession() (string, error) {
	v, ok := c.Setting(HerdrSessionKey)
	if !ok {
		return "", nil
	}
	return v, checkHerdrSession(v)
}

// herdrSessionName is the shape of a Herdr session name the herdr client accepts.
var herdrSessionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func checkHerdrSession(v string) error {
	switch {
	case v == "default":
		return fmt.Errorf(`%s "default" is refused: name a dedicated Herdr session`, HerdrSessionKey)
	case !herdrSessionName.MatchString(v):
		return fmt.Errorf("%s %q must be letters, digits, '-' or '_', starting with a letter or digit", HerdrSessionKey, v)
	}
	return nil
}

// IdleAfter returns the effective [director] idle_after as a duration: DefaultIdleAfter when it is not set.
// A value that is not a positive Go duration is an error; Parse refuses one already.
func (c *Config) IdleAfter() (time.Duration, error) {
	if c.Director == nil || c.Director.IdleAfter == "" {
		return DefaultIdleAfter, nil
	}
	return parseIdleAfter(c.Director.IdleAfter)
}

// Handoff returns the effective [director] handoff triggers: DefaultHandoffContextPercent and the others off when
// they are not set. A value Parse would refuse is an error.
func (c *Config) Handoff() (Handoff, error) {
	h := Handoff{ContextPercent: DefaultHandoffContextPercent}
	d := c.Director
	if d == nil {
		return h, nil
	}
	h.AfterTasks = d.HandoffAfterTasks
	if d.HandoffContextPercent != nil {
		h.ContextPercent = *d.HandoffContextPercent
	}
	var err error
	if d.HandoffIdle != "" {
		h.Idle, err = parseHandoffIdle(d.HandoffIdle)
	}
	if err == nil && (h.AfterTasks < 0 || h.ContextPercent < 0 || h.ContextPercent > 100) {
		err = fmt.Errorf("handoff thresholds %d tasks and %d%% are out of range", h.AfterTasks, h.ContextPercent)
	}
	return h, err
}

// Maintenance returns the effective [director] maintenance cadences: DefaultNoteAuditEverySessions and
// DefaultHallmarkAfterMerges when they are not set. A value Parse would refuse is an error.
func (c *Config) Maintenance() (Maintenance, error) {
	m := Maintenance{NoteAuditEverySessions: DefaultNoteAuditEverySessions, HallmarkAfterMerges: DefaultHallmarkAfterMerges}
	if d := c.Director; d != nil {
		if d.NoteAuditEverySessions != nil {
			m.NoteAuditEverySessions = *d.NoteAuditEverySessions
		}
		if d.HallmarkAfterMerges != nil {
			m.HallmarkAfterMerges = *d.HallmarkAfterMerges
		}
	}
	if m.NoteAuditEverySessions < 0 || m.HallmarkAfterMerges < 0 {
		return m, fmt.Errorf("maintenance cadences %d sessions and %d merges must not be negative", m.NoteAuditEverySessions, m.HallmarkAfterMerges)
	}
	return m, nil
}

func parseHandoffIdle(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("handoff_idle %q must be a duration such as \"30m\", or \"0s\" for off", v)
	}
	return d, nil
}

func parseIdleAfter(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("idle_after %q must be a positive duration such as \"5m\"", v)
	}
	return d, nil
}

// Profile returns the profile with this name, or an error wrapping ErrUnknownProfile.
func (c *Config) Profile(name string) (Profile, error) {
	p, ok := c.Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("%w %q", ErrUnknownProfile, name)
	}
	return p, nil
}

// CheckProfiles refuses a workflow whose steps name a profile this config does not declare,
// listing every such step in one error that wraps ErrUnknownProfile.
// Run it with the repository's effective workflow before a task binds it.
func (c *Config) CheckProfiles(def *workflow.Definition) error {
	var problems []string
	for _, step := range def.Steps {
		if name := step.Profile(); name != "" {
			if _, ok := c.Profiles[name]; !ok {
				problems = append(problems, fmt.Sprintf("step %q names unknown profile %q", step.ID, name))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("workflow %q: %w: %s", def.Name, ErrUnknownProfile, strings.Join(problems, "; "))
	}
	return nil
}

// Repository returns the repository with this id.
func (c *Config) Repository(id string) (Repository, bool) {
	for _, r := range c.Repositories {
		if r.ID == id {
			return r, true
		}
	}
	return Repository{}, false
}

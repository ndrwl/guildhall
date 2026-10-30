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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"

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

// TaskKinds lists every task kind, the default first.
var TaskKinds = []string{TaskKindFeature, TaskKindBug, TaskKindHallmark}

// TaskKindWorkflow returns the workflow name repository r runs for a task of kind taskKind, and whether the kind is
// known.
func (r Repository) TaskKindWorkflow(taskKind string) (string, bool) {
	switch taskKind {
	case TaskKindFeature:
		return r.Workflow, true
	case TaskKindBug, TaskKindHallmark:
		if name := r.Kinds[taskKind]; name != "" {
			return name, true
		}
		return taskKind, true
	}
	return "", false
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

// ChecksRefreshKey is the setting that sets how long a waiting validate step waits between reads of the same
// head's checks (docs/configuration.md#settings).
const ChecksRefreshKey = "checks_refresh"

// DefaultChecksRefresh is the checks refresh of a config that does not set ChecksRefreshKey.
const DefaultChecksRefresh = time.Minute

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
	// Merge holds what deliver requires before it merges; a missing section requires everything.
	Merge MergeOptions `toml:"merge"`
	// Workflow names the Director's workflow definition this repository runs for a feature task.
	Workflow string `toml:"workflow"`
	// Kinds names the workflow this repository runs for each other task kind, keyed by kind (TaskKindBug or TaskKindHallmark); a kind
	// it does not name runs the shipped workflow of that name. It is omitted from JSON when unset, so the config
	// digest of a config without it is unchanged.
	Kinds map[string]string `toml:"kinds" json:",omitempty"`
	// WorkflowOverrides chooses step actors for this repository only, keyed by step id (workflow.ApplyOverrides).
	WorkflowOverrides map[string]workflow.Override `toml:"workflow_overrides"`
	// NoCI declares that the repository runs no checks, so validate passes a head its forge reports none for.
	NoCI bool `toml:"no_ci" json:",omitempty"`
	// Profiles are this repository's grants, keyed by a declared claude profile's name: each adds its rules to that
	// profile's effective grant for this repository's tasks only (docs/adapters.md#artisan-permissions).
	// It is omitted from JSON when unset, so the config digest of a config without it is unchanged.
	Profiles map[string]RepositoryProfile `toml:"profiles" json:",omitempty"`
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
		problems = append(problems, checkAllowedTools(fmt.Sprintf("profile %q", name), p.AllowedToolsList())...)
	}

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
	var machines []string
	for i, r := range c.Repositories {
		name := fmt.Sprintf("repository %d", i+1)
		if !workflow.ValidID(r.ID) {
			add("%s: invalid repository id %q", name, r.ID)
		} else {
			name = fmt.Sprintf("repository %q", r.ID)
		}
		if seenID[r.ID] {
			add("duplicate repository id %q", r.ID)
		}
		seenID[r.ID] = true
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
			switch {
			case k == TaskKindFeature:
				add("%s: kinds.feature is not accepted; the repository's workflow setting is the feature kind's workflow", name)
			case k != TaskKindBug && k != TaskKindHallmark:
				add("%s: unknown task kind %q in kinds; the only ones are %s and %s", name, k, TaskKindBug, TaskKindHallmark)
			case !workflow.ValidID(r.Kinds[k]):
				add("%s: kinds.%s: invalid workflow name %q", name, k, r.Kinds[k])
			}
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

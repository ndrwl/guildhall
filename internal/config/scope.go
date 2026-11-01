package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Scope is where a setting applies (docs/configuration.md#setting-scopes): to the task Guildhall is running, so a
// task override may replace it, or to the whole copy, outside any one task.
type Scope string

const (
	// ScopeTask is a task-scoped setting: Guildhall reads it while running one task, so a task may override it.
	ScopeTask Scope = "task"
	// ScopeMachine is a machine-wide setting: it is read for the whole copy, and a task can never override it.
	ScopeMachine Scope = "machine"
)

// StandardsRevisionKey is the setting that names the central standards revision reviews are bound to
// (docs/configuration.md#settings).
const StandardsRevisionKey = "standards_revision"

// StepKeyPrefix starts a step key, such as step.build.effort, which overrides a workflow step for one task rather
// than a setting; WithTaskOverrides leaves step keys to the workflow.
const StepKeyPrefix = "step."

// MergeRequiresKeyPrefix starts a task override of one merge requirement, such as merge.requires.ci_green, which may
// only turn that [repository.merge.requires] key on (docs/configuration.md#setting-scopes); repository. before it is
// an alias. MergeRequires.WithTaskOverrides applies these; WithTaskOverrides leaves them out of Local.
const MergeRequiresKeyPrefix = "merge.requires."

// mergeRequirements names each [repository.merge.requires] key, in its documented order, with its field.
var mergeRequirements = []struct {
	name  string
	field func(*MergeRequires) **bool
}{
	{"guildmaster_approval", func(m *MergeRequires) **bool { return &m.GuildmasterApproval }},
	{"artisan_approval", func(m *MergeRequires) **bool { return &m.ArtisanApproval }},
	{"ci_green", func(m *MergeRequires) **bool { return &m.CIGreen }},
	{"nothing_open", func(m *MergeRequires) **bool { return &m.NothingOpen }},
}

// mergeRequiresOverride returns the requirement name a merge-requires override key names, under either spelling, and
// whether key is one; the name may be one no requirement has.
func mergeRequiresOverride(key string) (string, bool) {
	return strings.CutPrefix(strings.TrimPrefix(key, "repository."), MergeRequiresKeyPrefix)
}

// mergeRequiresRefusal reports why the merge-requires override name = value cannot apply, or nil: only true, for a
// requirement that exists, may be set, so a task can make its merge requirements stricter and never looser.
func mergeRequiresRefusal(name, value string) error {
	known := make([]string, len(mergeRequirements))
	for i, r := range mergeRequirements {
		known[i] = r.name
	}
	key := MergeRequiresKeyPrefix + name
	switch {
	case !slices.Contains(known, name):
		return fmt.Errorf("`%s`: unknown merge requirement %q; want one of %s", key, name, strings.Join(known, ", "))
	case value == "false":
		return fmt.Errorf("`%s`: a task override may only make merge requirements stricter; it cannot turn one off", key)
	case value != "true":
		return fmt.Errorf("`%s` = %q is not a boolean; a task override may only set it to true", key, value)
	}
	return nil
}

// ErrNotTaskOverridable reports a key a task override may not name: a machine-wide setting, a key kept out on
// purpose, or one pinned by a managed setting that is not overridable.
var ErrNotTaskOverridable = errors.New("not overridable per task")

// ErrUnknownSetting reports a key the setting registry does not name.
var ErrUnknownSetting = errors.New("unknown setting")

// settingEntry is one key of the setting registry: its scope and its value check.
type settingEntry struct {
	key   string
	scope Scope
	// prefix makes the entry name every key that starts with key, such as claude_skills_profile_<name>.
	prefix bool
	// check reports why v cannot be the setting's value, naming the key; nil when any value parses, or when a check
	// that needs the rest of the config covers it (checkClaudeSkills).
	check func(v string) error
}

// settingRegistry is every setting Guildhall reads, each declared once with its scope; validate runs each check on
// the managed and local sections in this order. TestEveryKeyIsRegistered fails for an exported *Key constant missing
// here, so no setting lands without a scope.
var settingRegistry = []settingEntry{
	{key: StallIntervalKey, scope: ScopeTask, check: func(v string) error { _, err := parseStallInterval(v); return err }},
	{key: MaxRunningAttemptsKey, scope: ScopeMachine, check: capCheck(MaxRunningAttemptsKey)},
	{key: MaxAttemptsPerDayKey, scope: ScopeMachine, check: capCheck(MaxAttemptsPerDayKey)},
	{key: HerdrSessionKey, scope: ScopeMachine, check: checkHerdrSession},
	{key: WakeQuietKey, scope: ScopeMachine, check: func(v string) error { _, err := parseWakeQuiet(v); return err }},
	{key: WakeMaxDelayKey, scope: ScopeMachine, check: func(v string) error { _, err := parseWakeMaxDelay(v); return err }},
	{key: ChecksRefreshKey, scope: ScopeTask, check: func(v string) error { _, err := parseChecksRefresh(v); return err }},
	{key: CILogLimitKey, scope: ScopeTask, check: func(v string) error { _, err := parseCILogLimit(v); return err }},
	{key: ArtisanWorkspaceCloseAfterKey, scope: ScopeMachine, check: func(v string) error { _, err := parseArtisanWorkspaceCloseAfter(v); return err }},
	{key: ArtisanPaneOutputKey, scope: ScopeMachine, check: checkArtisanPaneOutput},
	{key: PruneAfterKey, scope: ScopeMachine, check: func(v string) error { _, err := parsePruneAfter(v); return err }},
	{key: PruneDailyKey, scope: ScopeMachine, check: func(v string) error { _, err := parsePruneDaily(v); return err }},
	{key: LockWaitKey, scope: ScopeMachine, check: func(v string) error { _, err := parseLockWait(v); return err }},
	{key: DefaultEffortKey, scope: ScopeTask, check: checkDefaultEffort},
	{key: QualitySelfReviewKey, scope: ScopeTask, check: qualityCheck(QualitySelfReviewKey)},
	{key: QualitySizeBudgetLinesKey, scope: ScopeTask, check: qualityCheck(QualitySizeBudgetLinesKey)},
	{key: QualitySizeBudgetFilesKey, scope: ScopeTask, check: qualityCheck(QualitySizeBudgetFilesKey)},
	{key: QualityAuditSampleKey, scope: ScopeTask, check: qualityCheck(QualityAuditSampleKey)},
	{key: QualityReferenceRunsKey, scope: ScopeTask, check: qualityCheck(QualityReferenceRunsKey)},
	{key: QualityReferenceRunsPerTaskKey, scope: ScopeTask, check: qualityCheck(QualityReferenceRunsPerTaskKey)},
	{key: QualityReferenceCostCapKey, scope: ScopeTask, check: qualityCheck(QualityReferenceCostCapKey)},
	{key: QualityJudgeSeparateFamilyKey, scope: ScopeTask, check: qualityCheck(QualityJudgeSeparateFamilyKey)},
	{key: QualityJudgeProfilesKey, scope: ScopeTask, check: qualityCheck(QualityJudgeProfilesKey)},
	{key: QualitySkillEditsNeedApprovalKey, scope: ScopeTask, check: qualityCheck(QualitySkillEditsNeedApprovalKey)},
	// The Claude wrapper re-reads the skill policies from the copy's config at launch, outside any task.
	{key: ClaudeSkillsDirectorKey, scope: ScopeMachine},
	{key: ClaudeSkillsArtisanKey, scope: ScopeMachine},
	{key: ClaudeSkillsProfilePrefix, scope: ScopeMachine, prefix: true},
	// The standards revision binds reviews, so a per-task value would weaken the standards gate.
	{key: StandardsRevisionKey, scope: ScopeMachine},
	// The release pin is the copy's, read by the launcher and config reload, never by a task (checkRelease).
	{key: ReleaseKey, scope: ScopeMachine, check: checkReleaseVersion},
	{key: ReleaseSHA256Key, scope: ScopeMachine, check: checkReleaseSHA256},
}

// excludedEntry is config a task override may never name, kept in the registry so a refusal can name it.
type excludedEntry struct {
	name     string
	keys     []string
	prefixes []string
}

var excludedRegistry = []excludedEntry{
	{name: "`[repository.merge]`",
		keys:     []string{"repository.merge", "merge", "requires", "reapprove_after_rebase", "minor_conflict_files", "minor_conflict_lines"},
		prefixes: []string{"repository.merge.", "merge."}},
	{name: "`[director]`",
		keys: []string{"director", "idle_after", "handoff_after_tasks", "handoff_context_percent", "handoff_idle",
			"note_audit_every_sessions", "hallmark_after_merges"},
		prefixes: []string{"director."}},
	{name: "repository identity",
		keys: []string{"id", "path", "remote", "owner_machine", "repository.id", "repository.path", "repository.remote",
			"repository.owner_machine"}},
}

func capCheck(key string) func(string) error {
	return func(v string) error { _, err := parseCap(key, v); return err }
}

func qualityCheck(key string) func(string) error {
	for _, s := range qualitySettings {
		if s.key == key {
			return func(v string) error { var scratch Quality; return s.set(&scratch, v) }
		}
	}
	panic("config: no quality setting " + key)
}

func lookupSetting(key string) (settingEntry, bool) {
	for _, s := range settingRegistry {
		if s.prefix && strings.HasPrefix(key, s.key) && len(key) > len(s.key) || !s.prefix && s.key == key {
			return s, true
		}
	}
	return settingEntry{}, false
}

// SettingScope returns the scope of the setting key, and false when the registry names no such setting.
func SettingScope(key string) (Scope, bool) {
	s, ok := lookupSetting(key)
	return s.scope, ok
}

// taskOverrideRefusal reports why key = value cannot override a setting for one task under c, judged on its own:
// an excluded, unknown, machine-wide or pinned key, or a value its check refuses. It is nil if the override applies.
// A merge-requires key is judged by mergeRequiresRefusal, before the rest of [repository.merge] is kept out.
func (c *Config) taskOverrideRefusal(key, value string) error {
	if name, ok := mergeRequiresOverride(key); ok {
		return mergeRequiresRefusal(name, value)
	}
	for _, e := range excludedRegistry {
		if slices.Contains(e.keys, key) || slices.ContainsFunc(e.prefixes, func(p string) bool { return strings.HasPrefix(key, p) }) {
			return fmt.Errorf("%s is %w", e.name, ErrNotTaskOverridable)
		}
	}
	s, ok := lookupSetting(key)
	switch {
	case !ok:
		return fmt.Errorf("%w %q", ErrUnknownSetting, key)
	case s.scope != ScopeTask:
		return fmt.Errorf("`%s` is machine-wide; it cannot be overridden per task: %w", key, ErrNotTaskOverridable)
	}
	if m, managed := c.Managed[key]; managed && !m.Overridable {
		return fmt.Errorf("`%s` is pinned by a managed setting that is not overridable: %w", key, ErrNotTaskOverridable)
	}
	if s.check != nil {
		if err := s.check(value); err != nil {
			return err
		}
	}
	return nil
}

// CheckTaskOverride reports why key = value cannot override a setting for one task under c, or nil if it can: the
// key must be a task-scoped setting the managed section does not pin, and the value must pass the setting's check and
// leave the config valid. A step key is not a setting, so it is refused here.
func (c *Config) CheckTaskOverride(key, value string) error {
	if err := c.taskOverrideRefusal(key, value); err != nil {
		return err
	}
	_, skipped, err := c.WithTaskOverrides(map[string]string{key: value})
	if err != nil {
		return err
	}
	if len(skipped) > 0 {
		return errors.New(skipped[0].Reason)
	}
	return nil
}

// Skipped is a task override that does not apply when it is read, with the reason.
type Skipped struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Reason string `json:"reason"`
}

// WithTaskOverrides returns a copy of c whose effective settings take the task's overrides, key to value, first:
// each goes into the copy's Local, so precedence is task override, then local, then managed, and a managed key that
// is not overridable still wins. It never mutates c; the copy shares c's other maps, which callers must not change.
// Step keys are left to the workflow, and merge-requires keys to MergeRequires.WithTaskOverrides, which reports their
// skips. An override that does not apply now, because its key is unknown, machine-wide,
// excluded or pinned, its value fails its check, or it leaves the config invalid beside the others, is skipped with
// its reason rather than failing, so a later change to the copy's config cannot wedge a task. The error is for an
// invalid c.
func (c *Config) WithTaskOverrides(overrides map[string]string) (*Config, []Skipped, error) {
	if problems := c.validate(); len(problems) > 0 {
		return nil, nil, errors.New("invalid config: " + strings.Join(problems, "; "))
	}
	var skipped []Skipped
	var apply []string
	for _, key := range slices.Sorted(maps.Keys(overrides)) {
		if _, merge := mergeRequiresOverride(key); merge || strings.HasPrefix(key, StepKeyPrefix) {
			continue
		}
		if err := c.taskOverrideRefusal(key, overrides[key]); err != nil {
			skipped = append(skipped, Skipped{Key: key, Value: overrides[key], Reason: err.Error()})
			continue
		}
		apply = append(apply, key)
	}
	with := func(keys []string) *Config {
		cp := *c
		cp.Local = maps.Clone(c.Local)
		if cp.Local == nil {
			cp.Local = map[string]string{}
		}
		for _, key := range keys {
			cp.Local[key] = overrides[key]
		}
		return &cp
	}
	if out := with(apply); len(out.validate()) == 0 {
		return out, sortSkipped(skipped), nil
	}
	// Together they leave the config invalid, such as reference runs on with no cost cap; keep each override, in key
	// order, that the ones kept before it allow.
	var kept []string
	for _, key := range apply {
		if problems := with(append(slices.Clone(kept), key)).validate(); len(problems) > 0 {
			skipped = append(skipped, Skipped{Key: key, Value: overrides[key], Reason: strings.Join(problems, "; ")})
			continue
		}
		kept = append(kept, key)
	}
	return with(kept), sortSkipped(skipped), nil
}

// WithTaskOverrides returns a copy of m with a task's merge-requires overrides applied, the requirements they added,
// those that were off in m, in documented order, and each override that does not apply, in key order. It reads all of
// a task's overrides, ignoring keys that are not merge-requires keys. Only true applies, so the copy is never looser
// than m: a recorded false or other value is skipped with its reason whatever the CLI checked, and true on a
// requirement m already has is a silent no-op. It never mutates m.
func (m MergeRequires) WithTaskOverrides(overrides map[string]string) (MergeRequires, []string, []Skipped) {
	set := map[string]bool{}
	var skipped []Skipped
	for _, key := range slices.Sorted(maps.Keys(overrides)) {
		name, ok := mergeRequiresOverride(key)
		if !ok {
			continue
		}
		if err := mergeRequiresRefusal(name, overrides[key]); err != nil {
			skipped = append(skipped, Skipped{Key: key, Value: overrides[key], Reason: err.Error()})
			continue
		}
		set[name] = true
	}
	out := m
	var added []string
	for _, r := range mergeRequirements {
		if !set[r.name] {
			continue
		}
		on := true
		*r.field(&out) = &on
		if was := *r.field(&m); was != nil && !*was {
			added = append(added, r.name)
		}
	}
	return out, added, skipped
}

func sortSkipped(s []Skipped) []Skipped {
	slices.SortFunc(s, func(a, b Skipped) int { return strings.Compare(a.Key, b.Key) })
	return s
}

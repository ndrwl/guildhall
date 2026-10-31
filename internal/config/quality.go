package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/workflow"
)

// The quality settings (docs/configuration.md#quality-settings) switch the engineering-quality practices of
// docs/quality.md on or off and size them. They are settings read through Setting, so a Steward's managed value is
// read-only to the Director and a standalone copy sets them under [local]. Every practice is optional.
// The self-review switch and the size budget are read by the guildhall-self-review skill through director status,
// and the judge settings by the judge session through Judge (docs/decisions/quality-rubric.md); the reference-run and
// skill-edit settings are parsed and validated for later work, and the deprecated audit sample is parsed and validated
// but read by nothing.
const (
	// QualitySelfReviewKey switches the guildhall-self-review skill after each delivered task: "on" or "off".
	QualitySelfReviewKey = "quality_self_review"
	// QualitySizeBudgetLinesKey and QualitySizeBudgetFilesKey are the soft size budget: a change over either is
	// flagged, never blocked; "0" turns that half of the flag off.
	QualitySizeBudgetLinesKey = "quality_size_budget_lines"
	QualitySizeBudgetFilesKey = "quality_size_budget_files"
	// QualityAuditSampleKey is deprecated and has no effect (Deprecations): it once queued one in this many delivered
	// tasks for the Guildmaster's audit. It is still parsed, so a config that sets it loads with its digest unchanged.
	QualityAuditSampleKey = "quality_audit_sample"
	// QualityReferenceRunsKey chooses when the reference set runs on a real model: "off" or "on-change-and-release".
	QualityReferenceRunsKey = "quality_reference_runs"
	// QualityReferenceRunsPerTaskKey is how many times each reference task runs per workflow version.
	QualityReferenceRunsPerTaskKey = "quality_reference_runs_per_task"
	// QualityReferenceCostCapKey caps one reference evaluation's cost in US dollars; required while reference runs are on.
	QualityReferenceCostCapKey = "quality_reference_cost_cap_usd"
	// QualityJudgeProfilesKey names the profiles a judge session may run as, in order of preference, separated by
	// commas; eligibility and refusal rules are defined by Quality.Judge (docs/contracts/config.md).
	QualityJudgeProfilesKey = "quality_judge_profiles"
	// QualityJudgeSeparateFamilyKey asks for a judge of a different model family when quota allows: "on" or "off".
	QualityJudgeSeparateFamilyKey = "quality_judge_separate_family"
	// QualitySkillEditsNeedApprovalKey treats an edit to the review, build or spec skill text as a design change
	// that needs the Guildmaster's approval: "on" or "off".
	QualitySkillEditsNeedApprovalKey = "quality_skill_edits_need_approval"
)

// ReferenceRuns is when the reference set runs on a real model.
type ReferenceRuns string

const (
	// ReferenceRunsOff never runs the reference set; it is the default.
	ReferenceRunsOff ReferenceRuns = "off"
	// ReferenceRunsOnChangeAndRelease runs it for each workflow-change proposal and each release tag.
	ReferenceRunsOnChangeAndRelease ReferenceRuns = "on-change-and-release"
)

// The defaults of a config that sets no quality setting: self-review off, the 400-line or 15-file budget, an audit
// sample of five that has no effect, reference runs off at three per task, a judge of a separate family when quota allows, and
// skill edits needing approval.
const (
	DefaultQualitySelfReview             = false
	DefaultQualitySizeBudgetLines        = 400
	DefaultQualitySizeBudgetFiles        = 15
	DefaultQualityAuditSample            = 5
	DefaultQualityReferenceRuns          = ReferenceRunsOff
	DefaultQualityReferenceRunsPerTask   = 3
	DefaultQualityJudgeSeparateFamily    = true
	DefaultQualitySkillEditsNeedApproval = true
)

// Quality is the effective value of every quality setting.
type Quality struct {
	SelfReview      bool `json:"self_review"`
	SizeBudgetLines int  `json:"size_budget_lines"`
	SizeBudgetFiles int  `json:"size_budget_files"`
	// AuditSample is the deprecated quality_audit_sample as parsed; it has no effect, and stays in the status feed so
	// its shape does not change.
	AuditSample          int           `json:"audit_sample"`
	ReferenceRuns        ReferenceRuns `json:"reference_runs"`
	ReferenceRunsPerTask int           `json:"reference_runs_per_task"`
	// ReferenceCostCapUSD is the setting exactly as written, or "" when it is unset.
	ReferenceCostCapUSD string `json:"reference_cost_cap_usd,omitempty"`
	JudgeSeparateFamily bool   `json:"judge_separate_family"`
	// JudgeProfiles are the declared profiles a judge may run as, in order of preference; nil when unset.
	JudgeProfiles          []string `json:"judge_profiles,omitempty"`
	SkillEditsNeedApproval bool     `json:"skill_edits_need_approval"`
}

// Quality returns the effective quality settings, each its default when unset.
// A value it cannot use is an error naming its key, as is reference runs on with no cost cap; Parse refuses both
// already.
func (c *Config) Quality() (Quality, error) {
	q := Quality{SelfReview: DefaultQualitySelfReview, SizeBudgetLines: DefaultQualitySizeBudgetLines,
		SizeBudgetFiles: DefaultQualitySizeBudgetFiles, AuditSample: DefaultQualityAuditSample,
		ReferenceRuns: DefaultQualityReferenceRuns, ReferenceRunsPerTask: DefaultQualityReferenceRunsPerTask,
		JudgeSeparateFamily: DefaultQualityJudgeSeparateFamily, SkillEditsNeedApproval: DefaultQualitySkillEditsNeedApproval}
	for _, s := range qualitySettings {
		if v, ok := c.Setting(s.key); ok {
			if err := s.set(&q, v); err != nil {
				return Quality{}, err
			}
		}
	}
	if q.ReferenceRuns != ReferenceRunsOff && q.ReferenceCostCapUSD == "" {
		return Quality{}, fmt.Errorf("%s %q needs %s set", QualityReferenceRunsKey, q.ReferenceRuns, QualityReferenceCostCapKey)
	}
	for _, name := range q.JudgeProfiles {
		if _, ok := c.Profiles[name]; !ok {
			return Quality{}, fmt.Errorf("%s names %q, which is not a declared profile", QualityJudgeProfilesKey, name)
		}
	}
	return q, nil
}

// Deprecations returns one warning for each deprecated setting the managed or local section sets, which Parse
// still accepts: today only quality_audit_sample, which has no effect since the Guildmaster's audit was retired.
func (c *Config) Deprecations() []string {
	var warnings []string
	if _, ok := c.Managed[QualityAuditSampleKey]; ok {
		warnings = append(warnings, "managed "+auditSampleDeprecated)
	}
	if _, ok := c.Local[QualityAuditSampleKey]; ok {
		warnings = append(warnings, "local "+auditSampleDeprecated)
	}
	return warnings
}

// auditSampleDeprecated is the deprecation warning for quality_audit_sample.
const auditSampleDeprecated = QualityAuditSampleKey + " is deprecated and has no effect: no task is audited any more; the Director's periodic retrospective, [director] retrospective_after_merges, replaces it"

// qualitySettings parses each quality setting's value into its field of Quality.
var qualitySettings = []struct {
	key string
	set func(q *Quality, v string) error
}{
	{QualitySelfReviewKey, func(q *Quality, v string) (err error) {
		q.SelfReview, err = parseSwitch(QualitySelfReviewKey, v)
		return
	}},
	{QualitySizeBudgetLinesKey, func(q *Quality, v string) (err error) {
		q.SizeBudgetLines, err = parseCount(QualitySizeBudgetLinesKey, v)
		return
	}},
	{QualitySizeBudgetFilesKey, func(q *Quality, v string) (err error) {
		q.SizeBudgetFiles, err = parseCount(QualitySizeBudgetFilesKey, v)
		return
	}},
	{QualityAuditSampleKey, func(q *Quality, v string) (err error) {
		q.AuditSample, err = parseCount(QualityAuditSampleKey, v)
		return
	}},
	{QualityReferenceRunsKey, func(q *Quality, v string) error {
		switch r := ReferenceRuns(v); r {
		case ReferenceRunsOff, ReferenceRunsOnChangeAndRelease:
			q.ReferenceRuns = r
			return nil
		}
		return fmt.Errorf("%s %q must be %q or %q", QualityReferenceRunsKey, v, ReferenceRunsOff, ReferenceRunsOnChangeAndRelease)
	}},
	{QualityReferenceRunsPerTaskKey, func(q *Quality, v string) (err error) {
		q.ReferenceRunsPerTask, err = parseCap(QualityReferenceRunsPerTaskKey, v)
		return
	}},
	{QualityReferenceCostCapKey, func(q *Quality, v string) error {
		if !costPattern.MatchString(v) || !hasNonzeroDigit(v) {
			return fmt.Errorf("%s %q must be a positive amount of US dollars such as \"25\" or \"12.50\"", QualityReferenceCostCapKey, v)
		}
		q.ReferenceCostCapUSD = v
		return nil
	}},
	{QualityJudgeSeparateFamilyKey, func(q *Quality, v string) (err error) {
		q.JudgeSeparateFamily, err = parseSwitch(QualityJudgeSeparateFamilyKey, v)
		return
	}},
	{QualityJudgeProfilesKey, func(q *Quality, v string) error {
		var names []string
		for name := range strings.SplitSeq(v, ",") {
			name = strings.TrimSpace(name)
			switch {
			case !workflow.ValidID(name):
				return fmt.Errorf("%s %q must list profile names separated by commas", QualityJudgeProfilesKey, v)
			case slices.Contains(names, name):
				return fmt.Errorf("%s %q names %q twice", QualityJudgeProfilesKey, v, name)
			}
			names = append(names, name)
		}
		q.JudgeProfiles = names
		return nil
	}},
	{QualitySkillEditsNeedApprovalKey, func(q *Quality, v string) (err error) {
		q.SkillEditsNeedApproval, err = parseSwitch(QualitySkillEditsNeedApprovalKey, v)
		return
	}},
}

// costPattern is a plain decimal amount with at most two places, no sign and no leading zeros.
var costPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,2})?$`)

func hasNonzeroDigit(v string) bool {
	for _, r := range v {
		if r >= '1' && r <= '9' {
			return true
		}
	}
	return false
}

// checkQuality reports, when every quality setting in the managed and local sections parses, an effective combination
// Quality refuses; validate reports a value that does not parse through the setting registry.
func (c *Config) checkQuality() []string {
	var scratch Quality
	for _, s := range qualitySettings {
		if m, ok := c.Managed[s.key]; ok && s.set(&scratch, m.Value) != nil {
			return nil
		}
		if v, ok := c.Local[s.key]; ok && s.set(&scratch, v) != nil {
			return nil
		}
	}
	if _, err := c.Quality(); err != nil {
		return []string{err.Error()}
	}
	return nil
}

func parseSwitch(key, v string) (bool, error) {
	switch v {
	case "on":
		return true, nil
	case "off":
		return false, nil
	}
	return false, fmt.Errorf(`%s %q must be "on" or "off"`, key, v)
}

// parseCount parses a nonnegative whole number written plainly, where 0 turns its practice off.
func parseCount(key, v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || strconv.Itoa(n) != v {
		return 0, fmt.Errorf("%s %q must be a whole number, or \"0\" to turn it off", key, v)
	}
	return n, nil
}

var ErrNoJudge = errors.New("no judge profile is left")

// JudgeChoice is who judges a delivered task's self-review (docs/decisions/quality-rubric.md).
type JudgeChoice struct {
	Profile string `json:"profile,omitempty"`
	// Adapter is the judge's runtime adapter, which is its family.
	Adapter string `json:"adapter"`
	// Builder is the builder's adapter, or "" when it is unknown.
	Builder string `json:"builder_adapter,omitempty"`
	// SeparateFamily is true when the judge's family is known to differ from the builder's.
	SeparateFamily bool `json:"separate_family"`
	// Reason says why the family is not separate; empty when it is.
	Reason string `json:"reason,omitempty"`
}

func (q Quality) Judge(profiles map[string]Profile, builder, builderProfile string, failed []string) (JudgeChoice, error) {
	choose := func(profile, adapter, reason string) JudgeChoice {
		c := JudgeChoice{Profile: profile, Adapter: adapter, Builder: builder}
		switch {
		case builder == "":
			c.Reason = "the builder's family is unknown"
		case adapter != builder:
			c.SeparateFamily = true
		default:
			c.Reason = reason
		}
		return c
	}
	if len(q.JudgeProfiles) == 0 {
		return JudgeChoice{}, fmt.Errorf("%w: no profile is listed in %s", ErrNoJudge, QualityJudgeProfilesKey)
	}
	var usable []string
	otherFamily := false
	for _, name := range q.JudgeProfiles {
		if name == builderProfile {
			continue
		}
		otherFamily = otherFamily || profiles[name].Adapter != builder
		if !slices.Contains(failed, name) {
			usable = append(usable, name)
		}
	}
	if len(usable) == 0 {
		return JudgeChoice{}, fmt.Errorf("%w: every profile in %s is the builder's profile or failed for this self-review", ErrNoJudge, QualityJudgeProfilesKey)
	}
	if !q.JudgeSeparateFamily {
		return choose(usable[0], profiles[usable[0]].Adapter, QualityJudgeSeparateFamilyKey+" is off"), nil
	}
	for _, name := range usable {
		if builder != "" && profiles[name].Adapter != builder {
			return choose(name, profiles[name].Adapter, ""), nil
		}
	}
	reason := "no judge profile of another family than the builder's is listed"
	if otherFamily {
		reason = "every judge profile of another family than the builder's failed for this self-review"
	}
	return choose(usable[0], profiles[usable[0]].Adapter, reason), nil
}

package config

import (
	"fmt"
	"regexp"
	"strconv"
)

// The quality settings (docs/configuration.md#quality-settings) switch the engineering-quality practices of
// docs/quality.md on or off and size them. They are settings read through Setting, so a Steward's managed value is
// read-only to the Director and a standalone copy sets them under [local]. Every practice is optional.
// Today only the self-review switch and the size budget are read, by the guildhall-self-review skill through
// director status; the audit, reference-run and judge settings are parsed and validated for later work.
const (
	// QualitySelfReviewKey switches the guildhall-self-review skill after each delivered task: "on" or "off".
	QualitySelfReviewKey = "quality_self_review"
	// QualitySizeBudgetLinesKey and QualitySizeBudgetFilesKey are the soft size budget: a change over either is
	// flagged, never blocked; "0" turns that half of the flag off.
	QualitySizeBudgetLinesKey = "quality_size_budget_lines"
	QualitySizeBudgetFilesKey = "quality_size_budget_files"
	// QualityAuditSampleKey queues one in this many delivered tasks for the Guildmaster's audit; "0" turns it off.
	QualityAuditSampleKey = "quality_audit_sample"
	// QualityReferenceRunsKey chooses when the reference set runs on a real model: "off" or "on-change-and-release".
	QualityReferenceRunsKey = "quality_reference_runs"
	// QualityReferenceRunsPerTaskKey is how many times each reference task runs per workflow version.
	QualityReferenceRunsPerTaskKey = "quality_reference_runs_per_task"
	// QualityReferenceCostCapKey caps one reference evaluation's cost in US dollars; required while reference runs are on.
	QualityReferenceCostCapKey = "quality_reference_cost_cap_usd"
	// QualityJudgeSeparateFamilyKey asks for a judge of a different model family when quota allows: "on" or "off".
	QualityJudgeSeparateFamilyKey = "quality_judge_separate_family"
)

// ReferenceRuns is when the reference set runs on a real model.
type ReferenceRuns string

const (
	// ReferenceRunsOff never runs the reference set; it is the default.
	ReferenceRunsOff ReferenceRuns = "off"
	// ReferenceRunsOnChangeAndRelease runs it for each workflow-change proposal and each release tag.
	ReferenceRunsOnChangeAndRelease ReferenceRuns = "on-change-and-release"
)

// The defaults of a config that sets no quality setting: self-review off, the 400-line or 15-file budget, one in
// five tasks audited, reference runs off at three per task, and a judge of a separate family when quota allows.
const (
	DefaultQualitySelfReview           = false
	DefaultQualitySizeBudgetLines      = 400
	DefaultQualitySizeBudgetFiles      = 15
	DefaultQualityAuditSample          = 5
	DefaultQualityReferenceRuns        = ReferenceRunsOff
	DefaultQualityReferenceRunsPerTask = 3
	DefaultQualityJudgeSeparateFamily  = true
)

// Quality is the effective value of every quality setting.
type Quality struct {
	SelfReview      bool `json:"self_review"`
	SizeBudgetLines int  `json:"size_budget_lines"`
	SizeBudgetFiles int  `json:"size_budget_files"`
	// AuditSample is N in "one in N delivered tasks"; 0 is off.
	AuditSample          int           `json:"audit_sample"`
	ReferenceRuns        ReferenceRuns `json:"reference_runs"`
	ReferenceRunsPerTask int           `json:"reference_runs_per_task"`
	// ReferenceCostCapUSD is the setting exactly as written, or "" when it is unset.
	ReferenceCostCapUSD string `json:"reference_cost_cap_usd,omitempty"`
	JudgeSeparateFamily bool   `json:"judge_separate_family"`
}

// Quality returns the effective quality settings, each its default when unset.
// A value it cannot use is an error naming its key, as is reference runs on with no cost cap; Parse refuses both
// already.
func (c *Config) Quality() (Quality, error) {
	q := Quality{SelfReview: DefaultQualitySelfReview, SizeBudgetLines: DefaultQualitySizeBudgetLines,
		SizeBudgetFiles: DefaultQualitySizeBudgetFiles, AuditSample: DefaultQualityAuditSample,
		ReferenceRuns: DefaultQualityReferenceRuns, ReferenceRunsPerTask: DefaultQualityReferenceRunsPerTask,
		JudgeSeparateFamily: DefaultQualityJudgeSeparateFamily}
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
	return q, nil
}

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

// checkQuality reports each quality setting in the managed and local sections that Quality could not use, then,
// when those all parse, an effective combination it refuses.
func (c *Config) checkQuality() []string {
	var problems []string
	var scratch Quality
	for _, s := range qualitySettings {
		if m, ok := c.Managed[s.key]; ok {
			if err := s.set(&scratch, m.Value); err != nil {
				problems = append(problems, "managed "+err.Error())
			}
		}
		if v, ok := c.Local[s.key]; ok {
			if err := s.set(&scratch, v); err != nil {
				problems = append(problems, "local "+err.Error())
			}
		}
	}
	if len(problems) == 0 {
		if _, err := c.Quality(); err != nil {
			problems = append(problems, err.Error())
		}
	}
	return problems
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

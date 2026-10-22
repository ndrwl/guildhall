package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestQuality(t *testing.T) {
	defaults := Quality{SelfReview: false, SizeBudgetLines: 400, SizeBudgetFiles: 15, AuditSample: 5,
		ReferenceRuns: ReferenceRunsOff, ReferenceRunsPerTask: 3, JudgeSeparateFamily: true, SkillEditsNeedApproval: true}
	with := func(edit func(*Quality)) Quality {
		q := defaults
		edit(&q)
		return q
	}
	for _, tc := range []struct {
		name, local, managed string
		want                 Quality
	}{
		{name: "unset are the defaults", want: defaults},
		{name: "self-review on", local: `quality_self_review = "on"`, want: with(func(q *Quality) { q.SelfReview = true })},
		{name: "self-review off", local: `quality_self_review = "off"`, want: defaults},
		{name: "a local size budget", local: "quality_size_budget_lines = \"250\"\nquality_size_budget_files = \"8\"",
			want: with(func(q *Quality) { q.SizeBudgetLines, q.SizeBudgetFiles = 250, 8 })},
		{name: "zero turns a budget off", local: `quality_size_budget_files = "0"`, want: with(func(q *Quality) { q.SizeBudgetFiles = 0 })},
		{name: "audit off", local: `quality_audit_sample = "0"`, want: with(func(q *Quality) { q.AuditSample = 0 })},
		{name: "audit every task", local: `quality_audit_sample = "1"`, want: with(func(q *Quality) { q.AuditSample = 1 })},
		{name: "reference runs on with a cap",
			local: "quality_reference_runs = \"on-change-and-release\"\nquality_reference_runs_per_task = \"5\"\nquality_reference_cost_cap_usd = \"40.50\"",
			want: with(func(q *Quality) {
				q.ReferenceRuns, q.ReferenceRunsPerTask, q.ReferenceCostCapUSD = ReferenceRunsOnChangeAndRelease, 5, "40.50"
			})},
		{name: "a cost cap with reference runs off", local: `quality_reference_cost_cap_usd = "25"`,
			want: with(func(q *Quality) { q.ReferenceCostCapUSD = "25" })},
		{name: "judge of the same family", local: `quality_judge_separate_family = "off"`, want: with(func(q *Quality) { q.JudgeSeparateFamily = false })},
		{name: "skill edits without approval", local: `quality_skill_edits_need_approval = "off"`,
			want: with(func(q *Quality) { q.SkillEditsNeedApproval = false })},
		{name: "a managed switch", managed: "[managed.quality_self_review]\nvalue = \"on\"\n", want: with(func(q *Quality) { q.SelfReview = true })},
		{name: "an overridable managed budget replaced locally", local: `quality_size_budget_lines = "600"`,
			managed: "[managed.quality_size_budget_lines]\nvalue = \"300\"\noverridable = true\n", want: with(func(q *Quality) { q.SizeBudgetLines = 600 })},
		{name: "a managed cap beside local reference runs",
			local:   `quality_reference_runs = "on-change-and-release"`,
			managed: "[managed.quality_reference_cost_cap_usd]\nvalue = \"10\"\n",
			want:    with(func(q *Quality) { q.ReferenceRuns, q.ReferenceCostCapUSD = ReferenceRunsOnChangeAndRelease, "10" })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(director(t), `notes_dir = "notes"`, `notes_dir = "notes"`+"\n"+tc.local, 1)
			data = strings.Replace(data, "[local]", tc.managed+"[local]", 1)
			cfg, err := Parse([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := cfg.Quality(); got != tc.want || err != nil {
				t.Errorf("Quality() = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestQualityRefusesAnInvalidSetting(t *testing.T) {
	for _, tc := range []struct{ bad, key string }{
		{`quality_self_review = "yes"`, QualitySelfReviewKey},
		{`quality_self_review = "ON"`, QualitySelfReviewKey},
		{`quality_size_budget_lines = "-1"`, QualitySizeBudgetLinesKey},
		{`quality_size_budget_lines = "lots"`, QualitySizeBudgetLinesKey},
		{`quality_size_budget_files = "07"`, QualitySizeBudgetFilesKey},
		{`quality_audit_sample = "0.2"`, QualityAuditSampleKey},
		{`quality_reference_runs = "nightly"`, QualityReferenceRunsKey},
		{`quality_reference_runs_per_task = "0"`, QualityReferenceRunsPerTaskKey},
		{`quality_reference_cost_cap_usd = "0"`, QualityReferenceCostCapKey},
		{`quality_reference_cost_cap_usd = "$25"`, QualityReferenceCostCapKey},
		{`quality_reference_cost_cap_usd = "1e3"`, QualityReferenceCostCapKey},
		{`quality_reference_cost_cap_usd = "2.505"`, QualityReferenceCostCapKey},
		{`quality_judge_separate_family = "when quota allows"`, QualityJudgeSeparateFamilyKey},
		{`quality_skill_edits_need_approval = "first cycle"`, QualitySkillEditsNeedApprovalKey},
		{`quality_reference_runs = "on-change-and-release"`, QualityReferenceCostCapKey},
	} {
		data := strings.Replace(director(t), "[local]", "[local]\n"+tc.bad, 1)
		if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), tc.key) {
			t.Errorf("%q: Parse error %v, want one naming %s", tc.bad, err, tc.key)
		}
		managed := strings.Replace(tc.bad, " = ", "]\nvalue = ", 1)
		data = strings.Replace(director(t), "[local]", "[managed."+managed+"\n[local]", 1)
		if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), tc.key) {
			t.Errorf("managed %q: Parse error %v, want one naming %s", tc.bad, err, tc.key)
		}
	}
	for _, local := range []map[string]string{
		{QualityAuditSampleKey: "some"},
		{QualityReferenceRunsKey: string(ReferenceRunsOnChangeAndRelease)},
	} {
		cfg := &Config{Local: local}
		if _, err := cfg.Quality(); err == nil {
			t.Errorf("Quality of unparsed %v succeeded", local)
		}
	}
}

// TestQualityAudits: a delivered task is audited only while self-review is on and the sample is not 0; at 1 every
// task is, and otherwise the task's identity alone chooses, so asking again gives the same answer and about one task
// in N is chosen.
func TestQualityAudits(t *testing.T) {
	on := Quality{SelfReview: true, AuditSample: 1}
	for _, tc := range []struct {
		name string
		q    Quality
	}{
		{"self-review off", Quality{SelfReview: false, AuditSample: 1}},
		{"the audit off", Quality{SelfReview: true, AuditSample: 0}},
	} {
		for i := range 50 {
			if tc.q.Audits("app", fmt.Sprint("t-", i)) {
				t.Errorf("%s: Audits(app, t-%d) = true", tc.name, i)
			}
		}
	}
	for i := range 50 {
		if !on.Audits("app", fmt.Sprint("t-", i)) {
			t.Errorf("one in 1: Audits(app, t-%d) = false", i)
		}
	}
	five := Quality{SelfReview: true, AuditSample: 5}
	chosen := 0
	for i := range 2000 {
		id := fmt.Sprint("t-", i)
		got := five.Audits("app", id)
		if got != five.Audits("app", id) {
			t.Fatalf("Audits(app, %s) changed between calls", id)
		}
		if got {
			chosen++
		}
	}
	if chosen < 300 || chosen > 500 {
		t.Errorf("one in 5 chose %d of 2000 tasks, want about 400", chosen)
	}
}

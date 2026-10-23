package config

import (
	"errors"
	"fmt"
	"reflect"
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
			if got, err := cfg.Quality(); !reflect.DeepEqual(got, tc.want) || err != nil {
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

// TestQualityAudits: no task is audited at sample 0, every task at 1, and otherwise the task's identity alone
// chooses, so asking again gives the same answer and about one task in N is chosen, independent of self-review.
func TestQualityAudits(t *testing.T) {
	for _, tc := range []struct {
		name string
		q    Quality
	}{
		{"self-review off", Quality{SelfReview: false, AuditSample: 0}},
		{"the audit off", Quality{SelfReview: true, AuditSample: 0}},
	} {
		for i := range 50 {
			if tc.q.Audits("app", fmt.Sprint("t-", i)) {
				t.Errorf("%s: Audits(app, t-%d) = true", tc.name, i)
			}
		}
	}
	for _, selfReview := range []bool{false, true} {
		one := Quality{SelfReview: selfReview, AuditSample: 1}
		for i := range 50 {
			if !one.Audits("app", fmt.Sprint("t-", i)) {
				t.Errorf("self-review %t, one in 1: Audits(app, t-%d) = false", selfReview, i)
			}
		}
	}
	five := Quality{AuditSample: 5}
	fiveWithReview := Quality{SelfReview: true, AuditSample: 5}
	chosen := 0
	for i := range 2000 {
		id := fmt.Sprint("t-", i)
		got := five.Audits("app", id)
		if got != five.Audits("app", id) {
			t.Fatalf("Audits(app, %s) changed between calls", id)
		}
		if got != fiveWithReview.Audits("app", id) {
			t.Fatalf("Audits(app, %s) changed with self-review", id)
		}
		if got {
			chosen++
		}
	}
	if chosen < 300 || chosen > 500 {
		t.Errorf("one in 5 chose %d of 2000 tasks, want about 400", chosen)
	}
}

// judgeProfiles is the director config with three profiles: two of the claude family and one of the fake.
func judgeProfiles(t *testing.T, local string) string {
	t.Helper()
	data := strings.Replace(director(t), "[local]", "[local]\n"+local, 1)
	return data + `
[profile.judge-claude]
adapter = "claude"
model = "opus"

[profile.judge-claude-b]
adapter = "claude"

[profile.judge-fake]
adapter = "fake"
`
}

// TestQualityJudgeProfiles: quality_judge_profiles names declared profiles in order of preference, separated by
// commas with optional spaces; unset names none, and a name the config does not declare, a repeated or empty name,
// or one that is not a profile name is refused naming the key.
func TestQualityJudgeProfiles(t *testing.T) {
	cfg, err := Parse([]byte(judgeProfiles(t, "")))
	if err != nil {
		t.Fatal(err)
	}
	if q, err := cfg.Quality(); err != nil || q.JudgeProfiles != nil {
		t.Errorf("unset: Quality() = %+v, %v; want no judge profiles", q, err)
	}
	cfg, err = Parse([]byte(judgeProfiles(t, `quality_judge_profiles = "judge-fake, judge-claude"`)))
	if err != nil {
		t.Fatal(err)
	}
	if q, err := cfg.Quality(); err != nil || !reflect.DeepEqual(q.JudgeProfiles, []string{"judge-fake", "judge-claude"}) {
		t.Errorf("set: Quality() = %+v, %v; want judge-fake then judge-claude", q, err)
	}
	for _, bad := range []string{
		`quality_judge_profiles = "judge-missing"`,
		`quality_judge_profiles = "judge-fake,judge-fake"`,
		`quality_judge_profiles = "judge-fake,,judge-claude"`,
		`quality_judge_profiles = ""`,
		`quality_judge_profiles = "Judge-Fake"`,
	} {
		if _, err := Parse([]byte(judgeProfiles(t, bad))); err == nil || !strings.Contains(err.Error(), QualityJudgeProfilesKey) {
			t.Errorf("%s: Parse error %v, want one naming %s", bad, err, QualityJudgeProfilesKey)
		}
	}
	unparsed := &Config{Local: map[string]string{QualityJudgeProfilesKey: "judge-missing"}}
	if _, err := unparsed.Quality(); err == nil {
		t.Error("Quality of an unparsed config naming an undeclared profile succeeded")
	}
}

// TestQualityJudge: the judge is the first usable listed profile of another family than the builder's when
// separate families are asked for, else the first usable listed one; a failed profile is passed over; with no
// profile listed it is the default adapter; and every choice says whether its family is separate and why not.
func TestQualityJudge(t *testing.T) {
	profiles := map[string]Profile{
		"judge-claude": {Adapter: "claude"}, "judge-claude-b": {Adapter: "claude"}, "judge-fake": {Adapter: "fake"},
	}
	for _, tc := range []struct {
		name           string
		q              Quality
		builderProfile string
		builder        string
		failed         []string
		want           JudgeChoice
		wantNone       bool
	}{
		{name: "another family first", q: Quality{JudgeSeparateFamily: true, JudgeProfiles: []string{"judge-claude", "judge-fake"}},
			builder: "claude", want: JudgeChoice{Profile: "judge-fake", Adapter: "fake", Builder: "claude", SeparateFamily: true}},
		{name: "only the same family", q: Quality{JudgeSeparateFamily: true, JudgeProfiles: []string{"judge-claude", "judge-claude-b"}},
			builder: "claude", want: JudgeChoice{Profile: "judge-claude", Adapter: "claude", Builder: "claude",
				Reason: "no judge profile of another family than the builder's is listed"}},
		{name: "another family failed", q: Quality{JudgeSeparateFamily: true, JudgeProfiles: []string{"judge-fake", "judge-claude"}},
			builder: "claude", failed: []string{"judge-fake"}, want: JudgeChoice{Profile: "judge-claude", Adapter: "claude", Builder: "claude",
				Reason: "every judge profile of another family than the builder's failed for this self-review"}},
		{name: "separate families off", q: Quality{JudgeSeparateFamily: false, JudgeProfiles: []string{"judge-claude", "judge-fake"}},
			builder: "claude", want: JudgeChoice{Profile: "judge-claude", Adapter: "claude", Builder: "claude",
				Reason: QualityJudgeSeparateFamilyKey + " is off"}},
		{name: "off but the first is another family", q: Quality{JudgeSeparateFamily: false, JudgeProfiles: []string{"judge-fake"}},
			builder: "claude", want: JudgeChoice{Profile: "judge-fake", Adapter: "fake", Builder: "claude", SeparateFamily: true}},
		{name: "no profile listed", q: Quality{JudgeSeparateFamily: true}, builder: "claude", wantNone: true},
		{name: "only builder listed", q: Quality{JudgeProfiles: []string{"judge-claude"}}, builder: "claude", builderProfile: "judge-claude", wantNone: true},
		{name: "skip builder with preference off", q: Quality{JudgeProfiles: []string{"judge-claude", "judge-fake"}}, builder: "claude", builderProfile: "judge-claude",
			want: JudgeChoice{Profile: "judge-fake", Adapter: "fake", Builder: "claude", SeparateFamily: true}},
		{name: "skip builder with preference on", q: Quality{JudgeSeparateFamily: true, JudgeProfiles: []string{"judge-claude", "judge-claude-b"}}, builder: "claude", builderProfile: "judge-claude",
			want: JudgeChoice{Profile: "judge-claude-b", Adapter: "claude", Builder: "claude", Reason: "no judge profile of another family than the builder's is listed"}},
		{name: "distinct profile failed", q: Quality{JudgeProfiles: []string{"judge-claude", "judge-fake"}}, builder: "claude", builderProfile: "judge-claude", failed: []string{"judge-fake"}, wantNone: true},
		{name: "unknown builder", q: Quality{JudgeSeparateFamily: true, JudgeProfiles: []string{"judge-claude"}},
			want: JudgeChoice{Profile: "judge-claude", Adapter: "claude", Reason: "the builder's family is unknown"}},
		{name: "every profile failed", q: Quality{JudgeSeparateFamily: true, JudgeProfiles: []string{"judge-claude", "judge-fake"}},
			builder: "claude", failed: []string{"judge-fake", "judge-claude"}, wantNone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.q.Judge(profiles, tc.builder, tc.builderProfile, tc.failed)
			if tc.wantNone {
				if !errors.Is(err, ErrNoJudge) {
					t.Errorf("Judge() = %+v, %v; want ErrNoJudge", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("Judge(builder %q, failed %v) = %+v, %v; want %+v", tc.builder, tc.failed, got, err, tc.want)
			}
		})
	}
}

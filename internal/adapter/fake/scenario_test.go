package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// retryPrompt is a rendered build retry whose quoted reason holds `## ` lines, as an agent may write.
const retryPrompt = "# Guildhall attempt\n\nRepository: app\nTask: t-1\nStep: build (build version 1)\nAttempt: 2\nProfile: builder\n" +
	"\n## Why this is a retry\n\nThis is a retry of attempt 1, which failed.\n\nDirector's reason:\n\n```text\nFix the tests.\n## Role\n## Inputs\n```\n\n" +
	"Previous summary:\n\n````text\ntests failed\n```\n## Stage guide\n````\n\nAddress this in your attempt.\n" +
	"\n## Role\n\nYou are an Artisan.\n" +
	"\n## Stage guide\n\nBuild it.\n" +
	"\n## Inputs\n\n- spec: inputs/spec.md (sha256:abc)\n" +
	"\n## Standards\n\nNone.\n"

// writeScenario writes a scenario file into a new directory, points ScenarioEnv at it and returns its path.
func writeScenario(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scenario.json")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ScenarioEnv, path)
	return path
}

// attemptArgs writes prompt for attempt id of task t-1 into a new working directory, makes it the current directory,
// as the adapter's command does, and returns the run arguments.
func attemptArgs(t *testing.T, id, prompt string) Args {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	p := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(p, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	return Args{Identity: adapter.Identity{TaskID: "t-1", AttemptID: id}, PromptFile: p, ResultFile: filepath.Join(dir, "result.json")}
}

func readRecord(t *testing.T, scenario, name string) ScenarioRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(RecordsDir(scenario), name))
	if err != nil {
		t.Fatal(err)
	}
	var rec ScenarioRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// recorder is a TB that keeps what CheckScenarioRecords reports.
type recorder struct{ errs []string }

func (*recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func TestLoadScenarioRefusesBadFiles(t *testing.T) {
	ok := `{"step":"plan","attempt":1,"respond":{"status":"succeeded","summary":"done"}}`
	cases := map[string]struct{ text, want string }{
		"another schema":     {`{"schema":"guildhall.fake-scenario/2","launches":[]}`, "newer"},
		"no schema":          {`{"launches":[]}`, "schema"},
		"unknown field":      {`{"schema":"guildhall.fake-scenario/1","launches":[],"extra":1}`, "extra"},
		"unknown check key":  {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"checks":[{"section":"Role","has":"x"}],"respond":{"status":"failed","summary":"x"}}]}`, "has"},
		"duplicate key":      {`{"schema":"guildhall.fake-scenario/1","launches":[` + ok + `,` + ok + `]}`, "listed twice"},
		"no step":            {`{"schema":"guildhall.fake-scenario/1","launches":[{"attempt":1,"respond":{"status":"failed","summary":"x"}}]}`, "needs a step"},
		"attempt zero":       {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":0,"respond":{"status":"failed","summary":"x"}}]}`, "needs a step"},
		"empty check":        {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"checks":[{"section":"Role"}],"respond":{"status":"failed","summary":"x"}}]}`, "exactly one"},
		"check no section":   {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"checks":[{"contains":"x"}],"respond":{"status":"failed","summary":"x"}}]}`, "no section"},
		"two operators":      {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"checks":[{"section":"Role","contains":"x","absent":true}],"respond":{"status":"failed","summary":"x"}}]}`, "has 2"},
		"unknown section":    {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"checks":[{"section":"Rôle","contains":"x"}],"respond":{"status":"failed","summary":"x"}}]}`, "not header"},
		"header absent":      {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"checks":[{"section":"header","absent":true}],"respond":{"status":"failed","summary":"x"}}]}`, "always present"},
		"no status":          {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"respond":{"summary":"x"}}]}`, "status"},
		"no summary":         {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"respond":{"status":"failed"}}]}`, "summary is empty"},
		"absolute file":      {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"respond":{"status":"succeeded","summary":"x","files":[{"path":"/tmp/spec.md","text":"x"}]}}]}`, "relative"},
		"dot-dot file":       {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"respond":{"status":"succeeded","summary":"x","files":[{"path":"a/../../spec.md","text":"x"}]}}]}`, "relative"},
		"dot file":           {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"respond":{"status":"succeeded","summary":"x","files":[{"path":"./spec.md","text":"x"}]}}]}`, "relative"},
		"backslash file":     {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"respond":{"status":"succeeded","summary":"x","files":[{"path":"notes\\spec.md","text":"x"}]}}]}`, "relative"},
		"file twice":         {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"respond":{"status":"succeeded","summary":"x","files":[{"path":"a.md","text":"x"},{"path":"a.md","text":"y"}]}}]}`, "twice"},
		"oversized file":     {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"respond":{"status":"succeeded","summary":"x","files":[{"path":"a.md","text":"` + strings.Repeat("x", MaxPromptBytes+1) + `"}]}}]}`, "larger than"},
		"invalid review":     {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"review","attempt":1,"respond":{"review":{"schema":"guildhall.review-outcome/1","reviewer":"automated","verdict":"changes_requested","revisions":{"subject":{"code_head":"` + strings.Repeat("a", 40) + `"},"standards_revision":""}}}}]}`, "review"},
		"review and status":  {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"review","attempt":1,"respond":{"status":"succeeded","review":{"schema":"guildhall.review-outcome/1","reviewer":"automated","verdict":"approved","revisions":{"subject":{"code_head":"` + strings.Repeat("a", 40) + `"},"standards_revision":""}}}}]}`, "no status"},
		"escaping exact":     {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"exact":"../golden.md","respond":{"status":"failed","summary":"x"}}]}`, "exact"},
		"missing exact file": {`{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"exact":"golden.md","respond":{"status":"failed","summary":"x"}}]}`, "exact"},
		"not JSON":           {`{"schema":`, "JSON"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeScenario(t, c.text)
			if _, err := loadScenario(path); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("loadScenario = %v, want an error containing %q", err, c.want)
			}
		})
	}
	if _, err := loadScenario("scenario.json"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("relative path: %v, want a refusal", err)
	}
	path := writeScenario(t, `{"schema":"guildhall.fake-scenario/1","launches":[`+ok+`,{"step":"plan","attempt":2,"checks":[{"section":"Inputs","contains":"context: inputs/"},{"section":"Why this is a retry","absent":true},{"section":"Stage guide","not_contains":"guildhall-fake:"}],"respond":{"status":"failed","summary":"x"}}]}`)
	if l, err := loadScenario(path); err != nil || len(l.launches) != 2 {
		t.Errorf("loadScenario of a good file = %+v, %v", l, err)
	}
}

func TestSplitAttemptID(t *testing.T) {
	for id, want := range map[string]struct {
		step string
		n    int
	}{"plan-1": {"plan", 1}, "sign-off-2": {"sign-off", 2}, "review-astra-12": {"review-astra", 12}} {
		if step, n, err := SplitAttemptID(id); err != nil || step != want.step || n != want.n {
			t.Errorf("SplitAttemptID(%q) = %q, %d, %v; want %q, %d", id, step, n, err, want.step, want.n)
		}
	}
	for _, id := range []string{"plan", "plan-", "-1", "plan-0", "plan-01", "plan-x", "plan-+1"} {
		if _, _, err := SplitAttemptID(id); err == nil {
			t.Errorf("SplitAttemptID(%q) accepted", id)
		}
	}
}

func TestPromptSectionsSkipFencedHeadings(t *testing.T) {
	retry, ok := promptSection([]byte(retryPrompt), "Why this is a retry")
	if !ok || !strings.Contains(retry, "Fix the tests.\n## Role\n## Inputs\n") || !strings.Contains(retry, "Address this in your attempt.") {
		t.Errorf("retry section = %q, %v; want it whole, past its fenced headings", retry, ok)
	}
	if role, ok := promptSection([]byte(retryPrompt), "Role"); !ok || role != "\nYou are an Artisan.\n\n" {
		t.Errorf("Role = %q, %v", role, ok)
	}
	if header, ok := promptSection([]byte(retryPrompt), SectionHeader); !ok || !strings.HasSuffix(header, "Profile: builder\n\n") || strings.Contains(header, "##") {
		t.Errorf("header = %q, %v", header, ok)
	}
	if inputs, ok := promptSection([]byte(retryPrompt), "Inputs"); !ok || strings.Contains(inputs, "Standards") || !strings.Contains(inputs, "spec: inputs/") {
		t.Errorf("Inputs = %q, %v", inputs, ok)
	}
	if _, ok := promptSection([]byte(retryPrompt), "Skills"); ok {
		t.Error("found a Skills section the prompt does not have")
	}
}

func TestScenarioChecks(t *testing.T) {
	path := writeScenario(t, `{"schema":"guildhall.fake-scenario/1","launches":[{"step":"build","attempt":2,"checks":[
		{"section":"header","contains":"Attempt: 2"},
		{"section":"Why this is a retry","contains":"Fix the tests."},
		{"section":"Why this is a retry","contains":"tests failed"},
		{"section":"Role","not_contains":"Fix the tests."},
		{"section":"Inputs","contains":"spec: inputs/"},
		{"section":"Skills","absent":true},
		{"section":"prompt","not_contains":"guildhall-fake:"}],
		"respond":{"status":"succeeded","summary":"built"}}]}`)
	l, err := loadScenario(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.failures(0, []byte(retryPrompt)); len(got) != 0 {
		t.Errorf("failures = %q, want none", got)
	}
	path = writeScenario(t, `{"schema":"guildhall.fake-scenario/1","launches":[{"step":"build","attempt":2,"checks":[
		{"section":"Inputs","contains":"context: inputs/"},
		{"section":"Role","not_contains":"Artisan"},
		{"section":"Skills","contains":"x"},
		{"section":"Role","absent":true},
		{"section":"prompt","contains":"nowhere"}],
		"respond":{"status":"succeeded","summary":"built"}}]}`)
	if l, err = loadScenario(path); err != nil {
		t.Fatal(err)
	}
	want := []string{`section "Inputs" does not contain "context: inputs/"`, `section "Role" contains "Artisan"`, `section "Skills" is missing`, `section "Role" is present`, `section "prompt" does not contain "nowhere"`}
	if got := l.failures(0, []byte(retryPrompt)); !slices.Equal(got, want) {
		t.Errorf("failures =\n%q\nwant\n%q", got, want)
	}
}

func TestScenarioExact(t *testing.T) {
	path := writeScenario(t, `{"schema":"guildhall.fake-scenario/1","launches":[{"step":"build","attempt":2,"exact":"golden/build.md","respond":{"status":"succeeded","summary":"built"}}]}`)
	if err := os.MkdirAll(filepath.Join(filepath.Dir(path), "golden"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "golden", "build.md"), []byte(retryPrompt), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := loadScenario(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.failures(0, []byte(retryPrompt)); len(got) != 0 {
		t.Errorf("failures = %q, want none for the same bytes", got)
	}
	changed := strings.Replace(retryPrompt, "Build it.", "Build it now.", 1)
	line := 1 + strings.Count(retryPrompt[:strings.Index(retryPrompt, "Build it.")], "\n")
	if got := l.failures(0, []byte(changed)); !slices.Equal(got, []string{fmt.Sprintf("prompt does not match golden/build.md at line %d", line)}) {
		t.Errorf("failures = %q, want the golden mismatch at the guide's line", got)
	}
	if got := l.failures(0, []byte(strings.ReplaceAll(retryPrompt, "\n", "\r\n"))); len(got) != 1 {
		t.Errorf("failures = %q, want CRLF line endings to differ", got)
	}
}

// review is an approve outcome the scenario's review response writes.
var review = workflow.ReviewOutcome{Schema: workflow.ReviewOutcomeSchema, Reviewer: workflow.AutomatedReviewer, Verdict: workflow.VerdictApproved,
	Revisions: workflow.Revisions{Subject: workflow.Subject{CodeHead: strings.Repeat("a", 40)}, SpecDigest: "sha256:" + strings.Repeat("b", 64), StandardsRevision: "standards-1"}}

func TestScenarioResponds(t *testing.T) {
	outcome, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	writeScenario(t, `{"schema":"guildhall.fake-scenario/1","launches":[
		{"step":"plan","attempt":1,"respond":{"status":"succeeded","summary":"spec written","files":[{"path":"spec.md","text":"# Spec\n\nSort by id.\n"},{"path":"notes/extra.md","text":"more"}]}},
		{"step":"build","attempt":1,"respond":{"status":"failed","summary":"tests failed"}},
		{"step":"sign-off","attempt":2,"respond":{"review":`+string(outcome)+`}},
		{"step":"build","attempt":2,"respond":{"status":"succeeded","summary":"built"}}]}`)

	args := attemptArgs(t, "plan-1", "Plan it.\n")
	if _, err := run(t, args); err != nil {
		t.Fatal(err)
	}
	r, err := adapter.ReadResult(args.ResultFile, args.Identity)
	if err != nil || r.Status != adapter.StatusSucceeded || r.Summary != "spec written" || !slices.Equal(r.Outputs, []adapter.Output{{Path: "spec.md"}, {Path: "notes/extra.md"}}) {
		t.Errorf("plan result = %+v, %v", r, err)
	}
	if got, err := os.ReadFile("spec.md"); err != nil || string(got) != "# Spec\n\nSort by id.\n" {
		t.Errorf("spec.md = %q, %v", got, err)
	}
	if _, err := os.Stat(adapter.UsageFile(args.ResultFile)); err != nil {
		t.Errorf("usage under a scenario: %v, want ReportedUsage written", err)
	}

	args = attemptArgs(t, "build-1", "Build it.\n")
	if _, err := run(t, args); err != nil {
		t.Fatal(err)
	}
	if r, err := adapter.ReadResult(args.ResultFile, args.Identity); err != nil || r.Status != adapter.StatusFailed || r.Summary != "tests failed" || len(r.Outputs) != 0 {
		t.Errorf("build result = %+v, %v", r, err)
	}

	args = attemptArgs(t, "sign-off-2", "Review it.\n")
	if _, err := run(t, args); err != nil {
		t.Fatal(err)
	}
	r, err = adapter.ReadResult(args.ResultFile, args.Identity)
	if err != nil || r.Status != adapter.StatusSucceeded || r.Summary != "review approved" || !slices.Equal(r.Outputs, []adapter.Output{{Path: ScenarioReviewFile}}) {
		t.Errorf("review result = %+v, %v", r, err)
	}
	data, err := os.ReadFile(ScenarioReviewFile)
	if err != nil {
		t.Fatal(err)
	}
	if o, err := workflow.ParseReviewOutcome(data); err != nil || o.Verdict != workflow.VerdictApproved || o.Revisions != review.Revisions {
		t.Errorf("review outcome = %+v, %v", o, err)
	}

	// A build with no files lists no outputs, so Guildhall records the worktree's head.
	args = attemptArgs(t, "build-2", "Build it again.\n")
	if _, err := run(t, args); err != nil {
		t.Fatal(err)
	}
	if r, err := adapter.ReadResult(args.ResultFile, args.Identity); err != nil || r.Status != adapter.StatusSucceeded || len(r.Outputs) != 0 {
		t.Errorf("build-2 result = %+v, %v", r, err)
	}
}

// TestScenarioRefusesAFileAliasingTheResult: a scripted file at the result file's path fails the attempt, naming it,
// and writes nothing there itself.
func TestScenarioRefusesAFileAliasingTheResult(t *testing.T) {
	writeScenario(t, `{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"respond":{"status":"succeeded","summary":"x","files":[{"path":"RESULT.json","text":"{}"}]}}]}`)
	args := attemptArgs(t, "plan-1", "Plan it.\n")
	args.ResultFile = filepath.Join(filepath.Dir(args.PromptFile), "RESULT.json")
	_, err := run(t, args)
	if err == nil || !strings.Contains(err.Error(), "is the result file") {
		t.Fatalf("Run = %v, want the alias refused", err)
	}
	if r, err := adapter.ReadResult(args.ResultFile, args.Identity); err != nil || r.Status != adapter.StatusFailed || !strings.Contains(r.Summary, `fake scenario: file path "RESULT.json" is the result file`) {
		t.Errorf("result = %+v, %v; want a failed result naming the alias", r, err)
	}
}

func TestScenarioCheckFailureFailsTheAttempt(t *testing.T) {
	path := writeScenario(t, `{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,
		"checks":[{"section":"Inputs","contains":"context: inputs/"},{"section":"Role","absent":true}],
		"respond":{"status":"succeeded","summary":"spec written","files":[{"path":"spec.md","text":"# Spec\n"}]}}]}`)
	args := attemptArgs(t, "plan-1", "# Guildhall attempt\n\n## Role\n\nArtisan.\n\n## Inputs\n\nNone.\n")
	if _, err := run(t, args); err != nil {
		t.Fatal(err)
	}
	r, err := adapter.ReadResult(args.ResultFile, args.Identity)
	want := `fake scenario: step plan attempt 1: section "Inputs" does not contain "context: inputs/"; section "Role" is present`
	if err != nil || r.Status != adapter.StatusFailed || r.Summary != want || len(r.Outputs) != 0 {
		t.Errorf("result = %+v, %v; want failed with summary %q and no outputs", r, err, want)
	}
	if _, err := os.Stat("spec.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("spec.md after a failed check: %v, want none written", err)
	}
	rec := readRecord(t, path, "plan-1.json")
	if rec.Passed || rec.Step != "plan" || rec.Attempt != 1 || len(rec.Failures) != 2 || len(rec.PromptSHA256) != 64 {
		t.Errorf("record = %+v", rec)
	}
	var tb recorder
	CheckScenarioRecords(&tb, path)
	if len(tb.errs) != 1 || !strings.Contains(tb.errs[0], "plan-1 did not pass") {
		t.Errorf("CheckScenarioRecords reported %q, want the failed launch", tb.errs)
	}
}

// TestScenarioRecords: a passing launch records itself; a launch with no entry fails its attempt and records the
// miss; a second launch of the same attempt fails and records itself beside the first, which it leaves as it was;
// an entry never launched is reported.
func TestScenarioRecords(t *testing.T) {
	path := writeScenario(t, `{"schema":"guildhall.fake-scenario/1","launches":[
		{"step":"plan","attempt":1,"respond":{"status":"succeeded","summary":"spec written"}},
		{"step":"build","attempt":1,"respond":{"status":"succeeded","summary":"built"}}]}`)
	args := attemptArgs(t, "plan-1", "Plan it.\n")
	if _, err := run(t, args); err != nil {
		t.Fatal(err)
	}
	if rec := readRecord(t, path, "plan-1.json"); !rec.Passed || len(rec.Failures) != 0 {
		t.Errorf("plan-1 record = %+v, want passed", rec)
	}
	var tb recorder
	CheckScenarioRecords(&tb, path)
	if len(tb.errs) != 1 || !strings.Contains(tb.errs[0], "step build attempt 1 never happened") {
		t.Errorf("CheckScenarioRecords = %q, want build-1 reported unlaunched", tb.errs)
	}

	args = attemptArgs(t, "research-1", "Research it.\n")
	if _, err := run(t, args); err != nil {
		t.Fatal(err)
	}
	if r, err := adapter.ReadResult(args.ResultFile, args.Identity); err != nil || r.Status != adapter.StatusFailed || r.Summary != "fake scenario: no launch for step research attempt 1" {
		t.Errorf("unmatched result = %+v, %v", r, err)
	}
	if rec := readRecord(t, path, "research-1.json"); rec.Passed || rec.Step != "research" || !slices.Equal(rec.Failures, []string{"no launch for step research attempt 1"}) {
		t.Errorf("research-1 record = %+v", rec)
	}

	args = attemptArgs(t, "plan-1", "Plan it again.\n")
	if _, err := run(t, args); err != nil {
		t.Fatal(err)
	}
	if r, err := adapter.ReadResult(args.ResultFile, args.Identity); err != nil || r.Status != adapter.StatusFailed || r.Summary != "fake scenario: step plan attempt 1 was already launched" {
		t.Errorf("second plan-1 result = %+v, %v", r, err)
	}
	if rec := readRecord(t, path, "plan-1.json"); !rec.Passed {
		t.Errorf("first plan-1 record = %+v, want it kept", rec)
	}
	if rec := readRecord(t, path, "plan-1.again-2.json"); rec.Passed || !slices.Contains(rec.Failures, "attempt plan-1 was launched 2 times") {
		t.Errorf("second plan-1 record = %+v", rec)
	}
	tb = recorder{}
	CheckScenarioRecords(&tb, path)
	if len(tb.errs) != 3 {
		t.Errorf("CheckScenarioRecords = %q, want the unmatched launch, the repeat and build-1", tb.errs)
	}
}

// TestScenarioLoadFailureFailsTheAttempt: a scenario that cannot be loaded writes a failed result giving the
// reason, records the launch and makes Run fail, so the process exits 1.
func TestScenarioLoadFailureFailsTheAttempt(t *testing.T) {
	path := writeScenario(t, `{"schema":"guildhall.fake-scenario/1","launches":[],"extra":true}`)
	args := attemptArgs(t, "plan-1", "Plan it.\n")
	if _, err := run(t, args); err == nil || !errors.As(err, new(errScenario)) {
		t.Errorf("Run = %v, want the scenario error", err)
	}
	if r, err := adapter.ReadResult(args.ResultFile, args.Identity); err != nil || r.Status != adapter.StatusFailed || !strings.Contains(r.Summary, "fake scenario: scenario ") || !strings.Contains(r.Summary, "extra") {
		t.Errorf("result = %+v, %v", r, err)
	}
	if rec := readRecord(t, path, "plan-1.json"); rec.Passed || len(rec.Failures) != 1 {
		t.Errorf("record = %+v", rec)
	}
}

// TestScenarioRefusesTheOutputDirective: the output directive and a matching entry both script the result, so the
// launch is refused as a conflict; a directive prompt the scenario has no entry for fails as unmatched.
func TestScenarioRefusesTheOutputDirective(t *testing.T) {
	writeScenario(t, `{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"respond":{"status":"succeeded","summary":"x"}}]}`)
	args := attemptArgs(t, "plan-1", "guildhall-fake: output=spec.md:Sort.\n")
	if _, err := run(t, args); err == nil || !strings.Contains(err.Error(), "both script the result") {
		t.Errorf("Run = %v, want a conflict", err)
	}
	if r, err := adapter.ReadResult(args.ResultFile, args.Identity); err != nil || r.Status != adapter.StatusFailed || !strings.Contains(r.Summary, "both script the result") {
		t.Errorf("result = %+v, %v", r, err)
	}
	if _, err := os.Stat("spec.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("spec.md: %v, want no output written", err)
	}
}

// TestScenarioKeepsLifecycleDirectives: a scenario's result still waits for the delay directive and still comes as
// a late result after the cancel marker.
func TestScenarioKeepsLifecycleDirectives(t *testing.T) {
	writeScenario(t, `{"schema":"guildhall.fake-scenario/1","launches":[{"step":"plan","attempt":1,"respond":{"status":"succeeded","summary":"spec written"}}]}`)
	args := attemptArgs(t, "plan-1", "guildhall-fake: delay=3s\n")
	var slept []string
	if err := Run(t.Context(), args, new(strings.Builder), func(_ context.Context, d time.Duration) error { slept = append(slept, d.String()); return nil }); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(slept, []string{"3s"}) {
		t.Errorf("slept %q, want the delay", slept)
	}
	if r, err := adapter.ReadResult(args.ResultFile, args.Identity); err != nil || r.Summary != "spec written" {
		t.Errorf("result = %+v, %v", r, err)
	}
}

// TestNoScenarioKeepsTodaysResult: with the variable unset or empty the fake writes its prompt-digest result and no
// record.
func TestNoScenarioKeepsTodaysResult(t *testing.T) {
	for _, value := range []string{"", "unset"} {
		if value == "unset" {
			t.Setenv(ScenarioEnv, "x")
			if err := os.Unsetenv(ScenarioEnv); err != nil {
				t.Fatal(err)
			}
		} else {
			t.Setenv(ScenarioEnv, value)
		}
		args := attemptArgs(t, "plan-1", "Plan it.\n")
		if _, err := run(t, args); err != nil {
			t.Fatal(err)
		}
		if r, err := adapter.ReadResult(args.ResultFile, args.Identity); err != nil || r.Status != adapter.StatusSucceeded || !strings.HasPrefix(r.Summary, "fake artisan read 9 bytes of prompt, sha256 ") || len(r.Outputs) != 0 {
			t.Errorf("%s: result = %+v, %v", value, r, err)
		}
	}
}

// TestScenarioInAChildProcess runs `guildhall fake-artisan` as a real child with ScenarioEnv in its environment and
// reads back its result and record.
func TestScenarioInAChildProcess(t *testing.T) {
	path := writeScenario(t, `{"schema":"guildhall.fake-scenario/1","launches":[{"step":"sign-off","attempt":2,
		"checks":[{"section":"header","contains":"Attempt: 2"}],
		"respond":{"status":"succeeded","summary":"signed","files":[{"path":"out/note.md","text":"ok\n"}]}}]}`)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := attemptArgs(t, "sign-off-2", "# Guildhall attempt\n\nAttempt: 2\n\n## Role\n\nArtisan.\n")
	cmd := exec.Command(exe, Subcommand, "--task", args.TaskID, "--attempt", args.AttemptID, "--prompt", args.PromptFile, "--result", args.ResultFile)
	cmd.Dir = filepath.Dir(args.PromptFile)
	cmd.Env = append(os.Environ(), ScenarioEnv+"="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fake-artisan: %v\n%s", err, out)
	}
	if r, err := adapter.ReadResult(args.ResultFile, args.Identity); err != nil || r.Summary != "signed" || !slices.Equal(r.Outputs, []adapter.Output{{Path: "out/note.md"}}) {
		t.Errorf("result = %+v, %v", r, err)
	}
	if rec := readRecord(t, path, "sign-off-2.json"); !rec.Passed || rec.Step != "sign-off" || rec.Attempt != 2 {
		t.Errorf("record = %+v", rec)
	}
	var tb recorder
	CheckScenarioRecords(&tb, path)
	if len(tb.errs) != 0 {
		t.Errorf("CheckScenarioRecords = %q", tb.errs)
	}
}

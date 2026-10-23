package quality_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/quality"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const head = "9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b"

// selfReview is a self-review document of app/t-1 number 1 with D1 and B2 pending scores and G2 a pending gate.
const selfReview = `{
  "schema": "guildhall.self-review/1",
  "repository": "app",
  "task": "t-1",
  "number": 1,
  "base": "4f1c2a9e0b7d6c5a4f3e2d1c0b9a8f7e6d5c4b3a",
  "head": "` + head + `",
  "pull_request": "https://github.com/example/app/pull/7",
  "settings": {"size_budget_lines": 400, "size_budget_files": 15},
  "facts": {"files": 6, "items": []},
  "tools": {"dupl": "not run: not installed"},
  "criteria": [
    {"id": "E1", "kind": "gate", "judge": "script", "pass": true, "evidence": "validate-1-result"},
    {"id": "G2", "kind": "gate", "judge": "pending", "pass": null, "evidence": "assertions_removed: 0"},
    {"id": "D1", "kind": "score", "judge": "pending", "score": null, "evidence": "nontest_lines: 140"},
    {"id": "B2", "kind": "score", "judge": "pending", "score": null, "evidence": ""},
    {"id": "H1", "kind": "trend", "judge": "script", "value": 1, "evidence": "one round"}
  ],
  "flags": [],
  "lowest_pillar": ""
}`

var pending = map[string]string{"G2": quality.KindGate, "D1": quality.KindScore, "B2": quality.KindScore}

// TestReadSelfReview: the identity, head and pending criteria with their kinds are read; another schema, a missing
// identity or number and a head that is not an object id are refused.
func TestReadSelfReview(t *testing.T) {
	got, err := quality.ReadSelfReview([]byte(selfReview))
	if err != nil {
		t.Fatal(err)
	}
	want := quality.SelfReview{Repository: "app", Task: "t-1", Number: 1, Head: head, Pending: pending}
	if got.Repository != want.Repository || got.Task != want.Task || got.Number != want.Number || got.Head != want.Head || !mapsEqual(got.Pending, want.Pending) {
		t.Errorf("ReadSelfReview = %+v, want %+v", got, want)
	}
	for name, edit := range map[string][2]string{
		"another schema": {`guildhall.self-review/1`, `guildhall.self-review/2`},
		"no task":        {`"task": "t-1"`, `"task": ""`},
		"no number":      {`"number": 1`, `"number": 0`},
		"a short head":   {head, "9a8b7c"},
		"not json":       {`"schema"`, `schema`},
	} {
		if _, err := quality.ReadSelfReview([]byte(strings.Replace(selfReview, edit[0], edit[1], 1))); !errors.Is(err, quality.ErrSelfReview) {
			t.Errorf("%s: err = %v, want ErrSelfReview", name, err)
		}
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

const answer = `{"merge_as_is": "yes", "criteria": [
  {"id": "D1", "score": 3, "evidence": "140 non-test lines in 6 files"},
  {"id": "B2", "score": 2, "evidence": "internal/x/x_test.go:12 names its cases"},
  {"id": "G2", "pass": true, "evidence": "no assertion removed in the diff"}
]}`

// TestParseAnswer: an answer judges each pending criterion once, a score from 0 to 3 or a gate's pass, with
// evidence; every other shape is refused.
func TestParseAnswer(t *testing.T) {
	a, err := quality.ParseAnswer([]byte(answer), pending)
	if err != nil {
		t.Fatal(err)
	}
	if a.MergeAsIs != "yes" || len(a.Criteria) != 3 || *a.Criteria[0].Score != 3 || !*a.Criteria[2].Pass {
		t.Errorf("ParseAnswer = %+v", a)
	}
	for name, edit := range map[string][2]string{
		"merge maybe":         {`"merge_as_is": "yes"`, `"merge_as_is": "maybe"`},
		"an unknown field":    {`"merge_as_is": "yes",`, `"merge_as_is": "yes", "verdict": "fine",`},
		"a criterion not due": {`"id": "D1"`, `"id": "C2"`},
		"a duplicate":         {`"id": "B2"`, `"id": "D1"`},
		"a missing one": {`,
  {"id": "G2", "pass": true, "evidence": "no assertion removed in the diff"}`, ``},
		"a score above 3":   {`"score": 3`, `"score": 4`},
		"a negative score":  {`"score": 3`, `"score": -1`},
		"a fractional one":  {`"score": 3`, `"score": 2.5`},
		"a score on a gate": {`"pass": true`, `"score": 1`},
		"a pass on a score": {`"score": 3`, `"pass": true`},
		"both":              {`"score": 3`, `"score": 3, "pass": true`},
		"blank evidence":    {`"evidence": "140 non-test lines in 6 files"`, `"evidence": "  "`},
		"two objects":       {`]}`, `]} {}`},
	} {
		if _, err := quality.ParseAnswer([]byte(strings.Replace(answer, edit[0], edit[1], 1)), pending); !errors.Is(err, quality.ErrAnswer) {
			t.Errorf("%s: err = %v, want ErrAnswer", name, err)
		}
	}
	if _, err := quality.ParseAnswer(bytes.Repeat([]byte(" "), 1<<20+1), pending); !errors.Is(err, quality.ErrAnswer) {
		t.Errorf("an answer over 1 MiB: err = %v, want ErrAnswer", err)
	}
}

// launched is one call of the recording launcher.
type launched struct {
	Key                          artisan.AttemptKey
	Adapter, PromptFile, WorkDir string
}

// recorder is a Launcher that records each launch and fails the ones fail says to.
type recorder struct {
	mu    sync.Mutex
	calls []launched
	fail  func(n int) error
}

func (r *recorder) LaunchFiles(_ context.Context, k artisan.AttemptKey, adapterName, promptFile, workDir string) (interp.Handle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, launched{k, adapterName, promptFile, workDir})
	if r.fail != nil {
		if err := r.fail(len(r.calls)); err != nil {
			return interp.Handle{}, err
		}
	}
	return interp.Handle{Identity: adapter.Identity{TaskID: k.TaskID, AttemptID: k.AttemptID()}, Ref: k.String()}, nil
}

// outbox records each message once by id, as the store does.
type outbox struct {
	mu   sync.Mutex
	sent []messaging.Envelope
	err  error
}

func (o *outbox) Send(_ store.Recipient, e messaging.Envelope) (store.Receipt, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return store.Receipt{}, o.err
	}
	for _, s := range o.sent {
		if s.ID == e.ID {
			return store.Receipt{}, store.ErrMutationConflict
		}
	}
	o.sent = append(o.sent, e)
	return store.Receipt{}, nil
}

func (o *outbox) types() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []string
	for _, e := range o.sent {
		out = append(out, e.Type)
	}
	return out
}

// env is a delivered task app/t-1, built under profile builder-claude, with self-review 1 written, over a real store.
type env struct {
	t     *testing.T
	s     *store.Store
	judge *quality.Judge
	l     *recorder
	o     *outbox
}

const judgeConfig = `schema_version = 1
mode = "director"
name = "alpha"

[local]
quality_judge_profiles = "judge-fake, judge-claude"

[[repository]]
id = "app"
path = "/srv/app"
remote = "git@github.com:example/app.git"
owner_machine = "workstation"

[profile.builder-claude]
adapter = "claude"

[profile.judge-claude]
adapter = "claude"

[profile.judge-fake]
adapter = "fake"
`

func newEnv(t *testing.T, cfgText string, beforeDelivery ...func(*env)) *env {
	t.Helper()
	root := t.TempDir()
	s, err := store.Open(filepath.Join(root, "director.store"), store.Options{LockTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cfg, err := config.Parse([]byte(cfgText))
	if err != nil {
		t.Fatal(err)
	}
	d := "sha256:" + strings.Repeat("a", 64)
	if _, err := s.CreateTask("task", store.Task{Repository: "app", ID: "t-1", Title: "Task", Workflow: workflow.Revision{Name: "default", Digest: d}, ConfigDigest: d}); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, s: s, l: &recorder{}, o: &outbox{}}
	e.judge = &quality.Judge{Records: s, Outbox: e.o, Launcher: e.l, Config: cfg,
		StateDir: filepath.Join(root, "state"), DocumentDir: filepath.Join(root, "state", "documents"),
		QualityDir: filepath.Join(root, "quality"), Role: e.write(filepath.Join(root, "judge.md"), "You are the judge.\n"),
		ProfileRole: func(string) string { return "" }}
	e.build("builder-claude")
	for _, prepare := range beforeDelivery {
		prepare(e)
	}
	if _, err := s.SetTaskState("done", "app", "t-1", store.TaskDone); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.judge.QualityDir, "app", "t-1")
	e.write(filepath.Join(dir, "self-review-1.json"), selfReview)
	e.write(filepath.Join(dir, "self-review-1-judge.md"), "Score D1, B2 and G2 against docs/quality.md.\n")
	e.write(filepath.Join(dir, "self-review-1.diff"), "diff --git a/x b/x\n")
	return e
}

func (e *env) write(name, text string) string {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return name
}

// build records a succeeded build attempt of app/t-1 rendered for profile, with its prompt manifest.
func (e *env) build(profile string) {
	e.t.Helper()
	r, err := e.s.StartAttempt("build", "app", "t-1", "build")
	if err != nil {
		e.t.Fatal(err)
	}
	m := render.Manifest{Schema: render.ManifestSchema, Identity: render.Identity{Repository: "app", Task: "t-1", Step: "build", Kind: "build", KindVersion: 1, Attempt: r.Attempt, Profile: profile}}
	data, err := json.Marshal(m)
	if err != nil {
		e.t.Fatal(err)
	}
	k := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "build", Attempt: r.Attempt}
	e.write(filepath.Join(k.Dir(e.judge.StateDir), "prompt", "manifest.json"), string(data))
	if _, err := e.s.FinishAttempt("build-finish", "app", "t-1", "build", r.Attempt, store.AttemptSucceeded); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) judgeAttempts() []store.StageAttempt {
	e.t.Helper()
	as, err := e.s.Attempts("app", "t-1")
	if err != nil {
		e.t.Fatal(err)
	}
	return slices.DeleteFunc(as, func(a store.StageAttempt) bool { return a.Step != workflow.JudgeStep })
}

func (e *env) key(n int) artisan.AttemptKey {
	return artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: workflow.JudgeStep, Attempt: n}
}

// TestStartRefusals: Start starts nothing for a task that is not done, a self-review that is missing, foreign or
// has nothing pending, or while a launched judge attempt has not finished.
func TestStartRefusals(t *testing.T) {
	e := newEnv(t, judgeConfig)
	if _, err := e.s.CreateTask("open", store.Task{Repository: "app", ID: "t-2", Title: "Open", Workflow: workflow.Revision{Name: "default", Digest: "sha256:" + strings.Repeat("a", 64)}, ConfigDigest: "sha256:" + strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.judge.Start(context.Background(), "app", "t-2", 1); !errors.Is(err, quality.ErrNotDelivered) {
		t.Errorf("an open task: err = %v, want ErrNotDelivered", err)
	}
	if _, err := e.judge.Start(context.Background(), "app", "t-1", 2); !errors.Is(err, quality.ErrSelfReview) {
		t.Errorf("a missing self-review: err = %v, want ErrSelfReview", err)
	}
	dir := filepath.Join(e.judge.QualityDir, "app", "t-1")
	e.write(filepath.Join(dir, "self-review-3.json"), selfReview)
	if _, err := e.judge.Start(context.Background(), "app", "t-1", 3); !errors.Is(err, quality.ErrSelfReview) {
		t.Errorf("a self-review numbered 1 read as 3: err = %v, want ErrSelfReview", err)
	}
	e.write(filepath.Join(dir, "self-review-4.json"), strings.ReplaceAll(strings.Replace(selfReview, `"number": 1`, `"number": 4`, 1), `"judge": "pending"`, `"judge": "model"`))
	if _, err := e.judge.Start(context.Background(), "app", "t-1", 4); !errors.Is(err, quality.ErrSelfReview) {
		t.Errorf("nothing pending: err = %v, want ErrSelfReview", err)
	}
	if len(e.l.calls) != 0 || len(e.judgeAttempts()) != 0 {
		t.Fatalf("a refused start launched %+v and recorded %+v", e.l.calls, e.judgeAttempts())
	}
	if _, err := e.judge.Start(context.Background(), "app", "t-1", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.judge.Start(context.Background(), "app", "t-1", 1); !errors.Is(err, quality.ErrJudgeRunning) {
		t.Errorf("a second start while the first runs: err = %v, want ErrJudgeRunning", err)
	}
	if len(e.l.calls) != 1 {
		t.Errorf("launches %+v, want the first only", e.l.calls)
	}
}

func TestStartLaunchesAJudgeWithUnknownBuilderFamily(t *testing.T) {
	e := newEnv(t, judgeConfig)
	got, err := e.judge.Start(context.Background(), "app", "t-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := config.JudgeChoice{Profile: "judge-fake", Adapter: "fake", Reason: "the builder's family is unknown"}
	if got.Attempt != 1 || got.Review != 1 || got.Judge != want {
		t.Errorf("Start = %+v, want attempt 1 of review 1 judged by %+v", got, want)
	}
	if as := e.judgeAttempts(); len(as) != 1 || as[0].Number != 1 || as[0].Outcome != "" {
		t.Errorf("judge attempts %+v, want one running", as)
	}
	dir := e.key(1).Dir(e.judge.StateDir)
	if len(e.l.calls) != 1 {
		t.Fatalf("launches %+v, want one", e.l.calls)
	}
	c := e.l.calls[0]
	if c.Key != e.key(1) || c.Adapter != "fake" || c.PromptFile != filepath.Join(dir, "prompt", render.PromptFile) || c.WorkDir != filepath.Join(dir, "work") {
		t.Errorf("launched %+v", c)
	}
	prompt, err := os.ReadFile(c.PromptFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"You are the judge.", "Score D1, B2 and G2 against docs/quality.md.", quality.AnswerFile, "B2", "D1", "G2"} {
		if !strings.Contains(string(prompt), s) {
			t.Errorf("prompt does not contain %q:\n%s", s, prompt)
		}
	}
	for _, name := range []string{"self-review.json", "judge.md", "change.diff"} {
		if _, err := os.Stat(filepath.Join(dir, "prompt", render.InputsDir, name)); err != nil {
			t.Errorf("input %s: %v", name, err)
		}
	}
	if entries, err := os.ReadDir(c.WorkDir); err != nil || len(entries) != 0 {
		t.Errorf("working directory %v, %v; want it empty", entries, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "launched.json")); err != nil {
		t.Errorf("not marked launched: %v", err)
	}
}

// TestStartPassesOverAFailedProfile: after the first judge failed, the next start for the same self-review falls
// back to the next listed profile and says why its family is not separate.
func TestStartPassesOverAFailedProfile(t *testing.T) {
	e := newEnv(t, judgeConfig)
	if _, err := e.judge.Start(context.Background(), "app", "t-1", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.FinishAttempt("fail", "app", "t-1", workflow.JudgeStep, 1, store.AttemptFailed); err != nil {
		t.Fatal(err)
	}
	got, err := e.judge.Start(context.Background(), "app", "t-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := config.JudgeChoice{Profile: "judge-claude", Adapter: "claude",
		Reason: "the builder's family is unknown"}
	if got.Attempt != 2 || got.Judge != want {
		t.Errorf("Start = %+v, want attempt 2 judged by %+v", got, want)
	}
	if _, err := e.s.FinishAttempt("fail-2", "app", "t-1", workflow.JudgeStep, 2, store.AttemptFailed); err != nil {
		t.Fatal(err)
	}
	if _, err := e.judge.Start(context.Background(), "app", "t-1", 1); !errors.Is(err, config.ErrNoJudge) {
		t.Errorf("every profile failed: err = %v, want ErrNoJudge", err)
	}
}

// TestStartReplaysAnUnlaunchedAttempt: a launch that failed leaves the attempt recorded but not marked launched,
// and the next start launches the same attempt from the same preparation instead of recording another.
func TestStartReplaysAnUnlaunchedAttempt(t *testing.T) {
	e := newEnv(t, judgeConfig)
	e.l.fail = func(n int) error {
		if n == 1 {
			return errors.New("launcher lost")
		}
		return nil
	}
	if _, err := e.judge.Start(context.Background(), "app", "t-1", 1); err == nil {
		t.Fatal("a failed launch was not an error")
	}
	prompt := filepath.Join(e.key(1).Dir(e.judge.StateDir), "prompt", render.PromptFile)
	before, err := os.ReadFile(prompt)
	if err != nil {
		t.Fatal(err)
	}
	e.write(filepath.Join(e.judge.QualityDir, "app", "t-1", "self-review-1-judge.md"), "A changed brief.\n")
	got, err := e.judge.Start(context.Background(), "app", "t-1", 1)
	if err != nil || got.Attempt != 1 {
		t.Fatalf("replayed Start = %+v, %v; want attempt 1", got, err)
	}
	after, err := os.ReadFile(prompt)
	if err != nil || !bytes.Equal(before, after) {
		t.Errorf("the published preparation changed on replay: %v", err)
	}
	if len(e.l.calls) != 2 || e.l.calls[1].Key != e.key(1) || len(e.judgeAttempts()) != 1 {
		t.Errorf("launches %+v and attempts %+v, want attempt 1 launched twice and recorded once", e.l.calls, e.judgeAttempts())
	}
}

// TestStartUnderACap: a judge attempt counts against the guard rails; a refused start records and launches nothing.
func TestStartUnderACap(t *testing.T) {
	e := newEnv(t, judgeConfig)
	e.judge.Caps = store.AttemptCaps{MaxPerDay: 1}
	_, err := e.judge.Start(context.Background(), "app", "t-1", 1)
	if refusal, ok := errors.AsType[*store.CapRefusal](err); !ok || refusal.Cap != store.CapDaily {
		t.Fatalf("Start over the daily cap: err = %v, want the daily cap's refusal", err)
	}
	if len(e.l.calls) != 0 || len(e.judgeAttempts()) != 0 {
		t.Errorf("a capped start launched %+v and recorded %+v", e.l.calls, e.judgeAttempts())
	}
}

// finish ends judge attempt n as its launcher would: the answer in its working directory, a result naming it as
// the first output and the store outcome.
func (e *env) finish(n int, status adapter.Status, answer string) {
	e.t.Helper()
	k := e.key(n)
	if answer != "" {
		e.write(filepath.Join(k.Dir(e.judge.StateDir), "work", quality.AnswerFile), answer)
	}
	r := adapter.Result{Identity: adapter.Identity{TaskID: "t-1", AttemptID: k.AttemptID()},
		Status: status, Summary: "Judged.", Outputs: []adapter.Output{{Path: quality.AnswerFile}}}
	if err := adapter.WriteResult(filepath.Join(k.Dir(e.judge.StateDir), artisan.ResultFile), r); err != nil {
		e.t.Fatal(err)
	}
	outcome := store.AttemptSucceeded
	if status == adapter.StatusFailed {
		outcome = store.AttemptFailed
	}
	if _, err := e.s.FinishAttempt("finish-"+k.AttemptID(), "app", "t-1", workflow.JudgeStep, n, outcome); err != nil {
		e.t.Fatal(err)
	}
}

// TestTakeRecordsTheJudgement: a succeeded judge's accepted answer is recorded once as quality-judge-1-judgement,
// with the self-review's identity and head, the attempt and the choice, and the Director is told once.
func TestTakeRecordsTheJudgement(t *testing.T) {
	e := newEnv(t, judgeConfig)
	if _, err := e.judge.Take("app", "t-1", 1); !errors.Is(err, quality.ErrNotJudge) {
		t.Errorf("Take of no attempt: err = %v, want ErrNotJudge", err)
	}
	if _, err := e.judge.Start(context.Background(), "app", "t-1", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.judge.Take("app", "t-1", 1); !errors.Is(err, quality.ErrJudgeRunning) {
		t.Errorf("Take of a running attempt: err = %v, want ErrJudgeRunning", err)
	}
	e.finish(1, adapter.StatusSucceeded, answer)
	e.o.err = errors.New("notification unavailable")
	if _, err := e.judge.Take("app", "t-1", 1); !errors.Is(err, e.o.err) {
		t.Fatalf("Take notification failure = %v", err)
	}
	e.o.err = nil
	if err := os.RemoveAll(filepath.Join(e.key(1).Dir(e.judge.StateDir), "work")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got, err := e.judge.Take("app", "t-1", 1)
		if err != nil || got.Document != "quality-judge-1-judgement" || got.Refused != "" {
			t.Fatalf("Take = %+v, %v; want the judgement recorded", got, err)
		}
	}
	docs, err := e.s.Documents("app", "t-1")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(docs, func(d store.DocumentRef) bool { return d.Name == "quality-judge-1-judgement" })
	if i < 0 {
		t.Fatalf("documents %+v, want the judgement", docs)
	}
	data, err := os.ReadFile(filepath.Join(e.judge.DocumentDir, filepath.FromSlash(docs[i].Path)))
	if err != nil || digest.Of(data).Hex() != docs[i].SHA256 {
		t.Fatalf("judgement file %v, or its digest is not the recorded one", err)
	}
	j, err := quality.ParseJudgement(data)
	if err != nil {
		t.Fatal(err)
	}
	if j.Repository != "app" || j.Task != "t-1" || j.Review != 1 || j.Head != head || j.Attempt != 1 ||
		j.Judge.Profile != "judge-fake" || j.Judge.SeparateFamily || j.Judge.Builder != "" || j.MergeAsIs != "yes" || len(j.Criteria) != 3 {
		t.Errorf("judgement %+v", j)
	}
	if got := e.o.types(); !slices.Equal(got, []string{"judgement"}) {
		t.Errorf("messages %v, want one judgement", got)
	}
}

// TestTakeRefusesAnAnswer: an answer that breaks its contract records nothing and tells the Director why.
func TestTakeRefusesAnAnswer(t *testing.T) {
	e := newEnv(t, judgeConfig)
	if _, err := e.judge.Start(context.Background(), "app", "t-1", 1); err != nil {
		t.Fatal(err)
	}
	e.finish(1, adapter.StatusSucceeded, `{"merge_as_is": "perhaps", "criteria": []}`)
	got, err := e.judge.Take("app", "t-1", 1)
	if err != nil || got.Document != "" || !strings.Contains(got.Refused, "merge_as_is") {
		t.Fatalf("Take = %+v, %v; want the answer refused naming merge_as_is", got, err)
	}
	docs, _ := e.s.Documents("app", "t-1")
	if len(docs) != 0 {
		t.Errorf("documents %+v, want none", docs)
	}
	if got := e.o.types(); !slices.Equal(got, []string{"judgement-refused"}) {
		t.Errorf("messages %v, want one judgement-refused", got)
	}
}

func TestTakeRetryGuidance(t *testing.T) {
	for _, tc := range []struct {
		outcome     store.AttemptOutcome
		guidance    string
		nextProfile string
	}{
		{store.AttemptFailed, "passing over profile judge-fake", "judge-claude"},
		{store.AttemptStopped, "a stopped attempt does not exclude profile judge-fake, so unchanged settings choose it again", "judge-fake"},
	} {
		t.Run(string(tc.outcome), func(t *testing.T) {
			e := newEnv(t, judgeConfig)
			if _, err := e.judge.Start(context.Background(), "app", "t-1", 1); err != nil {
				t.Fatal(err)
			}
			if _, err := e.s.FinishAttempt("finish-judge", "app", "t-1", workflow.JudgeStep, 1, tc.outcome); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				got, err := e.judge.Take("app", "t-1", 1)
				if err != nil || got.Document != "" || got.Refused != "the attempt ended "+string(tc.outcome) {
					t.Fatalf("Take = %+v, %v", got, err)
				}
			}
			if len(e.o.sent) != 1 || e.o.sent[0].Type != "judge-failed" || !strings.Contains(e.o.sent[0].Text, tc.guidance) {
				t.Fatalf("messages %+v, want retry guidance %q", e.o.sent, tc.guidance)
			}
			if tc.outcome == store.AttemptStopped && strings.Contains(e.o.sent[0].Text, "passing over") {
				t.Fatal("stopped judge promises to pass over its profile")
			}
			docs, err := e.s.Documents("app", "t-1")
			if err != nil || len(docs) != 0 {
				t.Fatalf("documents = %+v, %v", docs, err)
			}
			next, err := e.judge.Start(context.Background(), "app", "t-1", 1)
			if err != nil || next.Attempt != 2 || next.Judge.Profile != tc.nextProfile {
				t.Fatalf("retry = %+v, %v; want profile %s", next, err, tc.nextProfile)
			}
		})
	}
}

// TestAgreement: each task's latest judgement is paired with its answered audit; the totals count agreement
// overall and by whether the judge's family was separate, and an unpaired task counts as audited or judged only.
func TestAgreement(t *testing.T) {
	root := t.TempDir()
	s, err := store.Open(filepath.Join(root, "director.store"), store.Options{LockTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	docs := filepath.Join(root, "documents")
	d := "sha256:" + strings.Repeat("a", 64)
	task := func(id string) {
		if _, err := s.CreateTask("task-"+id, store.Task{Repository: "app", ID: id, Title: "Task", Workflow: workflow.Revision{Name: "default", Digest: d}, ConfigDigest: d}); err != nil {
			t.Fatal(err)
		}
	}
	audit := func(id, choice string) {
		dec := store.Decision{Repository: "app", TaskID: id, ID: "audit", Kind: store.DecisionAudit, Subject: &store.Subject{Kind: store.SubjectHead, Digest: head},
			Options: []string{"yes", "no"}, Question: "Audit?"}
		if _, err := s.OpenDecision("audit-"+id, dec); err != nil {
			t.Fatal(err)
		}
		if choice != "" {
			if _, err := s.AnswerDecision("answer-"+id, "app", id, "audit", choice+"\n\nnothing else"); err != nil {
				t.Fatal(err)
			}
		}
	}
	judged := func(id string, n int, merge string, separate bool) {
		j := quality.Judgement{Schema: quality.JudgementSchema, Repository: "app", Task: id, Review: 1, Head: head, Attempt: n,
			Judge: config.JudgeChoice{Adapter: "fake", SeparateFamily: separate}, MergeAsIs: merge, Criteria: []quality.Judgment{}}
		data, err := json.Marshal(j)
		if err != nil {
			t.Fatal(err)
		}
		rel := "tasks/app/" + id + "/" + artisan.AttemptKey{Repository: "app", TaskID: id, Step: workflow.JudgeStep, Attempt: n}.AttemptID() + "/judgement.json"
		file := filepath.Join(docs, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0o644); err != nil {
			t.Fatal(err)
		}
		name := artisan.AttemptKey{Step: workflow.JudgeStep, Attempt: n}.AttemptID() + "-judgement"
		if _, err := s.PutDocument("doc-"+id+name, store.DocumentRef{Repository: "app", TaskID: id, Name: name, Path: rel, SHA256: digest.Of(data).Hex()}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"t-1", "t-2", "t-3", "t-4", "t-5"} {
		task(id)
	}
	judged("t-1", 1, "yes", true)
	audit("t-1", "yes")
	judged("t-2", 1, "yes", false)
	judged("t-2", 2, "no", false)
	audit("t-2", "yes")
	audit("t-3", "no")
	judged("t-4", 1, "yes", true)
	audit("t-5", "")
	got, err := quality.Agreement(s, docs, []string{"app"})
	if err != nil {
		t.Fatal(err)
	}
	wantPairs := []quality.Pair{
		{Repository: "app", Task: "t-1", Audit: "yes", Judge: "yes", Agree: true, SeparateFamily: true},
		{Repository: "app", Task: "t-2", Audit: "yes", Judge: "no", Agree: false, SeparateFamily: false},
	}
	if !slices.Equal(got.Pairs, wantPairs) || got.Audited != 3 || got.Judged != 3 ||
		got.Total != (quality.Count{Paired: 2, Agreed: 1}) || got.SeparateFamily != (quality.Count{Paired: 1, Agreed: 1}) || got.SameFamily != (quality.Count{Paired: 1, Agreed: 0}) {
		t.Errorf("Agreement = %+v", got)
	}
}

package interp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/messaging/messagingtest"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// head is the code head the scripted build lands, written as its output file.
const head = "0123456789abcdef0123456789abcdef01234567"

func TestRecordsContract(t *testing.T) {
	t.Run("store", func(t *testing.T) { interptest.RunRecordsContract(t, storeRecords) })
	t.Run("double", func(t *testing.T) { interptest.RunRecordsContract(t, interptest.NewRecordsHarness) })
}

func storeRecords(t *testing.T) interptest.RecordsHarness {
	s, err := store.Open(filepath.Join(t.TempDir(), "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return interptest.RecordsHarness{Records: s, CreateTask: s.CreateTask, AnswerDecision: s.AnswerDecision}
}

// backend is one set of records and mailbox the engine runs over.
type backend struct {
	name string
	new  func(t *testing.T) (interptest.RecordsHarness, messaging.Mailbox)
}

var backends = []backend{
	{"double", func(t *testing.T) (interptest.RecordsHarness, messaging.Mailbox) {
		fake := messagingtest.NewFake()
		fake.AddTask("app", "t7")
		return interptest.NewRecordsHarness(t), fake
	}},
	{"store", func(t *testing.T) (interptest.RecordsHarness, messaging.Mailbox) {
		h := storeRecords(t)
		return h, &messaging.StoreMailbox{Store: h.Records.(*store.Store)}
	}},
}

// env is one engine over one backend with task app/t7 bound to the straight workflow.
type env struct {
	eng     *interp.Engine
	h       interptest.RecordsHarness
	mailbox messaging.Mailbox
	script  *interptest.Script
	mu      sync.Mutex
	stages  []render.Stage
}

func newEnv(t *testing.T, b backend, exps ...interptest.Expectation) *env {
	return newEnvDefinition(t, b, straight(t), exps...)
}

func newEnvDefinition(t *testing.T, b backend, def *workflow.Definition, exps ...interptest.Expectation) *env {
	t.Helper()
	h, mailbox := b.new(t)
	task := store.Task{Repository: "app", ID: "t7", Title: "Straight path", Workflow: def.Revision(), ConfigDigest: "sha256:" + strings.Repeat("c", 64)}
	if _, err := h.CreateTask("task:app:t7", task); err != nil {
		t.Fatal(err)
	}
	e := &env{h: h, mailbox: mailbox, script: interptest.NewScript(t, exps...)}
	e.eng = &interp.Engine{
		Records:  h.Records,
		Launcher: e.script,
		Prompter: interp.PromptFunc(e.prompt),
		Outbox:   mailbox,
		Workflow: func(rev workflow.Revision) (*workflow.Definition, error) {
			if rev.Name != def.Name {
				return nil, errors.New("unknown workflow")
			}
			return def, nil
		},
		Config: &config.Config{
			Profiles: map[string]config.Profile{
				"planner":  {Adapter: interp.MockAdapter},
				"builder":  {Adapter: interp.MockAdapter},
				"reviewer": {Adapter: interp.MockAdapter},
			},
			Repositories: []config.Repository{{ID: "app", MergePolicy: config.MergeApproval}},
		},
		StandardsRevision: "standards-1",
		DefaultAdapter:    interp.MockAdapter,
		DocumentDir:       t.TempDir(),
		StateDir:          t.TempDir(),
	}
	return e
}

func straight(t *testing.T) *workflow.Definition {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(interptest.Testdata(), "workflows", "straight.json"))
	if err != nil {
		t.Fatal(err)
	}
	def, err := workflow.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.Validate(def); err != nil {
		t.Fatal(err)
	}
	return def
}

// prompt renders from the shared fixture baseline and records each stage it was asked for.
func (e *env) prompt(stage render.Stage, inputs []render.Input) (render.Rendered, error) {
	e.mu.Lock()
	e.stages = append(e.stages, stage)
	e.mu.Unlock()
	src := func(name string) render.Source {
		return render.Source{Name: name, Path: filepath.Join(interptest.Testdata(), "baseline", filepath.FromSlash(name))}
	}
	b := render.Baseline{Role: src("roles/artisan.md"), Guide: src("stages/" + stage.Step.Type + ".md"), Standards: []render.Source{src("standards/go.md")}}
	if stage.Step.Type == "review" {
		b.Role = src("roles/reviewer.md")
		b.Standards = append(b.Standards, src("standards/review.md"))
	}
	roots := []render.SkillRoot{
		{Name: "release", Dir: filepath.Join(interptest.Testdata(), "skills", "release")},
		{Name: "managed", Dir: filepath.Join(interptest.Testdata(), "skills", "managed")},
	}
	return render.Prompt(stage, inputs, b, roots)
}

func (e *env) advance(t *testing.T, ev interp.Event) interp.Next {
	t.Helper()
	ev.Repository, ev.Task = "app", "t7"
	next, err := e.eng.Advance(context.Background(), ev)
	if err != nil {
		t.Fatalf("Advance(%+v) = %v", ev, err)
	}
	t.Logf("Advance(%+v) => %+v", ev, next)
	return next
}

func (e *env) answer(t *testing.T, id, words string) {
	t.Helper()
	if _, err := e.h.AnswerDecision("answer:"+id, "app", "t7", id, words); err != nil {
		t.Fatal(err)
	}
}

func (e *env) attempts(t *testing.T) []store.StageAttempt {
	t.Helper()
	a, err := e.h.Records.Attempts("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (e *env) documents(t *testing.T) map[string]store.DocumentRef {
	t.Helper()
	docs, err := e.h.Records.Documents("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.DocumentRef{}
	for _, d := range docs {
		out[d.Name] = d
		t.Logf("document: %+v", d)
	}
	return out
}

func (e *env) decision(t *testing.T, id string) store.Decision {
	t.Helper()
	ds, err := e.h.Records.Decisions("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.ID == id {
			t.Logf("decision: %+v; subject: %+v", d, d.Subject)
			return d
		}
	}
	t.Fatalf("decision %q not recorded; have %+v", id, ds)
	return store.Decision{}
}

// inbox returns the ids of the Director's unacknowledged messages.
func (e *env) inbox(t *testing.T) []string {
	t.Helper()
	page, err := e.mailbox.List(store.Recipient{Role: store.RecipientDirector}, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range page.Messages {
		ids = append(ids, m.ID)
	}
	return ids
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func specV1(t *testing.T) []byte {
	data, err := os.ReadFile(filepath.Join(interptest.Testdata(), "docs", "spec-v1.md"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func planExpectation(t *testing.T, r interptest.Response) interptest.Expectation {
	return interptest.Expectation{Step: "plan", Attempt: 1, Profile: "planner", Inputs: map[string]string{}, Standards: []string{"standards/go.md"}, Respond: r}
}

// straightScript is the mock agent's script for the whole straight path.
func straightScript(t *testing.T) []interptest.Expectation {
	spec := "sha256:" + sum(specV1(t))
	headFile := []byte(head + "\n")
	return []interptest.Expectation{
		planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
		{
			Step: "build", Attempt: 1, Profile: "builder", Inputs: map[string]string{"spec": spec},
			Standards: []string{"standards/go.md"}, Skills: []string{"go-tdd", "guildhall-contributing"},
			Respond: interptest.Succeed("built", interptest.File{Path: "head", Data: headFile}),
		},
		{
			Step: "review", Attempt: 1, Profile: "reviewer", Inputs: map[string]string{"spec": spec, "head": "sha256:" + sum(headFile)},
			Standards: []string{"standards/go.md", "standards/review.md"},
			Respond:   interptest.Review(interptest.ReviewFixture(t, "approved")),
		},
	}
}

func TestStraightPath(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnv(t, b, straightScript(t)...)
			spec := sum(specV1(t))

			// task-created: the first Artisan step is rendered, recorded and launched.
			next := e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if want := (interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1}); next != want {
				t.Fatalf("after task-created: %+v, want %+v", next, want)
			}
			if got := e.attempts(t); len(got) != 1 || got[0].Step != "plan" || got[0].Number != 1 || got[0].Outcome != "" {
				t.Fatalf("attempts after launch = %+v", got)
			}
			if st := e.stages[0]; st.Repository != "app" || st.Task != "t7" || st.Step.ID != "plan" || st.Attempt != 1 || !strings.HasPrefix(st.Dir, e.eng.StateDir) {
				t.Errorf("rendered stage = %+v", st)
			}

			// The plan's result records its output and opens the approval bound to it.
			next = e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			if want := (interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"}); next != want {
				t.Fatalf("after the plan result: %+v, want %+v", next, want)
			}
			if got := e.attempts(t)[0].Outcome; got != store.AttemptSucceeded {
				t.Errorf("plan outcome = %q", got)
			}
			planDoc, ok := e.documents(t)["plan-1-spec"]
			if !ok || planDoc.SHA256 != spec {
				t.Fatalf("plan document = %+v, want sha256 %s", planDoc, spec)
			}
			copied, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, filepath.FromSlash(planDoc.Path)))
			if err != nil || sum(copied) != spec {
				t.Errorf("document file %s: %v", planDoc.Path, err)
			}
			accept := e.decision(t, "accept-1")
			if accept.Kind != store.DecisionApproval || accept.Subject == nil || *accept.Subject != (store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + planDoc.SHA256}) ||
				!slices.Equal(accept.Options, []string{"approved", "changes_requested"}) || accept.Answered {
				t.Errorf("approval decision = %+v", accept)
			}

			// Approving records the accepted spec and launches the build with it.
			e.answer(t, "accept-1", "approved\nGo ahead.")
			next = e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
			if want := (interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1}); next != want {
				t.Fatalf("after approval: %+v, want %+v", next, want)
			}
			if d := e.documents(t)["accept-1-spec"]; d.SHA256 != spec || d.Path != planDoc.Path {
				t.Errorf("accepted spec = %+v", d)
			}

			// The build's head flows to the Artisan review, whose approval opens the Guildmaster sign-off.
			next = e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
			if want := (interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 1}); next != want {
				t.Fatalf("after the build result: %+v, want %+v", next, want)
			}
			next = e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
			if want := (interp.Next{Wait: interp.WaitDecision, Step: "sign-off", Decision: "sign-off-1"}); next != want {
				t.Fatalf("after the review result: %+v, want %+v", next, want)
			}
			if _, ok := e.documents(t)["review-1-outcome"]; !ok {
				t.Error("review outcome not recorded as a document")
			}
			signOff := e.decision(t, "sign-off-1")
			if signOff.Kind != store.DecisionReview || signOff.Subject == nil || *signOff.Subject != (store.Subject{Kind: store.SubjectHead, Digest: head}) {
				t.Errorf("sign-off decision = %+v", signOff)
			}

			// Deliver under the approval merge policy opens the merge decision for the head.
			e.answer(t, "sign-off-1", "approved")
			next = e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "sign-off-1"})
			if want := (interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"}); next != want {
				t.Fatalf("after sign-off: %+v, want %+v", next, want)
			}
			merge := e.decision(t, "deliver-1")
			if merge.Kind != store.DecisionMerge || merge.Subject == nil || *merge.Subject != (store.Subject{Kind: store.SubjectHead, Digest: head}) || len(merge.Options) == 0 {
				t.Errorf("merge decision = %+v", merge)
			}
			doc := e.documents(t)["sign-off-1-outcome"]
			data, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, doc.Path))
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := workflow.ParseReviewOutcome(data)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("persisted human code review: %s", data)
			if outcome.Reviewer != workflow.HumanReviewer || outcome.Revisions.Subject.CodeHead != head || outcome.Revisions.SpecDigest != "sha256:"+spec || outcome.Revisions.StandardsRevision != "standards-1" {
				t.Fatalf("human outcome = %+v", outcome)
			}
			e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "sign-off-1"})
			if got, want := e.inbox(t), []string{"app.t7.decision.accept-1", "app.t7.decision.sign-off-1", "app.t7.decision.deliver-1"}; !slices.Equal(got, want) {
				t.Errorf("Director inbox = %q, want %q", got, want)
			}

			// Replaying any earlier event records nothing new and launches nothing.
			before := len(e.attempts(t))
			for _, ev := range []interp.Event{{Kind: interp.TaskCreated}, {Kind: interp.AttemptResult, Step: "plan", Attempt: 1}, {Kind: interp.DecisionAnswered, Decision: "accept-1"}} {
				if got := e.advance(t, ev); got != next {
					t.Errorf("replayed %s: %+v, want %+v", ev.Kind, got, next)
				}
			}
			if len(e.attempts(t)) != before || len(e.script.Launched()) != 3 || len(e.inbox(t)) != 3 {
				t.Errorf("replay changed records: %d attempts, %d launches", len(e.attempts(t)), len(e.script.Launched()))
			}
		})
	}
}

func advanceErr(t *testing.T, e *env, ev interp.Event) error {
	t.Helper()
	ev.Repository, ev.Task = "app", "t7"
	_, err := e.eng.Advance(context.Background(), ev)
	t.Logf("Advance(%+v) refused: %v", ev, err)
	return err
}

func TestSpecGateRefusesAnotherAcceptedSpec(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnv(t, b, straightScript(t)[0])
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			// The accepted spec recorded for the approval is not the document the Guildmaster was asked about.
			other := store.DocumentRef{Repository: "app", TaskID: "t7", Name: "accept-1-spec", Path: "tasks/app/t7/other.md", SHA256: strings.Repeat("d", 64)}
			if _, err := e.h.Records.PutDocument("tamper", other); err != nil {
				t.Fatal(err)
			}
			e.answer(t, "accept-1", "approved")
			err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
			if !errors.Is(err, interp.ErrSpecGate) {
				t.Fatalf("Advance = %v, want ErrSpecGate", err)
			}
			for _, a := range e.attempts(t) {
				if a.Step == "build" {
					t.Errorf("build attempt recorded behind a refused spec gate: %+v", a)
				}
			}
		})
	}
}

// crashingLauncher fails its first launch as if the process died between recording the attempt and launching it.
type crashingLauncher struct {
	interp.Launcher
	crashed bool
}

func (c *crashingLauncher) Launch(ctx context.Context, spec interp.Spec) (interp.Handle, error) {
	if !c.crashed {
		c.crashed = true
		return interp.Handle{}, errors.New("crash before launch")
	}
	return c.Launcher.Launch(ctx, spec)
}

func TestCrashBetweenRecordAndLaunchReplays(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnv(t, b, straightScript(t)[0])
			e.eng.Launcher = &crashingLauncher{Launcher: e.script}
			if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); err == nil {
				t.Fatal("Advance succeeded through a crashed launch")
			}
			if got := e.attempts(t); len(got) != 1 || got[0].Outcome != "" {
				t.Fatalf("attempts after the crash = %+v, want one running", got)
			}
			next := e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if want := (interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1}); next != want {
				t.Fatalf("replay = %+v, want %+v", next, want)
			}
			if got := e.attempts(t); len(got) != 1 {
				t.Errorf("replay recorded a duplicate attempt: %+v", got)
			}
			if got := len(e.script.Launched()); got != 1 {
				t.Errorf("mock launched %d times, want 1", got)
			}
			// A launched attempt is never launched again by a replay.
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if got := len(e.script.Launched()); got != 1 {
				t.Errorf("replay relaunched a launched attempt: %d launches", got)
			}
		})
	}
}

func TestZeroModelGuard(t *testing.T) {
	e := newEnv(t, backends[0])
	e.eng.Config.Profiles["planner"] = config.Profile{Adapter: "claude"}
	err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated})
	if !errors.Is(err, interp.ErrAdapterRefused) {
		t.Fatalf("Advance = %v, want ErrAdapterRefused", err)
	}
	if got := e.attempts(t); len(got) != 0 {
		t.Errorf("a refused adapter recorded attempts: %+v", got)
	}
}

func TestLoopsAreRefused(t *testing.T) {
	t.Run("artisan review requests changes", func(t *testing.T) {
		exps := straightScript(t)
		exps[2].Respond = interptest.Review(interptest.ReviewFixture(t, "changes-requested"))
		e := newEnv(t, backends[0], exps...)
		e.advance(t, interp.Event{Kind: interp.TaskCreated})
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
		e.answer(t, "accept-1", "approved")
		e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
		err := advanceErr(t, e, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
		if !errors.Is(err, interp.ErrUnsupported) || !strings.Contains(err.Error(), "loops in S6b") {
			t.Fatalf("Advance = %v, want ErrUnsupported naming loops in S6b", err)
		}
	})
	t.Run("Guildmaster requests changes", func(t *testing.T) {
		e := newEnv(t, backends[0], straightScript(t)[0])
		e.advance(t, interp.Event{Kind: interp.TaskCreated})
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
		e.answer(t, "accept-1", "changes_requested\nTighten the scope.")
		err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
		if !errors.Is(err, interp.ErrUnsupported) || !strings.Contains(err.Error(), "loops in S6b") {
			t.Fatalf("Advance = %v, want ErrUnsupported naming loops in S6b", err)
		}
	})
}

func TestFailedAttemptWaitsForTheDirector(t *testing.T) {
	e := newEnv(t, backends[0], planExpectation(t, interptest.Fail("no plan")))
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	next := e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	if want := (interp.Next{Wait: interp.WaitDirector, Step: "plan", Attempt: 1}); next != want {
		t.Fatalf("Advance = %+v, want %+v", next, want)
	}
	if got := e.attempts(t); len(got) != 1 || got[0].Outcome != store.AttemptFailed {
		t.Errorf("attempts = %+v", got)
	}
	if got := e.inbox(t); !slices.Equal(got, []string{"app.t7.attempt-failed.plan-1"}) {
		t.Errorf("Director inbox = %q", got)
	}
}

func TestEventsMustMatchTheRecords(t *testing.T) {
	e := newEnv(t, backends[0], straightScript(t)[0])
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	for name, ev := range map[string]interp.Event{
		"result of an unrecorded attempt": {Kind: interp.AttemptResult, Step: "plan", Attempt: 2},
		"result of an unknown step":       {Kind: interp.AttemptResult, Step: "ship", Attempt: 1},
		"answer to an unknown decision":   {Kind: interp.DecisionAnswered, Decision: "accept-1"},
		"unknown event kind":              {Kind: "nudged"},
	} {
		if err := advanceErr(t, e, ev); !errors.Is(err, interp.ErrEvent) {
			t.Errorf("%s: Advance = %v, want ErrEvent", name, err)
		}
	}
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	if err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}); !errors.Is(err, interp.ErrEvent) {
		t.Errorf("unanswered decision: Advance = %v, want ErrEvent", err)
	}
}

func TestBoundRevisionIsChecked(t *testing.T) {
	e := newEnv(t, backends[0])
	edited := *straight(t)
	edited.Description = "edited after the task bound it"
	e.eng.Workflow = func(workflow.Revision) (*workflow.Definition, error) { return &edited, nil }
	if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); !errors.Is(err, interp.ErrRevision) {
		t.Fatalf("Advance = %v, want ErrRevision", err)
	}
}

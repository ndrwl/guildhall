package interp_test

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

var errInjected = errors.New("injected write failure")

// writeKinds are the kinds of record the engine writes, keyed by its Records method.
// A write made while the engine applies an answered decision is a decision answer whatever its method.
var writeKinds = map[string]string{
	"StartAttempt":  "attempt start",
	"PutDocument":   "document put",
	"OpenDecision":  "decision open",
	"FinishAttempt": "attempt finish",
	"":              "decision answer",
}

// flakyRecords fails one write, counted across every write the engine makes: before it reaches the records,
// or after they committed it, as a crash on either side of the write would.
type flakyRecords struct {
	interp.Records
	writes, fail int
	after        bool
	answering    bool     // the engine is applying an answered decision
	kinds        []string // kinds[i] is the kind of write i+1
}

func (r *flakyRecords) write(method string, do func() (store.Receipt, error)) (store.Receipt, error) {
	if r.answering {
		method = ""
	}
	r.writes++
	r.kinds = append(r.kinds, writeKinds[method])
	if r.writes != r.fail {
		return do()
	}
	if r.after {
		if _, err := do(); err != nil {
			return store.Receipt{}, err
		}
	}
	return store.Receipt{}, errInjected
}

func (r *flakyRecords) StartAttempt(id, repo, task, step string) (store.Receipt, error) {
	return r.write("StartAttempt", func() (store.Receipt, error) { return r.Records.StartAttempt(id, repo, task, step) })
}

func (r *flakyRecords) FinishAttempt(id, repo, task, step string, n int, o store.AttemptOutcome) (store.Receipt, error) {
	return r.write("FinishAttempt", func() (store.Receipt, error) { return r.Records.FinishAttempt(id, repo, task, step, n, o) })
}

func (r *flakyRecords) PutDocument(id string, d store.DocumentRef) (store.Receipt, error) {
	return r.write("PutDocument", func() (store.Receipt, error) { return r.Records.PutDocument(id, d) })
}

func (r *flakyRecords) OpenDecision(id string, d store.Decision) (store.Receipt, error) {
	return r.write("OpenDecision", func() (store.Receipt, error) { return r.Records.OpenDecision(id, d) })
}

// historyOf is what a task's records say, compared across runs.
func historyOf(t *testing.T, e *env) string {
	t.Helper()
	docs := e.documents(t)
	out := ""
	for _, name := range slices.Sorted(maps.Keys(docs)) {
		out += fmt.Sprintf("document %s %s %s\n", name, docs[name].Path, docs[name].SHA256)
	}
	for _, a := range e.attempts(t) {
		out += fmt.Sprintf("attempt %s %d %s\n", a.Step, a.Number, a.Outcome)
	}
	ds, err := e.h.Records.Decisions("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		out += fmt.Sprintf("decision %s %s %+v %v %q\n", d.ID, d.Kind, d.Subject, d.Answered, d.Answer)
	}
	return out
}

// firstOfEachKind is the first write of each kind: the writes the real store fails, before and after commit.
func firstOfEachKind(kinds []string) map[string]int {
	first := map[string]int{}
	for i, k := range kinds {
		if _, ok := first[k]; !ok {
			first[k] = i + 1
		}
	}
	return first
}

// replayLoop runs a loop that exhausts its max, continues once and is accepted at the second exit, failing write
// fail (none when 0) and repeating the advance it failed. It returns the history, the launch count, the kind of
// every write and the final wait.
func replayLoop(t *testing.T, b backend, fail int, after bool) (string, int, []string, interp.Next) {
	f1 := workflow.Finding{ID: "f1", Rule: "S2", Summary: "No red test shown."}
	still := workflow.Disposition{Finding: "f1", State: workflow.StillOpen}
	o1 := bound(t, workflow.VerdictChangesRequested, head, []workflow.Finding{f1})
	o2 := bound(t, workflow.VerdictChangesRequested, head2, []workflow.Finding{f1}, still)
	o3 := bound(t, workflow.VerdictChangesRequested, head3, []workflow.Finding{f1}, still)
	type action struct {
		ev     interp.Event
		answer string
	}
	flow := []action{
		{ev: interp.Event{Kind: interp.TaskCreated}},
		{ev: interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}},
		{answer: "approved", ev: interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}},
		{ev: interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}},
		{ev: interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}},
		{ev: interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2}},
		{ev: interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}},
		{answer: "continue", ev: interp.Event{Kind: interp.DecisionAnswered, Decision: "review-x2"}},
		{ev: interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 3}},
		{ev: interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 3}},
		{answer: "accept", ev: interp.Event{Kind: interp.DecisionAnswered, Decision: "review-x3"}},
	}
	e := newEnvDefinition(t, b, loadWorkflow(t, "loop"),
		planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
		buildExp(t, 1, head, ""), reviewExp(t, "review", "reviewer", 1, head, nil, o1),
		buildExp(t, 2, head2, outcomeDigest(o1)), reviewExp(t, "review", "reviewer", 2, head2, &o1, o2),
		buildExp(t, 3, head3, outcomeDigest(o2)), reviewExp(t, "review", "reviewer", 3, head3, &o2, o3))
	flaky := &flakyRecords{Records: e.eng.Records, fail: fail, after: after}
	e.eng.Records = flaky
	var next interp.Next
	for _, a := range flow {
		if a.answer != "" {
			e.answer(t, a.ev.Decision, a.answer)
		}
		flaky.answering = a.ev.Kind == interp.DecisionAnswered
		if err := advanceErr(t, e, a.ev); err != nil {
			if !errors.Is(err, errInjected) {
				t.Fatalf("Advance(%+v) = %v", a.ev, err)
			}
			t.Logf("write %d failed in Advance(%+v); repeating it", fail, a.ev)
		}
		next = e.advance(t, a.ev)
	}
	return historyOf(t, e), len(e.script.Launched()), flaky.kinds, next
}

// TestReplayAfterEachWrite fails each of the engine's writes in turn, before and after it commits, over the
// in-memory records, and the first write of each kind over the real store, whose durability its own suite pins.
// Repeating the failed advance must end with the same records, the same launches and the same wait as a run
// without a failure: the history view never hides a write, and every partial advance is one a repeat completes.
func TestReplayAfterEachWrite(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			wantHistory, wantLaunches, kinds, wantNext := replayLoop(t, b, 0, false)
			want(t, wantNext, interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			fails := make([]int, len(kinds))
			for i := range fails {
				fails[i] = i + 1
			}
			if b.name == "store" {
				first := firstOfEachKind(kinds)
				fails = slices.Sorted(maps.Values(first))
				t.Logf("failing the first write of each kind: %v", first)
			}
			for _, fail := range fails {
				for _, after := range []bool{false, true} {
					t.Run(fmt.Sprintf("write %d/after %v", fail, after), func(t *testing.T) {
						t.Parallel()
						got, launches, _, next := replayLoop(t, b, fail, after)
						want(t, next, wantNext)
						if got != wantHistory || launches != wantLaunches {
							t.Fatalf("records after a failed write %d differ (%d launches, want %d):\n%s\nwant:\n%s", fail, launches, wantLaunches, got, wantHistory)
						}
					})
				}
			}
		})
	}
}

// TestReplaySampleCoversEveryWriteKind pins the real-store sample of TestReplayAfterEachWrite: the flow writes
// every kind of record, and every write method of Records is counted, so no kind is left out of the sample.
func TestReplaySampleCoversEveryWriteKind(t *testing.T) {
	records := reflect.TypeFor[interp.Records]()
	for i := range records.NumMethod() {
		m := records.Method(i)
		if m.Type.NumOut() > 0 && m.Type.Out(0) == reflect.TypeFor[store.Receipt]() && writeKinds[m.Name] == "" {
			t.Errorf("Records.%s writes a record flakyRecords does not count", m.Name)
		}
	}
	_, _, kinds, _ := replayLoop(t, backends[0], 0, false)
	first := firstOfEachKind(kinds)
	for _, kind := range writeKinds {
		if _, ok := first[kind]; !ok {
			t.Errorf("the replay flow writes no %s, so the real store never fails one; first of each kind: %v", kind, first)
		}
	}
}

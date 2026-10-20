package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// inspectStore builds a store with a fixed clock that advances one minute per mutation, so timeline
// times and durations are stable: task app/t-2 has a failed and a succeeded plan attempt, a running
// build attempt, two documents, an answered and an open decision and a message; app/t-1 and lib/t-2
// have records that must not appear in t-2's reads.
func inspectStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "guildhall.store")
	next := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	s, err := store.Open(path, store.Options{Now: func() time.Time {
		now := next
		next = next.Add(time.Minute)
		return now
	}})
	if err != nil {
		t.Fatal(err)
	}
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	task := func(repo, id, title string) store.Task {
		return store.Task{Repository: repo, ID: id, Title: title, Workflow: def.Revision(), ConfigDigest: "sha256:" + strings.Repeat("c", 64)}
	}
	doc := func(id, name, sum string) store.DocumentRef {
		return store.DocumentRef{Repository: "app", TaskID: id, Name: name, Path: "tasks/" + id + "/" + name + ".md", SHA256: strings.Repeat(sum, 64)}
	}
	steps := []func() (store.Receipt, error){
		func() (store.Receipt, error) { return s.CreateTask("i-1", task("app", "t-1", "Add a sign-out link")) },
		func() (store.Receipt, error) {
			return s.CreateTask("i-2", task("app", "t-2", "Fix the login redirect"))
		},
		func() (store.Receipt, error) { return s.CreateTask("i-3", task("lib", "t-2", "Bump the parser")) },
		func() (store.Receipt, error) { return s.StartAttempt("i-4", "app", "t-2", "plan") },
		func() (store.Receipt, error) { return s.StartAttempt("i-5", "app", "t-1", "plan") },
		func() (store.Receipt, error) {
			return s.FinishAttempt("i-6", "app", "t-2", "plan", 1, store.AttemptFailed)
		},
		func() (store.Receipt, error) { return s.StartAttempt("i-7", "app", "t-2", "plan") },
		func() (store.Receipt, error) {
			return s.FinishAttempt("i-8", "app", "t-2", "plan", 2, store.AttemptSucceeded)
		},
		func() (store.Receipt, error) { return s.PutDocument("i-9", doc("t-2", "plan", "a")) },
		func() (store.Receipt, error) {
			return s.OpenDecision("i-10", store.Decision{Repository: "app", TaskID: "t-2", ID: "accept-plan", Kind: store.DecisionApproval,
				Subject: &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + strings.Repeat("a", 64)},
				Options: []string{"approve", "reject"}, Question: "Accept the plan?"})
		},
		func() (store.Receipt, error) { return s.PutDocument("i-11", doc("t-1", "plan", "e")) },
		func() (store.Receipt, error) {
			return s.AnswerDecision("i-12", "app", "t-2", "accept-plan", "approve\nKeep the old route as a redirect.")
		},
		func() (store.Receipt, error) { return s.StartAttempt("i-13", "app", "t-2", "build") },
		func() (store.Receipt, error) { return s.PutDocument("i-14", doc("t-2", "spec", "b")) },
		func() (store.Receipt, error) {
			return s.SendMessage(store.Message{ID: "q-1", Recipient: store.RecipientDirector, Repository: "app", TaskID: "t-2",
				Step: "build", Attempt: 1, Type: "question", Text: "Which branch?"})
		},
		func() (store.Receipt, error) {
			return s.OpenDecision("i-15", store.Decision{Repository: "app", TaskID: "t-2", ID: "which-branch", Kind: store.DecisionQuestion, Question: "Which branch?"})
		},
		func() (store.Receipt, error) { return s.CreateTask("i-16", task("app", "t-3", "Later task")) },
	}
	for _, step := range steps {
		if _, err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTaskTimelineGolden(t *testing.T) {
	path := inspectStore(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runTask(t, "timeline", "--store", path, "--repo", "app", "--json", "t-2")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	checkGolden(t, "task-timeline.json", out)

	// A task with only its creation has empty lists, not null ones.
	code, out, _ = runTask(t, "timeline", "--store", path, "--repo", "app", "t-3", "--json")
	if code != 0 || !strings.Contains(out, `"attempts":[],"decisions":[]`) || !strings.Contains(out, `"sequence":17`) {
		t.Fatalf("task without records: exit %d, %s", code, out)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("timeline changed the store (%v)", err)
	}
}

func TestTaskFilesGolden(t *testing.T) {
	path := inspectStore(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runTask(t, "files", "--store", path, "--repo", "app", "--json", "t-2")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	checkGolden(t, "task-files.json", out)

	code, out, _ = runTask(t, "files", "--store", path, "--repo", "lib", "--json", "t-2")
	if code != 0 || !strings.Contains(out, `"documents":[]`) {
		t.Fatalf("task without documents: exit %d, %s", code, out)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("files changed the store (%v)", err)
	}
}

func TestTaskInspectionScope(t *testing.T) {
	path := inspectStore(t)
	for _, sub := range []string{"timeline", "files"} {
		t.Run(sub, func(t *testing.T) {
			code, out, stderr := runTask(t, sub, "--store", path, "--repo", "app", "--json", "t-2")
			if code != 0 || stderr != "" {
				t.Fatalf("exit %d, stderr %s", code, stderr)
			}
			var got struct {
				AsOf         inspectLabel      `json:"as_of"`
				OmittedKinds map[string]string `json:"omitted_kinds"`
			}
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatal(err)
			}
			if !got.AsOf.Partial || !reflect.DeepEqual(got.AsOf.Excluded, []string{"live worker state", "worker files", "transcript"}) || got.AsOf.Sequence != 17 || got.AsOf.CommittedAt.IsZero() || got.AsOf.Workflow.Digest == "" {
				t.Fatalf("inspection label = %+v", got.AsOf)
			}
			if sub == "timeline" && !reflect.DeepEqual(got.OmittedKinds, map[string]string{"findings": "not yet recorded by this version", "director_rulings": "not yet recorded by this version"}) {
				t.Fatalf("omitted kinds = %v", got.OmittedKinds)
			}
		})
	}
}

func TestTaskInspectionRefusals(t *testing.T) {
	path := inspectStore(t)
	missing := filepath.Join(t.TempDir(), "missing.store")
	for _, sub := range []string{"timeline", "files"} {
		cases := []struct {
			name string
			args []string
			code int
			err  string
		}{
			{"unknown task", []string{"--store", path, "--repo", "app", "--json", "t-9"}, 1, store.ErrNotFound.Error()},
			{"unknown repository", []string{"--store", path, "--repo", "web", "--json", "t-2"}, 1, store.ErrNotFound.Error()},
			{"no store", []string{"--store", missing, "--repo", "app", "--json", "t-2"}, 1, "no store"},
			{"no repo", []string{"--store", path, "--json", "t-2"}, 2, "requires --repo"},
			{"no json", []string{"--store", path, "--repo", "app", "t-2"}, 2, "pass --json"},
			{"no task", []string{"--store", path, "--repo", "app", "--json"}, 2, "takes 1 arguments"},
			{"write flag", []string{"--store", path, "--repo", "app", "--title", "x", "--json", "t-2"}, 2, "--title does not apply"},
		}
		for _, tc := range cases {
			t.Run(sub+" "+tc.name, func(t *testing.T) {
				code, out, stderr := runTask(t, append([]string{sub}, tc.args...)...)
				if code != tc.code || out != "" || !strings.Contains(stderr, tc.err) {
					t.Fatalf("exit %d, stdout %q, stderr %q; want exit %d with %q", code, out, stderr, tc.code, tc.err)
				}
			})
		}
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("a read created a store")
	}
}

// TestTimelineWithoutRecordNames pins how receipts committed before they named their record are shown:
// they stay in the events, attempts and decisions carry no times or durations and follow timed ones.
func TestTimelineWithoutRecordNames(t *testing.T) {
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	h := store.TaskHistory{
		Task:      store.Task{Repository: "app", ID: "t-1"},
		Attempts:  []store.StageAttempt{{Step: "build", Number: 1}, {Step: "plan", Number: 1}},
		Decisions: []store.Decision{{ID: "q-1"}, {ID: "q-2"}},
		Receipts: []store.Receipt{
			{MutationID: "legacy", Operation: "start-attempt", Attempt: 1, Sequence: 1, CommittedAt: at},
			{MutationID: "named", Operation: "start-attempt", Attempt: 1, Record: "attempt/plan/1", Sequence: 2, CommittedAt: at.Add(time.Minute)},
			{Operation: "open-decision", Sequence: 3, CommittedAt: at.Add(2 * time.Minute)},
			{Operation: "open-decision", Record: "decision/q-2", Sequence: 4, CommittedAt: at.Add(3 * time.Minute)},
		},
	}
	got := timeline(h)
	if len(got.Events) != 4 || len(got.Attempts) != 2 || got.Attempts[0].Step != "plan" || got.Attempts[1].Step != "build" {
		t.Fatalf("timeline = %+v", got)
	}
	if p := got.Attempts[0]; p.StartedAt == nil || !p.StartedAt.Equal(at.Add(time.Minute)) || p.FinishedAt != nil || p.DurationSeconds != nil {
		t.Fatalf("running named attempt = %+v", p)
	}
	if b := got.Attempts[1]; b.StartedAt != nil || b.DurationSeconds != nil {
		t.Fatalf("legacy attempt = %+v", b)
	}
	if d := got.Decisions[1]; d.ID != "q-1" || d.OpenedAt != nil || d.WaitSeconds != nil {
		t.Fatalf("legacy decision = %+v", d)
	}

	finished := at.Add(4 * time.Minute)
	answered := at.Add(5 * time.Minute)
	h.Attempts[0].Outcome = store.AttemptSucceeded
	h.Decisions[0].Answer = "Proceed"
	h.Receipts = append(h.Receipts,
		store.Receipt{Operation: "finish-attempt", Record: "attempt/build/1", Sequence: 5, CommittedAt: finished},
		store.Receipt{Operation: "answer-decision", Record: "decision/q-1", Sequence: 6, CommittedAt: answered},
	)
	got = timeline(h)
	if len(got.Events) != 6 || got.Attempts[0].Step != "plan" || got.Decisions[0].ID != "q-2" {
		t.Fatalf("mixed-version ordering = %+v", got)
	}
	if b := got.Attempts[1]; b.Step != "build" || b.StartedAt != nil || b.FinishedAt == nil || !b.FinishedAt.Equal(finished) || b.DurationSeconds != nil {
		t.Fatalf("mixed-version attempt = %+v", b)
	}
	if d := got.Decisions[1]; d.ID != "q-1" || d.OpenedAt != nil || d.AnsweredAt == nil || !d.AnsweredAt.Equal(answered) || d.WaitSeconds != nil {
		t.Fatalf("mixed-version decision = %+v", d)
	}
}

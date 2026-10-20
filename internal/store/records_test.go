package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/workflow"
)

func TestCreateTaskValidates(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Task)
	}{
		{"bad repository", func(t *Task) { t.Repository = "App" }},
		{"bad id", func(t *Task) { t.ID = "" }},
		{"empty title", func(t *Task) { t.Title = " " }},
		{"long title", func(t *Task) { t.Title = strings.Repeat("x", MaxTextBytes+1) }},
		{"bad workflow name", func(t *Task) { t.Workflow.Name = "" }},
		{"bad workflow digest", func(t *Task) { t.Workflow.Digest = "sha256:abc" }},
		{"bad config digest", func(t *Task) { t.ConfigDigest = strings.Repeat("b", 64) }},
		{"preset state", func(t *Task) { t.State = TaskDone }},
		{"bad dependency", func(t *Task) { t.DependsOn = []TaskRef{{Repository: "app", ID: "Bad"}} }},
		{"self dependency", func(t *Task) { t.DependsOn = []TaskRef{{Repository: "app", ID: "t-1"}} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStore(t)
			task := sampleTask("app", "t-1")
			tc.mutate(&task)
			if _, err := s.CreateTask("m-1", task); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	if _, err := (&Store{}).CreateTask("bad id with spaces", sampleTask("app", "t-1")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad mutation id: err = %v, want ErrInvalid", err)
	}
}

func TestTasksAreKeyedByRepository(t *testing.T) {
	s, _ := newStore(t)
	a := sampleTask("app", "t-1")
	b := sampleTask("lib", "t-1")
	b.Title = "Library task"
	mustCreate(t, s, "m-1", a)
	mustCreate(t, s, "m-2", b)
	if _, err := s.CreateTask("m-3", sampleTask("app", "t-1")); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate task: err = %v, want ErrExists", err)
	}

	got, err := s.Task("lib", "t-1")
	if err != nil || got.Title != "Library task" {
		t.Fatalf("lib task = %+v, %v", got, err)
	}
	if _, err := s.SetTaskState("m-4", "lib", "t-1", TaskCancelled); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Task("app", "t-1"); got.State != TaskOpen {
		t.Fatalf("change in one repository leaked into another: %+v", got)
	}
	repos, err := s.Repositories()
	if err != nil || strings.Join(repos, ",") != "app,lib" {
		t.Fatalf("Repositories = %v, %v", repos, err)
	}
	tasks, err := s.Tasks("app")
	if err != nil || len(tasks) != 1 || tasks[0].ID != "t-1" {
		t.Fatalf("Tasks(app) = %+v, %v", tasks, err)
	}
}

func TestTaskDependenciesMustExist(t *testing.T) {
	s, _ := newStore(t)
	dependent := sampleTask("app", "t-2")
	dependent.DependsOn = []TaskRef{{Repository: "lib", ID: "t-1"}}
	if _, err := s.CreateTask("m-1", dependent); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing dependency: err = %v, want ErrNotFound", err)
	}
	mustCreate(t, s, "m-2", sampleTask("lib", "t-1"))
	mustCreate(t, s, "m-3", dependent)
	got, _ := s.Task("app", "t-2")
	if len(got.DependsOn) != 1 || got.DependsOn[0] != (TaskRef{Repository: "lib", ID: "t-1"}) {
		t.Fatalf("DependsOn = %+v", got.DependsOn)
	}
}

func TestTaskRevisionIsImmutable(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	changed := sampleTask("app", "t-1")
	changed.Workflow = workflow.Revision{Name: "default", Digest: digest('f')}
	if _, err := s.CreateTask("m-2", changed); !errors.Is(err, ErrExists) {
		t.Fatalf("rebinding workflow: err = %v, want ErrExists", err)
	}
	got, _ := s.Task("app", "t-1")
	if got.Workflow.Digest != digest('a') {
		t.Fatalf("workflow revision changed to %q", got.Workflow.Digest)
	}
}

func TestTaskStateTransitions(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	if _, err := s.SetTaskState("m-2", "app", "t-1", "paused"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown state: err = %v, want ErrInvalid", err)
	}
	if _, err := s.SetTaskState("m-3", "app", "t-1", TaskDone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTaskState("m-4", "app", "t-1", TaskOpen); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reopening a finished task: err = %v, want ErrInvalid", err)
	}
	if _, err := s.StartAttempt("m-5", "app", "t-1", "build"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("attempt on a finished task: err = %v, want ErrInvalid", err)
	}
}

func TestStageAttempts(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	r, err := s.StartAttempt("m-2", "app", "t-1", "build")
	if err != nil || r.Attempt != 1 {
		t.Fatalf("first attempt = %+v, %v", r, err)
	}
	if _, err := s.StartAttempt("m-3", "app", "t-1", "build"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("second attempt while one runs: err = %v, want ErrInvalid", err)
	}
	if _, err := s.FinishAttempt("m-4", "app", "t-1", "build", 1, "maybe"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown outcome: err = %v, want ErrInvalid", err)
	}
	if _, err := s.FinishAttempt("m-5", "app", "t-1", "build", 2, AttemptFailed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing attempt: err = %v, want ErrNotFound", err)
	}
	if _, err := s.FinishAttempt("m-6", "app", "t-1", "build", 1, AttemptFailed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishAttempt("m-7", "app", "t-1", "build", 1, AttemptSucceeded); !errors.Is(err, ErrInvalid) {
		t.Fatalf("finishing twice: err = %v, want ErrInvalid", err)
	}
	r, err = s.StartAttempt("m-8", "app", "t-1", "build")
	if err != nil || r.Attempt != 2 {
		t.Fatalf("retry attempt = %+v, %v", r, err)
	}
	if _, err := s.StartAttempt("m-9", "app", "t-1", "review"); err != nil {
		t.Fatal(err)
	}

	got, err := s.Attempts("app", "t-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []StageAttempt{
		{Repository: "app", TaskID: "t-1", Step: "build", Number: 1, Outcome: AttemptFailed},
		{Repository: "app", TaskID: "t-1", Step: "build", Number: 2},
		{Repository: "app", TaskID: "t-1", Step: "review", Number: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("attempts = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("attempt %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDocumentReferences(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	doc := DocumentRef{Repository: "app", TaskID: "t-1", Name: "plan", Path: "tasks/t-1/plan.md", SHA256: strings.Repeat("d", 64)}
	for _, bad := range []func(*DocumentRef){
		func(d *DocumentRef) { d.Name = "Plan" },
		func(d *DocumentRef) { d.Path = "" },
		func(d *DocumentRef) { d.Path = "../plan.md" },
		func(d *DocumentRef) { d.Path = "/etc/plan.md" },
		func(d *DocumentRef) { d.Path = `C:\plan.md` },
		func(d *DocumentRef) { d.Path = `tasks\plan.md` },
		func(d *DocumentRef) { d.Path = "tasks//plan.md" },
		func(d *DocumentRef) { d.SHA256 = strings.Repeat("D", 64) },
		func(d *DocumentRef) { d.SHA256 = "sha256:" + strings.Repeat("d", 64) },
	} {
		d := doc
		bad(&d)
		if _, err := s.PutDocument("m-bad", d); !errors.Is(err, ErrInvalid) {
			t.Fatalf("PutDocument(%+v): err = %v, want ErrInvalid", d, err)
		}
	}
	if _, err := s.PutDocument("m-2", doc); err != nil {
		t.Fatal(err)
	}
	changed := doc
	changed.SHA256 = strings.Repeat("e", 64)
	if _, err := s.PutDocument("m-3", changed); !errors.Is(err, ErrExists) {
		t.Fatalf("rewriting a reference: err = %v, want ErrExists", err)
	}
	docs, err := s.Documents("app", "t-1")
	if err != nil || len(docs) != 1 || docs[0] != doc {
		t.Fatalf("Documents = %+v, %v", docs, err)
	}
}

func TestPaneBindings(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	if _, err := s.PaneBinding("app", "t-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unbound pane: err = %v, want ErrNotFound", err)
	}
	b := PaneBinding{Repository: "app", TaskID: "t-1", Session: "lab", Workspace: "w1", Tab: "w1:t2", Pane: "w1:p3"}
	if _, err := s.BindPane("m-2", b); err != nil {
		t.Fatal(err)
	}
	b.Pane = "w1:p4"
	if _, err := s.BindPane("m-3", b); err != nil {
		t.Fatalf("rebinding: %v", err)
	}
	got, err := s.PaneBinding("app", "t-1")
	if err != nil || got != b {
		t.Fatalf("PaneBinding = %+v, %v; want %+v", got, err, b)
	}
	if _, err := s.UnbindPane("m-4", "app", "t-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnbindPane("m-5", "app", "t-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unbinding twice: err = %v, want ErrNotFound", err)
	}
	// Removing the presentation binding leaves the task untouched (R1.52).
	if task, err := s.Task("app", "t-1"); err != nil || task.State != TaskOpen {
		t.Fatalf("task after unbind = %+v, %v", task, err)
	}
	b.Pane = ""
	if _, err := s.BindPane("m-6", b); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty pane: err = %v, want ErrInvalid", err)
	}
}

func TestDecisions(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	if _, err := s.OpenDecision("m-2", "app", "t-1", "accept-plan", "Accept the plan?"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenDecision("m-3", "app", "t-1", "accept-plan", "Again?"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate decision: err = %v, want ErrExists", err)
	}
	if _, err := s.AnswerDecision("m-4", "app", "t-1", "accept-plan", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty answer: err = %v, want ErrInvalid", err)
	}
	answer := "Yes, but keep the CLI out.\n"
	if _, err := s.AnswerDecision("m-5", "app", "t-1", "accept-plan", answer); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerDecision("m-6", "app", "t-1", "accept-plan", "No"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("answering twice: err = %v, want ErrInvalid", err)
	}
	got, err := s.Decisions("app", "t-1")
	want := Decision{Repository: "app", TaskID: "t-1", ID: "accept-plan", Question: "Accept the plan?", Answered: true, Answer: answer}
	if err != nil || len(got) != 1 || got[0] != want {
		t.Fatalf("Decisions = %+v, %v; want %+v", got, err, want)
	}
}

func TestRecordsRequireTask(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	for name, call := range map[string]func() error{
		"attempt": func() error { _, err := s.StartAttempt("m-2", "lib", "t-1", "build"); return err },
		"document": func() error {
			_, err := s.PutDocument("m-3", DocumentRef{Repository: "app", TaskID: "t-2", Name: "plan", Path: "p.md", SHA256: strings.Repeat("d", 64)})
			return err
		},
		"pane": func() error {
			_, err := s.BindPane("m-4", PaneBinding{Repository: "app", TaskID: "t-2", Session: "s", Workspace: "w", Tab: "t", Pane: "p"})
			return err
		},
		"decision": func() error { _, err := s.OpenDecision("m-5", "app", "t-2", "q", "Question?"); return err },
	} {
		if err := call(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s for a missing task: err = %v, want ErrNotFound", name, err)
		}
	}
}

package store

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

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
		{"bad fixed task", func(t *Task) { t.Fixes = &TaskRef{Repository: "app", ID: "Bad"} }},
		{"fixes itself", func(t *Task) { t.Fixes = &TaskRef{Repository: "app", ID: "t-1"} }},
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

func TestTaskTitleIsShort(t *testing.T) {
	s, _ := newStore(t)
	task := sampleTask("app", "t-1")
	task.Title = strings.Repeat("é", MaxTitleRunes)
	mustCreate(t, s, "m-1", task)
	if got, err := s.Task("app", "t-1"); err != nil || got.Title != task.Title {
		t.Fatalf("100-character title = %+v, %v", got, err)
	}

	long := sampleTask("app", "t-2")
	long.Title = strings.Repeat("é", MaxTitleRunes+1)
	_, err := s.CreateTask("m-2", long)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "title is 101 characters; the limit is 100") {
		t.Fatalf("101-character title: err = %v, want ErrInvalid naming 101 characters", err)
	}
	if _, err := s.Task("app", "t-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refused task was recorded: %v", err)
	}

	// A task created before titles were limited still reads.
	if _, err := s.CreateLegacyTaskForTests("m-3", long); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Task("app", "t-2"); err != nil || got.Title != long.Title {
		t.Fatalf("legacy long title = %+v, %v", got, err)
	}
}

func TestTaskDetail(t *testing.T) {
	s, _ := newStore(t)
	task := sampleTask("app", "t-1")
	task.Detail = "Running X fails:\n\n    error: Y"
	mustCreate(t, s, "m-1", task)
	if got, err := s.Task("app", "t-1"); err != nil || got.Detail != task.Detail {
		t.Fatalf("detail = %+v, %v", got, err)
	}

	empty := sampleTask("app", "t-2")
	mustCreate(t, s, "m-2", empty)

	long := sampleTask("app", "t-3")
	long.Detail = strings.Repeat("x", MaxTextBytes+1)
	if _, err := s.CreateTask("m-3", long); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long detail: err = %v, want ErrInvalid", err)
	}
	blank := sampleTask("app", "t-4")
	blank.Detail = "  \n"
	if _, err := s.CreateTask("m-4", blank); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank detail: err = %v, want ErrInvalid", err)
	}
}

// TestCreateTaskDigestWithoutDetail checks that a task with no detail has the request digest it had before the
// field existed, so a create-task receipt recorded then still replays.
func TestCreateTaskDigestWithoutDetail(t *testing.T) {
	type taskBeforeDetail struct {
		Repository   string            `json:"repository"`
		ID           string            `json:"id"`
		Title        string            `json:"title"`
		Workflow     workflow.Revision `json:"workflow"`
		ConfigDigest string            `json:"config_digest"`
		DependsOn    []TaskRef         `json:"depends_on,omitempty"`
		Fixes        *TaskRef          `json:"fixes,omitempty"`
		State        TaskState         `json:"state"`
	}
	task := sampleTask("app", "t-1")
	task.State = TaskOpen
	old := taskBeforeDetail{task.Repository, task.ID, task.Title, task.Workflow, task.ConfigDigest, task.DependsOn, task.Fixes, task.State}
	got, err := requestDigest("create-task", task)
	if err != nil {
		t.Fatal(err)
	}
	want, err := requestDigest("create-task", old)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("digest without detail = %s, want %s as before the field existed", got, want)
	}

	s, _ := newStore(t)
	first := mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	if first.RequestDigest != want {
		t.Fatalf("receipt digest = %s, want %s", first.RequestDigest, want)
	}
	if again := mustCreate(t, s, "m-1", sampleTask("app", "t-1")); again != first {
		t.Fatalf("replay = %+v, want %+v", again, first)
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
	changed.Workflow = workflow.Revision{Name: "default", Digest: sampleDigest('f')}
	if _, err := s.CreateTask("m-2", changed); !errors.Is(err, ErrExists) {
		t.Fatalf("rebinding workflow: err = %v, want ErrExists", err)
	}
	got, _ := s.Task("app", "t-1")
	if got.Workflow.Digest != sampleDigest('a') {
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

// TestJudgeAttempts: the judge step is the one step started on a task that is not open, and only on a done one;
// every other step still needs an open task.
func TestJudgeAttempts(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	if _, err := s.StartAttempt("m-2", "app", "t-1", workflow.JudgeStep); !errors.Is(err, ErrInvalid) {
		t.Fatalf("judge attempt on an open task: err = %v, want ErrInvalid", err)
	}
	if _, err := s.SetTaskState("m-3", "app", "t-1", TaskDone); err != nil {
		t.Fatal(err)
	}
	r, err := s.StartCappedAttempt("m-4", "app", "t-1", workflow.JudgeStep, AttemptCaps{MaxRunning: 4, MaxPerDay: 40})
	if err != nil || r.Attempt != 1 {
		t.Fatalf("judge attempt on a done task = %+v, %v", r, err)
	}
	if _, err := s.FinishAttempt("m-5", "app", "t-1", workflow.JudgeStep, 1, AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartAttempt("m-6", "app", "t-1", "build"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("build attempt on a done task: err = %v, want ErrInvalid", err)
	}
	mustCreate(t, s, "m-7", sampleTask("app", "t-2"))
	if _, err := s.SetTaskState("m-8", "app", "t-2", TaskCancelled); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartAttempt("m-9", "app", "t-2", workflow.JudgeStep); !errors.Is(err, ErrInvalid) {
		t.Fatalf("judge attempt on a cancelled task: err = %v, want ErrInvalid", err)
	}
}

// TestCreateTaskFixes: a task may name the earlier task whose defect it fixes, which must exist, and it is kept.
func TestCreateTaskFixes(t *testing.T) {
	s, _ := newStore(t)
	task := sampleTask("app", "t-2")
	task.Fixes = &TaskRef{Repository: "app", ID: "t-1"}
	if _, err := s.CreateTask("m-1", task); !errors.Is(err, ErrNotFound) {
		t.Fatalf("fixing a missing task: err = %v, want ErrNotFound", err)
	}
	mustCreate(t, s, "m-2", sampleTask("app", "t-1"))
	mustCreate(t, s, "m-3", task)
	got, err := s.Task("app", "t-2")
	if err != nil || got.Fixes == nil || *got.Fixes != *task.Fixes {
		t.Fatalf("Task = %+v, %v; want it to fix app/t-1", got, err)
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

func TestDocumentPathRejectsInvalidUTF8(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	doc := DocumentRef{Repository: "app", TaskID: "t-1", Name: "plan", SHA256: strings.Repeat("d", 64)}
	for _, path := range []string{"docs/\xff.md", "docs/\xfe.md"} {
		doc.Path = path
		if _, err := s.PutDocument("m-doc", doc); !errors.Is(err, ErrInvalid) {
			t.Fatalf("PutDocument(%q): %v, want ErrInvalid", path, err)
		}
	}
	if _, err := s.Receipt("m-doc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected mutation receipt: %v, want ErrNotFound", err)
	}
	if docs, err := s.Documents("app", "t-1"); err != nil || len(docs) != 0 {
		t.Fatalf("rejected documents: %+v, %v", docs, err)
	}
	doc.Path = "docs/日本語.md"
	if _, err := s.PutDocument("m-doc", doc); err != nil {
		t.Fatal(err)
	}
	if docs, err := s.Documents("app", "t-1"); err != nil || len(docs) != 1 || docs[0] != doc {
		t.Fatalf("persisted documents: %+v, %v", docs, err)
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

func sampleDecision(id string) Decision {
	return Decision{
		Repository: "app", TaskID: "t-1", ID: id, Kind: DecisionApproval,
		Subject:  &Subject{Kind: SubjectDocument, Digest: sampleDigest('c')},
		Options:  []string{"approve", "reject"},
		Question: "Approve the plan?",
	}
}

func TestDecisions(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	q := Decision{Repository: "app", TaskID: "t-1", ID: "accept-plan", Kind: DecisionQuestion, Question: "Accept the plan?"}
	if _, err := s.OpenDecision("m-2", q); err != nil {
		t.Fatal(err)
	}
	q.Question = "Again?"
	if _, err := s.OpenDecision("m-3", q); !errors.Is(err, ErrExists) {
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
	want := Decision{Repository: "app", TaskID: "t-1", ID: "accept-plan", Kind: DecisionQuestion, Question: "Accept the plan?", Answered: true, Answer: answer}
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("Decisions = %+v, %v; want %+v", got, err, want)
	}
}

func TestOpenDecisionValidates(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Decision)
	}{
		{"missing kind", func(d *Decision) { d.Kind = "" }},
		{"unknown kind", func(d *Decision) { d.Kind = "vote" }},
		{"missing subject", func(d *Decision) { d.Subject = nil }},
		{"audit without a subject", func(d *Decision) { d.Kind, d.Subject = DecisionAudit, nil }},
		{"unknown subject kind", func(d *Decision) { d.Subject.Kind = "branch" }},
		{"document digest without prefix", func(d *Decision) { d.Subject.Digest = strings.Repeat("c", 64) }},
		{"short head", func(d *Decision) { d.Subject = &Subject{Kind: SubjectHead, Digest: strings.Repeat("e", 39)} }},
		{"upper-case head", func(d *Decision) { d.Subject = &Subject{Kind: SubjectHead, Digest: strings.Repeat("E", 40)} }},
		{"blank option", func(d *Decision) { d.Options = []string{"approve", " "} }},
		{"multi-line option", func(d *Decision) { d.Options = []string{"approve\nnow"} }},
		{"carriage return option", func(d *Decision) { d.Options = []string{"approve\r"} }},
		{"padded option", func(d *Decision) { d.Options = []string{" approve"} }},
		{"duplicate option", func(d *Decision) { d.Options = []string{"approve", "approve"} }},
		{"long option", func(d *Decision) { d.Options = []string{strings.Repeat("x", MaxOptionBytes+1)} }},
		{"too many options", func(d *Decision) {
			d.Options = nil
			for i := range MaxDecisionOptions + 1 {
				d.Options = append(d.Options, fmt.Sprint("option-", i))
			}
		}},
		{"preset answer", func(d *Decision) { d.Answered, d.Answer = true, "approve" }},
		{"preset choice", func(d *Decision) { d.Choice = "approve" }},
		{"preset note", func(d *Decision) { d.Note = "fine" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStore(t)
			mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
			d := sampleDecision("approve-plan")
			tc.mutate(&d)
			if _, err := s.OpenDecision("m-2", d); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if got, err := s.Decisions("app", "t-1"); err != nil || len(got) != 0 {
				t.Fatalf("refused open wrote %+v, %v", got, err)
			}
		})
	}
	// A question may omit its subject, a head subject takes a SHA-1 or SHA-256 object id, and an audit binds to the head that merged.
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	for i, d := range []Decision{
		{Repository: "app", TaskID: "t-1", ID: "ask", Kind: DecisionQuestion, Question: "Which port?"},
		{Repository: "app", TaskID: "t-1", ID: "merge-sha1", Kind: DecisionMerge, Subject: &Subject{Kind: SubjectHead, Digest: strings.Repeat("e", 40)}, Question: "Merge?"},
		{Repository: "app", TaskID: "t-1", ID: "merge-sha256", Kind: DecisionMerge, Subject: &Subject{Kind: SubjectHead, Digest: strings.Repeat("f", 64)}, Question: "Merge?"},
		{Repository: "app", TaskID: "t-1", ID: "audit", Kind: DecisionAudit, Subject: &Subject{Kind: SubjectHead, Digest: strings.Repeat("e", 40)},
			Options: []string{"yes", "no"}, Question: "Would you have merged this as is?"},
	} {
		if _, err := s.OpenDecision(fmt.Sprint("m-open-", i), d); err != nil {
			t.Errorf("%s: %v", d.ID, err)
		}
	}
}

func TestAnswerDecisionWithOptions(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	if _, err := s.OpenDecision("m-2", sampleDecision("approve-plan")); err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []string{"maybe", "Approve", "approve it", "\napprove", "reject-ish\napprove", " approve \r\nKeep the migration behind a flag.", " approve ", "approve\r\r\nnote", "approve\t"} {
		if _, err := s.AnswerDecision("m-wrong", "app", "t-1", "approve-plan", wrong); !errors.Is(err, ErrInvalid) {
			t.Fatalf("answer %q: err = %v, want ErrInvalid", wrong, err)
		}
	}
	// A refused answer writes nothing: no receipt, the decision stays open and its mutation id stays free.
	if _, err := s.Receipt("m-wrong"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refused answer left a receipt: %v", err)
	}
	if got, _ := s.Decisions("app", "t-1"); got[0].Answered || got[0].Answer != "" {
		t.Fatalf("refused answer changed the decision: %+v", got[0])
	}

	answer := "approve\r\n\nKeep the migration behind a flag.\r\n"
	first, err := s.AnswerDecision("m-wrong", "app", "t-1", "approve-plan", answer)
	if err != nil {
		t.Fatal(err)
	}
	want := sampleDecision("approve-plan")
	want.Answered, want.Answer, want.Choice, want.Note = true, answer, "approve", "Keep the migration behind a flag."
	if got, err := s.Decisions("app", "t-1"); err != nil || !reflect.DeepEqual(got, []Decision{want}) {
		t.Fatalf("Decisions = %+v, %v; want %+v", got, err, want)
	}

	// Replaying the same answer returns the original receipt; any other second answer is refused.
	replay, err := s.AnswerDecision("m-wrong", "app", "t-1", "approve-plan", answer)
	if err != nil || replay != first {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, first)
	}
	if _, err := s.AnswerDecision("m-again", "app", "t-1", "approve-plan", "reject"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("second answer: err = %v, want ErrInvalid", err)
	}
	if _, err := s.AnswerDecision("m-wrong", "app", "t-1", "approve-plan", "reject"); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("different answer under the same mutation id: err = %v, want ErrMutationConflict", err)
	}
}

// TestLegacyDecisionRecords reads and answers a decision recorded before typed decisions.
func TestLegacyDecisionRecords(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	legacy := `{"repository":"app","task_id":"t-1","id":"accept-plan","question":"Accept the plan?","answered":false}`
	err := s.db.Update(func(tx *bolt.Tx) error {
		b, err := createRepoBucket(tx, "app", bucketDecisions)
		if err != nil {
			return err
		}
		return b.Put(recordKey("t-1", "accept-plan"), []byte(legacy))
	})
	if err != nil {
		t.Fatal(err)
	}
	all, err := s.AllDecisions("app")
	want := Decision{Repository: "app", TaskID: "t-1", ID: "accept-plan", Question: "Accept the plan?"}
	if err != nil || !reflect.DeepEqual(all, []Decision{want}) {
		t.Fatalf("AllDecisions = %+v, %v; want %+v", all, err, want)
	}
	if _, err := s.AnswerDecision("m-2", "app", "t-1", "accept-plan", "Anything at all\nwith lines"); err != nil {
		t.Fatalf("legacy answer: %v", err)
	}
	got, _ := s.Decisions("app", "t-1")
	want.Answered, want.Answer = true, "Anything at all\nwith lines"
	if !reflect.DeepEqual(got, []Decision{want}) {
		t.Fatalf("answered legacy decision = %+v, want %+v", got, want)
	}
}

func TestAllDecisionsOrder(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-2"))
	mustCreate(t, s, "m-2", sampleTask("app", "t-1"))
	mustCreate(t, s, "m-3", sampleTask("app", "t-a"))
	mustCreate(t, s, "m-4", sampleTask("app", "t"))
	for i, ref := range [][2]string{{"t-2", "a"}, {"t-1", "b"}, {"t-1", "a"}, {"t-a", "a"}, {"t", "b"}, {"t", "a"}} {
		d := sampleDecision(ref[1])
		d.TaskID = ref[0]
		if _, err := s.OpenDecision(fmt.Sprint("m-d-", i), d); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.AllDecisions("app")
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, d := range all {
		order = append(order, d.TaskID+"/"+d.ID)
	}
	if strings.Join(order, ",") != "t/a,t/b,t-1/a,t-1/b,t-2/a,t-a/a" {
		t.Fatalf("order = %v", order)
	}
	if none, err := s.AllDecisions("lib"); err != nil || len(none) != 0 {
		t.Fatalf("AllDecisions(lib) = %+v, %v", none, err)
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
		"decision": func() error {
			_, err := s.OpenDecision("m-5", Decision{Repository: "app", TaskID: "t-2", ID: "q", Kind: DecisionQuestion, Question: "Question?"})
			return err
		},
	} {
		if err := call(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s for a missing task: err = %v, want ErrNotFound", name, err)
		}
	}
}

func TestFinishAttemptFirstFinishStands(t *testing.T) {
	cases := []struct {
		name    string
		second  AttemptOutcome
		wantErr error
	}{
		{"an equal outcome returns the first receipt", AttemptFailed, nil},
		{"another outcome is refused", AttemptSucceeded, ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStore(t)
			mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
			if _, err := s.StartAttempt("m-2", "app", "t-1", "build"); err != nil {
				t.Fatal(err)
			}
			first, err := s.FinishAttempt("launch-finish:x", "app", "t-1", "build", 1, AttemptFailed)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				got, err := s.FinishAttempt("attempt-finish:x", "app", "t-1", "build", 1, tc.second)
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("second finish: err = %v, want %v", err, tc.wantErr)
				}
				if tc.wantErr == nil && got != first {
					t.Fatalf("second finish = %+v, want the first receipt %+v", got, first)
				}
			}
			if _, err := s.Receipt("attempt-finish:x"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("second finish's receipt: err = %v, want ErrNotFound", err)
			}
			got, err := s.Attempts("app", "t-1")
			if err != nil || len(got) != 1 || got[0].Outcome != AttemptFailed {
				t.Fatalf("attempts = %+v, %v; want one failed", got, err)
			}
			r, err := s.StartAttempt("m-3", "app", "t-1", "build")
			if err != nil || r.Sequence != first.Sequence+1 {
				t.Fatalf("next mutation = %+v, %v; want sequence %d", r, err, first.Sequence+1)
			}
		})
	}
}

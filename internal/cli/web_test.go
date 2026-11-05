package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
	"github.com/ndrwl/guildhall/internal/web"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// webSourceFor is the task pages' Source over the marked copy dir, at clerkNow.
func webSourceFor(t *testing.T, dir string) webSource {
	t.Helper()
	app, _, _ := newTestApp(nil, nil)
	app.Now = func() time.Time { return clerkNow }
	c, err := app.clerkCopy(dir)
	if err != nil {
		t.Fatal(err)
	}
	return webSource{a: app, c: c}
}

// TestWebSourceAsksFollowClerk: a task's asks are its open decisions; once answered, a decision waiting for task
// start is no ask but the story's answered node, and a void decision is no ask but a void node, as Clerk's Needs
// you split has it.
func TestWebSourceAsksFollowClerk(t *testing.T) {
	dir := clerkFixture(t)
	src := webSourceFor(t, dir)
	task, err := src.Task("app", "a-approve")
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Asks) != 1 || task.Asks[0].ID != "accept-plan" || task.Asks[0].Kind != "approval" || task.Asks[0].Question != "Accept the plan?" ||
		task.Asks[0].Subject != "sha256:"+strings.Repeat("a", 64) || task.Asks[0].OpenedAt == nil {
		t.Fatalf("asks = %+v", task.Asks)
	}
	if task.Stands != "waiting for your approval" || task.Title != "Approve the plan" || task.State != "open" || !task.ReadAt.Equal(clerkNow) {
		t.Errorf("task = %q %q %q %v", task.Stands, task.Title, task.State, task.ReadAt)
	}
	var plan, decision *web.Node
	for i, n := range task.Story {
		switch n.Title {
		case "The plan was written (round 1)":
			plan = &task.Story[i]
		case "The plan is ready for your approval":
			decision = &task.Story[i]
		}
	}
	if plan == nil || plan.State != web.StateDone || plan.Body != "Planned the change in three steps." {
		t.Errorf("plan node = %+v in %+v", plan, task.Story)
	}
	if decision == nil || decision.State != web.StateYou || decision.Ask != "accept-plan" || decision.Raw != "accept-plan" ||
		len(decision.Events) != 1 || decision.Events[0].Text != "decision accept-plan opened · approval · subject sha256:aaaaaaaaaaaa" {
		t.Errorf("decision node = %+v", decision)
	}
	if plan != nil && (plan.Raw != "plan-1" || len(plan.Events) != 2 || plan.Events[1].Text != "attempt plan-1 succeeded") {
		t.Errorf("plan node's raw records = %q %+v", plan.Raw, plan.Events)
	}
	if w := task.Where; w.Lead != "Waiting for you on 1 ask:" || w.Rest != "approval of the plan." || !w.You {
		t.Errorf("where it stands = %+v", w)
	}
	if task.Story[0].Title != "You added the task" {
		t.Errorf("the story does not start with the task added: %+v", task.Story[0])
	}

	answerClerkDecision(t, dir, "app", "a-approve", "accept-plan", "approve\n\nGo ahead.")
	task, err = src.Task("app", "a-approve")
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Asks) != 0 || task.Stands != "waiting for the Director" {
		t.Fatalf("an answered decision waiting for task start is an ask: %+v, %q", task.Asks, task.Stands)
	}
	for _, n := range task.Story {
		if n.Title == "You approved the plan" && (n.State != web.StateDone || n.Word != "Approve" || len(n.Said) != 1 || n.Said[0].Text != "Go ahead.") {
			t.Errorf("answered node = %+v", n)
		}
	}

	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenDecision("open-void", store.Decision{Repository: "app", TaskID: "a-approve", ID: "accept-2", Kind: store.DecisionApproval,
		Subject: &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + strings.Repeat("b", 64)}, Options: []string{"approved", "changes_requested"}, Question: "Accept again?"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VoidDecision("void", "app", "a-approve", "accept-2", "superseded: the spec changed"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	task, err = src.Task("app", "a-approve")
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Asks) != 0 {
		t.Errorf("a void decision is an ask: %+v", task.Asks)
	}
	found := false
	for _, n := range task.Story {
		if n.Title == "An earlier approval of the plan was withdrawn" {
			found = n.State == web.StateVoid && strings.Contains(n.Body, "superseded: the spec changed") && n.Ask == ""
		}
	}
	if !found {
		t.Errorf("no void node for accept-2 in %+v", task.Story)
	}
}

// TestWebSourceExplanation: an ask carries its readable headline and short phrase, and the Director's explanation
// from the status feed once decide explain records one with the subject_digest decide show prints, which is current,
// for a decision with a subject and for a question with none. Stale rendering is pinned in internal/web with records
// made by hand, since explain now refuses a digest that is not current.
func TestWebSourceExplanation(t *testing.T) {
	dir := clerkFixture(t)
	src := webSourceFor(t, dir)
	task, err := src.Task("app", "a-approve")
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Asks) != 1 || task.Asks[0].Headline != "The plan is ready for your approval." || task.Asks[0].Phrase != "approval of the plan" ||
		task.Asks[0].Explanation != nil {
		t.Fatalf("asks = %+v", task.Asks)
	}
	path := filepath.Join(dir, "director.store")
	code, out, stderr := runDecide(t, "", "open", "--store", path, "--repo", "app", "--task", "a-approve", "--question", "Which port?", "--option", "80", "--option", "8080")
	if code != 0 {
		t.Fatalf("open: exit %d, %s", code, stderr)
	}
	question := decodeJSON[struct {
		ID string `json:"id"`
	}](t, out).ID
	// The approval has a subject and the question has none, so both of SubjectDigestOf's branches are pinned.
	for _, id := range []string{"accept-plan", question} {
		code, out, stderr := runDecide(t, "", "show", "--store", path, "--repo", "app", "--task", "a-approve", "--decision", id)
		digest := decodeJSON[struct {
			SubjectDigest string `json:"subject_digest"`
		}](t, out).SubjectDigest
		if code != 0 || digest == "" {
			t.Fatalf("show %s: exit %d, %s %s", id, code, out, stderr)
		}
		if code, _, stderr := runDecide(t, "", "explain", "app/a-approve", id, "--store", path, "--subject-digest", digest, "--text", "It plans **three** steps."); code != 0 {
			t.Fatalf("explain %s: exit %d, %s", id, code, stderr)
		}
		if task, err = src.Task("app", "a-approve"); err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(task.Asks, func(a web.Ask) bool { return a.ID == id })
		if i < 0 {
			t.Fatalf("no ask %s in %+v", id, task.Asks)
		}
		if e := task.Asks[i].Explanation; e == nil || e.Text != "It plans **three** steps." || e.At.IsZero() || e.Stale {
			t.Errorf("%s with the printed digest %s: explanation = %+v, want current", id, digest, e)
		}
	}
}

// TestWebSourceDesignMockup: a design round's approval, bound to a designer attempt's design, carries that attempt's
// mock-up under its legacy name, designer-<k>-design-2; an approval bound to a designer attempt with no mock-up, or to another step's
// document with a -2 output, carries none.
func TestWebSourceDesignMockup(t *testing.T) {
	dir := clerkFixture(t)
	src := webSourceFor(t, dir)
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sum := func(c byte) string { return strings.Repeat(string(c), 64) }
	for _, d := range []store.DocumentRef{
		{Name: "designer-1-design", Path: "tasks/app/a-approve/designer-1/design.md", SHA256: sum('1')},
		{Name: "designer-1-design-2", Path: "tasks/app/a-approve/designer-1/mockup.html", SHA256: sum('2')},
		{Name: "designer-2-design", Path: "tasks/app/a-approve/designer-2/design.md", SHA256: sum('3')},
		{Name: "plan-1-spec", Path: "tasks/app/a-approve/plan-1/spec.md", SHA256: sum('4')},
		{Name: "plan-1-spec-2", Path: "tasks/app/a-approve/plan-1/extra.md", SHA256: sum('5')},
	} {
		d.Repository, d.TaskID = "app", "a-approve"
		if _, err := s.PutDocument("doc:"+d.Name, d); err != nil {
			t.Fatal(err)
		}
	}
	for id, c := range map[string]byte{"design-1": '1', "design-2": '3', "accept-9": '4'} {
		d := store.Decision{Repository: "app", TaskID: "a-approve", ID: id, Kind: store.DecisionApproval,
			Subject: &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + sum(c)}, Options: []string{"approved", "changes_requested"}, Question: "Approve?"}
		if _, err := s.OpenDecision("open:"+id, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	task, err := src.Task("app", "a-approve")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]web.Ask{}
	for _, a := range task.Asks {
		got[a.ID] = a
	}
	if a := got["design-1"]; a.SubjectDocument != "designer-1-design" || a.Mockup != "designer-1-design-2" {
		t.Errorf("design-1 ask = %+v, want its designer's mock-up", a)
	}
	if a := got["design-2"]; a.SubjectDocument != "designer-2-design" || a.Mockup != "" {
		t.Errorf("design-2 ask = %+v, want no mock-up", a)
	}
	if a := got["accept-9"]; a.SubjectDocument != "plan-1-spec" || a.Mockup != "" {
		t.Errorf("accept-9 ask = %+v, want no mock-up", a)
	}
}

// TestWebSourceDesignNamedMockup: a mock-up recorded under its named part, designer-<k>-mockup, with no output at
// index 2, still links from its design ask.
func TestWebSourceDesignNamedMockup(t *testing.T) {
	dir := clerkFixture(t)
	src := webSourceFor(t, dir)
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sum := func(c byte) string { return strings.Repeat(string(c), 64) }
	for _, d := range []store.DocumentRef{
		{Name: "designer-1-design", Path: "tasks/app/a-approve/designer-1/design.md", SHA256: sum('1')},
		{Name: "designer-1-mockup", Path: "tasks/app/a-approve/designer-1/mockup.md", SHA256: sum('2')},
	} {
		d.Repository, d.TaskID = "app", "a-approve"
		if _, err := s.PutDocument("doc:"+d.Name, d); err != nil {
			t.Fatal(err)
		}
	}
	d := store.Decision{Repository: "app", TaskID: "a-approve", ID: "design-1", Kind: store.DecisionApproval,
		Subject: &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + sum('1')}, Options: []string{"approved", "changes_requested"}, Question: "Approve?"}
	if _, err := s.OpenDecision("open:design-1", d); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	task, err := src.Task("app", "a-approve")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range task.Asks {
		if a.ID == "design-1" {
			if a.SubjectDocument != "designer-1-design" || a.Mockup != "designer-1-mockup" {
				t.Errorf("design-1 ask = %+v, want its named mock-up", a)
			}
			return
		}
	}
	t.Errorf("asks = %+v, want design-1", task.Asks)
}

// TestWebSourceDecisionTone:an answered decision's step and story node take their state from the page's option
// table, so a merge hold and a follow-ups none are done with their word, not sent back; and a question with options
// answered in free text shows the text.
func TestWebSourceDecisionTone(t *testing.T) {
	dir := clerkFixture(t)
	src := webSourceFor(t, dir)
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	subject := &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + strings.Repeat("d", 64)}
	for _, d := range []store.Decision{
		{ID: "deliver-1", Kind: store.DecisionMerge, Subject: subject, Options: []string{"merge", "hold"}, Question: "Merge it?"},
		{ID: "validate-1", Kind: store.DecisionFollowUps, Subject: subject, Options: []string{"pick", "none"}, Question: "Which follow-ups?"},
		{ID: "ask-1", Kind: store.DecisionQuestion, Options: []string{"left", "right"}, Question: "Which way?"},
	} {
		d.Repository, d.TaskID = "app", "a-approve"
		if _, err := s.OpenDecision("open:"+d.ID, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	answerClerkDecision(t, dir, "app", "a-approve", "deliver-1", "hold")
	answerClerkDecision(t, dir, "app", "a-approve", "validate-1", "none")
	answerClerkDecision(t, dir, "app", "a-approve", "ask-1", "Neither; go straight on.")
	task, err := src.Task("app", "a-approve")
	if err != nil {
		t.Fatal(err)
	}
	nodes := map[string]web.Node{}
	for _, n := range task.Story {
		nodes[n.Title] = n
	}
	if n, ok := nodes["You held the merge"]; !ok || n.State != web.StateDone || n.Word != "Hold" || n.Raw != "deliver-1" {
		t.Errorf("merge hold node = %+v", n)
	}
	if n, ok := nodes["You chose no follow-ups"]; !ok || n.State != web.StateDone || n.Word != "None" {
		t.Errorf("follow-ups none node = %+v", n)
	}
	if n, ok := nodes["You answered the Director's question"]; !ok || n.State != web.StateDone || n.Word != "answered" || len(n.Said) != 1 || n.Said[0].Text != "Neither; go straight on." {
		t.Errorf("free-text question node = %+v", n)
	}
	steps := map[string]web.Step{}
	for _, st := range task.Steps {
		steps[st.Name] = st
	}
	if st := steps["Deliver"]; st.State != web.StateDone || st.Note != "merge hold" || st.Raw != "deliver-1 hold" {
		t.Errorf("deliver step = %+v", st)
	}
	if st := steps["Validate"]; st.State != web.StateDone || st.Note != "follow-ups none" || st.Raw != "validate-1 none" {
		t.Errorf("validate step = %+v", st)
	}
}

// TestWebSourceStagesSlicesAndRefusals: a grill handed to the Director is its stage, not an ask; a multi-PR task has
// its slices, latest PR and slice merge; an unknown task is ErrNotFound and a held store ErrBusy.
func TestWebSourceStagesSlicesAndRefusals(t *testing.T) {
	dir := clerkFixture(t)
	src := webSourceFor(t, dir)
	grill, err := src.Task("app", "a-grill")
	if err != nil {
		t.Fatal(err)
	}
	if grill.DirectorStage != "grill" || len(grill.Asks) != 0 {
		t.Errorf("grill: stage %q, asks %+v", grill.DirectorStage, grill.Asks)
	}
	if len(grill.Steps) == 0 || grill.Steps[0].Name != "Grill" || grill.Steps[len(grill.Steps)-1].State != web.StateLater {
		t.Errorf("grill steps = %+v", grill.Steps)
	}
	sliced, err := src.Task("app", "a-sliced")
	if err != nil {
		t.Fatal(err)
	}
	if sl := sliced.Slices; sl == nil || sl.Current != 2 || sl.Total != 3 || len(sl.Items) != 3 || sl.Items[0].State != "merged" || sl.Items[0].PR == nil || sl.Items[0].PR.Number != 401 {
		t.Fatalf("slices = %+v", sliced.Slices)
	}
	if pr := sliced.Status.PR; pr == nil || pr.Number != 402 || sliced.Status.CI != web.CIPending || sliced.Status.Step != "Slice 2 validation" {
		t.Errorf("status = %+v", sliced.Status)
	}
	merged := false
	for _, n := range sliced.Story {
		merged = merged || n.Title == "Slice 1 was merged"
	}
	if !merged {
		t.Errorf("no slice merge node in %+v", sliced.Story)
	}
	var spec *web.Document
	for i, d := range sliced.Documents {
		if d.Name == "accept-1-spec" {
			spec = &sliced.Documents[i]
		}
	}
	if spec == nil || spec.Kind != "Spec" || spec.Path != "tasks/app/a-sliced/plan-1/spec.md" || spec.RecordedAt == nil {
		t.Errorf("spec document = %+v", spec)
	}

	if _, err := src.Task("app", "nope"); !errors.Is(err, web.ErrNotFound) {
		t.Errorf("unknown task: %v", err)
	}
	held, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	old := webLockWait
	// timing:allow deliberate refusal input: the writer holds the lock for the whole call, so any wait is refused.
	webLockWait = 10 * time.Millisecond
	defer func() { webLockWait = old }()
	if _, err := src.Task("app", "a-grill"); !errors.Is(err, web.ErrBusy) {
		t.Errorf("held store: %v, want ErrBusy", err)
	}
}

// TestWebSourceTaskList: the task list holds every open task and those closed in the last 7 days, as Clerk's
// /guild all does; each wait and stage lands in its group, with its where-it-stands sentence and since when; the
// open ones are in Clerk's order and the finished one last; a held store is ErrBusy.
func TestWebSourceTaskList(t *testing.T) {
	dir := clerkFixture(t)
	src := webSourceFor(t, dir)
	l, err := src.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	if !l.ReadAt.Equal(clerkNow) {
		t.Errorf("read at %v", l.ReadAt)
	}
	want := map[string]web.Group{
		"app/a-approve": web.GroupYou, "app/a-grill": web.GroupYou,
		"app/a-build": web.GroupBuild, "app/a-q1": web.GroupMergeQueue, "app/a-q2": web.GroupMergeQueue, "app/a-q3": web.GroupMergeQueue,
		"lib/l-front": web.GroupMergeQueue, "app/a-checks": web.GroupCI, "app/a-sliced": web.GroupCI, "app/a-queued": web.GroupBuild,
		"app/a-deps": web.GroupHeld, "app/a-bug": web.GroupHeld, "app/a-long": web.GroupHeld, "app/a-new": web.GroupHeld,
		"lib/a-new": web.GroupHeld, "app/a-odd": web.GroupHeld,
	}
	var order []string
	byKey := map[string]web.Listed{}
	for _, x := range l.Tasks {
		key := x.Repository + "/" + x.ID
		order = append(order, key)
		byKey[key] = x
		if g, ok := want[key]; !ok || g != x.Group {
			t.Errorf("%s is in group %q, want %q", key, x.Group, g)
		}
		if x.Where.Lead == "" || x.Title == "" || x.Since == nil {
			t.Errorf("%s has no sentence, title or since: %+v", key, x)
		}
	}
	if len(l.Tasks) != len(want) {
		t.Errorf("listed %v, want %d tasks", order, len(want))
	}
	for _, gone := range []string{"app/a-merged9", "app/a-merged2"} {
		if _, ok := byKey[gone]; ok {
			t.Errorf("%s, closed more than 24 hours ago, is listed", gone)
		}
	}
	if wantOrder := []string{"app/a-bug", "app/a-q1", "app/a-build"}; len(order) < 3 || strings.Join(order[:3], " ") != strings.Join(wantOrder, " ") {
		t.Errorf("order = %v", order)
	}
	// the row's columns: priority, step and attempt, slice, PR and CI, the ask and the merge-queue place
	if b := byKey["app/a-build"]; b.Step != "build" || b.Attempt < 1 {
		t.Errorf("a-build = %+v", b)
	}
	if q := byKey["app/a-queued"]; q.Step != "build" || q.Attempt != 1 || q.Priority != int(store.PriorityLow) {
		t.Errorf("a-queued = %+v", q)
	}
	if s := byKey["app/a-sliced"]; s.Slice != 2 || s.Slices != 3 || s.PR == nil || s.PR.Number != 402 || s.CI != web.CIPending {
		t.Errorf("a-sliced = %+v (PR %+v)", s, s.PR)
	}
	if c := byKey["app/a-checks"]; c.Slices != 0 || c.PR == nil || c.PR.Number != 212 {
		t.Errorf("a-checks = %+v", c)
	}
	if a := byKey["app/a-approve"]; a.Ask != "Approval of the plan." {
		t.Errorf("a-approve ask = %q", a.Ask)
	}
	if g := byKey["app/a-grill"]; g.Ask != "The grill runs in the Director's session with you." {
		t.Errorf("a-grill ask = %q", g.Ask)
	}
	if b := byKey["app/a-bug"]; b.Ask != "" || b.Queue != nil {
		t.Errorf("a-bug = %+v", b)
	}
	if q := byKey["app/a-q1"].Queue; q == nil || q.Position != 1 || !q.Front || q.WaitingOn != "rebase" {
		t.Errorf("a-q1 queue = %+v", q)
	}
	if q := byKey["app/a-q2"].Queue; q == nil || q.Position != 2 || q.Front || !q.Held {
		t.Errorf("a-q2 queue = %+v", q)
	}
	if q := byKey["lib/l-front"].Queue; q == nil || q.Position != 1 || !q.Front || q.WaitingOn != "ci" {
		t.Errorf("l-front queue = %+v", q)
	}

	// Recently finished covers 24 hours: widened past two days, a-merged2 shows, merged, newest first and last
	window := webFinishedWindow
	webFinishedWindow = 3 * 24 * time.Hour
	l3, err := src.Tasks()
	webFinishedWindow = window
	if err != nil {
		t.Fatal(err)
	}
	if n := len(l3.Tasks); n != len(want)+1 || l3.Tasks[n-1].ID != "a-merged2" {
		t.Fatalf("with a 3-day window: %d tasks, last %+v", n, l3.Tasks[n-1])
	}
	if m := l3.Tasks[len(l3.Tasks)-1]; m.Group != web.GroupFinished || m.Finished != "merged" || m.Where.Lead != "Done:" || m.Since == nil || clerkNow.Sub(*m.Since) > 3*24*time.Hour {
		t.Errorf("a-merged2 = %+v", m)
	}
	if a := byKey["app/a-approve"]; a.Where.Lead != "Waiting for you on 1 ask:" || a.Where.Rest != "approval of the plan." || !a.Where.You || a.Stands != "waiting for your approval" {
		t.Errorf("a-approve = %+v", a)
	}
	if g := byKey["app/a-grill"]; g.Where.Lead != "Waiting for you in the Director's session:" {
		t.Errorf("a-grill = %+v", g)
	}
	if c := byKey["app/a-checks"]; c.Where.Rest != "CI is running on PR #212." {
		t.Errorf("a-checks = %+v", c)
	}

	held, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	old := webLockWait
	// timing:allow deliberate refusal input: the writer holds the lock for the whole call, so any wait is refused.
	webLockWait = 10 * time.Millisecond
	defer func() { webLockWait = old }()
	if _, err := src.Tasks(); !errors.Is(err, web.ErrBusy) {
		t.Errorf("held store: %v, want ErrBusy", err)
	}
}

// TestWebListStageGroups: a starting, running or queued step is grouped by its step type in the bound workflow, never
// by its name: a step named build of type plan is in Grill and plan; with no workflow, the type named like the step;
// a designer step is in Grill and plan, and a step of no known type in Build and review.
func TestWebListStageGroups(t *testing.T) {
	def := &workflow.Definition{Steps: []workflow.Step{{ID: "build", Type: "plan"}, {ID: "draft", Type: "grill"}, {ID: "check", Type: "review"}}}
	for _, c := range []struct {
		def        *workflow.Definition
		step, wait string
		want       web.Group
	}{
		{def, "build", "attempt", web.GroupPlan}, {def, "draft--s2", "queued", web.GroupPlan}, {def, "check", "attempt", web.GroupBuild},
		{nil, "research", "attempt", web.GroupPlan}, {nil, "designer", "attempt", web.GroupPlan}, {nil, "deliver-rebase", "attempt", web.GroupBuild},
		{nil, "publish", "queued", web.GroupBuild}, {def, "mystery", "attempt", web.GroupBuild},
	} {
		ct := clerkTask{view: taskView{Task: store.Task{Repository: "app", ID: "t1", State: store.TaskOpen}}}
		ct.status.Next, _ = json.Marshal(clerkNext{Step: c.step, Wait: c.wait, Attempt: 1})
		b := webBuilder{d: clerkData{now: clerkNow}, ct: ct, def: c.def}
		if got := b.group(web.Where{}); got != c.want {
			t.Errorf("%s (%s): group %q, want %q", c.step, c.wait, got, c.want)
		}
	}
}

// TestWebStageGroupsCoverEveryStepType: every built-in step type has a stage group, a moving one.
func TestWebStageGroupsCoverEveryStepType(t *testing.T) {
	for _, k := range workflow.Kinds() {
		if g := webStageGroup[k.Type]; g != web.GroupPlan && g != web.GroupBuild {
			t.Errorf("step type %s has stage group %q, want plan or build", k.Type, g)
		}
	}
}

// TestWebListAttempt: the row's attempt is the status feed's latest of the next step, or next.json's when the feed
// has none.
func TestWebListAttempt(t *testing.T) {
	ct := clerkTask{view: taskView{Task: store.Task{Repository: "app", ID: "t1", State: store.TaskOpen}}}
	ct.status.Steps = []statusStep{{Step: "plan", Attempt: 2}, {Step: "build--s2", Attempt: 3}}
	b := webBuilder{ct: ct}
	for _, c := range []struct {
		n    clerkNext
		want int
	}{
		{clerkNext{Step: "build--s2", Wait: "checks"}, 3}, {clerkNext{Step: "build--s2", Wait: "attempt", Attempt: 4}, 4},
		{clerkNext{Step: "review", Wait: "queued", Attempt: 1}, 1}, {clerkNext{Step: "review", Wait: "dependencies"}, 0},
	} {
		if got := b.listAttempt(c.n); got != c.want {
			t.Errorf("%+v: attempt %d, want %d", c.n, got, c.want)
		}
	}
}

// TestWebListGroups pins the task list's group for every wait, a Director stage and an open or deferred ask.
func TestWebListGroups(t *testing.T) {
	task := func(wait string) webBuilder {
		ct := clerkTask{view: taskView{Task: store.Task{Repository: "app", ID: "t1", State: store.TaskOpen}}}
		if wait != "" {
			ct.status.Next, _ = json.Marshal(clerkNext{Step: "build", Wait: wait, Attempt: 1})
		}
		return webBuilder{d: clerkData{now: clerkNow}, ct: ct}
	}
	for _, c := range []struct {
		wait string
		want web.Group
	}{
		{string(interp.WaitAttempt), web.GroupBuild}, {string(interp.WaitQueued), web.GroupBuild},
		{string(interp.WaitChecks), web.GroupCI}, {string(interp.WaitMergeQueue), web.GroupMergeQueue},
		{string(interp.WaitDecision), web.GroupHeld}, {string(interp.WaitDirector), web.GroupHeld},
		{string(interp.WaitPaused), web.GroupHeld}, {string(interp.WaitDependencies), web.GroupHeld},
		{string(interp.WaitDone), web.GroupHeld}, {"mystery", web.GroupHeld}, {"", web.GroupHeld},
	} {
		b := task(c.wait)
		if got := b.group(b.where(nil, "", nil)); got != c.want {
			t.Errorf("wait %q: group %q, want %q", c.wait, got, c.want)
		}
	}
	b := task(string(interp.WaitDirectorStage))
	if got := b.group(b.where(nil, b.directorStage(), nil)); got != web.GroupYou {
		t.Errorf("Director stage: group %q, want you", got)
	}
	for _, asks := range [][]web.Ask{{{ID: "accept-1", Kind: "approval"}}, {{ID: "deliver-1", Kind: "merge", Deferred: true}}} {
		b := task(string(interp.WaitMergeQueue))
		if got := b.group(b.where(asks, "", nil)); got != web.GroupYou {
			t.Errorf("asks %+v: group %q, want you", asks, got)
		}
	}
}

// TestWebNeedsYouLeavesOthersToTheDirector: the copy's install item is no ask and no Director wait, the task's
// decision items are its asks, and its other needs_me items wait on the Director.
func TestWebNeedsYouLeavesOthersToTheDirector(t *testing.T) {
	ct := clerkTask{view: taskView{Task: store.Task{Repository: "app", ID: "t1", Title: "T", State: store.TaskOpen}}}
	d := clerkData{now: clerkNow, byKey: map[string]clerkTask{"app/t1": ct}}
	d.status.NeedsMe = []needsMeItem{
		{Kind: decisionKind, Repository: "app", TaskID: "t1", Decision: "d1", Reason: "Answer it"},
		{Kind: installKind, Repository: "app", TaskID: "t1", Reason: "install the new guildhall"},
		{Kind: checksKind, Repository: "app", TaskID: "t1", Reason: "checks are pending"},
		{Kind: checksKind, Repository: "app", TaskID: "t2", Reason: "another task's"},
	}
	d.status.Decisions = []statusDecision{{Repository: "app", TaskID: "t1", ID: "d1", Question: "Answer it"}, {Repository: "app", TaskID: "t2", ID: "d2"}}
	task := webBuilder{d: d, ct: ct}.task()
	if len(task.DirectorWaits) != 1 || task.DirectorWaits[0] != "checks are pending" {
		t.Errorf("Director waits = %q", task.DirectorWaits)
	}
	if len(task.Asks) != 1 || task.Asks[0].ID != "d1" {
		t.Errorf("asks = %+v", task.Asks)
	}
}

// webStub is a WebListen that binds loopback ports, as a test cannot bind a tailnet address, recording each address
// it was asked for; with fail set it refuses as an address off the tailnet is refused.
type webStub struct {
	mu    sync.Mutex
	asked []string
	addrs []string
	fail  error
}

func (w *webStub) listen(address string) (net.Listener, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.asked = append(w.asked, address)
	if w.fail != nil {
		return nil, w.fail
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err == nil {
		w.addrs = append(w.addrs, ln.Addr().String())
	}
	return ln, err
}

func (w *webStub) bound() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string{}, w.addrs...)
}

// testWebHost is the [web] address the tests configure, which the server requires as each request's Host.
const testWebHost = "host.tail.ts.net:4388"

// getPage fetches a page from a test server, its listener at url's host, as the configured address testWebHost.
func getPage(t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = testWebHost
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // a loopback test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// TestWebCommand: guildhall web serves the copy's pages at its [web] address until it is stopped; a copy with no
// [web] table and an address off the tailnet are refused.
func TestWebCommand(t *testing.T) {
	dir := clerkFixture(t)
	cfgPath := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	app, stdout, stderr := newTestApp(nil, nil)
	app.Getwd = func() (string, error) { return dir, nil }
	if code := app.Run([]string{"web"}); code != 1 || !strings.Contains(stderr.String(), "no [web] table") {
		t.Errorf("no [web]: exit %d, stderr %s", code, stderr)
	}
	if code := app.Run([]string{"web", "extra"}); code != 2 {
		t.Errorf("an argument: exit %d, want 2", code)
	}
	writeFile(t, cfgPath, string(b)+"\n[web]\naddress = \"127.0.0.1:4388\"\n")
	stderr.Reset()
	if code := app.Run([]string{"web", "--copy", dir}); code != 1 || !strings.Contains(stderr.String(), "not a Tailscale address") {
		t.Errorf("loopback: exit %d, stderr %s; want it refused off the tailnet", code, stderr)
	}

	writeFile(t, cfgPath, string(b)+"\n[web]\naddress = \"host.tail.ts.net:4388\"\n")
	stub := &webStub{}
	app.WebListen = stub.listen
	app.webSignals = make(chan os.Signal, 1)
	served := make(chan string, 1)
	app.webServing = func(addr string) { served <- addr }
	done := make(chan int, 1)
	go func() { done <- app.Run([]string{"web"}) }()
	addr := <-served
	code, body := getPage(t, "http://"+addr+"/app/a-approve")
	if code != http.StatusOK || !strings.Contains(body, "<h1>Approve the plan</h1>") || !strings.Contains(body, `id="accept-plan"`) {
		t.Errorf("page: %d\n%s", code, body)
	}
	app.webSignals <- os.Interrupt
	if code := <-done; code != 0 {
		t.Errorf("web exited %d, stderr %s", code, stderr)
	}
	if len(stub.asked) != 1 || stub.asked[0] != "host.tail.ts.net:4388" || !strings.Contains(stdout.String(), "serving the task pages at http://host.tail.ts.net:4388/<repo>/<task>") {
		t.Errorf("asked %q; stdout %s", stub.asked, stdout)
	}
	if _, err := net.Dial("tcp", addr); err == nil {
		t.Error("the server still listens after web stopped")
	}
}

// TestStartWebRecordsAndWarns: a [web] table serves and records the session serving it, which director status
// reports while that session runs; a bind failure only warns and is recorded; no table serves nothing.
func TestStartWebRecordsAndWarns(t *testing.T) {
	dir := clerkFixture(t)
	state := filepath.Join(dir, "state")
	cfg := &config.Config{Web: &config.Web{Address: "host.tail.ts.net:4388"}}
	app, stdout, _ := newTestApp(nil, nil)
	if s := app.startWeb(&config.Config{}, 1, dir, filepath.Join(dir, "director.store"), state); s != nil {
		t.Error("no [web] table serves")
	}
	if _, err := os.Stat(filepath.Join(state, webRecordFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("no [web] table wrote a record: %v", err)
	}
	stub := &webStub{}
	app.WebListen = stub.listen
	s := app.startWeb(cfg, 3, dir, filepath.Join(dir, "director.store"), state)
	if s == nil || len(stub.bound()) != 1 {
		t.Fatalf("not serving; stdout %s", stdout)
	}
	if code, _ := getPage(t, "http://"+stub.bound()[0]+"/app/a-grill"); code != http.StatusOK {
		t.Errorf("page: %d", code)
	}
	running := statusSession{State: string(store.DirectorRunning), Number: 3}
	cfgPath := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := readWebStatus(cfgPath, state, running); got == nil || got.State != webNotConfigured {
		t.Errorf("status with no [web]: %+v", got)
	}
	writeFile(t, cfgPath, string(b)+"\n[web]\naddress = \"host.tail.ts.net:4388\"\n")
	if got := readWebStatus(cfgPath, state, running); got == nil || *got != (webStatus{State: webServing, URL: "http://host.tail.ts.net:4388"}) {
		t.Errorf("status while serving: %+v", got)
	}
	if got := readWebStatus(cfgPath, state, statusSession{State: string(store.DirectorRunning), Number: 4}); got.State != webNotServing {
		t.Errorf("status of another session: %+v", got)
	}
	s.close()
	s.close()
	if _, err := net.Dial("tcp", stub.bound()[0]); err == nil {
		t.Error("still listening after close")
	}
	if got := readWebStatus(cfgPath, state, running); got.State != webNotServing {
		t.Errorf("status after close: %+v", got)
	}

	stub.fail = errors.New(`web address "host.tail.ts.net:4388" resolves to 192.168.1.5: not a Tailscale address`)
	stdout.Reset()
	if s := app.startWeb(cfg, 3, dir, filepath.Join(dir, "director.store"), state); s != nil {
		t.Error("a refused bind serves")
	}
	if !strings.Contains(stdout.String(), "web: warning: not serving the task pages:") {
		t.Errorf("no warning: %s", stdout)
	}
	if got := readWebStatus(cfgPath, state, running); got.State != webFailed || !strings.Contains(got.Reason, "not a Tailscale address") {
		t.Errorf("status after a refused bind: %+v", got)
	}
}

// TestDirectorSessionServesTheTaskPages: a supervised Director session with a [web] table serves the pages while it
// runs, binds again at a reload and stops serving when it ends; a Steward session's watchers serve nothing.
func TestDirectorSessionServesTheTaskPages(t *testing.T) {
	dir, _ := supervisedCopy(t)
	cfgPath := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfgPath, string(b)+"\n[web]\naddress = \"host.tail.ts.net:4388\"\n")
	stub := &webStub{}
	done := superviseWith(t, dir, time.Hour, func(a *App) { a.WebListen = stub.listen })
	testenv.WaitFor(t, "session 1 serves", func() bool { return len(stub.bound()) == 1 })
	first := stub.bound()[0]
	if code, body := getPage(t, "http://"+first+"/app/t1"); code != http.StatusOK || !strings.Contains(body, "Sort the task list") {
		t.Errorf("page: %d\n%s", code, body)
	}
	if code, out, stderr := directorCmd(t, nil, "status", "--json", "--copy", dir); code != 0 || !strings.Contains(out, `"web":{"state":"serving","url":"http://host.tail.ts.net:4388","links":"markdown"}`) {
		t.Errorf("status: exit %d, %s, %s", code, out, stderr)
	}
	// Run from a Director whose Claude has function hooks on, status asks for ticket tokens Lanterns links.
	hooks := func(name string) string {
		if name == "CLAUDE_CODE_ENABLE_FUNCTION_HOOKS" {
			return "1"
		}
		return ""
	}
	if code, out, stderr := directorCmd(t, hooks, "status", "--json", "--copy", dir); code != 0 || !strings.Contains(out, `"web":{"state":"serving","url":"http://host.tail.ts.net:4388","links":"ticket"}`) {
		t.Errorf("status with function hooks: exit %d, %s, %s", code, out, stderr)
	}
	copyRun(t, dir, "director", "reload")
	copyRun(t, dir, "director", "turn-ended")
	waitSessions(t, dir, 2)
	testenv.WaitFor(t, "session 2 binds again", func() bool { return len(stub.bound()) == 2 })
	if _, err := net.Dial("tcp", first); err == nil {
		t.Error("session 1's server still listens after the reload")
	}
	if code, _ := getPage(t, "http://"+stub.bound()[1]+"/app/t1"); code != http.StatusOK {
		t.Errorf("session 2's page: %d", code)
	}
	copyRun(t, dir, "director", "stop", "--now")
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d", code)
	}
	if _, err := net.Dial("tcp", stub.bound()[1]); err == nil {
		t.Error("the server still listens after the session stopped")
	}

	w, err := stewardRole.watch(nil, startPlan{cfg: &config.Config{Web: &config.Web{Address: "host.tail.ts.net:4388"}}}, 1, "", "", "", "")
	if err != nil || w.web != nil {
		t.Errorf("a Steward session's watchers: %+v, %v; want no web server", w, err)
	}
}

// TestDirectorSessionBindFailureOnlyWarns: a refused bind leaves the session running, and status says it failed.
func TestDirectorSessionBindFailureOnlyWarns(t *testing.T) {
	dir, _ := supervisedCopy(t)
	cfgPath := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfgPath, string(b)+"\n[web]\naddress = \"host.tail.ts.net:4388\"\n")
	stub := &webStub{fail: errors.New("not a Tailscale address")}
	done := superviseWith(t, dir, time.Hour, func(a *App) { a.WebListen = stub.listen })
	if code, out, stderr := directorCmd(t, nil, "status", "--json", "--copy", dir); code != 0 || !strings.Contains(out, `"web":{"state":"failed to bind","url":"http://host.tail.ts.net:4388","reason":"not a Tailscale address","links":"markdown"}`) {
		t.Errorf("status: exit %d, %s, %s", code, out, stderr)
	}
	copyRun(t, dir, "director", "stop", "--now")
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d", code)
	}
}

// feedbackFrom writes body to <dir>/documents/<rel> and returns a webBuilder whose files record it, under the
// slash path rel, as send-back-build-1 with its sha256.
func feedbackFrom(t *testing.T, dir, rel, body string) webBuilder {
	t.Helper()
	file := filepath.Join(dir, "documents", rel)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	doc := store.DocumentRef{Name: "send-back-build-1", Path: rel, SHA256: hex.EncodeToString(sum[:])}
	return webBuilder{fl: taskFiles{Documents: []timedDocument{{DocumentRef: doc}}}, state: dir}
}

func TestWebFeedbackReadsDotDotPrefixedName(t *testing.T) {
	b := feedbackFrom(t, t.TempDir(), "..foo/x.md", "Fix the test.\n")
	if got := b.feedback("build", 1); got != "Fix the test." {
		t.Errorf("feedback = %q, want the quoted text", got)
	}
}

func TestWebFeedbackRefusesBackslashPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a backslash is a path separator on Windows")
	}
	b := feedbackFrom(t, t.TempDir(), `a\b.md`, "Fix the test.\n")
	if got := b.feedback("build", 1); got != "" {
		t.Errorf("feedback = %q, want none for a backslash path", got)
	}
}

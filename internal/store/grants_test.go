package store

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// grantSteps are the claude steps of the sample tasks' workflow a grant may name.
var grantSteps = []string{"plan", "build", "review"}

func buildGrant(task string, rules ...string) TaskGrant {
	return TaskGrant{Repository: "app", TaskID: task, ID: "g1", Step: "build", Rules: rules, Reason: "The probe needs go.", Until: GrantUntilAttempt}
}

func grantStatus(t *testing.T, s *Store, task, id string) TaskGrantStatus {
	t.Helper()
	all, err := s.TaskGrantStatuses("app", task)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range all {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("no grant %s of app/%s in %+v", id, task, all)
	return TaskGrantStatus{}
}

func TestCheckTaskGrantRule(t *testing.T) {
	for _, rule := range []string{"Bash(claude:*)", "Bash(herdr:*)", "Bash(go:*)", "Bash(go test:*)", "Bash(git status:*)", "Bash(git commit:*)",
		"Bash(git pull:*)", "WebFetch", "WebSearch", "Read(./docs/**)", "Bash(npm run build)", "Bash(go*)", "Bash(env go test:*)",
		"Bash(timeout 60 go test:*)", "Bash(nice -n 10 make:*)", "Bash(sudo git status:*)"} {
		if err := CheckTaskGrantRule(rule); err != nil {
			t.Errorf("%s refused: %v", rule, err)
		}
	}
	for _, rule := range []string{"Bash", "Bash(*)", "Bash(:*)", "not a rule", "Bash(gh:*)", "Bash(gh pr merge:*)", "Bash(glab:*)", "Bash(guildhall:*)",
		"Bash(guildhall decide answer:*)", "Bash(guildhall *)", "Bash(/usr/local/bin/gh:*)", "Bash(gh.exe:*)", "Bash(git:*)", "Bash(git *)", "Bash(git)",
		"Bash(git push:*)", "Bash(git push origin main)", "Bash(git p*)", "Bash(git -C /repo push:*)", "Bash(GIT_DIR=x git push:*)", "Bash(git*)",
		"Bash(g*)", "Bash(gi*)", "Bash(gu*)", "Bash(gl*)", "Bash(g*:*)", "Bash(g* status:*)", "Bash(*h:*)", "Bash(/usr/bin/g*)",
		"Bash(env gh:*)", "Bash(sudo gh:*)", "Bash(command gh:*)", "Bash(xargs gh:*)", "Bash(timeout 5 gh:*)", "Bash(exec guildhall:*)",
		"Bash(nice -n 10 glab:*)", "Bash(env GH_TOKEN=x gh:*)", "Bash(nohup git push:*)", "Bash(sudo git:*)"} {
		if err := CheckTaskGrantRule(rule); err == nil {
			t.Errorf("%s allowed", rule)
		}
	}
}

// TestCreateTaskGrantRefusals: every refusal writes nothing, neither the grant nor its decision.
func TestCreateTaskGrantRefusals(t *testing.T) {
	s := parkFixture(t)
	for name, tc := range map[string]struct {
		edit func(*TaskGrant)
		want error
	}{
		"no rules":           {func(g *TaskGrant) { g.Rules = nil }, ErrInvalid},
		"refused rule":       {func(g *TaskGrant) { g.Rules = []string{"Bash(go:*)", "Bash(gh:*)"} }, ErrInvalid},
		"git push":           {func(g *TaskGrant) { g.Rules = []string{"Bash(git push:*)"} }, ErrInvalid},
		"blanket bash":       {func(g *TaskGrant) { g.Rules = []string{"Bash(*)"} }, ErrInvalid},
		"repeated rule":      {func(g *TaskGrant) { g.Rules = []string{"Bash(go:*)", "Bash(go:*)"} }, ErrInvalid},
		"blank reason":       {func(g *TaskGrant) { g.Reason = " " }, ErrInvalid},
		"unknown until":      {func(g *TaskGrant) { g.Until = "forever" }, ErrInvalid},
		"no until":           {func(g *TaskGrant) { g.Until = "" }, ErrInvalid},
		"all until attempt":  {func(g *TaskGrant) { g.Step = GrantAllSteps }, ErrInvalid},
		"step not claude":    {func(g *TaskGrant) { g.Step = "deliver" }, ErrInvalid},
		"invalid step":       {func(g *TaskGrant) { g.Step = "Build" }, ErrInvalid},
		"invalid id":         {func(g *TaskGrant) { g.ID = "g0" }, ErrInvalid},
		"decision given":     {func(g *TaskGrant) { g.Decision = "grant-g1" }, ErrInvalid},
		"already bound":      {func(g *TaskGrant) { g.Bound = []GrantUse{{"build", 1}} }, ErrInvalid},
		"closed task":        {func(g *TaskGrant) { g.TaskID = "t-2" }, ErrInvalid},
		"done task":          {func(g *TaskGrant) { g.TaskID = "t-3" }, ErrInvalid},
		"unknown task":       {func(g *TaskGrant) { g.TaskID = "t-9" }, ErrNotFound},
		"invalid repository": {func(g *TaskGrant) { g.Repository = "App" }, ErrInvalid},
	} {
		g := buildGrant("t-1", "Bash(go:*)")
		tc.edit(&g)
		if _, err := s.CreateTaskGrant("bad-"+strings.ReplaceAll(name, " ", "-"), g, grantSteps); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	if grants, err := s.TaskGrants("app", ""); err != nil || len(grants) != 0 {
		t.Errorf("grants after refusals = %+v, %v", grants, err)
	}
	if d, err := s.AllDecisions("app"); err != nil || len(d) != 0 {
		t.Errorf("decisions after refusals = %+v, %v", d, err)
	}
	g := buildGrant("t-1", "Bash(go:*)")
	g.Step = GrantAllSteps
	g.Until = GrantUntilDone
	if _, err := s.CreateTaskGrant("all-without-steps", g, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("all with no claude step: err = %v", err)
	}
}

// TestCreateTaskGrant: a grant opens its typed decision in the same mutation, replays under its id, conflicts with
// another request under it, and refuses a second open grant for an overlapping step, naming the first.
func TestCreateTaskGrant(t *testing.T) {
	s := parkFixture(t)
	g := buildGrant("t-1", "Bash(go:*)", "Bash(claude:*)")
	rc, err := s.CreateTaskGrant("task-grant:app:t-1:g1", g, grantSteps)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Record != "task-grant/g1" || rc.TaskID != "t-1" {
		t.Errorf("receipt = %+v", rc)
	}
	again, err := s.CreateTaskGrant("task-grant:app:t-1:g1", g, grantSteps)
	if err != nil || again != rc {
		t.Errorf("replay = %+v, %v; want %+v", again, err, rc)
	}
	other := g
	other.Reason = "Another reason."
	if _, err := s.CreateTaskGrant("task-grant:app:t-1:g1", other, grantSteps); !errors.Is(err, ErrMutationConflict) {
		t.Errorf("other request under the same id: err = %v", err)
	}
	d, err := s.Decisions("app", "t-1")
	if err != nil || len(d) != 1 {
		t.Fatalf("decisions = %+v, %v", d, err)
	}
	if d[0].ID != "grant-g1" || d[0].Kind != DecisionGrant || !slices.Equal(d[0].Options, []string{"approve", "reject"}) || d[0].Subject != nil ||
		!strings.Contains(d[0].Question, "Bash(go:*), Bash(claude:*)") || !strings.Contains(d[0].Question, "step build") || !strings.Contains(d[0].Question, g.Reason) {
		t.Errorf("decision = %+v", d[0])
	}
	if st := grantStatus(t, s, "t-1", "g1"); st.State != GrantPending || st.Decision != "grant-g1" || st.ApprovedAt != nil {
		t.Errorf("status = %+v", st)
	}

	second := buildGrant("t-1", "Bash(herdr:*)")
	second.ID = "g2"
	if _, err := s.CreateTaskGrant("task-grant:app:t-1:g2", second, grantSteps); !errors.Is(err, ErrExists) || !strings.Contains(err.Error(), "g1") {
		t.Errorf("second grant for build: err = %v", err)
	}
	second.Step, second.Until = GrantAllSteps, GrantUntilDone
	if _, err := s.CreateTaskGrant("task-grant:app:t-1:g2", second, grantSteps); !errors.Is(err, ErrExists) {
		t.Errorf("grant for every step over a pending build grant: err = %v", err)
	}
	second.Step, second.Until = "plan", GrantUntilAttempt
	if _, err := s.CreateTaskGrant("task-grant:app:t-1:g2", second, grantSteps); err != nil {
		t.Errorf("grant for another step: %v", err)
	}
	if _, err := s.RevokeTaskGrant("task-grant-revoke:app:t-1:g1", "app", "t-1", "g1", "Not needed."); err != nil {
		t.Fatal(err)
	}
	third := buildGrant("t-1", "Bash(go:*)")
	third.ID = "g3"
	if _, err := s.CreateTaskGrant("task-grant:app:t-1:g3", third, grantSteps); err != nil {
		t.Errorf("grant for build after its grant was revoked: %v", err)
	}
	third.ID, third.Step, third.Until = "g4", GrantAllSteps, GrantUntilDone
	if _, err := s.CreateTaskGrant("task-grant:app:t-1:g4", third, grantSteps); !errors.Is(err, ErrExists) {
		t.Errorf("grant for every step over open grants: err = %v", err)
	}
}

// TestTaskGrantStates: every derived state of the table in docs/decisions/task-grants.md, none written.
func TestTaskGrantStates(t *testing.T) {
	s, _ := newStore(t)
	for _, id := range []string{"t-1", "t-2", "t-3", "t-4", "t-5", "t-6"} {
		mustCreate(t, s, "task-"+id, sampleTask("app", id))
	}
	create := func(task string, until GrantUntil) {
		t.Helper()
		g := buildGrant(task, "Bash(go:*)")
		g.Until = until
		if _, err := s.CreateTaskGrant("grant-"+task, g, grantSteps); err != nil {
			t.Fatal(err)
		}
	}
	do := func(m string, f func(string) (Receipt, error)) {
		t.Helper()
		if _, err := f(m); err != nil {
			t.Fatal(err)
		}
	}
	state := func(task string) GrantState { t.Helper(); return grantStatus(t, s, task, "g1").State }

	// t-1: pending, then rejected.
	create("t-1", GrantUntilAttempt)
	if got := state("t-1"); got != GrantPending {
		t.Errorf("unanswered: %s", got)
	}
	do("answer-1", func(m string) (Receipt, error) {
		return s.AnswerDecision(m, "app", "t-1", "grant-g1", "reject\nNot now.")
	})
	if got := state("t-1"); got != GrantRejected {
		t.Errorf("rejected: %s", got)
	}
	if _, err := s.BindTaskGrant("bind-1", "app", "t-1", "g1", "build", 1); !errors.Is(err, ErrInvalid) {
		t.Errorf("bind a rejected grant: err = %v", err)
	}

	// t-2: approved and active, bound to build 1, expired once build 1 has an outcome.
	create("t-2", GrantUntilAttempt)
	do("answer-2", func(m string) (Receipt, error) { return s.AnswerDecision(m, "app", "t-2", "grant-g1", "approve") })
	st := grantStatus(t, s, "t-2", "g1")
	if st.State != GrantActive || st.ApprovedAt == nil {
		t.Errorf("approved: %+v", st)
	}
	if _, err := s.BindTaskGrant("bind-2", "app", "t-2", "g1", "plan", 1); !errors.Is(err, ErrInvalid) {
		t.Errorf("bind to another step: err = %v", err)
	}
	do("bind-2", func(m string) (Receipt, error) { return s.BindTaskGrant(m, "app", "t-2", "g1", "build", 1) })
	do("bind-2-again", func(m string) (Receipt, error) { return s.BindTaskGrant(m, "app", "t-2", "g1", "build", 1) })
	if _, err := s.BindTaskGrant("bind-2-other", "app", "t-2", "g1", "build", 2); !errors.Is(err, ErrInvalid) {
		t.Errorf("bind a one-attempt grant twice: err = %v", err)
	}
	do("start-2", func(m string) (Receipt, error) { return s.StartAttempt(m, "app", "t-2", "build") })
	if st := grantStatus(t, s, "t-2", "g1"); st.State != GrantActive || !slices.Equal(st.Bound, []GrantUse{{"build", 1}}) {
		t.Errorf("bound and running: %+v", st)
	}
	if v, err := s.DirectorView(); err != nil || len(v.Grants) != 1 || v.Grants[0].TaskID != "t-2" || v.Grants[0].ApprovedAt == nil {
		t.Errorf("view grants while active = %+v, %v", v.Grants, err)
	}
	do("finish-2", func(m string) (Receipt, error) { return s.FinishAttempt(m, "app", "t-2", "build", 1, AttemptFailed) })
	if got := state("t-2"); got != GrantExpired {
		t.Errorf("bound attempt finished: %s", got)
	}

	// t-3: until done, bound to two attempts, expired by merge; t-4 the same, expired by cancellation.
	for i, task := range []string{"t-3", "t-4"} {
		create(task, GrantUntilDone)
		do("answer-"+task, func(m string) (Receipt, error) { return s.AnswerDecision(m, "app", task, "grant-g1", "approve") })
		for n := 1; n <= 2; n++ {
			do("bind-"+task+"-"+string(rune('0'+n)), func(m string) (Receipt, error) { return s.BindTaskGrant(m, "app", task, "g1", "build", n) })
			do("start-"+task+"-"+string(rune('0'+n)), func(m string) (Receipt, error) { return s.StartAttempt(m, "app", task, "build") })
			do("finish-"+task+"-"+string(rune('0'+n)), func(m string) (Receipt, error) {
				return s.FinishAttempt(m, "app", task, "build", n, AttemptSucceeded)
			})
		}
		if got := state(task); got != GrantActive {
			t.Errorf("%s until done after two attempts: %s", task, got)
		}
		closed := []TaskState{TaskDone, TaskCancelled}[i]
		do("close-"+task, func(m string) (Receipt, error) { return s.SetTaskState(m, "app", task, closed) })
		if got := state(task); got != GrantExpired {
			t.Errorf("%s %s: %s", task, closed, got)
		}
	}

	// t-5: revoked while pending, and approving it afterwards activates nothing; revoked once.
	create("t-5", GrantUntilAttempt)
	rc, err := s.RevokeTaskGrant("revoke-5", "app", "t-5", "g1", "Asked by mistake.")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.RevokeTaskGrant("revoke-5", "app", "t-5", "g1", "Asked by mistake."); err != nil || again != rc {
		t.Errorf("revoke replay = %+v, %v", again, err)
	}
	if _, err := s.RevokeTaskGrant("revoke-5-again", "app", "t-5", "g1", "Again."); !errors.Is(err, ErrInvalid) {
		t.Errorf("second revoke: err = %v", err)
	}
	do("answer-5", func(m string) (Receipt, error) { return s.AnswerDecision(m, "app", "t-5", "grant-g1", "approve") })
	if st := grantStatus(t, s, "t-5", "g1"); st.State != GrantRevoked || st.Revoked == nil || st.Revoked.Reason != "Asked by mistake." {
		t.Errorf("revoked then approved: %+v", st)
	}

	// t-6: revoked while active.
	create("t-6", GrantUntilAttempt)
	do("answer-6", func(m string) (Receipt, error) { return s.AnswerDecision(m, "app", "t-6", "grant-g1", "approve") })
	do("revoke-6", func(m string) (Receipt, error) { return s.RevokeTaskGrant(m, "app", "t-6", "g1", "Done with it.") })
	if got := state("t-6"); got != GrantRevoked {
		t.Errorf("revoked while active: %s", got)
	}
	if _, err := s.RevokeTaskGrant("revoke-unknown", "app", "t-6", "g2", "No such grant."); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown grant: err = %v", err)
	}

	// The view lists active grants only, and none is active now; the expired and revoked ones left it with no write.
	v, err := s.DirectorView()
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Grants) != 0 {
		t.Errorf("view grants = %+v", v.Grants)
	}
	all, err := s.TaskGrantStatuses("", "")
	if err != nil || len(all) != 6 {
		t.Errorf("every grant = %d, %v", len(all), err)
	}
}

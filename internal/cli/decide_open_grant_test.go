package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
	"github.com/ndrwl/guildhall/internal/store"
)

// grantQuestion are decide open's flags on task t1 of d asking whether the planner may fetch, with extra flags,
// such as the grant's, after them.
func (d runDirector) grantQuestion(extra ...string) []string {
	return append([]string{"decide", "open", "--store", d.store, "--repo", "app", "--task", "t1",
		"--question", "May the planner fetch the upstream spec?", "--option", "fetch", "--option", "skip"}, extra...)
}

// fetchGrant are the grant flags proposing grant Bash(curl:*) on plan for option fetch, with d's config.
func (d runDirector) fetchGrant(extra ...string) []string {
	return append([]string{"--config", d.config, "--grant-option", "fetch", "--grant-step", "plan", "--grant-tool", "Bash(curl:*)",
		"--grant-reason", "The spec is online."}, extra...)
}

// decisionIDs are the ids of task t1's decisions in d's store.
func (d runDirector) decisionIDs(t *testing.T) []string {
	t.Helper()
	_, decisions := storeWrites(t, d.store, "app", "t1")
	var ids []string
	for _, dec := range decisions {
		ids = append(ids, dec.ID)
	}
	return ids
}

// TestDecideOpenProposesAGrant: a question with the grant flags records grant g1 pending with q1 as its decision
// and fetch as its approving option, opens no grant-g1 decision and shows the exact grant in its text; a retry with
// the same grant writes nothing, one with another grant opens a new question once the first is revoked; choosing
// fetch activates g1 with its approval time, and choosing another option rejects the next.
func TestDecideOpenProposesAGrant(t *testing.T) {
	d := newRunDirectorWith(t, "claude.toml")
	open := append(d.grantQuestion("--recommend", "fetch"), d.fetchGrant()...)
	code, out, stderr := d.run(t, open...)
	if code != 0 {
		t.Fatalf("open: exit %d, stderr %s", code, stderr)
	}
	q1 := decodeJSON[shownDecision](t, out)
	wantText := "May the planner fetch the upstream spec?\n\nChoosing `fetch` also approves grant g1: step `plan`, these extra allowed tools: Bash(curl:*), until the first attempt of the step bound after approval has an outcome. Reason: The spec is online."
	if q1.ID != "q1" || q1.Question != wantText || q1.SubjectDigest != store.SubjectDigestOf(q1.Decision) {
		t.Fatalf("q1 = %+v\nwant question %q", q1, wantText)
	}
	g := d.grantStates(t)["g1"]
	if g.State != store.GrantPending || g.Decision != "q1" || g.ApprovingOption != "fetch" || g.Step != "plan" || !slices.Equal(g.Rules, []string{"Bash(curl:*)"}) || g.Until != store.GrantUntilAttempt {
		t.Fatalf("g1 = %+v", g)
	}
	if code, out, _ := d.run(t, d.grantFlags("list")...); code != 0 || !strings.Contains(out, `"decision":"q1","approving_option":"fetch"`) {
		t.Errorf("task grant list = %d %s", code, out)
	}
	if ids := d.decisionIDs(t); !slices.Equal(ids, []string{"q1"}) {
		t.Errorf("decisions = %v; want q1 alone, no grant-g1", ids)
	}
	// The recommendation is made against the question as recorded, so it is current.
	if code, out, _ := d.run(t, "decide", "show", "--store", d.store, "--repo", "app", "--task", "t1", "--decision", "q1"); code != 0 || !strings.Contains(out, "also approves grant g1") {
		t.Errorf("decide show = %d %s", code, out)
	}
	rec, err := withStore(d.store, true, func(s *store.Store) (store.Recommendation, error) { return s.DecisionRecommendation("app", "t1", "q1") })
	if err != nil || !rec.Current(q1.Decision) || rec.Option != "fetch" {
		t.Errorf("recommendation = %+v, %v; want current fetch", rec, err)
	}

	receipts, decisions := storeWrites(t, d.store, "app", "t1")
	if code, again, stderr := d.run(t, open...); code != 0 || again != out {
		t.Errorf("retry: exit %d, %q, stderr %s; want q1 again", code, again, stderr)
	}
	assertNoWrite(t, d.store, "app", "t1", receipts, decisions)

	// The same question with another grant is another question, refused while g1 is pending on the same step.
	other := append(d.grantQuestion(), d.fetchGrant("--grant-tool", "Bash(wget:*)")...)
	if code, _, stderr := d.run(t, other...); code != 1 || !strings.Contains(stderr, "grant g1") || !strings.Contains(stderr, "revoke it first") {
		t.Errorf("another grant while g1 is pending: exit %d, stderr %s", code, stderr)
	}
	assertNoWrite(t, d.store, "app", "t1", receipts, decisions)
	// task grant create of the same grant names the proposal, and a different one is refused while it is pending.
	if code, _, stderr := d.run(t, d.grantFlags("create", "--step", "plan", "--tool", "Bash(curl:*)", "--reason", "The spec is online.")...); code != 1 ||
		!strings.Contains(stderr, "grant g1") || !strings.Contains(stderr, "question q1") {
		t.Errorf("create of the proposed grant: exit %d, stderr %s", code, stderr)
	}
	if code, _, stderr := d.run(t, d.grantFlags("create", "--step", "plan", "--tool", "Bash(go:*)", "--reason", "Go.")...); code != 1 || !strings.Contains(stderr, "revoke it first") {
		t.Errorf("a differing create: exit %d, stderr %s", code, stderr)
	}
	assertNoWrite(t, d.store, "app", "t1", receipts, decisions)

	d.answerGrant(t, "q1", "fetch\nGo ahead.")
	g = d.grantStates(t)["g1"]
	if g.State != store.GrantActive || g.ApprovedAt == nil {
		t.Fatalf("g1 after fetch = %+v", g)
	}
	if ids := d.decisionIDs(t); !slices.Equal(ids, []string{"q1"}) {
		t.Errorf("decisions after the answer = %v; want no grant-g1", ids)
	}

	if code, _, stderr := d.run(t, d.grantFlags("revoke", "--grant", "g1", "--reason", "Fetched.")...); code != 0 {
		t.Fatalf("revoke: exit %d, stderr %s", code, stderr)
	}
	code, out, stderr = d.run(t, other...)
	if q2 := decodeJSON[store.Decision](t, out); code != 0 || q2.ID != "q2" || !strings.Contains(q2.Question, "grant g2") || !strings.Contains(q2.Question, "Bash(curl:*), Bash(wget:*)") {
		t.Fatalf("another grant after the revoke: exit %d, %+v, stderr %s", code, q2, stderr)
	}
	d.answerGrant(t, "q2", "skip")
	if g := d.grantStates(t)["g2"]; g.State != store.GrantRejected || g.ApprovedAt != nil || g.Decision != "q2" {
		t.Errorf("g2 after skip = %+v", g)
	}
	// A differing create opens its own grant-g3 decision once no proposal is open on the step.
	if code, out, stderr := d.run(t, d.grantFlags("create", "--step", "plan", "--tool", "Bash(go:*)", "--reason", "Go.")...); code != 0 || !strings.Contains(out, "task-grant:app:t1:g3") {
		t.Errorf("create after the answers: exit %d, %q, stderr %s", code, out, stderr)
	}
	if ids := d.decisionIDs(t); !slices.Contains(ids, "grant-g3") {
		t.Errorf("decisions = %v; want grant-g3", ids)
	}
}

// TestDecideOpenGrantUsage: grant flags that do not name one grant are usage errors, as is --config with none;
// without grant flags no config is needed; each writes nothing.
func TestDecideOpenGrantUsage(t *testing.T) {
	d := newRunDirectorWith(t, "claude.toml")
	before := lastSequence(t, d.store)
	cfg := []string{"--config", d.config}
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no option":       {d.grantQuestion(append(cfg, "--grant-step", "plan", "--grant-tool", "Bash(curl:*)", "--grant-reason", "Why.")...), "--grant-option"},
		"no step":         {d.grantQuestion(append(cfg, "--grant-option", "fetch", "--grant-tool", "Bash(curl:*)", "--grant-reason", "Why.")...), "--grant-step"},
		"no tool":         {d.grantQuestion(append(cfg, "--grant-option", "fetch", "--grant-step", "plan", "--grant-reason", "Why.")...), "--grant-tool"},
		"no reason":       {d.grantQuestion(append(cfg, "--grant-option", "fetch", "--grant-step", "plan", "--grant-tool", "Bash(curl:*)")...), "--grant-reason"},
		"until alone":     {d.grantQuestion("--grant-until", "done"), "--grant-option"},
		"unknown option":  {d.grantQuestion(d.fetchGrant("--grant-option", "maybe")...), `--grant-option "maybe" must be one of`},
		"bad until":       {d.grantQuestion(d.fetchGrant("--grant-until", "forever")...), "--grant-until"},
		"all for attempt": {d.grantQuestion(d.fetchGrant("--grant-step", "all")...), "--grant-until done"},
		"no config":       {d.grantQuestion("--grant-option", "fetch", "--grant-step", "plan", "--grant-tool", "Bash(curl:*)", "--grant-reason", "Why."), "requires --config"},
		"config alone":    {d.grantQuestion(cfg...), "--config applies"},
	} {
		if code, out, stderr := d.run(t, tc.args...); code != 2 || out != "" || !strings.Contains(stderr, tc.want) || !strings.Contains(stderr, "usage:") {
			t.Errorf("%s: exit %d, %q, stderr %s; want 2 naming %q", name, code, out, stderr, tc.want)
		}
	}
	if lastSequence(t, d.store) != before {
		t.Errorf("a usage error wrote")
	}
	if code, _, stderr := d.run(t, d.grantQuestion()...); code != 0 {
		t.Errorf("a question with no grant and no config: exit %d, stderr %s", code, stderr)
	}
}

// TestDecideOpenGrantRefusals: a grant the store would refuse, for a step that runs no claude profile or with a
// forbidden rule, or overlapping a grant made by task grant create, refuses the whole open, writing neither the
// question nor the grant.
func TestDecideOpenGrantRefusals(t *testing.T) {
	d := newRunDirectorWith(t, "claude.toml")
	if code, _, stderr := d.run(t, d.grantFlags("create", "--step", "plan", "--tool", "Bash(go:*)", "--reason", "Go.")...); code != 0 {
		t.Fatalf("create: exit %d, stderr %s", code, stderr)
	}
	receipts, decisions := storeWrites(t, d.store, "app", "t1")
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"fake step": {d.grantQuestion(d.fetchGrant("--grant-step", "build")...), "claude adapter"},
		"gh":        {d.grantQuestion(d.fetchGrant("--grant-tool", "Bash(gh:*)")...), "never allows"},
		"overlap":   {d.grantQuestion(d.fetchGrant()...), "grant g1"},
	} {
		if code, out, stderr := d.run(t, tc.args...); code != 1 || out != "" || !strings.Contains(stderr, tc.want) {
			t.Errorf("%s: exit %d, %q, stderr %s; want 1 naming %q", name, code, out, stderr, tc.want)
		}
	}
	assertNoWrite(t, d.store, "app", "t1", receipts, decisions)
	if gs := d.grantStates(t); len(gs) != 1 {
		t.Errorf("grants = %+v; want g1 alone", gs)
	}
}

// TestQuestionGrantReachesTheClaudeSession is the plan's done-when: the Director asks with the grant flags through
// its copy, the Guildmaster chooses the grant option once, director status lists the grant active with q1 as its
// decision and no grant decision needs them, and the next plan attempt's Claude session is granted it.
func TestQuestionGrantReachesTheClaudeSession(t *testing.T) {
	claudetest.Install(t)
	d, copyDir := newRunCopyOn(t, "claude-straight", "claude.toml")
	if code, _, stderr := d.run(t, "decide", "open", "--copy", copyDir, "--repo", "app", "--task", "t1", "--question", "May the planner build with go?",
		"--option", "yes", "--option", "no", "--grant-option", "yes", "--grant-step", "plan", "--grant-tool", "Bash(go:*)", "--grant-reason", "The probe builds with go."); code != 0 {
		t.Fatalf("open: exit %d, stderr %s", code, stderr)
	}
	d.answerGrant(t, "q1", "yes")
	code, out, stderr := d.run(t, "director", "status", "--copy", copyDir, "--json")
	if code != 0 {
		t.Fatalf("status: exit %d, stderr %s", code, stderr)
	}
	var st struct {
		Grants  []statusGrant `json:"grants"`
		NeedsMe []needsMeItem `json:"needs_me"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	if g := st.Grants; len(g) != 1 || g[0].Grant != "g1" || g[0].Decision != "q1" || g[0].ApprovingOption != "yes" || g[0].ApprovedAt == nil {
		t.Fatalf("status grants = %+v", g)
	}
	for _, n := range st.NeedsMe {
		if strings.Contains(n.Reason, "grant-g1") || strings.Contains(n.Command, "grant-g1") {
			t.Errorf("needs_me names a grant decision: %+v", n)
		}
	}
	if code, out, stderr := d.run(t, "task", "start", "--copy", copyDir, "--repo", "app", "t1"); code != 0 {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	d.waitResult(t, "plan", 1)
	if got, err := claudetest.Granted(d.result("plan", 1)); err != nil || !slices.Contains(got, "Bash(go:*)") {
		t.Errorf("plan-1 was granted %q, %v; want Bash(go:*) among them", got, err)
	}
	if b, err := os.ReadFile(filepath.Join(d.state, "documents", "tasks", "app", "t1", "plan-1.inputs.json")); err != nil || !strings.Contains(string(b), `"grant":{"id":"g1","rules":["Bash(go:*)"]}`) {
		t.Errorf("plan-1 binding = %s, %v", b, err)
	}
}

// TestWebAnswerApprovesAProposedGrant: the task page says the grant option also approves the grant, and choosing
// it there activates the grant, the answer recorded as the page's.
func TestWebAnswerApprovesAProposedGrant(t *testing.T) {
	h, path, _ := pageFixture(t)
	q := store.Decision{Repository: "app", TaskID: "a-approve", ID: "q1", Kind: store.DecisionQuestion, Options: []string{"fetch", "skip"}, Question: "May the planner fetch?"}
	g := store.TaskGrant{Repository: "app", TaskID: "a-approve", ID: "g1", Step: "plan", Rules: []string{"Bash(curl:*)"}, Reason: "The spec is online.",
		Until: store.GrantUntilAttempt, ApprovingOption: "fetch"}
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		return s.OpenQuestionGranting("decision-open:app:a-approve:q1", q, g, []string{"plan"}, nil, nil)
	}); err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRequest(http.MethodGet, "/app/a-approve", nil)
	get.Host, get.RemoteAddr = pageHost, pageNode
	gw := httptest.NewRecorder()
	h.ServeHTTP(gw, get)
	if body := gw.Body.String(); !strings.Contains(body, "also approves grant g1") {
		t.Fatalf("the page does not say fetch approves g1:\n%s", body)
	}
	// A question is bound to no subject, so the page's form carries none.
	if w := postPage(t, h, "a-approve", url.Values{"decision": {"q1"}, "subject": {""}, "option": {"fetch"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("answer: %d\n%s", w.Code, w.Body)
	}
	if d := pageDecision(t, path, "a-approve", "q1"); d.Choice != "fetch" || d.AnsweredVia != store.AnsweredViaPage {
		t.Fatalf("q1 = %+v", d)
	}
	gs, err := withStore(path, true, func(s *store.Store) ([]store.TaskGrantStatus, error) { return s.TaskGrantStatuses("app", "a-approve") })
	if err != nil || len(gs) != 1 || gs[0].State != store.GrantActive || gs[0].ApprovedAt == nil {
		t.Fatalf("grants = %+v, %v", gs, err)
	}
}

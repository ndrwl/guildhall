package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// grantFlags are the flags of a task grant command on task t-1 of d.
func (d runDirector) grantFlags(sub string, extra ...string) []string {
	args := []string{"task", "grant", sub, "--store", d.store, "--repo", "app", "--task", "t-1"}
	if sub == "create" {
		args = append(args, "--config", d.config)
	}
	return append(args, extra...)
}

// answerGrant records words as the answer to decision id of task t-1 through decide answer.
func (d runDirector) answerGrant(t *testing.T, id, words string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "answer")
	writeFile(t, file, words)
	if code, _, stderr := d.run(t, "decide", "answer", "--store", d.store, "--repo", "app", "--task", "t-1", "--decision", id, "--answer-file", file); code != 0 {
		t.Fatalf("decide answer %s: exit %d, stderr %s", id, code, stderr)
	}
}

func (d runDirector) grantStates(t *testing.T) map[string]store.TaskGrantStatus {
	t.Helper()
	code, out, stderr := d.run(t, d.grantFlags("list")...)
	if code != 0 {
		t.Fatalf("task grant list: exit %d, stderr %s", code, stderr)
	}
	var list []store.TaskGrantStatus
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("task grant list printed %q: %v", out, err)
	}
	states := map[string]store.TaskGrantStatus{}
	for _, g := range list {
		states[g.ID] = g
	}
	return states
}

// TestTaskGrantCommands: create records a grant and opens its decision, replays, and refuses a second open grant,
// a step that runs no claude profile and the forbidden rules; decide answer approve activates it and reject does
// not; revoke records once; list shows every derived state.
func TestTaskGrantCommands(t *testing.T) {
	d := newRunDirectorWith(t, "claude.toml")
	create := d.grantFlags("create", "--step", "plan", "--tool", "Bash(go:*)", "--tool", "Bash(claude:*)", "--reason", "The probe runs go and claude.")
	code, out, stderr := d.run(t, create...)
	if code != 0 {
		t.Fatalf("create: exit %d, stderr %s", code, stderr)
	}
	var rc store.Receipt
	if err := json.Unmarshal([]byte(out), &rc); err != nil || rc.MutationID != "task-grant:app:t-1:g1" || rc.Record != "task-grant/g1" || strings.Count(out, "\n") != 1 {
		t.Fatalf("create printed %q (%v)", out, err)
	}
	before := lastSequence(t, d.store)
	if code, again, stderr := d.run(t, create...); code != 0 || again != out || lastSequence(t, d.store) != before {
		t.Errorf("replay: exit %d, %q, stderr %s; want the original receipt and no write", code, again, stderr)
	}
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"second open grant":  {d.grantFlags("create", "--step", "plan", "--tool", "Bash(herdr:*)", "--reason", "More."), "grant g1"},
		"every step overlap": {d.grantFlags("create", "--step", "all", "--until", "done", "--tool", "Bash(herdr:*)", "--reason", "More."), "grant g1"},
		"fake step":          {d.grantFlags("create", "--step", "build", "--tool", "Bash(go:*)", "--reason", "Build."), "claude adapter"},
		"unknown step":       {d.grantFlags("create", "--step", "nope", "--tool", "Bash(go:*)", "--reason", "Nope."), "claude adapter"},
		"gh":                 {d.grantFlags("create", "--step", "plan", "--tool", "Bash(gh:*)", "--reason", "Forge."), "never allows"},
		"git push":           {d.grantFlags("create", "--step", "plan", "--tool", "Bash(git push:*)", "--reason", "Push."), "git push"},
		"blanket":            {d.grantFlags("create", "--step", "plan", "--tool", "Bash(*)", "--reason", "All."), "every shell command"},
	} {
		if code, out, stderr := d.run(t, tc.args...); code != 1 || out != "" || !strings.Contains(stderr, tc.want) {
			t.Errorf("%s: exit %d, %q, stderr %s; want 1 naming %q", name, code, out, stderr, tc.want)
		}
	}
	if lastSequence(t, d.store) != before {
		t.Errorf("a refused create wrote")
	}

	code, out, _ = d.run(t, "decide", "list", "--store", d.store)
	if code != 0 || !strings.Contains(out, `"id":"grant-g1","kind":"grant","options":["approve","reject"]`) {
		t.Errorf("decide list = %s", out)
	}
	if g := d.grantStates(t)["g1"]; g.State != store.GrantPending {
		t.Errorf("before the answer: %+v", g)
	}
	d.answerGrant(t, "grant-g1", "approve\nOnly for the probe.")
	g := d.grantStates(t)["g1"]
	if g.State != store.GrantActive || g.ApprovedAt == nil || !slices.Equal(g.Rules, []string{"Bash(go:*)", "Bash(claude:*)"}) || g.Until != store.GrantUntilAttempt {
		t.Errorf("after approve: %+v", g)
	}

	revoke := d.grantFlags("revoke", "--grant", "g1", "--reason", "No longer needed.")
	code, out, stderr = d.run(t, revoke...)
	if code != 0 || !strings.Contains(out, `"mutation_id":"task-grant-revoke:app:t-1:g1"`) {
		t.Fatalf("revoke: exit %d, %q, stderr %s", code, out, stderr)
	}
	if code, again, _ := d.run(t, revoke...); code != 0 || again != out {
		t.Errorf("revoke replay: exit %d, %q", code, again)
	}
	if code, _, stderr := d.run(t, d.grantFlags("revoke", "--grant", "g1", "--reason", "Again.")...); code != 1 || !strings.Contains(stderr, "already used for a different request") {
		t.Errorf("second revoke: exit %d, stderr %s", code, stderr)
	}
	if code, _, stderr := d.run(t, d.grantFlags("revoke", "--grant", "g7", "--reason", "None.")...); code != 1 || !strings.Contains(stderr, "not found") {
		t.Errorf("unknown grant: exit %d, stderr %s", code, stderr)
	}

	if code, out, stderr := d.run(t, d.grantFlags("create", "--step", "all", "--until", "done", "--tool", "Bash(herdr:*)", "--reason", "Herdr everywhere.")...); code != 0 || !strings.Contains(out, "task-grant:app:t-1:g2") {
		t.Fatalf("create after the revoke: exit %d, %q, stderr %s", code, out, stderr)
	}
	d.answerGrant(t, "grant-g2", "reject")
	states := d.grantStates(t)
	if states["g1"].State != store.GrantRevoked || states["g2"].State != store.GrantRejected || states["g2"].ApprovedAt != nil || len(states) != 2 {
		t.Errorf("states = %+v", states)
	}
	// list without --task reads every repository's grants.
	if code, out, _ := d.run(t, "task", "grant", "list", "--store", d.store); code != 0 || strings.Count(out, `"id":"g`) != 2 {
		t.Errorf("list of every grant: exit %d, %s", code, out)
	}
}

// TestTaskGrantUsage: usage errors exit 2 and write nothing.
func TestTaskGrantUsage(t *testing.T) {
	d := newRunDirectorWith(t, "claude.toml")
	before := lastSequence(t, d.store)
	for name, args := range map[string][]string{
		"no subcommand":      {"task", "grant"},
		"unknown subcommand": {"task", "grant", "approve"},
		"no reason":          d.grantFlags("create", "--step", "plan", "--tool", "Bash(go:*)"),
		"no tool":            d.grantFlags("create", "--step", "plan", "--reason", "Why."),
		"no step":            d.grantFlags("create", "--tool", "Bash(go:*)", "--reason", "Why."),
		"bad until":          d.grantFlags("create", "--step", "plan", "--tool", "Bash(go:*)", "--reason", "Why.", "--until", "forever"),
		"all for an attempt": d.grantFlags("create", "--step", "all", "--tool", "Bash(go:*)", "--reason", "Why."),
		"grant on create":    d.grantFlags("create", "--step", "plan", "--tool", "Bash(go:*)", "--reason", "Why.", "--grant", "g1"),
		"no grant":           d.grantFlags("revoke", "--reason", "Why."),
		"tool on revoke":     d.grantFlags("revoke", "--grant", "g1", "--reason", "Why.", "--tool", "Bash(go:*)"),
		"list task no repo":  {"task", "grant", "list", "--store", d.store, "--task", "t-1"},
		"argument":           append(d.grantFlags("list"), "t-1"),
		"bad task id":        {"task", "grant", "list", "--store", d.store, "--repo", "app", "--task", "T1"},
	} {
		if code, out, stderr := d.run(t, args...); code != 2 || out != "" || !strings.Contains(stderr, "usage:") {
			t.Errorf("%s: exit %d, %q, stderr %s; want 2 with the usage", name, code, out, stderr)
		}
	}
	if lastSequence(t, d.store) != before {
		t.Errorf("a usage error wrote")
	}
}

// TestTaskGrantOnABugTask: create checks a step against the workflow the task bound, the shipped bug workflow for a
// bug task, not the repository's feature workflow, and takes the step's role profile resolved at use.
func TestTaskGrantOnABugTask(t *testing.T) {
	d := newRunDirectorWith(t, "claude.toml")
	if code, stderr := kindTask(t, d.store, d.config, "t-2", "--kind", "bug"); code != 0 {
		t.Fatalf("bug task: exit %d, stderr %s", code, stderr)
	}
	create := func(step string) (int, string, string) {
		return d.run(t, "task", "grant", "create", "--store", d.store, "--config", d.config, "--repo", "app", "--task", "t-2",
			"--step", step, "--tool", "Bash(go:*)", "--reason", "The repro needs go.")
	}
	// build is a step of the bug workflow only; its role profile is the shipped builder, which runs claude.
	if code, out, stderr := create("build"); code != 0 || !strings.Contains(out, "task-grant:app:t-2:g1") {
		t.Fatalf("create on build: exit %d, %q, stderr %s", code, out, stderr)
	}
	if code, _, stderr := create("nope"); code != 1 || !strings.Contains(stderr, "claude adapter") {
		t.Errorf("unknown step: exit %d, stderr %s", code, stderr)
	}
}

// TestTaskGrantReachesTheClaudeSession is the plan's first and second done-when: the Director creates a grant, the
// Guildmaster's approve is recorded, and the task's next attempt of the step is bound with it and its Claude session
// granted it; director status lists the grant until that attempt has an outcome, and then no longer, with nothing
// written to config.toml.
func TestTaskGrantReachesTheClaudeSession(t *testing.T) {
	claudetest.Install(t)
	d, copyDir := newRunCopyOn(t, "claude-straight", "claude.toml")
	config, err := os.ReadFile(d.config)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := d.run(t, "task", "grant", "create", "--copy", copyDir, "--repo", "app", "--task", "t-1", "--step", "plan",
		"--tool", "Bash(go:*)", "--reason", "The probe builds with go."); code != 0 {
		t.Fatalf("create: exit %d, stderr %s", code, stderr)
	}
	d.answerGrant(t, "grant-g1", "approve")
	activeGrants := func() []statusGrant {
		t.Helper()
		code, out, stderr := d.run(t, "director", "status", "--copy", copyDir, "--json")
		if code != 0 {
			t.Fatalf("status: exit %d, stderr %s", code, stderr)
		}
		var st struct {
			Grants []statusGrant `json:"grants"`
		}
		if err := json.Unmarshal([]byte(out), &st); err != nil {
			t.Fatal(err)
		}
		return st.Grants
	}
	if g := activeGrants(); len(g) != 1 || g[0].Grant != "g1" || g[0].Step != "plan" || len(g[0].Bound) != 0 || g[0].ApprovedAt == nil {
		t.Fatalf("status grants before the attempt = %+v", g)
	}

	if code, out, stderr := d.run(t, "task", "start", "--copy", copyDir, "--repo", "app", "t-1"); code != 0 {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	d.waitResult(t, "plan", 1)
	want := append(slices.Clone(claude.ShippedDefaultGrants["planner"]), "Bash(go:*)")
	if got, err := claudetest.Granted(d.result("plan", 1)); err != nil || !slices.Equal(got, want) {
		t.Errorf("plan-1 was granted %q, %v; want %q", got, err, want)
	}
	binding, err := os.ReadFile(filepath.Join(d.state, "documents", "tasks", "app", "t-1", "plan-1.inputs.json"))
	if err != nil || !strings.Contains(string(binding), `"grant":{"id":"g1","rules":["Bash(go:*)"]}`) {
		t.Errorf("plan-1 binding = %s, %v", binding, err)
	}
	for end := time.Now().Add(testenv.Ceiling(time.Minute)); ; time.Sleep(50 * time.Millisecond) {
		if g := activeGrants(); len(g) == 0 {
			break
		} else if time.Now().After(end) {
			t.Fatalf("status still lists %+v after plan-1 finished", g)
		}
	}
	if g := d.grantStates(t)["g1"]; g.State != store.GrantExpired || !slices.Equal(g.Bound, []store.GrantUse{{Step: "plan", Attempt: 1}}) {
		t.Errorf("grant after plan-1 = %+v", g)
	}
	if after, err := os.ReadFile(d.config); err != nil || string(after) != string(config) {
		t.Errorf("config.toml changed: %v", err)
	}
}

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
	"github.com/ndrwl/guildhall/skills"
)

// acceptSpec is a one-slice plan with no open questions that touches nothing sensitive, about 120 lines in 4 files.
const acceptSpec = "# Sort the task list\n\n## Summary\n\n" +
	"- **Goal:** Sort the task list by id.\n" +
	"- **Size:** about 120 non-test lines in 4 files.\n" +
	"- **Touches:** none.\n" +
	"- **Open questions:** None.\n\n" +
	"## Goal\n\nSort it.\n\n## Open questions\n\nNone.\n"

// acceptCopy is the dogfood copy with app's director_accept_lines and director_accept_files set to lines and files,
// its task t1 run unattended up to accept-1, the plan's spec being spec; plan's later attempts, up to rounds, answer
// spec too.
func acceptCopy(t *testing.T, spec string, lines, files int) runDirector {
	return acceptCopyRounds(t, spec, lines, files, 1)
}

// acceptCopyRounds is acceptCopy whose scenario answers plan attempts 1 to rounds.
func acceptCopyRounds(t *testing.T, spec string, lines, files, rounds int) runDirector {
	t.Helper()
	d, _, _ := newDogfoodCopy(t, forgetest.NewFake())
	// The unattended grill's prompt names its skill, so the release skills are installed.
	d.installSkills = skills.Install
	conf, err := os.ReadFile(d.config)
	if err != nil {
		t.Fatal(err)
	}
	keys := []byte("workflow = \"fake-dogfood\"\ndirector_accept_lines = " + strconv.Itoa(lines) + "\ndirector_accept_files = " + strconv.Itoa(files) + "\n")
	writeFile(t, d.config, string(bytes.Replace(conf, []byte("workflow = \"fake-dogfood\"\n"), keys, 1)))
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.SetGrillMode("grill-mode:t1", "app", "t1", workflow.GrillUnattended, "Small and agreed.", store.GrillModeByDirector, 0)
	}); err != nil {
		t.Fatal(err)
	}
	respond := func(file, text string) fake.Respond {
		return fake.Respond{Status: adapter.StatusSucceeded, Summary: "written", Files: []fake.ScenarioFile{{Path: file, Text: text}}}
	}
	scenario := fake.Scenario{Schema: fake.ScenarioSchema, Launches: []fake.ScenarioLaunch{
		{Step: "grill", Attempt: 1, Respond: respond("notes.md", "# Grill notes\n\n## Questions\n\nNone.\n")},
		{Step: "research", Attempt: 1, Respond: respond("notes.md", "# Research notes\n\nNone.\n")},
	}}
	for n := 1; n <= rounds; n++ {
		scenario.Launches = append(scenario.Launches, fake.ScenarioLaunch{Step: "plan", Attempt: n, Respond: respond("spec.md", spec)})
	}
	path := filepath.Join(t.TempDir(), "scenario.json")
	data, err := json.Marshal(scenario)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
	t.Setenv(fake.ScenarioEnv, path)
	step := func(want string, args ...string) {
		t.Helper()
		code, out, stderr := d.run(t, args...)
		if code != 0 || strings.TrimSpace(out) != want {
			t.Fatalf("guildhall %q: exit %d, %q, stderr %s; want %s", args, code, out, stderr, want)
		}
	}
	done := func(name, want string) {
		t.Helper()
		d.waitResult(t, name, 1)
		step(want, append([]string{"step", "done"}, d.flags("--task", "t1", "--step", name, "--attempt", "1", "--result", d.result(name, 1))...)...)
	}
	step(`{"wait":"attempt","step":"grill","attempt":1}`, append([]string{"task", "start"}, d.flags("t1")...)...)
	done("grill", `{"wait":"attempt","step":"research","attempt":1}`)
	done("research", `{"wait":"attempt","step":"plan","attempt":1}`)
	done("plan", `{"wait":"decision","step":"accept","decision":"accept-1"}`)
	return d
}

// accept runs decide accept --director on accept-1 of d's task t1 with reason.
func (d runDirector) accept(t *testing.T, id, reason string) (int, string, string) {
	t.Helper()
	return d.run(t, "decide", "accept", "app/t1", id, "--director", "--reason", reason, "--store", d.store, "--config", d.config, "--state", d.state)
}

// TestDecideAcceptDirector: on a repository with the keys set, decide accept --director records accept-1 approved
// with the reason as its note, AnsweredVia director and a director conversation entry carrying the reason; a retry
// replays, decide answer's words then conflict, and the task page and Clerk say it was accepted by the Director.
// Starting the task with the decision then runs build.
func TestDecideAcceptDirector(t *testing.T) {
	d := acceptCopy(t, acceptSpec, 200, 8)
	const reason = "One slice, 120 lines in 4 files, touches none."
	code, out, stderr := d.accept(t, "accept-1", reason)
	if code != 0 {
		t.Fatalf("decide accept: exit %d, %s", code, stderr)
	}
	if code, again, stderr := d.accept(t, "accept-1", reason); code != 0 || again != out {
		t.Errorf("retry: exit %d, %q, stderr %s; want the first receipt %q", code, again, stderr, out)
	}
	dec, err := withStore(d.store, true, func(s *store.Store) (store.Decision, error) { return findDecision(s, "app", "t1", "accept-1") })
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Answered || dec.Choice != "approved" || dec.Note != reason || dec.AnsweredVia != store.AnsweredViaDirector || dec.AnsweredBy != "" {
		t.Errorf("accept-1 = %+v, want approved by the Director with the reason", dec)
	}
	conv, err := withStore(d.store, true, func(s *store.Store) ([]store.ConversationEntry, error) { return s.Conversation("app", "t1") })
	if err != nil {
		t.Fatal(err)
	}
	if len(conv) != 1 || conv[0].Author != store.ConversationDirector || conv[0].Tag != "accept-1" || !strings.Contains(conv[0].Text, reason) {
		t.Errorf("conversation = %+v, want one director entry with the reason", conv)
	}
	file := filepath.Join(t.TempDir(), "answer")
	writeFile(t, file, "approved\n")
	if code, _, stderr := d.run(t, "decide", "answer", "--store", d.store, "--repo", "app", "--task", "t1", "--decision", "accept-1", "--answer-file", file); code != 1 || !strings.Contains(stderr, "different request") {
		t.Errorf("decide answer after the Director's: exit %d, stderr %s; want a conflict", code, stderr)
	}
	if got := webAnswered(dec); got != "approved (accepted by the Director)" {
		t.Errorf("webAnswered = %q", got)
	}
	if got := webAnswerWhere(dec); !strings.HasPrefix(got, "accepted by the Director") {
		t.Errorf("webAnswerWhere = %q", got)
	}
	if code, page, stderr := d.run(t, "clerk", "inspect", "app/t1", "--copy", markCopy(t, filepath.Dir(d.config))); code != 0 || !strings.Contains(page, "accepted by the Director: One slice") {
		t.Errorf("clerk inspect: exit %d, %s %s; want accepted by the Director with the reason", code, page, stderr)
	}
	code, feed, stderr := d.run(t, "director", "status", "--json", "--store", d.store, "--state", d.state, "--config", d.config)
	var status directorStatus
	if code != 0 || json.Unmarshal([]byte(feed), &status) != nil {
		t.Fatalf("director status: exit %d, stderr %s", code, stderr)
	}
	i := slices.IndexFunc(status.Tasks, func(st statusTask) bool { return st.Repository == "app" && st.ID == "t1" })
	if i < 0 || status.Tasks[i].DirectorAccepted == nil ||
		*status.Tasks[i].DirectorAccepted != (statusDirectorAccepted{Decision: "accept-1", Text: "accepted by the Director", Reason: reason}) {
		t.Errorf("director status tasks = %+v, want t1 accepted by the Director with the reason", status.Tasks)
	}
	if slices.ContainsFunc(status.NeedsMe, func(n needsMeItem) bool { return n.Decision == "accept-1" }) {
		t.Errorf("needs_me = %+v, want accept-1 gone from it", status.NeedsMe)
	}
	d.waitResult(t, "plan", 1)
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("--decision", "accept-1", "t1")...)...); code != 0 || strings.TrimSpace(out) != `{"wait":"attempt","step":"build","attempt":1}` {
		t.Errorf("task start after the Director's acceptance: exit %d, %q, stderr %s; want build", code, out, stderr)
	}
}

// TestDecideAcceptDirectorRefused: an ineligible plan, a decision the setting does not cover and usage errors are
// refused, each leaving accept-1 open.
func TestDecideAcceptDirectorRefused(t *testing.T) {
	d := acceptCopy(t, acceptSpec, 100, 8)
	open := func() {
		t.Helper()
		dec, err := withStore(d.store, true, func(s *store.Store) (store.Decision, error) { return findDecision(s, "app", "t1", "accept-1") })
		if err != nil || !dec.Open() {
			t.Fatalf("accept-1 = %+v, %v; want it still open", dec, err)
		}
	}
	if code, _, stderr := d.accept(t, "accept-1", "Small."); code != 1 || !strings.Contains(stderr, "stays with the Guildmaster") || !strings.Contains(stderr, "over director_accept_lines = 100") {
		t.Errorf("too large: exit %d, stderr %s", code, stderr)
	}
	open()
	if code, _, stderr := d.accept(t, "accept-x1", "Small."); code != 1 || !strings.Contains(stderr, "not found") {
		t.Errorf("a decision that does not exist: exit %d, stderr %s", code, stderr)
	}
	// A question the Director opened on the task is no plan acceptance.
	code, out, stderr := d.run(t, "decide", "open", "--store", d.store, "--repo", "app", "--task", "t1", "--question", "Sort by id or title?", "--option", "id", "--option", "title")
	var q store.Decision
	if code != 0 || json.Unmarshal([]byte(out), &q) != nil || q.ID == "" {
		t.Fatalf("decide open: exit %d, %q, stderr %s", code, out, stderr)
	}
	if code, _, stderr := d.accept(t, q.ID, "Small."); code != 1 || !strings.Contains(stderr, "not an open plan acceptance") {
		t.Errorf("a question decision: exit %d, stderr %s", code, stderr)
	}
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"no --director", []string{"decide", "accept", "app/t1", "accept-1", "--reason", "x", "--store", d.store, "--config", d.config, "--state", d.state}, "requires --director"},
		{"no --reason", []string{"decide", "accept", "app/t1", "accept-1", "--director", "--store", d.store, "--config", d.config, "--state", d.state}, "requires --reason"},
		{"one argument", []string{"decide", "accept", "app/t1", "--director", "--reason", "x", "--store", d.store, "--config", d.config, "--state", d.state}, "takes <repo>/<task>"},
	} {
		if code, _, stderr := d.run(t, c.args...); code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d, stderr %s; want %q", c.name, code, stderr, c.want)
		}
	}
	open()
}

// TestDecideAcceptDirectorLoopExit: after accept's max of three loops back, the fourth changes_requested opens the
// loop exit accept-x4, which decide accept --director refuses and leaves open, as it refuses to answer accept-1 over
// the Guildmaster's changes_requested.
func TestDecideAcceptDirectorLoopExit(t *testing.T) {
	const rounds = 4
	d := acceptCopyRounds(t, acceptSpec, 200, 8, rounds)
	file := filepath.Join(t.TempDir(), "answer")
	writeFile(t, file, "changes_requested\nSplit it.\n")
	for n := 1; n <= rounds; n++ {
		id := "accept-" + strconv.Itoa(n)
		if code, _, stderr := d.run(t, "decide", "answer", "--store", d.store, "--repo", "app", "--task", "t1", "--decision", id, "--answer-file", file); code != 0 {
			t.Fatalf("decide answer %s: exit %d, %s", id, code, stderr)
		}
		code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("--decision", id, "t1")...)...)
		if n == rounds {
			if code != 0 || strings.TrimSpace(out) != `{"wait":"decision","step":"accept","decision":"accept-x4"}` {
				t.Fatalf("task start after %s: exit %d, %q, stderr %s; want accept-x4", id, code, out, stderr)
			}
			break
		}
		if want := `{"wait":"attempt","step":"plan","attempt":` + strconv.Itoa(n+1) + `}`; code != 0 || strings.TrimSpace(out) != want {
			t.Fatalf("task start after %s: exit %d, %q, stderr %s; want %s", id, code, out, stderr, want)
		}
		d.waitResult(t, "plan", n+1)
		want := `{"wait":"decision","step":"accept","decision":"accept-` + strconv.Itoa(n+1) + `"}`
		if code, out, stderr := d.run(t, append([]string{"step", "done"}, d.flags("--task", "t1", "--step", "plan", "--attempt", strconv.Itoa(n+1), "--result", d.result("plan", n+1))...)...); code != 0 || strings.TrimSpace(out) != want {
			t.Fatalf("plan %d done: exit %d, %q, stderr %s; want %s", n+1, code, out, stderr, want)
		}
	}
	if code, _, stderr := d.accept(t, "accept-x4", "Small."); code != 1 || !strings.Contains(stderr, "not an open plan acceptance") {
		t.Errorf("loop exit: exit %d, stderr %s; want it refused", code, stderr)
	}
	dec, err := withStore(d.store, true, func(s *store.Store) (store.Decision, error) { return findDecision(s, "app", "t1", "accept-x4") })
	if err != nil || !dec.Open() || dec.Kind != store.DecisionLoopExit {
		t.Errorf("accept-x4 = %+v, %v; want an open loop exit", dec, err)
	}
	if code, _, stderr := d.accept(t, "accept-1", "Small."); code != 1 || !strings.Contains(stderr, "different request") {
		t.Errorf("accept-1 the Guildmaster answered: exit %d, stderr %s; want a conflict", code, stderr)
	}
}

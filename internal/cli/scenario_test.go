package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

var updateScenario = flag.Bool("update-scenario", false, "rewrite testdata/scenario's golden plan prompt from TestScenarioDogfoodToDeliver's run")

// scenarioGolden is the plan prompt TestScenarioDogfoodToDeliver compares byte for byte.
var scenarioGolden = filepath.Join("testdata", "scenario", "plan-1.golden.md")

// newDogfoodCopy is a Director over a short-pathed copy holding testdata/run/dogfood.toml, the fake-dogfood
// workflow and a baseline of only standards/go.md, so every prompt is rendered from the release baseline's roles
// and guides. Repository app is a real repository and remote, so the build's head is its worktree's, and f is its
// forge. It returns the Director and the remote's trunk, where the task branch starts.
func newDogfoodCopy(t *testing.T, f *forgetest.Fake) (runDirector, string, string) {
	t.Helper()
	t.Setenv(roleEnv, "guildhall")
	dir, err := os.MkdirTemp("", "gs")
	if err != nil {
		t.Fatal(err)
	}
	run := filepath.Join("testdata", "run")
	repository, remote, base := gittest.Repository(t)
	conf, err := os.ReadFile(filepath.Join(run, "dogfood.toml"))
	if err != nil {
		t.Fatal(err)
	}
	conf = bytes.Replace(conf, []byte(`path = "/srv/app"`), []byte("path = '"+repository+"'"), 1)
	conf = bytes.Replace(conf, []byte(`remote = "git@github.com:example/app.git"`), []byte("remote = '"+remote+"'"), 1)
	cfg := filepath.Join(dir, "config.toml")
	writeFile(t, cfg, string(conf))
	for _, name := range []string{"workflows/fake-dogfood.json", "baseline/standards/go.md"} {
		data, err := os.ReadFile(filepath.Join(run, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, filepath.FromSlash(name))), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, filepath.FromSlash(name)), string(data))
	}
	d := runDirector{store: filepath.Join(dir, "director.store"), config: cfg, state: filepath.Join(dir, "state"), forge: f}
	s, err := store.Open(d.store, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d.stopAll(t)
		_ = os.RemoveAll(dir)
	})
	f.SetBranch(remote, "trunk", base)
	f.AddCommit(repository, base)
	f.SetChecks(remote, base, forge.Check{Name: "ci", State: forge.CheckPassed})
	// Its review names a profile the shipped default does not, which weakens the default, so it needs approval.
	approveRepo(t, d.store, cfg, "app")
	newTask(t, d.store, cfg, "app", "Sort the task list", "t-1")
	return d, remote, base
}

// TestScenarioDogfoodToDeliver drives task app/t-1 on the dogfood-shaped workflow from its grill to its merge, with
// every Artisan the fake playing a scenario file that checks each prompt rendered from the release baseline: the
// test plays the Director's grill, the plan is given the grill and research notes and matches its golden prompt
// byte for byte, the scripted build-1 fails and the Director's retry reason and its summary reach build-2's prompt,
// and the review sees its base, the standards and the finding-id wording before it approves. Publish, validate and
// deliver run in this process over the fake forge.
func TestScenarioDogfoodToDeliver(t *testing.T) {
	f := forgetest.NewFake()
	d, remote, base := newDogfoodCopy(t, f)
	const (
		notes      = "# Grill notes\n\n1. Sort by id? Yes.\n"
		research   = "# Research notes\n\nThe list is sorted in internal/list.go.\n"
		spec       = "# Spec\n\nSort the task list by id.\n"
		reason     = "Make TestSort pass: compare ids, not titles."
		buildFails = "tests failed: TestSort compares titles"
	)
	outcome := workflow.ReviewOutcome{Schema: workflow.ReviewOutcomeSchema, Reviewer: workflow.AutomatedReviewer, Verdict: workflow.VerdictApproved,
		Revisions: workflow.Revisions{Subject: workflow.Subject{CodeHead: base}, SpecDigest: string(digest.Of([]byte(spec))), StandardsRevision: "standards-1"}}
	noRetry := fake.Check{Section: "Why this is a retry", Absent: true}
	noDirectives := fake.Check{Section: "prompt", NotContains: "guildhall-fake:"}
	plan := fake.ScenarioLaunch{Step: "plan", Attempt: 1, Exact: "plan-1.golden.md",
		Checks: []fake.Check{noRetry, noDirectives,
			{Section: "Inputs", Contains: "- context: inputs/tasks/app/t-1/grill-1/notes.md ("},
			{Section: "Inputs", Contains: "- context: inputs/tasks/app/t-1/research-1/notes.md ("}},
		Respond: fake.Respond{Status: adapter.StatusSucceeded, Summary: "spec written", Files: []fake.ScenarioFile{{Path: "spec.md", Text: spec}}}}
	if *updateScenario {
		plan.Exact = ""
	}
	scenario := fake.Scenario{Schema: fake.ScenarioSchema, Launches: []fake.ScenarioLaunch{
		{Step: "research", Attempt: 1, Checks: []fake.Check{noRetry, noDirectives,
			{Section: fake.SectionHeader, Contains: "Step: research (research version 1)\nAttempt: 1\nProfile: researcher\n"},
			{Section: "Inputs", Contains: "None."}},
			Respond: fake.Respond{Status: adapter.StatusSucceeded, Summary: "research notes written", Files: []fake.ScenarioFile{{Path: "notes.md", Text: research}}}},
		plan,
		{Step: "build", Attempt: 1, Checks: []fake.Check{noRetry, noDirectives,
			{Section: "Inputs", Contains: "- spec: inputs/tasks/app/t-1/plan-1/spec.md (" + string(digest.Of([]byte(spec))) + ")"}},
			Respond: fake.Respond{Status: adapter.StatusFailed, Summary: buildFails}},
		{Step: "build", Attempt: 2, Checks: []fake.Check{noDirectives,
			{Section: "Why this is a retry", Contains: "This is a retry of attempt 1, which failed."},
			{Section: "Why this is a retry", Contains: "Director's reason:\n\n```text\n" + reason + "\n```"},
			{Section: "Why this is a retry", Contains: "Previous summary:\n\n```text\n" + buildFails + "\n```"}},
			Respond: fake.Respond{Status: adapter.StatusSucceeded, Summary: "built"}},
		{Step: "review", Attempt: 1, Checks: []fake.Check{noRetry, noDirectives,
			{Section: fake.SectionHeader, Contains: "Step: review (review version 1)\nAttempt: 1\nReview base: " + base + "\nProfile: reviewer\n"},
			{Section: "Standards", Contains: "### standards/go.md\n\n# Go\n\nRun gofmt."},
			{Section: "Stage guide", Contains: "The verdict is `approved`, `changes_requested` or `rejected`, not `APPROVE`."},
			{Section: "Required outputs", Contains: "A finding `id` is lowercase letters, digits and hyphens, starting with a letter, at most 63 characters"},
			{Section: "Required outputs", Contains: "keep a finding's id in later rounds"}},
			Respond: fake.Respond{Review: &outcome}},
	}}
	path := filepath.Join(t.TempDir(), "scenario.json")
	data, err := json.MarshalIndent(scenario, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
	if !*updateScenario {
		golden, err := os.ReadFile(scenarioGolden)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(filepath.Dir(path), "plan-1.golden.md"), string(golden))
	}
	t.Setenv(fake.ScenarioEnv, path)

	// step runs the CLI and wants its output to be want, or anything when want is empty.
	step := func(want string, args ...string) {
		t.Helper()
		code, out, stderr := d.run(t, args...)
		if code != 0 || want != "" && strings.TrimSpace(out) != want {
			t.Fatalf("guildhall %q: exit %d, %q, stderr %s; want %s", args, code, out, stderr, want)
		}
	}
	// done takes attempt n's result and wants the advance's output to be want; otherwise it names the result's
	// summary, where a failed scenario check is named.
	done := func(name string, n int, want string) {
		t.Helper()
		d.waitResult(t, name, n)
		code, out, stderr := d.run(t, append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", name, "--attempt", strconv.Itoa(n), "--result", d.result(name, n))...)...)
		if code != 0 || strings.TrimSpace(out) != want {
			r, err := adapter.ReadResult(d.result(name, n), adapter.Identity{TaskID: "t-1", AttemptID: name + "-" + strconv.Itoa(n)})
			t.Fatalf("step done %s-%d: exit %d, %q, stderr %s; want %s\nresult: %s %q (%v)", name, n, code, out, stderr, want, r.Status, r.Summary, err)
		}
	}
	answer := func(decision, words, want string) {
		t.Helper()
		file := filepath.Join(t.TempDir(), "answer")
		writeFile(t, file, words)
		step("", "decide", "answer", "--store", d.store, "--repo", "app", "--task", "t-1", "--decision", decision, "--answer-file", file)
		step(want, append([]string{"task", "start"}, d.flags("--decision", decision, "t-1")...)...)
	}

	step(`{"wait":"director-stage","step":"grill","attempt":1}`, append([]string{"task", "start"}, d.flags("t-1")...)...)
	file := filepath.Join(t.TempDir(), "agreed.md")
	writeFile(t, file, notes)
	step(`{"wait":"attempt","step":"research","attempt":1}`, append([]string{"step", "finish"}, d.flags("--task", "t-1", "--step", "grill", "--attempt", "1", "--summary", "Agreed: sort by id.", "--output", "notes="+file)...)...)
	done("research", 1, `{"wait":"attempt","step":"plan","attempt":1}`)
	done("plan", 1, `{"wait":"decision","step":"accept","decision":"accept-1"}`)
	if *updateScenario {
		prompt, err := os.ReadFile(filepath.Join(artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1}.Dir(d.state), "prompt", render.PromptFile))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, scenarioGolden, string(prompt))
	}
	answer("accept-1", "approved\n", `{"wait":"attempt","step":"build","attempt":1}`)
	done("build", 1, `{"wait":"director","step":"build","attempt":1}`)
	step(`{"wait":"attempt","step":"build","attempt":2}`, append([]string{"step", "retry"}, d.flags("--task", "t-1", "--step", "build", "--attempt", "1", "--reason", reason)...)...)
	done("build", 2, `{"wait":"attempt","step":"review","attempt":1}`)
	done("review", 1, `{"wait":"decision","step":"deliver","decision":"deliver-1"}`)
	answer("deliver-1", "merge\n", `{"wait":"done","step":"deliver"}`)

	if prs := f.PullRequests(remote); len(prs) != 1 || prs[0].Branch != "guildhall/t-1" || prs[0].Head != base || prs[0].State != forge.Merged {
		t.Errorf("pull requests = %+v, want the task branch's head merged", prs)
	}
	if task := readTask(t, d.store, "app", "t-1"); task.State != store.TaskDone {
		t.Errorf("task state = %s, want done", task.State)
	}
	fake.CheckScenarioRecords(t, path)
	var failed []string
	for _, a := range d.attempts(t) {
		if a.Outcome != store.AttemptSucceeded {
			failed = append(failed, a.Step+"-"+strconv.Itoa(a.Number)+" "+string(a.Outcome))
		}
	}
	if len(failed) != 1 || failed[0] != "build-1 failed" {
		t.Errorf("attempts not succeeded = %q, want only the scripted build-1 failure", failed)
	}
	if r, err := adapter.ReadResult(d.result("build", 1), adapter.Identity{TaskID: "t-1", AttemptID: "build-1"}); err != nil || r.Summary != buildFails {
		t.Errorf("build-1 result = %+v, %v; want the scripted failure, not a scenario mismatch", r, err)
	}
}

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/reconcile"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

func directorCmd(t *testing.T, getenv func(string) string, args ...string) (int, string, string) {
	t.Helper()
	return directorCmdPolicies(t, getenv, nil, args...)
}

// directorCmdPolicies is directorCmd reading repository policies with g; nil reads none.
func directorCmdPolicies(t *testing.T, getenv func(string) string, g PolicyGit, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	if g != nil {
		app.PolicyGit = g
	}
	app.Getenv = getenv
	app.Now = func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) }
	// A fixed build, so the feed's version block is the same on every runner.
	app.BuildInfo = testBuildInfo(nil)
	code := app.Run(append([]string{"director"}, args...))
	return code, stdout.String(), stderr.String()
}

// exitedLauncher is the identity of a process that has exited, as a killed launcher leaves in its ownership record.
func exitedLauncher(t *testing.T) store.ProcessIdentity {
	t.Helper()
	inv := t.TempDir()
	if err := artisantest.Command(inv, artisantest.Worker{Lifetime: time.Millisecond}).Run(); err != nil {
		t.Fatal(err)
	}
	entries, err := artisantest.Inventory(inv)
	if err != nil || len(entries) != 1 {
		t.Fatalf("inventory %v, %v", entries, err)
	}
	return store.ProcessIdentity(entries[0].Identity)
}

// statusCopy builds a Director copy whose store has a fixed clock advancing one minute per mutation, with one
// app task in each state: t-1 unstarted, t-2 running with no owner, t-3 failed latest with a next.json waiting
// for the Director, t-4 with an open decision, t-5 done, and t-6 running under a killed launcher whose attempt
// directory remains, as the reconcile fixtures leave it. lib/t-1 sends a question; app/t-2 has an unread
// worker-stalled message and an acknowledged note. app/t-7's plan attempt succeeded with its result file and app/t-8's
// was stopped, while each task's next.json still waits on that attempt, as a watcher whose advance failed leaves them.
// app/t-3's plan attempt reported a measured and estimated spend and its build attempt an unknown one. app/t-9's
// plan attempt is a Director stage the engine handed to the Director, with no ownership record (F46). app/t-10's
// build attempt failed and was parked, while its next.json waits for the Director on it. t-6's attempt has a heartbeat.
// app/t-11's plan attempt, on an open task, and app/t-12's, on a done one, have a failed outcome.json the store never
// recorded, as a launcher that died before FinishAttempt leaves them.
// The question woke the Director and the worker-stalled message, woken last, was queued.
func statusCopy(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "director.store")
	state := filepath.Join(dir, "state")
	next := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
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
	killed := exitedLauncher(t)
	steps := []func() (store.Receipt, error){
		func() (store.Receipt, error) { return s.CreateTask("i-1", task("app", "t-1", "Unstarted")) },
		func() (store.Receipt, error) { return s.CreateTask("i-2", task("app", "t-2", "Running")) },
		func() (store.Receipt, error) { return s.CreateTask("i-3", task("app", "t-3", "Failed latest")) },
		func() (store.Receipt, error) { return s.CreateTask("i-4", task("app", "t-4", "Decision open")) },
		func() (store.Receipt, error) { return s.CreateTask("i-5", task("app", "t-5", "Done")) },
		func() (store.Receipt, error) { return s.CreateTask("i-6", task("app", "t-6", "Killed launcher")) },
		func() (store.Receipt, error) { return s.CreateTask("i-7", task("lib", "t-1", "Other repository")) },
		func() (store.Receipt, error) { return s.StartAttempt("i-8", "app", "t-2", "plan") },
		func() (store.Receipt, error) { return s.StartAttempt("i-9", "app", "t-3", "plan") },
		func() (store.Receipt, error) {
			return s.FinishAttempt("i-10", "app", "t-3", "plan", 1, store.AttemptSucceeded)
		},
		func() (store.Receipt, error) { return s.StartAttempt("i-11", "app", "t-3", "build") },
		func() (store.Receipt, error) {
			return s.FinishAttempt("i-12", "app", "t-3", "build", 1, store.AttemptFailed)
		},
		func() (store.Receipt, error) { return s.StartAttempt("i-13", "app", "t-4", "plan") },
		func() (store.Receipt, error) {
			return s.FinishAttempt("i-14", "app", "t-4", "plan", 1, store.AttemptSucceeded)
		},
		func() (store.Receipt, error) {
			return s.OpenDecision("i-15", store.Decision{Repository: "app", TaskID: "t-4", ID: "accept-plan", Kind: store.DecisionApproval,
				Subject: &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + strings.Repeat("a", 64)},
				Options: []string{"approve", "reject"}, Question: "Accept the plan?"})
		},
		func() (store.Receipt, error) { return s.StartAttempt("i-16", "app", "t-5", "plan") },
		func() (store.Receipt, error) {
			return s.FinishAttempt("i-17", "app", "t-5", "plan", 1, store.AttemptStopped)
		},
		func() (store.Receipt, error) { return s.SetTaskState("i-18", "app", "t-5", store.TaskDone) },
		func() (store.Receipt, error) {
			return s.StartOwnedAttempt("i-19", store.Ownership{Repository: "app", TaskID: "t-6", Step: "build", LauncherIdentity: killed})
		},
		func() (store.Receipt, error) {
			return s.SendMessage(store.Message{ID: "app.t-2.worker-stalled.plan-1.abc", Recipient: store.RecipientDirector, Repository: "app", TaskID: "t-2",
				Step: "plan", Attempt: 1, Type: "worker-stalled", Text: "No output for 20m."})
		},
		func() (store.Receipt, error) {
			return s.SendMessage(store.Message{ID: "n-1", Recipient: store.RecipientDirector, Repository: "app", TaskID: "t-4", Type: "note", Text: "Read."})
		},
		func() (store.Receipt, error) {
			return s.AckMessage(store.Recipient{Role: store.RecipientDirector}, "n-1")
		},
		func() (store.Receipt, error) {
			return s.SendMessage(store.Message{ID: "q-1", Recipient: store.RecipientDirector, Repository: "lib", TaskID: "t-1", Type: "question", Text: "Which parser?",
				Sender: store.SenderArtisan})
		},
		func() (store.Receipt, error) {
			return s.StartDirectorSession(store.DirectorSession{Number: 1, State: store.DirectorRecovering, Identity: store.ProcessIdentity{PID: 4242, Token: "boot:4242"},
				Release: "v0.4.0", ConfigDigest: "sha256:" + strings.Repeat("c", 64),
				Pane: store.DirectorPane{Session: "lab", Workspace: "w1", Tab: "w1:t2", Pane: "w1:p2"}})
		},
		func() (store.Receipt, error) { return s.SetDirectorState(1, store.DirectorRunning, 24) },
		func() (store.Receipt, error) { return s.CreateTask("i-26", task("app", "t-7", "Result not taken")) },
		func() (store.Receipt, error) { return s.StartAttempt("i-27", "app", "t-7", "plan") },
		func() (store.Receipt, error) {
			return s.FinishAttempt("i-28", "app", "t-7", "plan", 1, store.AttemptSucceeded)
		},
		func() (store.Receipt, error) { return s.CreateTask("i-29", task("app", "t-8", "Stop not taken")) },
		func() (store.Receipt, error) { return s.StartAttempt("i-30", "app", "t-8", "plan") },
		func() (store.Receipt, error) {
			return s.FinishAttempt("i-31", "app", "t-8", "plan", 1, store.AttemptStopped)
		},
		func() (store.Receipt, error) {
			return s.RecordAttemptSpend("i-32", "app", "t-3", "plan", 1, store.Spend{Tokens: store.SpendMeasured, InputTokens: 1000, OutputTokens: 200,
				CacheCreationInputTokens: 300, CacheReadInputTokens: 400, Cost: store.SpendEstimated, CostUSD: 0.5})
		},
		func() (store.Receipt, error) {
			return s.RecordAttemptSpend("i-33", "app", "t-3", "build", 1, store.UnknownSpend)
		},
		func() (store.Receipt, error) {
			return s.RecordWake(store.Wake{MessageID: "q-1", SenderType: "local", Decision: store.WakeWoke})
		},
		func() (store.Receipt, error) {
			return s.RecordWake(store.Wake{MessageID: "app.t-2.worker-stalled.plan-1.abc", SenderType: "guildhall", Decision: store.WakeQueued, Reason: "runtime not running"})
		},
		func() (store.Receipt, error) { return s.CreateTask("i-34", task("app", "t-9", "Director stage")) },
		func() (store.Receipt, error) { return s.StartAttempt("i-35", "app", "t-9", "plan") },
		func() (store.Receipt, error) { return s.CreateTask("i-36", task("app", "t-10", "Parked")) },
		func() (store.Receipt, error) { return s.StartAttempt("i-37", "app", "t-10", "build") },
		func() (store.Receipt, error) {
			return s.FinishAttempt("i-38", "app", "t-10", "build", 1, store.AttemptFailed)
		},
		func() (store.Receipt, error) {
			return s.ParkAttempt("i-39", "app", "t-10", "build", 1, "Held until app/t-4 merges.", 0, nil)
		},
		func() (store.Receipt, error) { return s.CreateTask("i-40", task("app", "t-11", "Settled on disk")) },
		func() (store.Receipt, error) { return s.StartAttempt("i-41", "app", "t-11", "plan") },
		func() (store.Receipt, error) {
			return s.CreateTask("i-42", task("app", "t-12", "Settled on disk, done"))
		},
		func() (store.Receipt, error) { return s.StartAttempt("i-43", "app", "t-12", "plan") },
		func() (store.Receipt, error) { return s.SetTaskState("i-44", "app", "t-12", store.TaskDone) },
	}
	for _, step := range steps {
		if _, err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	t6 := artisan.AttemptKey{Repository: "app", TaskID: "t-6", Step: "build", Attempt: 1}
	if err := os.MkdirAll(t6.Dir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := artisan.WriteHeartbeat(t6.Dir(state), artisan.Heartbeat{Repository: "app", TaskID: "t-6", Step: "build", Attempt: 1, Sequence: 7,
		Time: time.Date(2026, 10, 22, 11, 59, 40, 0, time.UTC), WorkerState: "running", LastOutputAt: time.Date(2026, 10, 22, 11, 57, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	// The launchers of t-11 and t-12 wrote a failed outcome.json and died before the store recorded it.
	for _, task := range []string{"t-11", "t-12"} {
		dir := artisan.AttemptKey{Repository: "app", TaskID: task, Step: "plan", Attempt: 1}.Dir(state)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, artisan.OutcomeFile), `{"schema":"`+artisan.OutcomeSchema+`","state":"failed","detail":"worker exited 1"}`+"\n")
	}
	for _, task := range []string{"t-3", "t-4"} {
		if err := os.MkdirAll(filepath.Join(state, "app", task), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(state, "app", "t-3", "next.json"), `{"wait":"director","step":"build","attempt":1}`+"\n")
	writeFile(t, filepath.Join(state, "app", "t-4", "next.json"), `{"wait":"decision","step":"plan","decision":"accept-plan"}`+"\n")
	for _, task := range []string{"t-7", "t-8"} {
		if err := os.MkdirAll(artisan.AttemptKey{Repository: "app", TaskID: task, Step: "plan", Attempt: 1}.Dir(state), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(state, "app", task, "next.json"), `{"wait":"attempt","step":"plan","attempt":1}`+"\n")
	}
	writeFile(t, filepath.Join(artisan.AttemptKey{Repository: "app", TaskID: "t-7", Step: "plan", Attempt: 1}.Dir(state), artisan.ResultFile), "{}\n")
	handed := artisan.AttemptKey{Repository: "app", TaskID: "t-9", Step: "plan", Attempt: 1}.Dir(state)
	if err := os.MkdirAll(handed, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(handed, "launched.json"), `{"launcher":"director","identity":{"task_id":"t-9","attempt_id":"plan-1"}}`+"\n")
	writeFile(t, filepath.Join(state, "app", "t-9", "next.json"), `{"wait":"director-stage","step":"plan","attempt":1}`+"\n")
	if err := os.MkdirAll(filepath.Join(state, "app", "t-10"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(state, "app", "t-10", "next.json"), `{"wait":"director","step":"build","attempt":1}`+"\n")
	return dir
}

// checkIndentedGolden compares a JSON line with a golden file holding it indented, for review.
func checkIndentedGolden(t *testing.T, name, line string) {
	t.Helper()
	var b bytes.Buffer
	if err := json.Indent(&b, []byte(line), "", "  "); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, line)
	}
	checkGolden(t, name, b.String())
}

func TestDirectorStatusGolden(t *testing.T) {
	dir := statusCopy(t)
	// The copy's config gives the guard rails the status shows beside today's counts, and the quality settings.
	writeFile(t, filepath.Join(dir, "config.toml"), `schema_version = 1
mode = "director"
name = "alpha"

[local]
max_running_attempts = "3"
quality_self_review = "on"
quality_size_budget_lines = "300"

[[repository]]
id = "app"
path = "/srv/app"
remote = "git@github.com:example/app.git"
owner_machine = "workstation"

[repository.merge.requires]
guildmaster_approval = false

[[repository]]
id = "lib"
path = "/srv/lib"
remote = "git@github.com:example/lib.git"
owner_machine = "workstation"
`)
	path := filepath.Join(dir, "director.store")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// app has policies at main's commit; lib's policy file is refused.
	pol := &fakePolicyGit{shown: map[string]git.Shown{
		"/srv/app": {Commit: strings.Repeat("a", 40), Data: []byte("# Policies\n\n- Keep the up-to-date branch protection on main for now.\n- Squash merges only.\n")},
		"/srv/lib": {Commit: strings.Repeat("b", 40), Data: []byte("- One\n- One\n")},
	}}
	code, out, stderr := directorCmdPolicies(t, nil, pol, "status", "--copy", dir, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if fetched := pol.fetches(); len(fetched) != 0 {
		t.Errorf("status fetched %q; it only reads", fetched)
	}
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("status prints one JSON line: %q", out)
	}
	// The step done command names the result file under the copy, written in the golden as $COPY/....
	result, err := json.Marshal(filepath.Join(artisan.AttemptKey{Repository: "app", TaskID: "t-7", Step: "plan", Attempt: 1}.Dir(filepath.Join(dir, "state")), artisan.ResultFile))
	if err != nil {
		t.Fatal(err)
	}
	// The feed names the copy it was read from, written in the golden as $COPY.
	copyField, err := json.Marshal(dir)
	if err != nil {
		t.Fatal(err)
	}
	copyField = append([]byte(`"copy":`), append(copyField, ',')...)
	if !strings.Contains(out, string(copyField)) {
		t.Fatalf("status does not name the copy as %s: %s", copyField, out)
	}
	golden := strings.Replace(out, strings.Trim(string(result), `"`), "$COPY/state/app/t-7/plan-1/result.json", 1)
	checkIndentedGolden(t, "director-status.json", strings.Replace(golden, string(copyField), `"copy":"$COPY",`, 1))
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("status changed the store (%v)", err)
	}

	// The explicit paths read the same copy, though they name no copy, and discovery from inside the copy selects it.
	_, explicit, _ := directorCmdPolicies(t, nil, pol, "status", "--store", path, "--state", filepath.Join(dir, "state"), "--config", filepath.Join(dir, "config.toml"), "--json")
	app, stdout, _ := newTestApp(nil, nil)
	app.PolicyGit = pol
	app.Now = func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) }
	app.BuildInfo = testBuildInfo(nil)
	app.Getwd = func() (string, error) { return filepath.Join(dir, "workflows"), nil }
	app.Run([]string{"director", "status", "--json"})
	if explicit != strings.Replace(out, string(copyField), "", 1) || stdout.String() != out {
		t.Fatalf("explicit paths or discovery read differently:\n%s\n%s\n%s", out, explicit, stdout)
	}
}

// TestDirectorStatusShowsSpend: each task sums its finished attempts' spend, an attempt without a known number
// counted as unknown, and the guard rails sum the spend of the attempts started today beside the caps.
func TestDirectorStatusShowsSpend(t *testing.T) {
	dir := statusCopy(t)
	code, out, stderr := directorCmd(t, nil, "status", "--copy", dir, "--json")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	var st directorStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	spend := map[string]store.SpendTotal{}
	for _, task := range st.Tasks {
		spend[task.Repository+"/"+task.ID] = task.Spend
	}
	t3 := store.SpendTotal{Attempts: 2, TokensUnknown: 1, CostUnknown: 1, Tokens: store.SpendMeasured, InputTokens: 1000, OutputTokens: 200,
		CacheCreationInputTokens: 300, CacheReadInputTokens: 400, Cost: store.SpendEstimated, CostUSD: 0.5}
	if got := spend["app/t-3"]; got != t3 {
		t.Errorf("app/t-3 spend = %+v, want %+v", got, t3)
	}
	// A running attempt is not counted; a finished one without a record is unknown.
	if got := spend["app/t-2"]; got != store.NoSpend {
		t.Errorf("app/t-2 spend = %+v, want no attempts", got)
	}
	if got, want := spend["app/t-5"], (store.SpendTotal{Attempts: 1, TokensUnknown: 1, CostUnknown: 1, Tokens: store.SpendUnknown, Cost: store.SpendUnknown}); got != want {
		t.Errorf("app/t-5 spend = %+v, want %+v", got, want)
	}
	// Every attempt in the fixture started today: of the seven finished (t-3 twice, t-4, t-5, t-7, t-8 and t-10)
	// only t-3's plan attempt reported its spend, so six are unknown.
	today := st.Session.GuardRails.SpendToday
	want := t3
	want.Attempts, want.TokensUnknown, want.CostUnknown = 7, 6, 6
	if today != want {
		t.Errorf("spend_today = %+v, want %+v", today, want)
	}
}

// TestDirectorStatusMarksOverrides: a task with a task override in effect is marked, and every other task is not.
func TestDirectorStatusMarksOverrides(t *testing.T) {
	dir := statusCopy(t)
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTaskOverride("override", "app", "t-2", store.TaskOverrideSet, "default_effort", "high", 0, "Guildmaster: high."); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := directorCmd(t, nil, "status", "--copy", dir, "--json")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	var st struct {
		Tasks []struct {
			Repository, ID string
			Overrides      bool `json:"overrides"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	for _, task := range st.Tasks {
		if want := task.Repository == "app" && task.ID == "t-2"; task.Overrides != want {
			t.Errorf("%s/%s overrides = %t, want %t", task.Repository, task.ID, task.Overrides, want)
		}
	}
	if len(st.Tasks) < 2 {
		t.Errorf("status lists %d tasks", len(st.Tasks))
	}
}

func TestDirectorStatusOfAnEmptyCopy(t *testing.T) {
	dir := t.TempDir()
	if app, _, stderr := newTestApp(nil, nil); app.Run([]string{"store", "init", filepath.Join(dir, "director.store")}) != 0 {
		t.Fatalf("store init: %s", stderr)
	}
	code, out, stderr := directorCmd(t, nil, "status", "--copy", dir, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	copyField, err := json.Marshal(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"as_of":{"partial":true,"excluded":["worker files","transcripts","forge state"],"sequence":0,"committed_at":"0001-01-01T00:00:00Z"},` +
		`"copy":` + string(copyField) + `,"session":{"state":"no session","guard_rails":{"running_attempts":0,"attempts_today":0,"day":"2026-10-22",` +
		`"spend_today":{"attempts":0,"tokens_unknown":0,"cost_unknown":0,"tokens":"unknown","input_tokens":0,"output_tokens":0,` +
		`"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cost":"unknown","cost_usd":0}},"start_queue":[]},"mode":{"name":"standalone"},"tasks":[],"merge_queues":[],"decisions":[],` +
		`"messages":{"unread":0,"by_type":{},"headers":[]},"recent":[],"needs_me":[],"parked":[],"held":[],"running":[],"grants":[],` +
		`"version":{"version":"dev","revision":"` + testRevision + `","time":"2026-10-27T21:00:24Z","modified":false,"go_version":"go1.27.2",` +
		`"platform":"linux/amd64","pr":null,"task":null,"behind_main":null}}` + "\n"
	if out != want {
		t.Fatalf("empty copy:\n got %s\nwant %s", out, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "state")); !os.IsNotExist(err) {
		t.Fatalf("status created the state directory (%v)", err)
	}
}

// TestDirectorStatusOfAnInvalidConfig: a config Parse refuses gives its reason as the guard rails' config_error
// and no quality settings, rather than failing the feed.
func TestDirectorStatusOfAnInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	if app, _, stderr := newTestApp(nil, nil); app.Run([]string{"store", "init", filepath.Join(dir, "director.store")}) != 0 {
		t.Fatalf("store init: %s", stderr)
	}
	writeFile(t, filepath.Join(dir, "config.toml"), `schema_version = 1
mode = "director"
name = "alpha"

[local]
quality_self_review = "yes"

[[repository]]
id = "app"
path = "/srv/app"
remote = "git@github.com:example/app.git"
owner_machine = "workstation"
`)
	code, out, stderr := directorCmd(t, nil, "status", "--copy", dir, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	var st struct {
		Session struct {
			GuardRails map[string]any `json:"guard_rails"`
		} `json:"session"`
		Quality *json.RawMessage `json:"quality"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	if msg, _ := st.Session.GuardRails["config_error"].(string); !strings.Contains(msg, "quality_self_review") {
		t.Errorf("config_error = %q, want the refusal naming quality_self_review", msg)
	}
	if st.Quality != nil {
		t.Errorf("quality = %s, want none for a config that cannot be read", *st.Quality)
	}
}

func TestDirectorStatusWaitsForTheWriter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := statusCopy(t)
		w, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		var code int
		var out, stderr string
		go func() {
			defer close(done)
			code, out, stderr = directorCmd(t, nil, "status", "--copy", dir, "--json")
		}()
		synctest.Wait()
		select {
		case <-done:
			t.Fatalf("status finished while the writer held the store: exit %d, %s", code, stderr)
		default:
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		<-done
		if code != 0 || !strings.Contains(out, `"sequence":23`) {
			t.Fatalf("after the writer let go: exit %d, %s%s", code, out, stderr)
		}
	})
}

// TestDirectorStatusNext copies next.json as written, Windows paths included, and reports one that is not JSON.
func TestDirectorStatusNext(t *testing.T) {
	dir := statusCopy(t)
	state := filepath.Join(dir, "state")
	written := `{"wait":"attempt","step":"build","attempt":2,"result":"C:\\Users\\gm\\copy\\state\\app\\t-3\\build-2\\result.json"}`
	writeFile(t, filepath.Join(state, "app", "t-3", "next.json"), written+"\n")
	writeFile(t, filepath.Join(state, "app", "t-4", "next.json"), "{not json")
	code, out, stderr := directorCmd(t, nil, "status", "--copy", dir, "--json")
	if code != 0 {
		t.Fatalf("exit %d, %s", code, stderr)
	}
	var got struct {
		Tasks []struct {
			ID        string          `json:"id"`
			Next      json.RawMessage `json:"next"`
			NextError string          `json:"next_error"`
		} `json:"tasks"`
		NeedsMe []struct {
			Kind string `json:"kind"`
		} `json:"needs_me"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	for _, task := range got.Tasks {
		switch task.ID {
		case "t-3":
			if string(task.Next) != written || task.NextError != "" {
				t.Errorf("t-3 next %s (%s), want %s", task.Next, task.NextError, written)
			}
		case "t-4":
			if task.Next != nil || !strings.Contains(task.NextError, "not JSON") {
				t.Errorf("t-4 next %s, error %q", task.Next, task.NextError)
			}
		}
	}
	for _, item := range got.NeedsMe {
		if item.Kind == "director" {
			t.Errorf("a next.json waiting on an attempt is not a director item: %s", out)
		}
	}
}

// TestDirectorStatusMeasuresHeartbeatsByItsClock: a live launcher's heartbeat that is fresh by the wall clock but
// older than reconcile.DefaultStaleAfter by the status clock is stuck, listed under running and a resume item.
func TestDirectorStatusMeasuresHeartbeatsByItsClock(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	self, err := artisan.Self()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask("i-1", store.Task{Repository: "app", ID: "t-1", Title: "Live", Workflow: def.Revision(), ConfigDigest: "sha256:" + strings.Repeat("c", 64)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartOwnedAttempt("i-2", store.Ownership{Repository: "app", TaskID: "t-1", Step: "build", LauncherIdentity: store.ProcessIdentity(self)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	beat := time.Now().UTC().Truncate(time.Second)
	key := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}
	if err := os.MkdirAll(key.Dir(filepath.Join(dir, "state")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := artisan.WriteHeartbeat(key.Dir(filepath.Join(dir, "state")), artisan.Heartbeat{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1,
		Sequence: 1, Time: beat, WorkerState: "running", LastOutputAt: beat}); err != nil {
		t.Fatal(err)
	}
	st, err := readDirectorStatus(context.Background(), filepath.Join(dir, "director.store"), filepath.Join(dir, "state"),
		func() time.Time { return beat.Add(reconcile.DefaultStaleAfter + time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Running) != 1 {
		t.Fatalf("running = %+v, want the one attempt", st.Running)
	}
	r := st.Running[0]
	if r.State != reconcile.Stuck || r.Launcher != "alive" || !r.HeartbeatAt.Equal(beat) || !r.LastOutputAt.Equal(beat) || r.WorkerState != "running" ||
		r.Command != "guildhall artisan status --repo app --task t-1 --step build --attempt 1" {
		t.Errorf("running entry %+v, want stuck with the heartbeat's times", r)
	}
	if len(st.NeedsMe) != 1 || st.NeedsMe[0].Kind != "resume" || !strings.HasPrefix(st.NeedsMe[0].Reason, "stuck: ") {
		t.Errorf("needs_me = %+v, want one stuck resume item", st.NeedsMe)
	}
}

// TestDirectorStartStartupListsRunningAttempts: a new session's startup.md lists an attempt with no store outcome
// under its own Running section, with its state, heartbeat and output ages and the artisan status command, between
// In flight and Parked; its killed launcher also makes it an in-flight resume item.
func TestDirectorStartStartupListsRunningAttempts(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "/exit")
	if code, out, stderr := runDirectorCmd(t, "start", "--copy", dir); code != 0 {
		t.Fatalf("first session: exit %d\n%s\n%s", code, out, stderr)
	}
	newTask(t, filepath.Join(dir, "director.store"), filepath.Join(dir, "config.toml"), "app", "Sort", "t-2")
	killed := exitedLauncher(t)
	withTestStore(t, dir, func(s *store.Store) (store.Receipt, error) {
		return s.StartOwnedAttempt("start", store.Ownership{Repository: "app", TaskID: "t-2", Step: "build", LauncherIdentity: killed})
	})
	attempt := artisan.AttemptKey{Repository: "app", TaskID: "t-2", Step: "build", Attempt: 1}.Dir(filepath.Join(dir, "state"))
	if err := os.MkdirAll(attempt, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := artisan.WriteHeartbeat(attempt, artisan.Heartbeat{Repository: "app", TaskID: "t-2", Step: "build", Attempt: 1, Sequence: 3,
		Time: now.Add(-2 * time.Minute), WorkerState: "running", LastOutputAt: now.Add(-3 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := runDirectorCmd(t, "start", "fake", "--copy", dir); code != 0 {
		t.Fatalf("second session: exit %d\n%s\n%s", code, out, stderr)
	}
	b, err := os.ReadFile(filepath.Join(dir, "director", "session-2", render.StartupFile))
	if err != nil {
		t.Fatal(err)
	}
	startup := string(b)
	inFlight, running, ok := strings.Cut(startup, "## Running\n")
	if !ok {
		t.Fatalf("startup.md has no running section:\n%s", startup)
	}
	if !strings.Contains(inFlight, "## In flight\n") || !strings.Contains(inFlight, "resume: exited: ") {
		t.Errorf("in-flight section before Running lacks the exited attempt's resume item:\n%s", startup)
	}
	want := "- running: attempt 1 of build of app/t-2; state exited; heartbeat 2m ago; last output 3m ago; launcher identity gone; no containment recorded; " +
		"check with `guildhall artisan status --repo app --task t-2 --step build --attempt 1`\n"
	if !strings.Contains(running, "guildhall director ready") || !strings.HasSuffix(running, want) {
		t.Errorf("running section = %q, want it to ask for confirmation before director ready and end with %q", running, want)
	}
}

func TestDirectorStatusUsage(t *testing.T) {
	dir := statusCopy(t)
	for _, c := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"no subcommand", nil, 2, "director: the commands are status, start, ready, pause, resume, stop, note, follow-up, maintenance, reload, turn-started, turn-ended, wait-wake, context-report, enrol, leave and mode"},
		{"unknown subcommand", []string{"enroll"}, 2, "director: the commands are status, start, ready, pause, resume, stop, note, follow-up, maintenance, reload, turn-started, turn-ended, wait-wake, context-report, enrol, leave and mode"},
		{"no json", []string{"status", "--copy", dir}, 2, "prints JSON only; pass --json"},
		{"no copy", []string{"status", "--json"}, 2, "director status: no Guildhall copy found from the current directory; pass --copy <dir> or cd into a copy"},
		{"store without state", []string{"status", "--json", "--store", filepath.Join(dir, "director.store")}, 2, "--store and --state are required, or --copy"},
		{"argument", []string{"status", "--json", "--copy", dir, "extra"}, 2, `unexpected argument "extra"`},
		{"unknown flag", []string{"status", "--json", "--copy", dir, "--repo", "app"}, 2, "flag provided but not defined: -repo"},
		{"missing store file", []string{"status", "--json", "--copy", t.TempDir()}, 1, "guildhall store init"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, out, stderr := directorCmd(t, nil, c.args...)
			if code != c.code || out != "" || !strings.Contains(stderr, c.want) {
				t.Fatalf("exit %d, stdout %q, stderr %q; want exit %d naming %q", code, out, stderr, c.code, c.want)
			}
		})
	}
}

// TestDirectorStatusInstall: a binary behind its own repository's default branch as last fetched adds one install
// item at the end of needs_me, which is not a wake; one up to date, with no revision or with no own repository adds
// none.
func TestDirectorStatusInstall(t *testing.T) {
	behind := []string{"y (#160)", "x (#159)"}
	for name, c := range map[string]struct {
		remote string
		behind []string
		info   func(*debug.BuildInfo)
		want   bool
	}{
		"behind":        {remote: "git@github.com:ndrwl/guildhall.git", behind: behind, want: true},
		"up to date":    {remote: "git@github.com:ndrwl/guildhall.git"},
		"no revision":   {remote: "git@github.com:ndrwl/guildhall.git", behind: behind, info: func(i *debug.BuildInfo) { i.Settings = nil }},
		"no own remote": {remote: "git@github.com:ndrwl/other.git", behind: behind},
	} {
		t.Run(name, func(t *testing.T) {
			dir := versionCopy(t, c.remote)
			g := &fakeVersionGit{subjects: map[string]string{testRevision: "x (#158)"}, ref: true, behind: c.behind}
			app, stdout, stderr := newTestApp(nil, nil)
			app.BuildInfo, app.VersionGit = testBuildInfo(c.info), g
			if code := app.Run([]string{"director", "status", "--json", "--copy", dir}); code != 0 {
				t.Fatalf("director status = %d, %s", code, stderr)
			}
			var out struct {
				NeedsMe []needsMeItem `json:"needs_me"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			for _, call := range g.calls {
				if strings.HasPrefix(call, "fetch ") {
					t.Errorf("director status ran %q", call)
				}
			}
			if !c.want {
				if len(out.NeedsMe) != 0 {
					t.Errorf("needs_me = %+v, want none", out.NeedsMe)
				}
				return
			}
			want := needsMeItem{Kind: installKind, Repository: "guildhall", Count: 2,
				Reason:  "the installed guildhall (abd97485) is 2 merges behind main (#160, #159); ask the Guildmaster to install",
				Command: "guildhall version"}
			if len(out.NeedsMe) != 1 || out.NeedsMe[0] != want {
				t.Errorf("needs_me = %+v, want only %+v", out.NeedsMe, want)
			}
			if n := waitingKinds(out.NeedsMe); len(n) != 0 {
				t.Errorf("waitingKinds = %v, want the install item not to wake the Director", n)
			}
		})
	}
}

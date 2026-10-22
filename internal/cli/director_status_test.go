package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

func directorCmd(t *testing.T, getenv func(string) string, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Getenv = getenv
	app.Now = func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) }
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
// app/t-3's plan attempt reported a measured and estimated spend and its build attempt an unknown one.
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
			return s.SendMessage(store.Message{ID: "q-1", Recipient: store.RecipientDirector, Repository: "lib", TaskID: "t-1", Type: "question", Text: "Which parser?"})
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
	}
	for _, step := range steps {
		if _, err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(artisan.AttemptKey{Repository: "app", TaskID: "t-6", Step: "build", Attempt: 1}.Dir(state), 0o755); err != nil {
		t.Fatal(err)
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
merge_policy = "auto"

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
	code, out, stderr := directorCmd(t, nil, "status", "--copy", dir, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("status prints one JSON line: %q", out)
	}
	// The step done command names the result file under the copy, written in the golden as $COPY/....
	result, err := json.Marshal(filepath.Join(artisan.AttemptKey{Repository: "app", TaskID: "t-7", Step: "plan", Attempt: 1}.Dir(filepath.Join(dir, "state")), artisan.ResultFile))
	if err != nil {
		t.Fatal(err)
	}
	checkIndentedGolden(t, "director-status.json", strings.Replace(out, strings.Trim(string(result), `"`), "$COPY/state/app/t-7/plan-1/result.json", 1))
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("status changed the store (%v)", err)
	}

	// The explicit paths read the same copy, and GUILDHALL_COPY selects it.
	_, explicit, _ := directorCmd(t, nil, "status", "--store", path, "--state", filepath.Join(dir, "state"), "--config", filepath.Join(dir, "config.toml"), "--json")
	_, env, _ := directorCmd(t, func(k string) string {
		if k == copyEnvVar {
			return dir
		}
		return ""
	}, "status", "--json")
	if explicit != out || env != out {
		t.Fatalf("explicit paths or GUILDHALL_COPY read differently:\n%s\n%s\n%s", out, explicit, env)
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
	// Every attempt in the fixture started today: of the six finished (t-3 twice, t-4, t-5, t-7 and t-8) only
	// t-3's plan attempt reported its spend, so five are unknown.
	today := st.Session.GuardRails.SpendToday
	want := t3
	want.Attempts, want.TokensUnknown, want.CostUnknown = 6, 5, 5
	if today != want {
		t.Errorf("spend_today = %+v, want %+v", today, want)
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
	want := `{"as_of":{"partial":true,"excluded":["worker files","transcripts","forge state"],"sequence":0,"committed_at":"0001-01-01T00:00:00Z"},` +
		`"session":{"state":"no session","guard_rails":{"running_attempts":0,"attempts_today":0,"day":"2026-10-22",` +
		`"spend_today":{"attempts":0,"tokens_unknown":0,"cost_unknown":0,"tokens":"unknown","input_tokens":0,"output_tokens":0,` +
		`"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cost":"unknown","cost_usd":0}}},"mode":{"name":"standalone"},"tasks":[],"decisions":[],` +
		`"messages":{"unread":0,"by_type":{},"headers":[]},"recent":[],"needs_me":[]}` + "\n"
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
	select {
	case <-done:
		t.Fatalf("status finished while the writer held the store: exit %d, %s", code, stderr)
	case <-time.After(500 * time.Millisecond):
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	<-done
	if code != 0 || !strings.Contains(out, `"sequence":23`) {
		t.Fatalf("after the writer let go: exit %d, %s%s", code, out, stderr)
	}
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

func TestDirectorStatusUsage(t *testing.T) {
	dir := statusCopy(t)
	for _, c := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"no subcommand", nil, 2, "director: the commands are status, start, ready, pause, resume and stop"},
		{"unknown subcommand", []string{"reload"}, 2, "director: the commands are status, start, ready, pause, resume and stop"},
		{"no json", []string{"status", "--copy", dir}, 2, "prints JSON only; pass --json"},
		{"no store", []string{"status", "--json"}, 2, "--store and --state are required, or --copy"},
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

package cli

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// clerkNow is the clock every Clerk test reads by.
var clerkNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// clerkTOML is directorTOML with app's [repository.kinds] naming its own bug workflow, fix-flow.
var clerkTOML = strings.Replace(directorTOML, "\n[[repository]]\nid = \"lib\"", "\n[repository.kinds]\nbug = \"fix-flow\"\n\n[[repository]]\nid = \"lib\"", 1)

// clerkFixture is a marked copy with a store whose clock is set per step, holding, in app: a-new never started;
// a-build running build attempt 1 under a live launcher; a-approve waiting on an approval; a-checks waiting on the
// checks of PR #212; a merge queue of three, a-q1 the front rebasing, a-q2 held with passed checks and a-q3; a-queued
// second in the start queue; a-deps waiting on a-new; a-grill handed to the Director's grill stage; a-odd with a
// wait Clerk does not know; a-bug bound to the
// custom bug workflow; a-long with a title too long to show whole; a-merged2 merged two days ago and a-merged9 nine.
// lib has its own a-new, and a merge queue whose front l-front waits on CI. Priorities are mixed, with ties.
func clerkFixture(t *testing.T) string {
	t.Helper()
	dir := markCopy(t, filepath.Dir(writeDirector(t, clerkTOML)))
	state := filepath.Join(dir, "state")
	clock := clerkNow.Add(-10 * 24 * time.Hour)
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{Now: func() time.Time {
		clock = clock.Add(time.Minute)
		return clock
	}})
	if err != nil {
		t.Fatal(err)
	}
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	must := func(_ store.Receipt, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	mutation := func() string { n++; return "m-" + string(rune('a'+n/26)) + string(rune('a'+n%26)) }
	task := func(repo, id, title string, p store.Priority, deps ...store.TaskRef) {
		t.Helper()
		must(s.CreateTask(mutation(), store.Task{Repository: repo, ID: id, Title: title, Workflow: def.Revision(), ConfigDigest: "sha256:" + strings.Repeat("c", 64), DependsOn: deps}))
		if p != store.DefaultPriority {
			must(s.SetTaskPriority(mutation(), repo, id, p))
		}
	}
	pr := func(repo, id string, number int) {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"schema": "guildhall.pull-request/1", "remote": "origin", "number": number,
			"url": "https://forge.invalid/" + repo + "/pull/" + strconv.Itoa(number), "branch": "guildhall/" + id, "head": strings.Repeat("1", 40), "state": "open"})
		rel := "tasks/" + repo + "/" + id + "/publish-1/pull-request.json"
		path := filepath.Join(state, "documents", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, string(data))
		must(s.PutDocument(mutation(), store.DocumentRef{Repository: repo, TaskID: id, Name: "publish-1-pr", Path: rel, SHA256: digest.Of(data).Hex()}))
	}
	next := func(repo, id, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(state, repo, id), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(state, repo, id, "next.json"), body)
	}
	queue := func(repo, id string, number int) {
		t.Helper()
		must(s.PutDocument(mutation(), store.DocumentRef{Repository: repo, TaskID: id, Name: "deliver-1-authorized", Path: "tasks/" + repo + "/" + id + "/deliver-1/authorized.json", SHA256: strings.Repeat("0", 64)}))
		must(s.JoinMergeQueue(mutation(), repo, id, "deliver-1", "document/deliver-1-authorized", "https://forge.invalid/"+repo+"/pull/"+strconv.Itoa(number)))
		next(repo, id, `{"wait":"merge-queue","step":"deliver"}`)
	}

	// Closed tasks first, by the clock: a-merged9 closes nine days before clerkNow and a-merged2 two.
	task("app", "a-merged9", "Merged long ago", store.PriorityNormal)
	must(s.PutDocument(mutation(), store.DocumentRef{Repository: "app", TaskID: "a-merged9", Name: "deliver-1-merge", Path: "tasks/app/a-merged9/deliver-1/merge.json", SHA256: strings.Repeat("0", 64)}))
	must(s.SetTaskState(mutation(), "app", "a-merged9", store.TaskDone))
	clock = clerkNow.Add(-2 * 24 * time.Hour)
	task("app", "a-merged2", "Merged this week", store.PriorityHigh)
	must(s.PutDocument(mutation(), store.DocumentRef{Repository: "app", TaskID: "a-merged2", Name: "deliver-1-merge", Path: "tasks/app/a-merged2/deliver-1/merge.json", SHA256: strings.Repeat("0", 64)}))
	must(s.SetTaskState(mutation(), "app", "a-merged2", store.TaskDone))
	clock = clerkNow.Add(-6 * time.Hour)

	task("app", "a-new", "Never started", store.PriorityNormal)
	task("lib", "a-new", "Same id in lib", store.PriorityLow)
	task("app", "a-build", "Running build", store.PriorityHighest)
	task("app", "a-approve", "Approve the plan", store.PriorityHigh)
	task("app", "a-checks", "Checks pending", store.PriorityHigh)
	task("app", "a-q1", "Front of the queue", store.PriorityUrgent)
	task("app", "a-q2", "Held in the queue", store.PriorityHighest)
	task("app", "a-q3", "Last in the queue", store.PriorityNormal)
	task("app", "a-queued", "Queued start", store.PriorityLow)
	task("app", "a-deps", "Depends on a-new", store.PriorityNormal, store.TaskRef{Repository: "app", ID: "a-new"})
	task("app", "a-grill", "Grill the Guildmaster", store.PriorityNormal)
	task("app", "a-odd", "Unknown wait", store.PriorityNormal)
	task("app", "a-long", "A title far too long to show whole in a hundred columns, so Clerk cuts it with an ellipsis", store.PriorityNormal)
	task("lib", "l-front", "Lib front", store.PriorityNormal)
	bug := def.Revision()
	bug.Name = "fix-flow"
	must(s.CreateTask(mutation(), store.Task{Repository: "app", ID: "a-bug", Title: "Fix the crash", Workflow: bug, ConfigDigest: "sha256:" + strings.Repeat("c", 64)}))
	must(s.SetTaskPriority(mutation(), "app", "a-bug", store.PriorityUrgent))

	self, err := artisan.Self()
	if err != nil {
		t.Fatal(err)
	}
	// The build's launcher is this test, alive, and its endpoint answers, so the attempt classifies as running.
	key := artisan.AttemptKey{Repository: "app", TaskID: "a-build", Step: "build", Attempt: 1}
	short, err := os.MkdirTemp("", "ck")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	endpoint, err := artisan.EndpointPath(short, key)
	if err != nil {
		t.Fatal(err)
	}
	must(s.StartOwnedAttempt(mutation(), store.Ownership{Repository: "app", TaskID: "a-build", Step: "build", LauncherIdentity: store.ProcessIdentity(self), Endpoint: endpoint}))
	ep, err := artisan.Listen(endpoint, key, self, func() (artisan.Identity, error) { return self, nil }, clerkAnswering{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ep.Close() })
	if err := os.MkdirAll(key.Dir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := artisan.WriteHeartbeat(key.Dir(state), artisan.Heartbeat{Repository: "app", TaskID: "a-build", Step: "build", Attempt: 1,
		Sequence: 1, Time: clerkNow.Add(-10 * time.Second), WorkerState: "running", LastOutputAt: clerkNow.Add(-10 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	next("app", "a-build", `{"wait":"attempt","step":"build","attempt":1}`)

	must(s.StartAttempt(mutation(), "app", "a-approve", "plan"))
	must(s.FinishAttempt(mutation(), "app", "a-approve", "plan", 1, store.AttemptSucceeded))
	must(s.OpenDecision(mutation(), store.Decision{Repository: "app", TaskID: "a-approve", ID: "accept-plan", Kind: store.DecisionApproval,
		Subject: &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + strings.Repeat("a", 64)}, Options: []string{"approve", "reject"}, Question: "Accept the plan?"}))
	next("app", "a-approve", `{"wait":"decision","step":"accept","decision":"accept-plan"}`)
	planDir := artisan.AttemptKey{Repository: "app", TaskID: "a-approve", Step: "plan", Attempt: 1}.Dir(state)
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(planDir, artisan.ResultFile), `{"summary":"Planned the change in three steps."}`)

	pr("app", "a-checks", 212)
	next("app", "a-checks", `{"wait":"checks","step":"validate"}`)

	pr("app", "a-q1", 301)
	pr("app", "a-q2", 302)
	must(s.SendMessage(store.Message{ID: "app.a-q2.checks-passed.validate-1", Recipient: store.RecipientDirector, Repository: "app", TaskID: "a-q2",
		Type: store.MessageChecksPassed, Text: "The checks passed."}))
	queue("app", "a-q1", 301)
	clock = clock.Add(90 * time.Minute)
	queue("app", "a-q2", 302)
	queue("app", "a-q3", 303)
	must(s.ClaimMergeFront(mutation(), "app", "a-q1"))
	must(s.SetMergeHold(mutation(), "app", "a-q2", true))
	must(s.StartAttempt(mutation(), "app", "a-q1", "deliver-rebase"))
	queue("lib", "l-front", 41)
	must(s.ClaimMergeFront(mutation(), "lib", "l-front"))

	// a-sliced is a multi-PR task whose accepted spec lists three slices: slice 1 merged as PR #401 and slice 2's PR
	// #402 waits on its checks.
	doc := func(repo, id, name, rel string, data []byte) {
		t.Helper()
		path := filepath.Join(state, "documents", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, string(data))
		must(s.PutDocument(mutation(), store.DocumentRef{Repository: repo, TaskID: id, Name: name, Path: rel, SHA256: digest.Of(data).Hex()}))
	}
	prDoc := func(id string, number, slice int, state string) []byte {
		branch := "guildhall/" + id
		if slice > 1 {
			branch += ".s" + strconv.Itoa(slice)
		}
		data, _ := json.Marshal(map[string]any{"schema": "guildhall.pull-request/1", "remote": "origin", "number": number,
			"url": "https://forge.invalid/app/pull/" + strconv.Itoa(number), "branch": branch, "head": strings.Repeat(strconv.Itoa(slice), 40), "state": state})
		return data
	}
	task("app", "a-sliced", "Large work in three", store.PriorityNormal)
	doc("app", "a-sliced", "accept-1-spec", "tasks/app/a-sliced/plan-1/spec.md", []byte(slicedSpec))
	doc("app", "a-sliced", "deliver-1-merge", "tasks/app/a-sliced/deliver-1/merge.json", prDoc("a-sliced", 401, 1, "merged"))
	doc("app", "a-sliced", "publish--s2-1-pr", "tasks/app/a-sliced/publish--s2-1/pull-request.json", prDoc("a-sliced", 402, 2, "open"))
	next("app", "a-sliced", `{"wait":"checks","step":"validate--s2"}`)

	next("app", "a-queued", `{"wait":"queued","step":"build","attempt":1,"position":2}`)
	next("app", "a-deps", `{"wait":"dependencies","step":"research"}`)
	next("app", "a-grill", `{"wait":"director-stage","step":"grill"}`)
	next("app", "a-odd", `{"wait":"mystery","step":"build"}`)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// slicedSpec is a-sliced's accepted spec, listing three slices.
const slicedSpec = `# Large work in three

## Goal

Land the large work as three pull requests.

## Slices

1. **Parser**: the parser; about 100 lines in 2 files.
2. **Engine**: the engine; about 200 lines in 4 files.
3. **Views**: the views; about 100 lines in 3 files.
`

// clerkAnswering is a running attempt's endpoint handler.
type clerkAnswering struct{}

func (clerkAnswering) Status() (artisan.EndpointState, error) {
	return artisan.EndpointState{Phase: "running"}, nil
}
func (clerkAnswering) Stop() (artisan.EndpointState, error)   { return clerkAnswering{}.Status() }
func (clerkAnswering) Cancel() (artisan.EndpointState, error) { return clerkAnswering{}.Status() }

// runClerk runs `guildhall clerk` in wd at clerkNow.
func runClerk(t *testing.T, wd string, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Getwd = func() (string, error) { return wd, nil }
	app.Now = func() time.Time { return clerkNow }
	code := app.Run(append([]string{"clerk"}, args...))
	return code, stdout.String(), stderr.String()
}

func TestClerkGolden(t *testing.T) {
	dir := clerkFixture(t)
	for _, tc := range []struct {
		golden string
		args   []string
	}{
		{"clerk-guild.txt", []string{"guild"}},
		{"clerk-guild-all.txt", []string{"guild", "--all"}},
		{"clerk-decisions.txt", []string{"decisions"}},
		{"clerk-queue.txt", []string{"queue"}},
		{"clerk-task.txt", []string{"task", "a-checks"}},
		{"clerk-inspect.txt", []string{"inspect", "app/a-approve"}},
		{"clerk-task-sliced.txt", []string{"task", "a-sliced"}},
		{"clerk-inspect-sliced.txt", []string{"inspect", "app/a-sliced"}},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			code, out, stderr := runClerk(t, filepath.Join(dir, "director"), tc.args...)
			if code != 0 {
				t.Fatalf("exit %d, stderr %s", code, stderr)
			}
			checkGolden(t, tc.golden, out)
			for line := range strings.Lines(out) {
				if n := len([]rune(strings.TrimSuffix(line, "\n"))); n > clerkWidth {
					t.Errorf("line of %d columns, over %d: %q", n, clerkWidth, line)
				}
			}
		})
	}
}

// TestClerkWhereItStands: every task's phrase, as /guild --all shows it.
func TestClerkWhereItStands(t *testing.T) {
	dir := clerkFixture(t)
	code, out, stderr := runClerk(t, dir, "guild", "--all")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	tasks, _, _ := strings.Cut(out, "\nNeeds you\n")
	for id, want := range map[string]string{
		"app/a-new":     "not started",
		"app/a-build":   "build running",
		"app/a-approve": "waiting for your approval",
		"app/a-checks":  "CI pending, PR #212",
		"app/a-q1":      "in merge queue 1/3, rebasing",
		"app/a-q2":      "in merge queue 2/3",
		"app/a-q3":      "in merge queue 3/3",
		"app/a-queued":  "queued 2",
		"app/a-deps":    "waiting on app/a-new",
		"app/a-grill":   "waiting for the Director",
		"app/a-odd":     "waiting (mystery)",
		"app/a-merged2": "merged",
		"lib/l-front":   "in merge queue 1/1, CI pending",
		"app/a-sliced":  "slice 2 of 3, CI pending, PR #402",
	} {
		line := clerkLine(tasks, id)
		if !strings.HasSuffix(line, "  "+want) {
			t.Errorf("%s: line %q, want it to end %q", id, line, want)
		}
	}
	if line := clerkLine(tasks, "app/a-bug"); !strings.Contains(line, " bug ") {
		t.Errorf("a-bug, bound to the custom bug workflow, is not shown as a bug: %q", line)
	}
	if strings.Contains(tasks, "a-merged9") {
		t.Errorf("a task merged nine days ago is shown:\n%s", tasks)
	}
	code, out, _ = runClerk(t, dir, "guild")
	if code != 0 || strings.Contains(out, "a-merged2") || !strings.Contains(out, "app/a-new ") {
		t.Errorf("guild without --all: exit %d, want open tasks only:\n%s", code, out)
	}
	// Priority first, then id: p0 a-bug before p0 a-q1, and the p3 ties in id order.
	var order []string
	for line := range strings.Lines(tasks) {
		if f := strings.Fields(line); len(f) > 1 && strings.Contains(f[0], "/") {
			order = append(order, f[0])
		}
	}
	want := []string{"app/a-bug", "app/a-q1", "app/a-build", "app/a-q2", "app/a-approve", "app/a-checks", "app/a-merged2",
		"app/a-deps", "app/a-grill", "app/a-long", "app/a-new", "app/a-odd", "app/a-q3", "app/a-sliced", "lib/l-front", "lib/a-new", "app/a-queued"}
	if !slices.Equal(order, want) {
		t.Errorf("order %v, want %v", order, want)
	}
	if line := clerkLine(tasks, "app/a-long"); !strings.Contains(line, "…") {
		t.Errorf("long title not cut: %q", line)
	}

	// Once accept-plan is answered, a-approve waits for the Director to continue it, not for the Guildmaster.
	answerClerkDecision(t, dir, "app", "a-approve", "accept-plan", "approve")
	code, out, stderr = runClerk(t, dir, "guild")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	tasks, _, _ = strings.Cut(out, "\nNeeds you\n")
	if line := clerkLine(tasks, "app/a-approve"); !strings.HasSuffix(line, "  waiting for the Director") {
		t.Errorf("answered accept-plan: line %q, want it to end %q", line, "waiting for the Director")
	}
}

// answerClerkDecision answers decision id of repo/task in the copy dir's store.
func answerClerkDecision(t *testing.T, dir, repo, task, id, answer string) {
	t.Helper()
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerDecision("answer:"+id, repo, task, id, answer); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// clerkSection is the body of out's section named heading, up to the next blank line.
func clerkSection(out, heading string) string {
	_, rest, ok := strings.Cut(out, "\n"+heading+"\n")
	if !ok {
		return ""
	}
	body, _, _ := strings.Cut(rest, "\n\n")
	return body + "\n"
}

// TestClerkNeedsYou: Needs you lists only the Guildmaster's asks, open decisions and Director stages in the Tasks
// table's order, and every other needs_me item is under Waiting on the Director; with only Director items left,
// Needs you says nothing needs you.
func TestClerkNeedsYou(t *testing.T) {
	dir := clerkFixture(t)
	code, out, stderr := runClerk(t, dir, "guild")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if got, want := clerkSection(out, "Needs you"), "- decision app/a-approve accept-plan: Accept the plan?\n"+
		"- director-stage app/a-grill grill: the Director runs grill with you in its session\n"; got != want {
		t.Errorf("Needs you =\n%s\nwant\n%s", got, want)
	}
	director := clerkSection(out, "Waiting on the Director")
	for _, want := range []string{"- advance app/a-queued build:", "- messages:", "- checks app/a-checks validate:", "- dependencies app/a-deps research:",
		"- merge-queue app/a-q1 deliver:", "- merge-queue lib/l-front deliver:"} {
		if !strings.Contains(director, want) {
			t.Errorf("Waiting on the Director has no %q:\n%s", want, director)
		}
	}
	if strings.Contains(director, "decision") || strings.Contains(director, "director-stage") {
		t.Errorf("Waiting on the Director holds a Guildmaster ask:\n%s", director)
	}

	answerClerkDecision(t, dir, "app", "a-approve", "accept-plan", "approve")
	writeFile(t, filepath.Join(dir, "state", "app", "a-grill", "next.json"), `{"wait":"director","step":"grill"}`)
	code, out, stderr = runClerk(t, dir, "guild")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if got := clerkSection(out, "Needs you"); got != "Nothing needs you.\n" {
		t.Errorf("only Director items: Needs you =\n%s", got)
	}
	if !strings.Contains(clerkSection(out, "Waiting on the Director"), "- checks app/a-checks validate:") {
		t.Errorf("only Director items: Waiting on the Director lost its items:\n%s", out)
	}
}

// clerkLine is the line of out that starts with name, trimmed.
func clerkLine(out, name string) string {
	for line := range strings.Lines(out) {
		if strings.HasPrefix(line, name+" ") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// TestClerkQueueDecisionAndEmpty: a front with an open decision waits for the Guildmaster's answer, and a copy
// whose queues are all empty says so in queue and guild.
func TestClerkQueueDecisionAndEmpty(t *testing.T) {
	dir := clerkFixture(t)
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenDecision("q-merge", store.Decision{Repository: "lib", TaskID: "l-front", ID: "merge-approval", Kind: store.DecisionApproval,
		Subject: &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + strings.Repeat("b", 64)},
		Options: []string{"approve", "reject"}, Question: "Merge now?"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runClerk(t, dir, "queue")
	if line := clerkLine(out[strings.Index(out, "\nlib\n"):], "1"); code != 0 || !strings.HasSuffix(line, "waiting for your answer") {
		t.Fatalf("exit %d, stderr %s, lib front %q:\n%s", code, stderr, line, out)
	}

	empty := markCopy(t, newCopy(t))
	code, out, stderr = runClerk(t, empty, "queue")
	if code != 0 || out != "Merge queue, as of 2026-10-30 12:00 UTC, local state\n\nThe merge queue is empty.\n" {
		t.Fatalf("empty queue: exit %d, stderr %s, out %q", code, stderr, out)
	}
	code, out, _ = runClerk(t, empty, "guild")
	for _, want := range []string{"No open tasks.\n", "Nothing needs you.\n", "No open pull requests.\n", "The merge queue is empty.\n"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("empty guild: exit %d, want %q in:\n%s", code, want, out)
		}
	}
	if code, out, _ = runClerk(t, empty, "decisions"); code != 0 || !strings.HasSuffix(out, "\nNo open decisions.\n") {
		t.Errorf("empty decisions: exit %d, %q", code, out)
	}
}

// TestClerkReadOnly: no Clerk subcommand changes the store's receipts or any file of the copy but an attempt.lock.
func TestClerkReadOnly(t *testing.T) {
	dir := clerkFixture(t)
	snapshot := func() (map[string]string, []store.Receipt) {
		files := map[string]string{}
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() == "attempt.lock" {
				return err
			}
			b, err := os.ReadFile(path)
			files[path] = string(b)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{ReadOnly: true, RequireStore: true})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		receipts, err := s.Receipts()
		if err != nil {
			t.Fatal(err)
		}
		return files, receipts
	}
	files, receipts := snapshot()
	for _, args := range [][]string{{"guild"}, {"guild", "--all"}, {"decisions"}, {"queue"}, {"task", "a-build"}, {"inspect", "app/a-q1"}} {
		if code, _, stderr := runClerk(t, dir, args...); code != 0 {
			t.Fatalf("%v: exit %d, stderr %s", args, code, stderr)
		}
		after, again := snapshot()
		if !slices.EqualFunc(receipts, again, func(a, b store.Receipt) bool { return a.Sequence == b.Sequence && a.MutationID == b.MutationID }) {
			t.Errorf("%v changed the receipts", args)
		}
		for path, body := range after {
			if files[path] != body {
				t.Errorf("%v changed or added %s", args, path)
			}
		}
		if len(after) != len(files) {
			t.Errorf("%v: %d files, want %d", args, len(after), len(files))
		}
	}
}

func TestClerkFormatting(t *testing.T) {
	for _, tc := range []struct {
		in   store.SpendTotal
		want string
	}{
		{store.NoSpend, "No spend recorded.\n"},
		{store.SpendTotal{Attempts: 3, Tokens: store.SpendMeasured, InputTokens: 10, OutputTokens: 20, CacheCreationInputTokens: 3, CacheReadInputTokens: 4,
			Cost: store.SpendEstimated, CostUSD: 1.234, CostUnknown: 1},
			"3 attempts: 10 input, 20 output, 3 cache-write, 4 cache-read tokens (measured); $1.23 (estimated, 1\n  unknown)\n"},
	} {
		if got := clerkSpend(tc.in); got != tc.want {
			t.Errorf("clerkSpend(%+v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for d, want := range map[time.Duration]string{30 * time.Second: "under 1m", 12 * time.Minute: "12m", 125 * time.Minute: "2h 5m", 76 * time.Hour: "3d 4h"} {
		if got := clerkDuration(d); got != want {
			t.Errorf("clerkDuration(%v) = %q, want %q", d, got, want)
		}
	}
	if got := clerkCut("abcdef", 4); got != "abc…" {
		t.Errorf("clerkCut = %q", got)
	}
	if got := clerkWrap("  "+strings.Repeat("word ", 30), "    "); !strings.HasPrefix(got, "  word") || !strings.Contains(got, "\n    word") {
		t.Errorf("clerkWrap = %q", got)
	}
	if clerkPRNumber("https://forge.invalid/app/pull/12") != "#12" || clerkPRNumber("") != "-" {
		t.Error("clerkPRNumber")
	}
}

func TestClerkErrors(t *testing.T) {
	dir := clerkFixture(t)
	outside := t.TempDir()
	if code, out, stderr := runClerk(t, outside, "guild"); code != 1 || out != "" ||
		stderr != "guildhall: clerk guild: no Guildhall copy found from "+outside+"; pass --copy <dir> or cd into a copy\n" {
		t.Errorf("outside a copy: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	under := filepath.Join(dir, "state", "app")
	if code, _, stderr := runClerk(t, under, "queue"); code != 1 || !strings.Contains(stderr, "no Guildhall copy found from "+under) {
		t.Errorf("under state/: exit %d, stderr %q", code, stderr)
	}
	unmarked := bareCopy(t)
	if code, _, stderr := runClerk(t, outside, "guild", "--copy", unmarked); code != 1 || !strings.Contains(stderr, "is not a Guildhall copy") {
		t.Errorf("--copy unmarked: exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(unmarked, copyMarkerFile)); !os.IsNotExist(err) {
		t.Errorf("--copy unmarked wrote a marker: %v", err)
	}
	if code, out, stderr := runClerk(t, outside, "guild", "--copy", dir); code != 0 || !strings.HasPrefix(out, "Guild, as of ") {
		t.Errorf("--copy marked: exit %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runClerk(t, dir, "task", "a-nope"); code != 1 || stderr != "guildhall: clerk task: no task a-nope in this copy\n" {
		t.Errorf("unknown id: exit %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runClerk(t, dir, "inspect", "a-new"); code != 1 ||
		stderr != "guildhall: clerk inspect: task a-new is in more than one repository; name one of app/a-new, lib/a-new\n" {
		t.Errorf("ambiguous id: exit %d, stderr %q", code, stderr)
	}
	if code, out, stderr := runClerk(t, dir, "task", "lib/a-new"); code != 0 || !strings.Contains(out, "a-new: Same id in lib") {
		t.Errorf("<repo>/<id>: exit %d, stderr %q, out %s", code, stderr, out)
	}
	for _, args := range [][]string{{}, {"nope"}, {"task"}, {"queue", "x"}, {"decisions", "--all"}} {
		if code, _, _ := runClerk(t, dir, args...); code != 2 {
			t.Errorf("%v: exit %d, want a usage error", args, code)
		}
	}

	old := clerkLockWait
	clerkLockWait = 50 * time.Millisecond
	t.Cleanup(func() { clerkLockWait = old })
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if code, out, stderr := runClerk(t, dir, "guild"); code != 1 || out != "" ||
		stderr != "guildhall: clerk guild: the store is busy (the Director is writing); try again in a moment\n" {
		t.Errorf("locked store: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
}

// TestDirectorStatusSlices: director status gives a multi-PR task a slices object, the current slice, the total and
// each slice's title, state and pull request, and gives a one-slice task none, so its JSON is unchanged.
func TestDirectorStatusSlices(t *testing.T) {
	dir := clerkFixture(t)
	st, err := readDirectorStatusConfig(t.Context(), filepath.Join(dir, "director.store"), filepath.Join(dir, "state"), "", func() time.Time { return clerkNow })
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, task := range st.Tasks {
		data, err := json.Marshal(task)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case task.ID == "a-sliced":
			seen = true
			out, err := json.Marshal(task.Slices)
			if err != nil {
				t.Fatal(err)
			}
			checkIndentedGolden(t, "director-status-slices.json", string(out))
		case strings.Contains(string(data), `"slices"`):
			t.Errorf("one-slice task %s/%s has slices: %s", task.Repository, task.ID, data)
		}
	}
	if !seen {
		t.Error("a-sliced is not in the status")
	}
}

// TestClerkKindRenamedHallmark: a task bound to the shipped hallmark workflow still reads hallmark once its
// repository's [repository.kinds] hallmark names another workflow, since Clerk reads the kind with interp.TaskKindOf
// (hallmark d90 H13).
func TestClerkKindRenamedHallmark(t *testing.T) {
	toml := strings.Replace(directorTOML, "\n[[repository]]\nid = \"lib\"", "\n[repository.kinds]\nhallmark = \"audit-v2\"\n\n[[repository]]\nid = \"lib\"", 1)
	dir := markCopy(t, filepath.Dir(writeDirector(t, toml)))
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	def, err := workflow.Hallmark()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask("m-1", store.Task{Repository: "app", ID: "h1", Title: "Review the whole", Workflow: def.Revision(), ConfigDigest: "sha256:" + strings.Repeat("c", 64)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runClerk(t, filepath.Join(dir, "director"), "task", "h1")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if !strings.Contains(out, "\nKind:             hallmark\n") {
		t.Fatalf("clerk task does not show Kind: hallmark:\n%s", out)
	}
}

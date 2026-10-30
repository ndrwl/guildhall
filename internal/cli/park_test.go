package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// TestStepParkAndUnpark: step park records a park and prints it, an identical park replays, a park with another
// reason while parked is refused quoting the prior one, unpark lifts it and a second unpark is refused, and a park
// after the unpark records anew. Neither command writes the task's next.json.
func TestStepParkAndUnpark(t *testing.T) {
	d := newRunDirector(t)
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		if _, err := s.StartAttempt("m-1", "app", "t-1", "plan"); err != nil {
			return store.Receipt{}, err
		}
		return s.FinishAttempt("m-2", "app", "t-1", "plan", 1, store.AttemptFailed)
	}); err != nil {
		t.Fatal(err)
	}
	next := `{"wait":"director","step":"plan","attempt":1}` + "\n"
	writeNext(t, d.state, "t-1", next)
	park := func(reason string) []string {
		return append([]string{"step", "park"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--reason", reason)...)
	}
	unpark := append([]string{"step", "unpark"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1")...)
	parkedLine := `{"repository":"app","task_id":"t-1","step":"plan","attempt":1,"reason":"Held until t-9 merges.","parked":true,"cycle":0}` + "\n"

	if code, out, stderr := d.run(t, unpark...); code != 1 || out != "" || !strings.Contains(stderr, "step unpark: attempt 1 of plan of app/t-1 is not parked") {
		t.Errorf("unpark before any park: exit %d, %q, stderr %s", code, out, stderr)
	}
	if code, out, stderr := d.run(t, park("Held until t-9 merges.")...); code != 0 || out != parkedLine {
		t.Fatalf("park: exit %d, %q, stderr %s; want %s", code, out, stderr, parkedLine)
	}
	before := lastSequence(t, d.store)
	if code, out, stderr := d.run(t, park("Held until t-9 merges.")...); code != 0 || out != parkedLine || lastSequence(t, d.store) != before {
		t.Errorf("identical park: exit %d, %q, stderr %s; want a replay writing nothing", code, out, stderr)
	}
	want := `step park: attempt 1 of plan of app/t-1 is already parked, with reason "Held until t-9 merges."; unpark it first`
	if code, out, stderr := d.run(t, park("Another reason.")...); code != 1 || out != "" || !strings.Contains(stderr, want) || lastSequence(t, d.store) != before {
		t.Errorf("park with another reason: exit %d, %q, stderr %s; want exit 1 naming %q", code, out, stderr, want)
	}
	unparkedLine := `{"repository":"app","task_id":"t-1","step":"plan","attempt":1,"reason":"Held until t-9 merges.","parked":false,"cycle":1}` + "\n"
	if code, out, stderr := d.run(t, unpark...); code != 0 || out != unparkedLine {
		t.Fatalf("unpark: exit %d, %q, stderr %s; want %s", code, out, stderr, unparkedLine)
	}
	if code, _, stderr := d.run(t, unpark...); code != 1 || !strings.Contains(stderr, "is not parked") {
		t.Errorf("second unpark: exit %d, stderr %s", code, stderr)
	}
	again := `{"repository":"app","task_id":"t-1","step":"plan","attempt":1,"reason":"Another reason.","parked":true,"cycle":1}` + "\n"
	if code, out, stderr := d.run(t, park("Another reason.")...); code != 0 || out != again {
		t.Errorf("park after unpark: exit %d, %q, stderr %s; want %s", code, out, stderr, again)
	}
	if b, err := os.ReadFile(filepath.Join(d.state, "app", "t-1", "next.json")); err != nil || string(b) != next {
		t.Errorf("next.json = %q, %v; want it unchanged", b, err)
	}
	// A succeeded attempt is refused by the store.
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		if _, err := s.StartAttempt("m-3", "app", "t-1", "build"); err != nil {
			return store.Receipt{}, err
		}
		return s.FinishAttempt("m-4", "app", "t-1", "build", 1, store.AttemptSucceeded)
	}); err != nil {
		t.Fatal(err)
	}
	succeeded := append([]string{"step", "park"}, d.flags("--task", "t-1", "--step", "build", "--attempt", "1", "--reason", "Hold.")...)
	if code, _, stderr := d.run(t, succeeded...); code != 1 || !strings.Contains(stderr, "only a failed or stopped attempt is parked") {
		t.Errorf("park of a succeeded attempt: exit %d, stderr %s", code, stderr)
	}
}

// TestStepParkUsage: the step usage lists park and unpark, and each refuses missing or malformed flags with exit 2.
func TestStepParkUsage(t *testing.T) {
	d := runDirector{store: "s", config: "c", state: "st"}
	if code, _, stderr := d.run(t, "step", "undo"); code != 2 || !strings.Contains(stderr, "step: the commands are done, finish, retry, send-back, park and unpark") {
		t.Errorf("step undo: exit %d, stderr %s", code, stderr)
	}
	if _, out, _ := d.run(t, "help"); !strings.Contains(out, "step park") || !strings.Contains(out, "step unpark") {
		t.Errorf("help does not list step park and unpark:\n%s", out)
	}
	for name, args := range map[string][]string{
		"park without store":          {"step", "park", "--config", "c", "--state", "st", "--repo", "app", "--task", "t-1", "--step", "plan", "--attempt", "1", "--reason", "r"},
		"park without reason":         append([]string{"step", "park"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1")...),
		"park with a blank reason":    append([]string{"step", "park"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--reason", " ")...),
		"park with a long reason":     append([]string{"step", "park"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--reason", strings.Repeat("r", store.MaxTextBytes+1))...),
		"park without attempt":        append([]string{"step", "park"}, d.flags("--task", "t-1", "--step", "plan", "--reason", "r")...),
		"park without step":           append([]string{"step", "park"}, d.flags("--task", "t-1", "--attempt", "1", "--reason", "r")...),
		"park with a bad id":          append([]string{"step", "park"}, d.flags("--task", "T 1", "--step", "plan", "--attempt", "1", "--reason", "r")...),
		"park with a bad number":      append([]string{"step", "park"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "0", "--reason", "r")...),
		"park with an argument":       append([]string{"step", "park"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--reason", "r", "extra")...),
		"unpark without task":         append([]string{"step", "unpark"}, d.flags("--step", "plan", "--attempt", "1")...),
		"unpark with a bad number":    append([]string{"step", "unpark"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "x")...),
		"unpark with a reason":        append([]string{"step", "unpark"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--reason", "r")...),
		"unpark with an unknown flag": append([]string{"step", "unpark"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--force")...),
	} {
		if code, out, _ := d.run(t, args...); code != 2 || out != "" {
			t.Errorf("%s: exit %d, %q; want 2", name, code, out)
		}
	}
}

// parkCopy is a Director copy whose app tasks each failed build attempt 1, with next.json as the engine leaves a
// failure unless a case says otherwise: t-1 parked; t-2 not parked; t-3 parked and then retried, its attempt 2
// failed unparked; t-4 parked while its next.json still waits on the attempt; t-5 parked while its next.json waits
// for the Director on a decision; t-6 parked while its next.json was held by a pause; t-7 parked and then
// cancelled; t-8 parked and then unparked.
func parkCopy(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	next := map[string]string{
		"t-3": `{"wait":"director","step":"build","attempt":2}`,
		"t-4": `{"wait":"attempt","step":"build","attempt":1}`,
		"t-5": `{"wait":"director","step":"build","attempt":1,"decision":"accept-plan"}`,
		"t-6": `{"wait":"paused","step":"build","attempt":1}`,
	}
	for _, id := range []string{"t-1", "t-2", "t-3", "t-4", "t-5", "t-6", "t-7", "t-8"} {
		steps := []func() (store.Receipt, error){
			func() (store.Receipt, error) {
				return s.CreateTask("create-"+id, store.Task{Repository: "app", ID: id, Title: "Park " + id, Workflow: def.Revision(),
					ConfigDigest: "sha256:" + strings.Repeat("c", 64)})
			},
			func() (store.Receipt, error) { return s.StartAttempt("start-"+id, "app", id, "build") },
			func() (store.Receipt, error) {
				return s.FinishAttempt("fail-"+id, "app", id, "build", 1, store.AttemptFailed)
			},
		}
		if id != "t-2" {
			steps = append(steps, func() (store.Receipt, error) {
				return s.ParkAttempt("park-"+id, "app", id, "build", 1, "Held for "+id+".", 0, nil)
			})
		}
		switch id {
		case "t-3":
			steps = append(steps, func() (store.Receipt, error) { return s.StartAttempt("retry-"+id, "app", id, "build") },
				func() (store.Receipt, error) {
					return s.FinishAttempt("fail-2-"+id, "app", id, "build", 2, store.AttemptFailed)
				})
		case "t-7":
			steps = append(steps, func() (store.Receipt, error) { return s.SetTaskState("cancel-"+id, "app", id, store.TaskCancelled) })
		case "t-8":
			steps = append(steps, func() (store.Receipt, error) { return s.UnparkAttempt("unpark-"+id, "app", id, "build", 1, 0) })
		}
		for _, step := range steps {
			if _, err := step(); err != nil {
				t.Fatalf("%s: %v", id, err)
			}
		}
		if err := os.MkdirAll(filepath.Join(state, "app", id), 0o755); err != nil {
			t.Fatal(err)
		}
		n, ok := next[id]
		if !ok {
			n = `{"wait":"director","step":"build","attempt":1}`
		}
		writeFile(t, filepath.Join(state, "app", id, "next.json"), n+"\n")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestDirectorStartStartupListsParkedAttempts: a new session's startup.md lists a parked attempt in its own
// section, as information, and not as in-flight work.
func TestDirectorStartStartupListsParkedAttempts(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "/exit")
	if code, out, stderr := runDirectorCmd(t, "start", "--copy", dir); code != 0 {
		t.Fatalf("first session: exit %d\n%s\n%s", code, out, stderr)
	}
	newTask(t, filepath.Join(dir, "director.store"), filepath.Join(dir, "config.toml"), "app", "Sort", "t-2")
	withTestStore(t, dir, func(s *store.Store) (store.Receipt, error) {
		if _, err := s.StartAttempt("start", "app", "t-2", "build"); err != nil {
			return store.Receipt{}, err
		}
		if _, err := s.FinishAttempt("fail", "app", "t-2", "build", 1, store.AttemptFailed); err != nil {
			return store.Receipt{}, err
		}
		return s.ParkAttempt("park", "app", "t-2", "build", 1, "Held until\napp/t-1 merges.", 0, nil)
	})
	writeNext(t, filepath.Join(dir, "state"), "t-2", `{"wait":"director","step":"build","attempt":1}`+"\n")
	if code, out, stderr := runDirectorCmd(t, "start", "fake", "--copy", dir); code != 0 {
		t.Fatalf("second session: exit %d\n%s\n%s", code, out, stderr)
	}
	b, err := os.ReadFile(filepath.Join(dir, "director", "session-2", render.StartupFile))
	if err != nil {
		t.Fatal(err)
	}
	startup := string(b)
	inFlight, parked, ok := strings.Cut(startup, "## Parked\n")
	if !ok {
		t.Fatalf("startup.md has no parked section:\n%s", startup)
	}
	want := "- parked: attempt 1 of build of app/t-2 failed; reason: Held until app/t-1 merges.; retry with `guildhall step retry --repo app --task t-2 --step build --attempt 1`\n"
	if !strings.Contains(parked, "held on purpose") || !strings.HasSuffix(parked, want) {
		t.Errorf("parked section = %q, want it to say the attempts are held on purpose and end with %q", parked, want)
	}
	if strings.Contains(inFlight, "t-2") {
		t.Errorf("in-flight section names the parked task:\n%s", inFlight)
	}
}

// TestDirectorStatusLeavesParkedAttemptsOut: a counting park drops its attempt's retry and director items from
// needs_me and lists it under parked; an unparked, retried or closed task's park does not count, and a lost
// advance, a director wait on a decision and a paused retry stay.
func TestDirectorStatusLeavesParkedAttemptsOut(t *testing.T) {
	dir := parkCopy(t)
	code, out, stderr := directorCmd(t, nil, "status", "--copy", dir, "--json")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	var st directorStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	var items []string
	for _, it := range st.NeedsMe {
		items = append(items, fmt.Sprintf("%s %s %d", it.Kind, it.TaskID, it.Attempt))
	}
	want := []string{"advance t-4 1", "retry t-2 1", "retry t-3 2", "retry t-8 1",
		"director t-2 0", "director t-3 0", "director t-5 0", "director t-8 0", "paused t-6 0"}
	if strings.Join(items, ", ") != strings.Join(want, ", ") {
		t.Errorf("needs_me = %v\nwant %v", items, want)
	}
	var parked []string
	for _, p := range st.Parked {
		parked = append(parked, p.TaskID)
		if p.Repository != "app" || p.Step != "build" || p.Attempt != 1 || p.Outcome != store.AttemptFailed || p.Reason != "Held for "+p.TaskID+"." ||
			p.ParkedAt == nil || p.ParkedAt.IsZero() || p.Command != "guildhall step retry --repo app --task "+p.TaskID+" --step build --attempt 1" {
			t.Errorf("parked entry %+v", p)
		}
	}
	if got := strings.Join(parked, " "); got != "t-1 t-4 t-5 t-6" {
		t.Errorf("parked = %s, want t-1 t-4 t-5 t-6", got)
	}
	for _, task := range st.Tasks {
		for _, step := range task.Steps {
			want := task.ID == "t-1" || task.ID == "t-4" || task.ID == "t-5" || task.ID == "t-6"
			if step.Parked != want {
				t.Errorf("%s step %+v: parked = %v, want %v", task.ID, step, step.Parked, want)
			}
		}
	}
}

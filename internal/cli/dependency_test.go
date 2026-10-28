package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

// newDependentDirector is the run Director with app/t-2 depending on the open app/t-1.
func newDependentDirector(t *testing.T) runDirector {
	t.Helper()
	d := newRunDirector(t)
	newTask(t, d.store, d.config, "app", "Sort the archive too", "t-2", "app/t-1")
	return d
}

// readNextFile reads a task's next.json.
func readNextFile(t *testing.T, d runDirector, task string) next {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(d.state, "app", task, nextFile))
	if err != nil {
		t.Fatal(err)
	}
	return decodeJSON[next](t, string(b))
}

// TestTaskStartHoldsADependentTask: task start of a task whose dependency is open records nothing, writes
// "wait":"dependencies" to next.json, sends one dependency-held message and lists a dependencies item in director
// status, after a task waiting for checks; the dependency watcher's list selects it.
func TestTaskStartHoldsADependentTask(t *testing.T) {
	d := newDependentDirector(t)
	for range 2 {
		code, stdout, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-2")...)...)
		if code != 0 {
			t.Fatalf("task start: exit %d, %s %s", code, stdout, stderr)
		}
		if got := decodeJSON[next](t, stdout); got != (next{Wait: "dependencies", Step: "plan"}) {
			t.Fatalf("task start printed %+v", got)
		}
	}
	if got := readNextFile(t, d, "t-2"); got != (next{Wait: "dependencies", Step: "plan"}) {
		t.Fatalf("next.json = %+v", got)
	}
	attempts, err := withStore(d.store, true, func(s *store.Store) ([]store.StageAttempt, error) { return s.Attempts("app", "t-2") })
	if err != nil || len(attempts) != 0 {
		t.Fatalf("attempts = %+v, %v; want none", attempts, err)
	}
	unread, err := withStore(d.store, true, func(s *store.Store) (map[string]int, error) { return s.DirectorUnread() })
	if err != nil || unread[store.MessageDependencyHeld] != 1 {
		t.Fatalf("unread = %v, %v; want one dependency-held", unread, err)
	}

	writeNext(t, d.state, "t-1", `{"wait":"checks","step":"validate"}`+"\n")
	st, err := readDirectorStatus(context.Background(), d.store, d.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	var item needsMeItem
	for _, it := range st.NeedsMe {
		kinds = append(kinds, it.Kind)
		if it.Kind == dependenciesKind {
			item = it
		}
	}
	if i, j := slices.Index(kinds, checksKind), slices.Index(kinds, dependenciesKind); i < 0 || j != i+1 || j != len(kinds)-1 {
		t.Errorf("needs_me kinds = %v, want dependencies last, after checks", kinds)
	}
	if item.TaskID != "t-2" || item.Step != "plan" || item.Command != "guildhall task start --repo app t-2" || !strings.Contains(item.Reason, "plan") {
		t.Errorf("dependencies item = %+v", item)
	}
	if n := waitingKinds(st.NeedsMe); n[dependenciesKind] != 0 {
		t.Errorf("waitingKinds counts the dependencies item as the Director's: %v", n)
	}

	got, err := dependenciesWaiting(d.store, d.state)()
	if err != nil || !slices.Equal(got, []taskRef{{"app", "t-2"}}) {
		t.Errorf("dependenciesWaiting = %v, %v; want app/t-2", got, err)
	}
	if got, err := checksWaiting(d.store, d.state)(); err != nil || !slices.Equal(got, []taskRef{{"app", "t-1"}}) {
		t.Errorf("checksWaiting = %v, %v; want only app/t-1", got, err)
	}

	// A done task adds no such item.
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.SetTaskState("cancel-t-2", "app", "t-2", store.TaskCancelled)
	}); err != nil {
		t.Fatal(err)
	}
	st, err = readDirectorStatus(context.Background(), d.store, d.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range st.NeedsMe {
		if it.Kind == dependenciesKind {
			t.Errorf("a cancelled task is listed: %+v", it)
		}
	}
	if got, err := dependenciesWaiting(d.store, d.state)(); err != nil || len(got) != 0 {
		t.Errorf("dependenciesWaiting after cancelling = %v, %v", got, err)
	}
}

// TestTaskStartIgnoreDeps: task start --ignore-deps --reason records the override, shown with its reason in task
// timeline, and the task passes the gate, here to the pause hold; repeating it with the same reason replays it, and
// another reason is refused as already overridden.
func TestTaskStartIgnoreDeps(t *testing.T) {
	d := newDependentDirector(t)
	recordSession(t, d.store, false, store.DirectorRunning, store.DirectorPaused)
	const reason = "t-2 touches none of t-1's files."
	for range 2 {
		code, stdout, stderr := d.run(t, append([]string{"task", "start", "--ignore-deps", "--reason", reason}, d.flags("t-2")...)...)
		if code != 0 {
			t.Fatalf("task start --ignore-deps: exit %d, %s %s", code, stdout, stderr)
		}
		if got := decodeJSON[next](t, stdout); got != (next{Wait: "paused", Step: "plan"}) {
			t.Fatalf("task start --ignore-deps printed %+v, want the gate passed and the pause hold", got)
		}
	}
	code, out, stderr := runTask(t, "timeline", "--store", d.store, "--repo", "app", "--json", "t-2")
	if code != 0 {
		t.Fatalf("task timeline: exit %d, %s", code, stderr)
	}
	tl := decodeJSON[taskTimeline](t, out)
	if o := tl.DependencyOverride; o == nil || o.Reason != reason || o.OverriddenAt == nil {
		t.Errorf("timeline dependency_override = %+v, want the reason and its time", o)
	}
	if !slices.ContainsFunc(tl.Events, func(r store.Receipt) bool { return r.Operation == "override-dependencies" }) {
		t.Errorf("timeline events omit the override: %+v", tl.Events)
	}

	code, _, stderr = d.run(t, append([]string{"task", "start", "--ignore-deps", "--reason", "Another reason."}, d.flags("t-2")...)...)
	if code == 0 || !strings.Contains(stderr, "already overridden") {
		t.Errorf("a second override with another reason: exit %d, %s", code, stderr)
	}
	// Without the flag the override still stands.
	code, stdout, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-2")...)...)
	if code != 0 || decodeJSON[next](t, stdout) != (next{Wait: "paused", Step: "plan"}) {
		t.Errorf("task start after the override: exit %d, %s %s", code, stdout, stderr)
	}
}

// TestTaskStartIgnoreDepsRefusals: malformed flags are usage errors, and a closed task, one with no dependencies
// and new work while the session recovers are refused, recording no override.
func TestTaskStartIgnoreDepsRefusals(t *testing.T) {
	d := newDependentDirector(t)
	newTask(t, d.store, d.config, "app", "Closed", "t-3", "app/t-1")
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.SetTaskState("cancel-t-3", "app", "t-3", store.TaskCancelled)
	}); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		args []string
		code int
		want string
	}{
		"no reason":        {[]string{"--ignore-deps", "t-2"}, 2, "--reason"},
		"empty reason":     {[]string{"--ignore-deps", "--reason", " ", "t-2"}, 2, "--reason"},
		"reason alone":     {[]string{"--reason", "why", "t-2"}, 2, "--ignore-deps"},
		"oversized reason": {[]string{"--ignore-deps", "--reason", strings.Repeat("r", store.MaxTextBytes+1), "t-2"}, 2, "limit"},
		"closed task":      {[]string{"--ignore-deps", "--reason", "why", "t-3"}, 1, "cancelled"},
		"no dependencies":  {[]string{"--ignore-deps", "--reason", "why", "t-1"}, 1, "no dependencies"},
	} {
		code, _, stderr := d.run(t, append([]string{"task", "start"}, d.flags(tc.args...)...)...)
		if code != tc.code || !strings.Contains(stderr, tc.want) {
			t.Errorf("%s: exit %d, %s; want exit %d naming %q", name, code, stderr, tc.code, tc.want)
		}
	}
	recordSession(t, d.store, false)
	if code, _, stderr := d.run(t, append([]string{"task", "start", "--ignore-deps", "--reason", "why"}, d.flags("t-2")...)...); code == 0 {
		t.Errorf("an override while recovering: exit 0, %s", stderr)
	}
	for _, task := range []string{"t-1", "t-2", "t-3"} {
		h, err := withStore(d.store, true, func(s *store.Store) (store.TaskHistory, error) { return s.TaskHistory("app", task) })
		if err != nil || h.DependencyOverride != nil {
			t.Errorf("%s: a refused override was recorded: %+v, %v", task, h.DependencyOverride, err)
		}
	}
}

// TestDependencyOverrideMutationFits: the override's mutation id is dependency-override.<repo>.<task> and fits the
// store's 128 bytes for the longest ids.
func TestDependencyOverrideMutationFits(t *testing.T) {
	if got := dependencyOverrideMutation("app", "t-2"); got != "dependency-override.app.t-2" {
		t.Errorf("mutation id = %q", got)
	}
	long := "a" + strings.Repeat("b", 99)
	if got := dependencyOverrideMutation(long, long); len(got) > 128 || !strings.HasPrefix(got, "dependency-override.") {
		t.Errorf("long mutation id = %q (%d bytes)", got, len(got))
	}
}

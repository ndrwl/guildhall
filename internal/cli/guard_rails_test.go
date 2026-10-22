package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
)

// lastSequence is the store's newest receipt sequence, so a test can see that a refusal wrote nothing.
func lastSequence(t *testing.T, path string) uint64 {
	t.Helper()
	v, err := withStore(path, true, func(s *store.Store) (store.DirectorView, error) { return s.DirectorView() })
	if err != nil {
		t.Fatal(err)
	}
	return v.AsOf.Sequence
}

// TestUnlistedRepositoryRefusedByEveryCommand: every command that acts on a repository refuses one the config
// does not list, naming the repositories guard rail, and writes nothing.
func TestUnlistedRepositoryRefusedByEveryCommand(t *testing.T) {
	d := newRunDirector(t)
	// A task of an unlisted repository, recorded behind the config's back, so only the rail refuses it.
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		task, err := s.Task("app", "t-1")
		if err != nil {
			return store.Receipt{}, err
		}
		task.Repository, task.ID = "other", "t-9"
		return s.CreateTask("other-task", task)
	}); err != nil {
		t.Fatal(err)
	}
	flags := []string{"--store", d.store, "--config", d.config, "--state", d.state, "--repo", "other"}
	note := `{"schema":"guildhall.message/1","id":"n-other","recipient":"director","repository":"other","task":"t-9","type":"note","text":"Hello."}`
	for _, tc := range []struct {
		name  string
		args  []string
		stdin string
	}{
		{"task new", append([]string{"task", "new", "--title", "Elsewhere"}, append(flags[:4:4], "--repo", "other", "t-10")...), ""},
		{"task start", append([]string{"task", "start"}, append(flags, "t-9")...), ""},
		{"step done", append([]string{"step", "done"}, append(flags, "--task", "t-9", "--step", "plan", "--attempt", "1", "--result", "result.json")...), ""},
		{"step finish", append([]string{"step", "finish"}, append(flags, "--task", "t-9", "--step", "grill", "--attempt", "1", "--summary", "Abandoned.", "--failed")...), ""},
		{"step retry", append([]string{"step", "retry"}, append(flags, "--task", "t-9", "--step", "plan", "--attempt", "1")...), ""},
		{"messages send", []string{"messages", "send", "--store", d.store, "--config", d.config, "director", "-"}, note},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := lastSequence(t, d.store)
			app, stdout, stderr := newTestApp(nil, nil)
			app.Stdin = strings.NewReader(tc.stdin)
			if code := app.Run(tc.args); code != 1 || !strings.Contains(stderr.String(), `guard rail repositories: the config does not list repository "other"`) {
				t.Fatalf("exit %d, stdout %s, stderr %s; want exit 1 naming the repositories guard rail", code, stdout, stderr)
			}
			if after := lastSequence(t, d.store); after != before {
				t.Errorf("a refusal wrote the store: sequence %d, then %d", before, after)
			}
		})
	}
}

// TestRunningCapRefusesTheFifthLaunch: with the default caps, task start refuses a launch while four attempts run,
// exiting 1 naming the rail and telling the Director once, and launches it once one finishes.
func TestRunningCapRefusesTheFifthLaunch(t *testing.T) {
	d := newRunDirector(t)
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		task, err := s.Task("app", "t-1")
		if err != nil {
			return store.Receipt{}, err
		}
		task.ID = "t-2"
		if _, err := s.CreateTask("t-2", task); err != nil {
			return store.Receipt{}, err
		}
		for i := range config.DefaultMaxRunningAttempts {
			if _, err := s.StartAttempt(fmt.Sprintf("occupy-%d", i), "app", "t-2", fmt.Sprintf("step-%d", i)); err != nil {
				return store.Receipt{}, err
			}
		}
		return store.Receipt{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	start := append([]string{"task", "start"}, d.flags("t-1")...)
	for range 2 {
		code, out, stderr := d.run(t, start...)
		if code != 1 || out != "" || !strings.Contains(stderr, "guard rail "+config.MaxRunningAttemptsKey) {
			t.Fatalf("fifth launch: exit %d, %q, stderr %s; want exit 1 naming guard rail %s", code, out, stderr, config.MaxRunningAttemptsKey)
		}
	}
	if got := d.attempts(t); len(got) != 0 {
		t.Fatalf("a refused launch recorded %+v", got)
	}
	refused, err := withStore(d.store, true, func(s *store.Store) ([]store.Message, error) {
		inbox, _, err := s.Inbox(store.Recipient{Role: store.RecipientDirector}, 0, 100)
		if err != nil {
			return nil, err
		}
		var out []store.Message
		for _, m := range inbox {
			if m.Type == store.MessageBudgetRefused {
				out = append(out, m)
			}
		}
		return out, nil
	})
	if err != nil || len(refused) != 1 || refused[0].TaskID != "t-1" {
		t.Fatalf("budget-refused messages = %+v, %v; want one for t-1", refused, err)
	}

	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.FinishAttempt("finish-occupied", "app", "t-2", "step-0", 1, store.AttemptFailed)
	}); err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := d.run(t, start...); code != 0 || out != `{"wait":"attempt","step":"plan","attempt":1}`+"\n" {
		t.Fatalf("launch after one finished: exit %d, %q, stderr %s", code, out, stderr)
	}
}

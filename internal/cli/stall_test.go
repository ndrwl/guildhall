package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestTaskStartObservesAStalledWorker: an advance over a config that leaves stall_interval at its default
// reads a launched attempt's heartbeat and sends the Director one worker-stalled message for a heartbeat
// that is fresh with output older than 15 minutes; one setting "0s" sends nothing (docs/running.md).
// The attempt's launcher is killed after launch so the hand-written heartbeat is the only one.
func TestTaskStartObservesAStalledWorker(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "run"))); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(dir, "baseline", "stages", "plan.md")
	guide, err := os.ReadFile(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan, append(guide, "guildhall-fake: result=never\nguildhall-fake: lifetime=2m\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	off := filepath.Join(dir, "no-stall.toml")
	if err := os.WriteFile(off, bytes.Replace(cfg, []byte("standards_revision"), []byte("stall_interval = \"0s\"\nstandards_revision"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	d := newRunDirectorAt(t, filepath.Join(dir, "config.toml"))
	start := func(config string) {
		t.Helper()
		e := d
		e.config = config
		if code, out, stderr := e.run(t, append([]string{"task", "start"}, e.flags("t-1")...)...); code != 0 || out != `{"wait":"attempt","step":"plan","attempt":1}`+"\n" {
			t.Fatalf("task start --config %s: exit %d, %q, stderr %s", filepath.Base(config), code, out, stderr)
		}
	}
	start(d.config)
	key := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1}
	s, err := store.Open(d.store, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.Ownership(key.Repository, key.TaskID, key.Step, key.Attempt)
	_ = s.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := artisantest.KillVerified(artisan.Identity(o.LauncherIdentity)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Sweep the orphaned worker so the cleanup's stop proves the attempt not running.
		_, _, _ = d.run(t, "artisan", "resume", "--store", d.store, "--state", d.state, "--repo", "app", "--task", "t-1", "--step", "plan", "--attempt", "1")
	})
	hb, err := artisan.ReadHeartbeat(key.Dir(d.state))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	hb.Sequence++
	hb.Time, hb.LastOutputAt, hb.WorkerState = now, now.Add(-20*time.Minute), "running"
	if err := artisan.WriteHeartbeat(key.Dir(d.state), hb); err != nil {
		t.Fatal(err)
	}
	start(off)
	if got := d.stalls(t); got != 0 {
		t.Errorf("stall_interval 0s: %d worker-stalled messages, want 0", got)
	}
	for range 2 {
		start(d.config)
		if got := d.stalls(t); got != 1 {
			t.Errorf("default stall_interval: %d worker-stalled messages, want 1", got)
		}
	}
}

// stalls counts the worker-stalled messages in the Director's inbox.
func (d runDirector) stalls(t *testing.T) int {
	t.Helper()
	s, err := store.Open(d.store, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	msgs, _, err := s.Inbox(store.Recipient{Role: store.RecipientDirector}, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range msgs {
		if m.Type == "worker-stalled" {
			n++
		}
	}
	return n
}

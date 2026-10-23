package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
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
	if err := os.WriteFile(plan, append(guide, "guildhall-fake: result=never\nguildhall-fake: lifetime=1m\n"...), 0o600); err != nil {
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
	// Kill the launcher only once its worker runs: on Windows the worker starts suspended and joins the
	// launcher's Job Object before it resumes, so a launcher killed in between leaves it suspended outside any job.
	var o store.Ownership
	for end := time.Now().Add(testenv.Ceiling(time.Minute)); ; time.Sleep(20 * time.Millisecond) {
		s, err := store.Open(d.store, store.Options{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		o, err = s.Ownership(key.Repository, key.TaskID, key.Step, key.Attempt)
		_ = s.Close()
		if err != nil {
			t.Fatal(err)
		}
		if o.State == store.OwnerRunning && o.Containment.Worker != (store.ProcessIdentity{}) {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("plan-1 worker never ran: ownership %+v", o)
		}
	}
	if err := artisantest.KillVerified(artisan.Identity(o.LauncherIdentity)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Sweep the orphaned worker so the cleanup proves the attempt not running and its worker tree empty;
		// the sweep settles the attempt, so the recorded worker itself must be proven gone here.
		d.sweep(t, "plan", 1)
		worker := artisan.Identity(o.Containment.Worker)
		lv, err := artisan.Probe(worker)
		for end := time.Now().Add(testenv.Ceiling(10 * time.Second)); err == nil && lv != artisan.Gone && time.Now().Before(end); {
			time.Sleep(50 * time.Millisecond)
			lv, err = artisan.Probe(worker)
		}
		if err != nil || lv != artisan.Gone {
			t.Errorf("plan-1 worker %d is %v (%v)", worker.PID, lv, err)
		}
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
	// The engine's outbox wakes the Director for the stall once, as Guildhall; with no session it queues.
	if ws := readWakes(t, d.store); len(ws) != 1 || ws[0].SenderType != "guildhall" || ws[0].Decision != store.WakeQueued || ws[0].Reason != "no session" {
		t.Errorf("wakes = %+v; want one queued guildhall wake", ws)
	}
}

// TestTheLauncherSendsAStallWithoutAnAdvance: with stall_interval at 2s, a launched worker that stays silent gets
// one worker-stalled message from its own launcher, with nothing advancing the task meanwhile, under the id the
// engine derives for the same crossing, and the launcher wakes the Director for it as Guildhall; a later
// advance observing the same heartbeat replays it and sends nothing new.
func TestTheLauncherSendsAStallWithoutAnAdvance(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "run"))); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(dir, "baseline", "stages", "plan.md")
	guide, err := os.ReadFile(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan, append(guide, "guildhall-fake: result=never\nguildhall-fake: lifetime=1m\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, bytes.Replace(b, []byte("standards_revision"), []byte("stall_interval = \"2s\"\nstandards_revision"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	d := newRunDirectorAt(t, cfg)
	start := func() {
		t.Helper()
		if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 || out != `{"wait":"attempt","step":"plan","attempt":1}`+"\n" {
			t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
		}
	}
	start()
	t.Cleanup(func() { d.stopAll(t) })
	for end := time.Now().Add(testenv.Ceiling(time.Minute)); d.stalls(t) == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("the launcher sent no worker-stalled message within %v", testenv.Ceiling(time.Minute))
		}
	}
	key := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1}
	hb, err := artisan.ReadHeartbeat(key.Dir(d.state))
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(d.store, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	msgs, _, err := s.Inbox(store.Recipient{Role: store.RecipientDirector}, 0, 100)
	_ = s.Close()
	if err != nil {
		t.Fatal(err)
	}
	sent, err := interp.StalledMessage(key, hb.LastOutputAt)
	if err != nil {
		t.Fatal(err)
	}
	if m := msgs[len(msgs)-1]; m.ID != sent.ID {
		t.Fatalf("stall %s, want the engine's id for output at %s, %s", m.ID, hb.LastOutputAt, sent.ID)
	}
	start()
	if got := d.stalls(t); got != 1 {
		t.Errorf("after an advance: %d worker-stalled messages, want 1", got)
	}
	if ws := readWakes(t, d.store); len(ws) != 1 || ws[0].SenderType != "guildhall" || ws[0].Decision != store.WakeQueued {
		t.Errorf("wakes = %+v; want one queued guildhall wake", ws)
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

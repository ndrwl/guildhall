package artisan_test

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// startedProcess starts a test-binary worker outside any unit and returns
// its recorded identity; cleanup releases and reaps it.
func startedProcess(t *testing.T) (artisan.Identity, *exec.Cmd) {
	t.Helper()
	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	cmd := artisantest.Command(dir, artisantest.Worker{Release: release, Lifetime: time.Minute})
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0o600)
		_ = cmd.Wait()
	})
	for end := time.Now().Add(testenv.Ceiling(10 * time.Second)); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if inv, _ := artisantest.Inventory(dir); len(inv) == 1 {
			return inv[0].Identity, cmd
		}
	}
	t.Fatal("worker did not record its identity")
	return artisan.Identity{}, nil
}

func waitGone(t *testing.T, ids ...artisan.Identity) {
	t.Helper()
	for _, id := range ids {
		lv, err := artisan.Probe(id)
		for end := time.Now().Add(testenv.Ceiling(10 * time.Second)); lv != artisan.Gone && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			lv, err = artisan.Probe(id)
		}
		if lv != artisan.Gone {
			t.Errorf("%v is %v (%v), want gone", id, lv, err)
		}
	}
}

func TestKill(t *testing.T) {
	id, cmd := startedProcess(t)
	if err := artisan.Kill(artisan.Identity{PID: id.PID, Token: id.Token + "x"}); err != nil {
		t.Fatalf("Kill of a reused PID = %v", err)
	}
	if lv, err := artisan.Probe(id); lv != artisan.Alive {
		t.Fatalf("Kill of another token ended the process: %v, %v", lv, err)
	}
	if err := artisan.Kill(id); err != nil {
		t.Fatalf("Kill = %v", err)
	}
	// Repeat before waiting: termination is asynchronous on Windows, and
	// another cleanup caller can encounter the process while it exits.
	if err := artisan.Kill(id); err != nil {
		t.Errorf("Kill during termination = %v", err)
	}
	waitGone(t, id)
	_ = cmd.Wait()
	if err := artisan.Kill(id); err != nil {
		t.Errorf("Kill of a gone process = %v", err)
	}
}

func TestSweepKillsARecordedUnitFromOutside(t *testing.T) {
	sentinel, _ := startedProcess(t)
	u, err := artisan.NewUnit("sweep-" + rand.Text()[:8])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	dir := t.TempDir()
	worker, err := u.Start(artisantest.Command(dir, artisantest.Worker{Grandchildren: 1, IgnoreInterrupt: true, Lifetime: time.Minute}))
	if err != nil {
		t.Fatal(err)
	}
	var tree []artisan.Identity
	for end := time.Now().Add(testenv.Ceiling(10 * time.Second)); len(tree) < 2 && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		inv, _ := artisantest.Inventory(dir)
		tree = tree[:0]
		for _, e := range inv {
			tree = append(tree, e.Identity)
		}
	}
	if len(tree) != 2 {
		t.Fatalf("worker tree %v, want the worker and one grandchild", tree)
	}
	c := u.Containment()
	swept, err := artisan.Sweep(c, worker)
	if err != nil {
		t.Fatalf("Sweep = %v", err)
	}
	for _, id := range tree {
		if !slices.Contains(swept, id) {
			t.Errorf("Sweep returned %v, which does not name %v", swept, id)
		}
	}
	var left []artisan.Identity
	for end := time.Now().Add(testenv.Ceiling(10 * time.Second)); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if left, err = artisan.Contained(c, worker); err == nil && len(left) == 0 {
			break
		}
		_, _ = artisan.Sweep(c, worker)
	}
	if err != nil || len(left) != 0 {
		t.Fatalf("Contained after Sweep = %v, %v; want empty", left, err)
	}
	waitGone(t, tree...)
	if lv, err := artisan.Probe(sentinel); lv != artisan.Alive {
		t.Errorf("the unrelated sentinel is %v (%v) after the sweep", lv, err)
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	if swept, err := artisan.Sweep(c, worker); err != nil || len(swept) != 0 {
		t.Errorf("Sweep of a closed unit = %v, %v; want nothing", swept, err)
	}
}

func TestSweepRefusesAnUnknownKind(t *testing.T) {
	if _, err := artisan.Sweep(artisan.Containment{Kind: "other"}, artisan.Identity{PID: 1, Token: "t"}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Sweep of an unknown kind = %v, want errors.ErrUnsupported", err)
	}
}

func TestSettle(t *testing.T) {
	cases := []struct {
		name           string
		marker, result bool
		want           artisan.Outcome
	}{
		{"nothing", false, false, artisan.Outcome{State: artisan.Failed, Detail: "launcher exited"}},
		{"result", false, true, artisan.Outcome{State: artisan.Finished}},
		{"marker", true, false, artisan.Outcome{State: artisan.Cancelled}},
		{"marker and late result", true, true, artisan.Outcome{State: artisan.Cancelled, LateResultRejected: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			f := artisantest.NewFiles(dir)
			if tc.marker {
				if err := f.Mark(); err != nil {
					t.Fatal(err)
				}
			}
			if tc.result {
				if err := os.WriteFile(f.Result, []byte(artisantest.ResultBody), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := artisan.Settle(dir, f, "launcher exited")
			if err != nil {
				t.Fatalf("Settle = %v", err)
			}
			if got.Schema != artisan.OutcomeSchema || got.State != tc.want.State || got.LateResultRejected != tc.want.LateResultRejected || got.Rung != 0 || len(got.Survivors) != 0 {
				t.Errorf("Settle = %+v, want %+v", got, tc.want)
			}
			if tc.want.Detail != "" && got.Detail != tc.want.Detail {
				t.Errorf("detail %q, want %q", got.Detail, tc.want.Detail)
			}
			file, err := artisan.ReadOutcome(dir)
			if err != nil || file.State != got.State || file.LateResultRejected != got.LateResultRejected {
				t.Errorf("outcome file %+v (%v) does not record %+v", file, err, got)
			}
			if m, _ := f.Marked(); m != tc.marker {
				t.Errorf("marker present %v after Settle, want %v", m, tc.marker)
			}
			if !logged(t, dir, "settled") {
				t.Error("no settled event")
			}
			again, err := artisan.Settle(dir, f, "other")
			if err != nil || again.State != got.State || again.Detail != got.Detail {
				t.Errorf("second Settle = %+v, %v; want the first decision %+v", again, err, got)
			}
		})
	}
	t.Run("an unreadable outcome is not replaced", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, artisan.OutcomeFile)
		if err := os.WriteFile(p, []byte("garbage"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := artisan.Settle(dir, artisantest.NewFiles(dir), "x"); !errors.Is(err, artisan.ErrInvalidOutcome) {
			t.Errorf("Settle over a damaged outcome = %v, want ErrInvalidOutcome", err)
		}
		if b, _ := os.ReadFile(p); string(b) != "garbage" {
			t.Errorf("damaged outcome replaced with %q", b)
		}
	})
}

// logged reports whether the attempt's events file has an event of kind.
func logged(t *testing.T, dir, kind string) bool {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, artisan.EventsFile))
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	for s := bufio.NewScanner(f); s.Scan(); {
		var e struct{ Event string }
		if json.Unmarshal(s.Bytes(), &e) == nil && e.Event == kind {
			return true
		}
	}
	return false
}

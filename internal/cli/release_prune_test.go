package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// tickingClock gives the fixture's App a clock that moves on a minute at each read, so releases installed one
// after another have distinct install times, newest last.
func (f *pinFixture) tickingClock() time.Time {
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	f.app.Now = func() time.Time {
		at = at.Add(time.Minute)
		return at
	}
	return at
}

// secondCopy is another standalone copy with a store, sharing the fixture's Guildhall home.
func (f *pinFixture) secondCopy() string {
	f.t.Helper()
	dir := bareCopy(f.t)
	init, _, stderr := newTestApp(nil, nil)
	if code := init.Run([]string{"store", "init", filepath.Join(dir, "director.store")}); code != 0 {
		f.t.Fatalf("store init: exit %d, stderr %s", code, stderr)
	}
	return dir
}

func (f *pinFixture) prunes() []store.ReleasePruned {
	f.t.Helper()
	got, err := withStore(filepath.Join(f.copy, "director.store"), true, func(s *store.Store) ([]store.ReleasePruned, error) { return s.ReleasePrunes() })
	if err != nil {
		f.t.Fatal(err)
	}
	return got
}

// TestReleaseRollback: rollback moves the pin to the previous version of the newest release-pin event, with source
// rollback, and a second rollback undoes the first; a first pin, a missing or damaged previous release and a
// managed copy are refused, leaving the pin.
func TestReleaseRollback(t *testing.T) {
	f := newPinFixture(t)
	f.tickingClock()
	h := release.Home{Dir: f.home, GOOS: runtime.GOOS}
	if code := f.run("release", "rollback", "--copy", f.copy); code != 1 || !strings.Contains(f.stderr.String(), "no release pin to roll back") {
		t.Fatalf("rollback with no pin = %d, %q", code, f.stderr)
	}
	if code := f.run("release", "adopt", "--copy", f.copy); code != 0 {
		t.Fatalf("release adopt = %d", code)
	}
	if code := f.run("release", "rollback", "--copy", f.copy); code != 1 || !strings.Contains(f.stderr.String(), "its first pin") {
		t.Fatalf("rollback of a first pin = %d, %q", code, f.stderr)
	}
	sum := f.install("v1.1.0", "release v1.1.0", 1)
	if code := f.run("release", "use", "v1.1.0", "--copy", f.copy); code != 0 {
		t.Fatalf("release use = %d", code)
	}
	adopted, err := h.Runnable("v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if code := f.run("release", "rollback", "--copy", f.copy); code != 0 || !strings.Contains(f.stdout.String(), "pins v1.0.0") || !strings.Contains(f.stdout.String(), "was v1.1.0") {
		t.Fatalf("rollback = %d, %q", code, f.stdout)
	}
	pins := f.pins()
	if len(pins) != 3 || pins[2].Version != "v1.0.0" || pins[2].SHA256 != adopted.SHA256 || pins[2].Source != store.PinRollback || pins[2].PreviousVersion != "v1.1.0" {
		t.Fatalf("pins %+v, want a rollback to v1.0.0 from v1.1.0", pins)
	}
	if pin, _ := release.ReadPin(f.copy); pin != "v1.0.0" {
		t.Fatalf("release.pin = %q after rollback", pin)
	}
	if code := f.run("release", "rollback", "--copy", f.copy); code != 0 {
		t.Fatalf("second rollback = %d", code)
	}
	if pins := f.pins(); len(pins) != 4 || pins[3].Version != "v1.1.0" || pins[3].SHA256 != sum || pins[3].Source != store.PinRollback {
		t.Fatalf("pins %+v, want the second rollback back to v1.1.0", pins)
	}

	// A damaged previous release refuses, and the pin stays.
	if err := os.WriteFile(h.Binary("v1.0.0"), []byte("damaged"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := f.run("release", "rollback", "--copy", f.copy); code != 1 || !strings.Contains(f.stderr.String(), "damaged") || !strings.Contains(f.stderr.String(), "release install v1.0.0") {
		t.Fatalf("rollback to a damaged release = %d, %q", code, f.stderr)
	}
	if pin, _ := release.ReadPin(f.copy); pin != "v1.1.0" || len(f.pins()) != 4 {
		t.Fatalf("a refused rollback moved the pin to %q", pin)
	}

	// A managed copy rolls back by rollout.
	writeFile(t, filepath.Join(f.copy, managedFile), "")
	if code := f.run("release", "rollback", "--copy", f.copy); code != 1 || !strings.Contains(f.stderr.String(), "is managed") || !strings.Contains(f.stderr.String(), "rollout") {
		t.Fatalf("rollback of a managed copy = %d, %q", code, f.stderr)
	}
}

// TestReleasePrune: prune keeps the newest three installed releases and every older one any inventoried copy
// holds, by its pin, its rollback target or an active task's record, and status gives each kept one's reasons; an
// older release nothing holds is removed and recorded as release-pruned in the copy's store, as are stale
// .incoming/ entries; a copy whose pin cannot be read refuses the prune, removing nothing; and copy A's prune of
// its own pin change leaves copy B's pin unchanged.
func TestReleasePrune(t *testing.T) {
	f := newPinFixture(t)
	f.tickingClock()
	h := release.Home{Dir: f.home, GOOS: runtime.GOOS}
	b := f.secondCopy()
	if code := f.run("release", "adopt", "--copy", f.copy); code != 0 {
		t.Fatalf("release adopt = %d", code)
	}
	if code := f.newTask("t-1"); code != 0 {
		t.Fatalf("task new = %d", code)
	}
	for _, v := range []string{"v1.1.0", "v1.2.0", "v1.3.0"} {
		f.install(v, "release "+v, 1)
	}
	for _, step := range [][]string{{"v1.1.0", f.copy}, {"v1.2.0", f.copy}, {"v1.3.0", b}} {
		if code := f.run("release", "use", step[0], "--copy", step[1]); code != 0 {
			t.Fatalf("release use %s --copy %s = %d", step[0], step[1], code)
		}
	}
	for _, v := range []string{"v1.4.0", "v1.5.0", "v1.6.0"} {
		f.install(v, "release "+v, 1)
	}

	// Every release past the newest three is held, so nothing goes, and status says why each stays.
	if code := f.run("release", "prune", "--copy", f.copy); code != 0 || !strings.Contains(f.stdout.String(), "kept v1.0.0 past the newest 3: recorded by active task app/t-1 of copy "+f.copy) {
		t.Fatalf("release prune = %d, %q", code, f.stdout)
	}
	if len(f.prunes()) != 0 {
		t.Fatal("a prune of held releases recorded a removal")
	}
	if code := f.run("release", "status", "--json", "--copy", f.copy); code != 0 {
		t.Fatalf("release status = %d", code)
	}
	var rep releaseStatusReport
	if err := json.Unmarshal([]byte(f.stdout.String()), &rep); err != nil {
		t.Fatal(err)
	}
	kept := map[string][]string{}
	for _, r := range rep.Releases {
		kept[r.Version] = r.Kept
		if r.Prunable {
			t.Errorf("%s is prunable, though held", r.Version)
		}
	}
	for v, want := range map[string][]string{
		"v1.6.0": {}, "v1.5.0": {}, "v1.4.0": {},
		"v1.3.0": {"pinned by copy " + b},
		"v1.2.0": {"pinned by copy " + f.copy},
		"v1.1.0": {"rollback target of copy " + f.copy},
		"v1.0.0": {"recorded by active task app/t-1 of copy " + f.copy},
	} {
		if !slices.Equal(kept[v], want) {
			t.Errorf("%s kept for %q, want %q", v, kept[v], want)
		}
	}
	if len(rep.PruneBlocked) != 0 {
		t.Errorf("prune blocked by %q", rep.PruneBlocked)
	}

	// Once the task is cancelled, nothing holds v1.0.0.
	if _, err := withStore(filepath.Join(f.copy, "director.store"), false, func(s *store.Store) (store.Receipt, error) {
		return s.CancelTask("cancel-t-1", "app", "t-1", "done with it")
	}); err != nil {
		t.Fatal(err)
	}
	if code := f.run("release", "prune", "--dry-run", "--copy", f.copy); code != 0 || !strings.Contains(f.stdout.String(), "would remove v1.0.0") {
		t.Fatalf("release prune --dry-run = %d, %q", code, f.stdout)
	}
	if _, err := h.Check("v1.0.0"); err != nil {
		t.Fatalf("a dry run removed v1.0.0: %v", err)
	}

	// A copy whose pin cannot be read refuses the prune.
	pinB := readText(t, release.PinPath(b))
	writeFile(t, release.PinPath(b), "not a version\n")
	if code := f.run("release", "prune", "--copy", f.copy); code != 1 || !strings.Contains(f.stderr.String(), "removing nothing") || !strings.Contains(f.stderr.String(), "copy "+b) {
		t.Fatalf("release prune with an unreadable copy = %d, %q", code, f.stderr)
	}
	if code := f.run("release", "status", "--copy", f.copy); code != 0 || !strings.Contains(f.stdout.String(), "prune refuses: copy "+b) {
		t.Fatalf("release status with an unreadable copy = %d, %q", code, f.stdout)
	}
	// So does a copy whose pin file and newest event disagree.
	writeFile(t, release.PinPath(b), "v1.2.0\n")
	if code := f.run("release", "prune", "--copy", f.copy); code != 1 || !strings.Contains(f.stderr.String(), "store pins release v1.3.0") {
		t.Fatalf("release prune with a disagreeing copy = %d, %q", code, f.stderr)
	}
	if _, err := h.Check("v1.0.0"); err != nil {
		t.Fatalf("a refused prune removed v1.0.0: %v", err)
	}
	writeFile(t, release.PinPath(b), pinB)

	// Stale .incoming/ entries go too; a fresh one stays.
	now := f.app.Now()
	stale, fresh := filepath.Join(h.IncomingDir(), "v9.0.0"), filepath.Join(h.IncomingDir(), "v9.1.0")
	for _, d := range []string{stale, fresh} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(stale, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fresh, now, now); err != nil {
		t.Fatal(err)
	}
	// A stray entry under releases/, not named as a release version, is left alone: never planned for removal, so
	// it neither fails the prune nor is removed, and status says so.
	stray := h.ReleaseDir("stray")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := f.run("release", "prune", "--dry-run", "--copy", f.copy); code != 0 || strings.Contains(f.stdout.String(), "would remove stray") || !strings.Contains(f.stdout.String(), "left "+stray+": "+release.NotARelease) {
		t.Fatalf("release prune --dry-run with a stray entry = %d, %q", code, f.stdout)
	}
	if code := f.run("release", "status", "--json", "--copy", f.copy); code != 0 {
		t.Fatalf("release status = %d", code)
	}
	rep = releaseStatusReport{}
	if err := json.Unmarshal([]byte(f.stdout.String()), &rep); err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Releases {
		if r.Left != (r.Version == "stray") || (r.Version == "stray" && r.Prunable) {
			t.Errorf("status of %s: left %v, prunable %v", r.Version, r.Left, r.Prunable)
		}
	}
	if code := f.run("release", "status", "--copy", f.copy); code != 0 || !strings.Contains(f.stdout.String(), "    "+release.NotARelease+"\n") {
		t.Fatalf("release status with a stray entry = %d, %q", code, f.stdout)
	}
	adopted := f.pins()[0]
	if code := f.run("release", "prune", "--copy", f.copy); code != 0 || !strings.Contains(f.stdout.String(), "removed v1.0.0") || !strings.Contains(f.stdout.String(), "removed stale "+stale) {
		t.Fatalf("release prune = %d, %q", code, f.stdout)
	}
	if !fileExists(stray) {
		t.Fatal("prune removed the stray entry under releases/")
	}
	if err := os.Remove(stray); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Check("v1.0.0"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("v1.0.0 after prune: %v, want it removed", err)
	}
	if fileExists(stale) || !fileExists(fresh) {
		t.Fatalf("stale entry left %v, fresh entry removed %v", fileExists(stale), !fileExists(fresh))
	}
	if all, _ := h.List(); len(all) != 6 {
		t.Fatalf("%d releases left, want 6", len(all))
	}
	pr := f.prunes()
	if len(pr) != 1 || pr[0].Version != "v1.0.0" || pr[0].SHA256 != adopted.SHA256 || !strings.Contains(pr[0].Reason, "older than the newest 3") {
		t.Fatalf("release-pruned events %+v", pr)
	}

	// A's pin change prunes after it, leaving B's pin; B's pin keeps v1.3.0 though it is now past the newest three.
	f.install("v1.7.0", "release v1.7.0", 1)
	if code := f.run("release", "use", "v1.6.0", "--copy", f.copy); code != 0 || !strings.Contains(f.stdout.String(), "prune: removed v1.1.0") {
		t.Fatalf("release use = %d, %q; want v1.1.0, no longer A's rollback target, pruned", code, f.stdout)
	}
	if pin, _ := release.ReadPin(b); pin != "v1.3.0" {
		t.Fatalf("copy B's pin = %q after A's pin change", pin)
	}
	for v, want := range map[string]bool{"v1.1.0": false, "v1.2.0": true, "v1.3.0": true, "v1.4.0": false} {
		_, err := h.Check(v)
		if got := err == nil; got != want {
			t.Errorf("%s installed %v, want %v", v, got, want)
		}
	}
}

// TestReleasePruneWaitsForTheHomeLock: a prune started while another process holds the home lock, as task new does
// while it records a task's release, waits for it and sees the reference made under it, so the release stays.
func TestReleasePruneWaitsForTheHomeLock(t *testing.T) {
	f := newPinFixture(t)
	f.tickingClock()
	h := release.Home{Dir: f.home, GOOS: runtime.GOOS}
	if code := f.run("release", "adopt", "--copy", f.copy); code != 0 {
		t.Fatalf("release adopt = %d", code)
	}
	for _, v := range []string{"v1.1.0", "v1.2.0"} {
		f.install(v, "release "+v, 1)
	}
	if code := f.run("release", "use", "v1.2.0", "--copy", f.copy); code != 0 {
		t.Fatalf("release use = %d", code)
	}
	for _, v := range []string{"v1.3.0", "v1.4.0"} {
		f.install(v, "release "+v, 1)
	}
	old, err := h.Runnable("v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	// v1.0.0 is the copy's rollback target and v1.1.0 is held by nothing, until the task below records it.
	unlock, err := h.Lock(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		out pruneOutcome
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := f.app.pruneReleases(h, filepath.Join(f.copy, "director.store"), false)
		done <- result{out, err}
	}()
	time.Sleep(200 * time.Millisecond) // timing:allow checks only that prune is still waiting; a slow machine cannot make it fail

	select {
	case r := <-done:
		t.Fatalf("prune ran while the home lock was held: %+v, %v", r.out, r.err)
	default:
	}
	d := "sha256:" + strings.Repeat("a", 64)
	if _, err := withStore(filepath.Join(f.copy, "director.store"), false, func(s *store.Store) (store.Receipt, error) {
		return s.CreateTaskWithRelease("task-late", store.Task{Repository: "app", ID: "late", Title: "T", Workflow: workflow.Revision{Name: "default", Digest: d}, ConfigDigest: d},
			store.TaskRelease{Version: old.Version, SHA256: old.SHA256})
	}); err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, it := range r.out.Items {
		if it.Remove {
			t.Errorf("prune removed %s, though the task created under the lock records v1.1.0 and the copy rolls back to v1.0.0", it.Version)
		}
	}
	if _, err := h.Runnable("v1.1.0"); err != nil {
		t.Fatalf("v1.1.0 after prune: %v", err)
	}
}

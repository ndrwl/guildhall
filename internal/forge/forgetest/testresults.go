package forgetest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/testresults"
)

// RunFixture is one completed push run of a head, as SetTestRun and Harness.SetTestRun give it.
type RunFixture struct {
	// ID is the run's id, and Conclusion how it finished, lower-cased as GitHub names it.
	ID         int64
	Conclusion string
	// Artifacts are the run's artifacts; only test-timings-* ones are read.
	Artifacts []ArtifactFixture
}

// ArtifactFixture is one artifact of a run: its name, its files' contents by file name, and whether it expired.
type ArtifactFixture struct {
	Name    string
	Files   map[string]string
	Expired bool
}

// SetTestRun makes run the completed push run remote reports for head, replacing any it reported, and configures
// the test results reader: until it is first called, every read reports "not configured".
func (f *Fake) SetTestRun(remote, head string, run RunFixture) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.testRuns == nil {
		f.testRuns = map[string]map[string]RunFixture{}
	}
	if f.testRuns[remote] == nil {
		f.testRuns[remote] = map[string]RunFixture{}
	}
	run.Artifacts = slices.Clone(run.Artifacts)
	for i, a := range run.Artifacts {
		run.Artifacts[i].Files = maps.Clone(a.Files)
	}
	f.testRuns[remote][head] = run
}

// SetTestResultsError makes every later TestResults fail with err, configured or not; a nil err clears it.
func (f *Fake) SetTestResultsError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.testErr = err
}

// TestResultsCalls returns how many times TestResults was called, failed calls included.
func (f *Fake) TestResultsCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.testReads
}

// TestResults implements forge.GitHub.TestResults's contract over the runs SetTestRun set: a head with none is
// forge.ErrNoTestRun, a run with no test-timings-* artifact has no legs and says so, an expired artifact is one leg
// saying so, and every other artifact's files become legs through forge.TestArtifactLegs, as GitHub reads a
// downloaded one. Unconfigured, it is forge.ErrTestResultsUnavailable "not configured", so consumers that never set
// a run see one predictable reason. A cancelled context is returned as it is.
func (f *Fake) TestResults(ctx context.Context, c forge.Commit) (forge.TestResults, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.testReads++
	if ctx.Err() != nil {
		return forge.TestResults{}, ctx.Err()
	}
	if err := c.Validate(); err != nil {
		return forge.TestResults{}, err
	}
	switch {
	case f.testErr != nil:
		return forge.TestResults{}, f.testErr
	case f.testRuns == nil:
		return forge.TestResults{}, &forge.TestResultsUnavailableError{Reason: "not configured"}
	}
	run, ok := f.testRuns[c.Remote][c.Head]
	if !ok {
		return forge.TestResults{}, fmt.Errorf("%w at %s", forge.ErrNoTestRun, c.Head)
	}
	out := forge.TestResults{Run: forge.CIRun{ID: run.ID, URL: fmt.Sprintf("https://forge.invalid/runs/%d", run.ID), Head: c.Head, Conclusion: run.Conclusion}}
	arts := slices.DeleteFunc(slices.Clone(run.Artifacts), func(a ArtifactFixture) bool { return !strings.HasPrefix(a.Name, forge.TestArtifactPrefix) })
	slices.SortStableFunc(arts, func(a, b ArtifactFixture) int { return cmp.Compare(a.Name, b.Name) })
	if len(arts) == 0 {
		out.Unavailable = fmt.Sprintf("run %d has no %s* artifact", run.ID, forge.TestArtifactPrefix)
		return out, nil
	}
	for _, a := range arts {
		if a.Expired {
			out.Legs = append(out.Legs, forge.TestLeg{Name: forge.TestLegName(a.Name, "test.json"), Artifact: a.Name, File: "test.json",
				Unavailable: "artifact " + a.Name + " expired"})
			continue
		}
		out.Legs = append(out.Legs, forge.TestArtifactLegs(a.Name, a.Files)...)
	}
	return out, nil
}

// TestResultsReader is the optional test results read under the contract: forge.GitHub's and Fake's.
type TestResultsReader interface {
	TestResults(ctx context.Context, c forge.Commit) (forge.TestResults, error)
}

// Streams the test results cases give: a passing package and one with no test files, a failing package with a
// skipped test, and the Director suites managed.
const (
	passStream = `{"Action":"start","Package":"acme/a"}
{"Action":"output","Package":"acme/empty","Output":"?   \tacme/empty\t[no test files]\n"}
{"Action":"skip","Package":"acme/empty","Elapsed":0}
{"Action":"run","Package":"acme/a","Test":"TestA"}
{"Action":"pass","Package":"acme/a","Test":"TestA","Elapsed":1.5}
{"Action":"pass","Package":"acme/a","Elapsed":1.6}
`
	failStream = `{"Action":"run","Package":"acme/b","Test":"TestB"}
{"Action":"output","Package":"acme/b","Test":"TestB","Output":"    b_test.go:9: boom\n"}
{"Action":"fail","Package":"acme/b","Test":"TestB","Elapsed":0.25}
{"Action":"run","Package":"acme/b","Test":"TestSymlink"}
{"Action":"output","Package":"acme/b","Test":"TestSymlink","Output":"    b_test.go:20: symlinks unsupported\n"}
{"Action":"skip","Package":"acme/b","Test":"TestSymlink","Elapsed":0}
{"Action":"fail","Package":"acme/b","Elapsed":0.3}
`
	managedStream = `{"Action":"run","Package":"acme/cli","Test":"TestManaged"}
{"Action":"pass","Package":"acme/cli","Test":"TestManaged","Elapsed":2}
{"Action":"pass","Package":"acme/cli","Elapsed":2.1}
`
)

// runTestResultsContract runs the test results cases when the harness's forge reads test results and can be given
// runs.
func runTestResultsContract(t *testing.T, newHarness func(t *testing.T) Harness) {
	ctx := context.Background()
	commit := func(h Harness, head string) forge.Commit {
		return forge.Commit{Repository: h.Repository, Remote: h.Remote, Head: head}
	}
	reader := func(t *testing.T) (Harness, TestResultsReader) {
		t.Helper()
		h := newHarness(t)
		r, ok := h.Forge.(TestResultsReader)
		if !ok || h.SetTestRun == nil {
			t.Skip("this forge cannot read test results or be given runs")
		}
		return h, r
	}

	t.Run("reads each leg of a head's completed push run", func(t *testing.T) {
		h, r := reader(t)
		h.SetTestRun(t, h.Heads[0], RunFixture{ID: 41, Conclusion: "failure", Artifacts: []ArtifactFixture{
			{Name: "test-timings-windows-latest", Files: map[string]string{"test.json": failStream}},
			{Name: "coverage", Files: map[string]string{"test.json": failStream}},
			{Name: "test-timings-ubuntu-latest", Files: map[string]string{"test.json": passStream, "managed.json": managedStream}},
		}})
		got, err := r.TestResults(ctx, commit(h, h.Heads[0]))
		if err != nil {
			t.Fatalf("TestResults = %v", err)
		}
		if got.Run.ID != 41 || got.Run.Head != h.Heads[0] || got.Run.Conclusion != "failure" || !strings.Contains(got.Run.URL, "41") || got.Unavailable != "" {
			t.Errorf("Run = %+v, unavailable %q; want run 41 at the head, failed, with its URL", got.Run, got.Unavailable)
		}
		var names []string
		for _, l := range got.Legs {
			names = append(names, l.Name+"="+l.Artifact+"/"+l.File)
			if l.Unavailable != "" {
				t.Errorf("leg %s unavailable: %s", l.Name, l.Unavailable)
			}
		}
		want := []string{"ubuntu=test-timings-ubuntu-latest/test.json", "ubuntu managed=test-timings-ubuntu-latest/managed.json", "windows=test-timings-windows-latest/test.json"}
		if !slices.Equal(names, want) {
			t.Fatalf("legs = %q, want %q", names, want)
		}
		ubuntu, managed, windows := got.Legs[0].Summary, got.Legs[1].Summary, got.Legs[2].Summary
		if !slices.Equal(ubuntu.Packages, []testresults.Package{{Path: "acme/a", State: testresults.Pass, Elapsed: 1.6}, {Path: "acme/empty", State: testresults.NoTests}}) ||
			len(ubuntu.Skips) != 0 || !slices.Equal(ubuntu.Tests, []testresults.Test{{Package: "acme/a", Name: "TestA", Elapsed: 1.5}}) {
			t.Errorf("ubuntu = %+v", ubuntu)
		}
		if !slices.Equal(managed.Packages, []testresults.Package{{Path: "acme/cli", State: testresults.Pass, Elapsed: 2.1}}) {
			t.Errorf("ubuntu managed = %+v", managed)
		}
		if !slices.Equal(windows.Packages, []testresults.Package{{Path: "acme/b", State: testresults.Fail, Elapsed: 0.3}}) ||
			!slices.Equal(windows.Skips, []testresults.Skip{{Package: "acme/b", Test: "TestSymlink", Reason: "b_test.go:20: symlinks unsupported"}}) ||
			!slices.Equal(windows.Tests, []testresults.Test{{Package: "acme/b", Name: "TestB", Failed: true, Elapsed: 0.25}}) {
			t.Errorf("windows = %+v", windows)
		}
	})

	t.Run("reports a head with no completed push run", func(t *testing.T) {
		h, r := reader(t)
		h.SetTestRun(t, h.Heads[0], RunFixture{ID: 41, Conclusion: "success"})
		if got, err := r.TestResults(ctx, commit(h, h.Heads[1])); !errors.Is(err, forge.ErrNoTestRun) {
			t.Errorf("TestResults of a head with no run = %+v, %v; want ErrNoTestRun", got, err)
		}
	})

	t.Run("names a run with no test-timings artifact", func(t *testing.T) {
		h, r := reader(t)
		h.SetTestRun(t, h.Heads[0], RunFixture{ID: 9, Conclusion: "success", Artifacts: []ArtifactFixture{{Name: "coverage", Files: map[string]string{"test.json": passStream}}}})
		got, err := r.TestResults(ctx, commit(h, h.Heads[0]))
		if err != nil || got.Run.ID != 9 || len(got.Legs) != 0 || got.Unavailable != "run 9 has no test-timings-* artifact" {
			t.Errorf("TestResults = %+v, %v; want run 9 with no legs and the reason", got, err)
		}
	})

	t.Run("gives a reason for an expired artifact and one without test.json", func(t *testing.T) {
		h, r := reader(t)
		h.SetTestRun(t, h.Heads[0], RunFixture{ID: 42, Conclusion: "success", Artifacts: []ArtifactFixture{
			{Name: "test-timings-macos-latest", Files: map[string]string{"test.json": passStream}, Expired: true},
			{Name: "test-timings-ubuntu-latest", Files: map[string]string{"managed.json": managedStream}},
			{Name: "test-timings-windows-latest", Files: map[string]string{"test.json": "not json\n"}},
		}})
		got, err := r.TestResults(ctx, commit(h, h.Heads[0]))
		if err != nil || len(got.Legs) != 4 {
			t.Fatalf("TestResults = %+v, %v; want four legs", got, err)
		}
		for i, want := range []struct{ name, reason string }{
			{"macos", "artifact test-timings-macos-latest expired"},
			{"ubuntu", "artifact test-timings-ubuntu-latest has no test.json"},
			{"ubuntu managed", ""},
			{"windows", ""},
		} {
			if l := got.Legs[i]; l.Name != want.name || l.Unavailable != want.reason {
				t.Errorf("leg %d = %s %q, want %s %q", i, l.Name, l.Unavailable, want.name, want.reason)
			}
		}
		if s := got.Legs[3].Summary; len(s.Packages) != 0 || len(s.Tests) != 0 {
			t.Errorf("a stream of no events = %+v, want an empty summary", s)
		}
	})

	t.Run("refuses an invalid commit and returns a cancelled context as it is", func(t *testing.T) {
		h, r := reader(t)
		h.SetTestRun(t, h.Heads[0], RunFixture{ID: 41, Conclusion: "success"})
		if _, err := r.TestResults(ctx, commit(h, "HEAD")); !errors.Is(err, forge.ErrInvalid) {
			t.Errorf("TestResults of an invalid commit = %v, want ErrInvalid", err)
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := r.TestResults(cancelled, commit(h, h.Heads[0])); !errors.Is(err, context.Canceled) || errors.Is(err, forge.ErrTestResultsUnavailable) {
			t.Errorf("TestResults cancelled = %v, want context.Canceled unwrapped", err)
		}
	})
}

package interp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/interp"
)

// event is one test2json event line.
func event(action, pkg, test string, elapsed float64, output string) string {
	e := map[string]any{"Action": action, "Package": pkg}
	if test != "" {
		e["Test"] = test
	}
	if action == "pass" || action == "fail" || action == "skip" {
		e["Elapsed"] = elapsed
	}
	if output != "" {
		e["Output"] = output
	}
	data, err := json.Marshal(e)
	if err != nil {
		panic(err)
	}
	return string(data) + "\n"
}

// stream joins event lines into a test2json stream.
func stream(lines ...string) string { return strings.Join(lines, "") }

var (
	// ubuntuStream: acme/a passes with two tests and a skipped subtest, acme/b passes, acme/empty has no test files.
	ubuntuStream = stream(
		event("run", "acme/a", "TestSlow", 0, ""),
		event("pass", "acme/a", "TestSlow", 3.5, ""),
		event("run", "acme/a", "TestFast", 0, ""),
		event("run", "acme/a", "TestFast/sub", 0, ""),
		event("output", "acme/a", "TestFast/sub", 0, "    a_test.go:7: needs docker\n"),
		event("skip", "acme/a", "TestFast/sub", 0, ""),
		event("pass", "acme/a", "TestFast", 0.25, ""),
		event("pass", "acme/a", "", 3.9, ""),
		event("run", "acme/b", "TestB", 0, ""),
		event("pass", "acme/b", "TestB", 1, ""),
		event("pass", "acme/b", "", 1.2, ""),
		event("output", "acme/empty", "", 0, "?   \tacme/empty\t[no test files]\n"),
		event("skip", "acme/empty", "", 0, ""),
	)
	// managedStream: the Director suites managed, acme/cli.
	managedStream = stream(
		event("run", "acme/cli", "TestManaged", 0, ""),
		event("pass", "acme/cli", "TestManaged", 2, ""),
		event("pass", "acme/cli", "", 2.1, ""),
	)
	// windowsStream: acme/a fails, acme/b is not run, acme/empty has no test files.
	windowsStream = stream(
		event("run", "acme/a", "TestSlow", 0, ""),
		event("output", "acme/a", "TestSlow", 0, "    a_test.go:9: boom\n"),
		event("fail", "acme/a", "TestSlow", 4.25, ""),
		event("run", "acme/a", "TestSymlink", 0, ""),
		event("output", "acme/a", "TestSymlink", 0, "    a_test.go:20: symlinks unsupported\n"),
		event("skip", "acme/a", "TestSymlink", 0, ""),
		event("fail", "acme/a", "", 4.5, ""),
		event("skip", "acme/empty", "", 0, ""),
	)
)

// ciRun is run id of a run with conclusion whose artifacts are the ubuntu, windows and macos legs: ubuntu with its
// managed stream, windows failing acme/a, and macos as given (no macos artifact when it is nil).
func ciRun(id int64, conclusion string, macos *forgetest.ArtifactFixture) forgetest.RunFixture {
	run := forgetest.RunFixture{ID: id, Conclusion: conclusion, Artifacts: []forgetest.ArtifactFixture{
		{Name: "test-timings-windows-latest", Files: map[string]string{"test.json": windowsStream}},
		{Name: "test-timings-ubuntu-latest", Files: map[string]string{"test.json": ubuntuStream, "managed.json": managedStream}},
		{Name: "coverage", Files: map[string]string{"test.json": windowsStream}},
	}}
	if macos != nil {
		run.Artifacts = append(run.Artifacts, *macos)
	}
	return run
}

// section returns the baseline's ## Test results section, up to the failed-jobs block.
func section(t *testing.T, text string) string {
	t.Helper()
	_, s, ok := strings.Cut(text, "\n## Test results\n\n")
	if !ok {
		t.Fatal("baseline has no ## Test results section")
	}
	s, _, ok = strings.Cut(s, "\n## Failed jobs\n")
	if !ok {
		t.Fatal("the ## Test results section is not followed by the failed-jobs block")
	}
	return s
}

// TestHallmarkBaselineTestResults: the completed push run at the current head is named with its URL, head and
// conclusion; its legs are one package table, ubuntu, ubuntu managed, windows, with each package's state and elapsed
// seconds and — where a leg did not run it, packages with no test files counted rather than listed, and per leg the
// slowest top-level tests (never a subtest) and every skip, a subtest's included, with its reason. An artifact other
// than test-timings-* is not read, and the section adds no failed job.
func TestHallmarkBaselineTestResults(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			h := newBaselineEnv(t, b, hallmarkExp(1, map[string]string{}, report("# Hallmark\n")))
			c := h.merged(t, "t5", 105)
			h.forge.SetTestRun(h.remote, c, ciRun(41, "failure", nil))
			want(t, h.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 1})
			_, text := h.baseline(t)
			s := section(t, text)
			containsInOrder(t, s,
				"Run 41 (https://forge.invalid/runs/41) at `"+c+"`, the current head; conclusion: failure.\n\n",
				"### Packages (3)\n\n| Package | ubuntu | ubuntu managed | windows |\n|---|---|---|---|\n",
				"| acme/a | pass 3.9s | — | fail 4.5s |\n",
				"| acme/b | pass 1.2s | — | — |\n",
				"| acme/cli | — | pass 2.1s | — |\n",
				"\nPackages with no test files on every leg that ran them: 1.\n",
				"### Slowest tests: ubuntu (3 of 3 top-level tests)\n\n| Package | Test | Elapsed |\n|---|---|---|\n",
				"| acme/a | TestSlow | 3.50s |\n| acme/b | TestB | 1.00s |\n| acme/a | TestFast | 0.25s |\n",
				"### Skips: ubuntu (1)\n\n| Package | Test | Reason |\n|---|---|---|\n| acme/a | TestFast/sub | a_test.go:7: needs docker |\n",
				"### Slowest tests: ubuntu managed (1 of 1 top-level tests)", "| acme/cli | TestManaged | 2.00s |\n",
				"### Skips: ubuntu managed (0)\n\nNo test was skipped.\n",
				"### Slowest tests: windows (1 of 1 top-level tests)", "| acme/a | TestSlow | 4.25s |\n",
				"### Skips: windows (1)", "| acme/a | TestSymlink | a_test.go:20: symlinks unsupported |\n",
			)
			if strings.Count(s, "TestFast/sub") != 1 {
				t.Error("the section ranks subtest TestFast/sub, or lists its skip twice")
			}
			for _, absent := range []string{"acme/empty", "unavailable", "macos", "coverage"} {
				if strings.Contains(s, absent) {
					t.Errorf("the section has %q", absent)
				}
			}
			if jobs, err := interp.BaselineJobs([]byte(text)); err != nil || len(jobs) != 0 {
				t.Fatalf("failed jobs = %+v, %v; want none", jobs, err)
			}
			if got := h.forge.TestResultsCalls(); got != 1 {
				t.Fatalf("TestResults calls = %d, want one at the current head", got)
			}
		})
	}
}

// TestHallmarkBaselineTestResultsBehind: with no completed push run at the current head, the newest first-parent
// ancestor with a run that has test-timings-* artifacts is used and the section says how far behind it is; a newer
// ancestor whose run has none is passed over.
func TestHallmarkBaselineTestResultsBehind(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			h := newBaselineEnv(t, b, hallmarkExp(1, map[string]string{}, report("# Hallmark\n")))
			older := h.commit(t, "older")
			bare := h.commit(t, "bare")
			h.commit(t, "current")
			h.forge.SetTestRun(h.remote, older, ciRun(40, "success", nil))
			h.forge.SetTestRun(h.remote, bare, forgetest.RunFixture{ID: 44, Conclusion: "success", Artifacts: []forgetest.ArtifactFixture{{Name: "coverage"}}})
			want(t, h.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 1})
			_, text := h.baseline(t)
			s := section(t, text)
			containsInOrder(t, s, "Run 40 (https://forge.invalid/runs/40) at `"+older+"`, 2 first-parent commits behind the current head; conclusion: success.\n", "### Packages (3)")
			if strings.Contains(s, "44") {
				t.Error("the section names the run with no test-timings artifact")
			}
			if got := h.forge.TestResultsCalls(); got != 3 {
				t.Fatalf("TestResults calls = %d, want three: the head, the bare run and the older one", got)
			}
		})
	}
}

// TestHallmarkBaselineTestResultsUnavailable: each part with no results is one reason line, and the attempt still
// launches: an expired or over-cap leg beside readable ones, a run whose every artifact expired (still named), a run at
// the current head with no test-timings-* artifact, no completed run within the walk, a reader error, and a forge
// without the reader.
func TestHallmarkBaselineTestResultsUnavailable(t *testing.T) {
	for _, b := range backends {
		for _, tc := range []struct {
			name  string
			set   func(h *baselineEnv, current string)
			lines []string
		}{
			{"a leg expired", func(h *baselineEnv, c string) {
				h.forge.SetTestRun(h.remote, c, ciRun(41, "success", &forgetest.ArtifactFixture{Name: "test-timings-macos-latest", Expired: true}))
			}, []string{"Run 41 ", "\n\nunavailable: macos: artifact test-timings-macos-latest expired\n\n### Packages (3)", "| Package | ubuntu | ubuntu managed | windows |\n"}},
			{"a leg over the size cap", func(h *baselineEnv, c string) {
				h.forge.results = func(context.Context, forge.Commit) (forge.TestResults, error) {
					return forge.TestResults{Run: forge.CIRun{ID: 42, URL: "https://forge.invalid/runs/42", Head: c, Conclusion: "success"}, Legs: []forge.TestLeg{
						{Name: "ubuntu", Artifact: "test-timings-ubuntu-latest", File: "test.json", Unavailable: fmt.Sprintf("artifact test-timings-ubuntu-latest is larger than %d bytes", forge.MaxTestArtifactBytes)},
						forge.TestArtifactLegs("test-timings-windows-latest", map[string]string{"test.json": windowsStream})[0],
					}}, nil
				}
			}, []string{"Run 42 ", "unavailable: ubuntu: artifact test-timings-ubuntu-latest is larger than 67108864 bytes\n", "| Package | windows |\n"}},
			{"every artifact expired", func(h *baselineEnv, c string) {
				h.forge.SetTestRun(h.remote, c, forgetest.RunFixture{ID: 43, Conclusion: "success", Artifacts: []forgetest.ArtifactFixture{
					{Name: "test-timings-ubuntu-latest", Expired: true}, {Name: "test-timings-windows-latest", Expired: true}}})
			}, []string{"Run 43 (https://forge.invalid/runs/43) at `", "\n\ntest results unavailable: ubuntu: artifact test-timings-ubuntu-latest expired; windows: artifact test-timings-windows-latest expired\n"}},
			{"no test-timings artifact", func(h *baselineEnv, c string) {
				h.forge.SetTestRun(h.remote, c, forgetest.RunFixture{ID: 9, Conclusion: "success"})
			}, []string{"Run 9 ", "\n\ntest results unavailable: run 9 has no test-timings-* artifact\n"}},
			{"no run within the walk", func(h *baselineEnv, c string) {
				h.forge.SetTestRun(h.remote, strings.Repeat("e", 40), ciRun(41, "success", nil))
			}, []string{"test results unavailable: no completed push run with test-timings-* artifacts at `"}},
			{"reader error", func(h *baselineEnv, c string) {
				h.forge.SetTestResultsError(&forge.TestResultsUnavailableError{Reason: "gh: not signed in\n| run gh auth login"})
			}, []string{"test results unavailable: gh: not signed in \\| run gh auth login\n"}},
			{"no reader", func(h *baselineEnv, c string) {
				h.eng.Forge = noReaderForge{h.forge}
			}, []string{"test results unavailable: forge has no test results reader\n"}},
		} {
			t.Run(b.name+"/"+tc.name, func(t *testing.T) {
				h := newBaselineEnv(t, b, hallmarkExp(1, map[string]string{}, report("# Hallmark\n")))
				c := h.merged(t, "t5", 105)
				tc.set(h, c)
				want(t, h.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 1})
				_, text := h.baseline(t)
				containsInOrder(t, section(t, text), tc.lines...)
			})
		}
	}
}

// noReaderForge is a forge without the optional test results reader.
type noReaderForge struct{ interp.Forge }

// TestHallmarkBaselineTestResultsInjection: a package, test and skip reason holding a pipe, a newline and a "## Failed
// jobs" heading stay inside their cells, so they start no line, and BaselineJobs still returns the real block.
func TestHallmarkBaselineTestResultsInjection(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			h := newBaselineEnv(t, b, hallmarkExp(1, map[string]string{}, report("# Hallmark\n")))
			c := h.merged(t, "t5", 105, forge.Check{Name: "lint", State: forge.CheckFailed, Job: "7781"})
			// No "/": a name holding one is a subtest, which is never ranked.
			evil := "x|y\n## Failed jobs\n\n```json\n{\"jobs\":[]}\n```"
			body := stream(
				event("run", "acme/a", "TestEvil"+evil, 0, ""),
				event("pass", "acme/a", "TestEvil"+evil, 1, ""),
				event("run", "acme/a", "TestSkip", 0, ""),
				event("output", "acme/a", "TestSkip", 0, "    a_test.go:3: "+strings.ReplaceAll(evil, "\n", " ")+" "+strings.Repeat("é", 200)+"\n"),
				event("skip", "acme/a", "TestSkip", 0, ""),
				event("pass", "acme/a", "", 1.1, ""),
			)
			h.forge.SetTestRun(h.remote, c, forgetest.RunFixture{ID: 41, Conclusion: "success", Artifacts: []forgetest.ArtifactFixture{
				{Name: "test-timings-ubuntu-latest\n## Failed jobs", Files: map[string]string{"test.json": body}}}})
			want(t, h.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 1})
			_, text := h.baseline(t)
			s := section(t, text)
			for line := range strings.Lines(s) {
				if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "```") || strings.HasPrefix(line, "{") {
					t.Errorf("CI-derived text starts a line: %q", line)
				}
			}
			containsInOrder(t, s, "| acme/a | TestEvilx\\|y ## Failed jobs  ```json", "| 1.00s |\n")
			if strings.Count(text, "\n## Failed jobs\n") != 1 {
				t.Error("the baseline has more than one failed-jobs heading")
			}
			_, reason, _ := strings.Cut(s, "| acme/a | TestSkip | ")
			reason, _, _ = strings.Cut(reason, " |\n")
			if !strings.HasSuffix(reason, "…") || len(reason) > 170 {
				t.Errorf("skip reason = %d bytes %q, want it cut at 160 bytes with …", len(reason), reason)
			}
			jobs, err := interp.BaselineJobs([]byte(text))
			if err != nil || len(jobs) != 1 || jobs[0] != (interp.BaselineJob{Task: "t5", Remote: h.remote, Head: c, Check: "lint", Job: "7781"}) {
				t.Fatalf("failed jobs = %+v, %v; want t5's lint job", jobs, err)
			}
		})
	}
}

// TestHallmarkBaselineTestResultsCancelled: an advance whose context ends during the test results read returns the
// context's error and records no baseline.
func TestHallmarkBaselineTestResultsCancelled(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			h := newBaselineEnv(t, b)
			h.merged(t, "t5", 105)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h.forge.results = func(ctx context.Context, _ forge.Commit) (forge.TestResults, error) {
				cancel()
				return forge.TestResults{}, ctx.Err()
			}
			if _, err := h.eng.Advance(ctx, interp.Event{Kind: interp.TaskCreated, Repository: "app", Task: "t7"}); !errors.Is(err, context.Canceled) {
				t.Fatalf("Advance = %v, want the context's error", err)
			}
			if _, ok := h.documents(t)["baseline-hallmark"]; ok {
				t.Fatal("a cancelled advance recorded a baseline")
			}
			if _, err := os.Stat(filepath.Join(h.eng.DocumentDir, "tasks", "app", "t7", "baseline-hallmark.md")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("baseline file = %v, want none", err)
			}
		})
	}
}

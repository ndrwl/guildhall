package interp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/testresults"
)

// testResultsWalk is how many of the current head's first-parent ancestors the baseline's test results look at for a
// completed push run when the current head has none.
const testResultsWalk = 20

// testResultsSlowest is how many of each leg's slowest top-level tests the baseline lists.
const testResultsSlowest = 20

// testResultsReason is the most bytes of a skip's reason the baseline keeps, cut at a rune boundary.
const testResultsReason = 160

// firstParents lists a head and at most n of its first-parent ancestors, newest first; git.Program implements it.
// Workspaces without it give the current head alone.
type firstParents interface {
	FirstParents(ctx context.Context, repository, head string, n int) ([]string, error)
}

var _ firstParents = git.Program{}

// legOrder is the order the baseline gives CI's legs in; a leg it does not name follows them, in the reader's order.
var legOrder = []string{"ubuntu", "ubuntu managed", "windows", "macos"}

// testResultsSection writes the baseline's ## Test results section: what the completed push CI run nearest the
// current head recorded of the test suite, read through the forge's optional TestResultsReader
// (docs/interpreter.md#report-only-workflows). It uses the current head's run when it has one, and otherwise the
// newest of its first testResultsWalk first-parent ancestors with a completed push run that has test-timings-*
// artifacts. Every problem is one "test results unavailable" line; only the advance's context ending is returned.
func (r *run) testResultsSection(b *strings.Builder, repo config.Repository, current string) error {
	b.WriteString("\n## Test results\n\n")
	reader, ok := r.e.Forge.(TestResultsReader)
	if !ok {
		b.WriteString("test results unavailable: forge has no test results reader\n")
		return nil
	}
	if current == "" {
		b.WriteString("test results unavailable: the current head is unknown\n")
		return nil
	}
	commits := []string{current}
	if fp, ok := r.e.Workspaces.(firstParents); ok {
		got, err := fp.FirstParents(r.ctx, repo.Path, current, testResultsWalk)
		if err != nil {
			if r.ctx.Err() != nil {
				return r.ctx.Err()
			}
			fmt.Fprintf(b, "test results unavailable: first-parent history of `%s`: %s\n", current, cell(err.Error()))
			return nil
		}
		commits = got
	}
	for behind, c := range commits {
		res, err := reader.TestResults(r.ctx, forge.Commit{Repository: repo.Path, Remote: repo.Remote, Head: c})
		switch {
		case r.ctx.Err() != nil:
			return r.ctx.Err()
		case errors.Is(err, forge.ErrNoTestRun):
			continue
		case err != nil:
			fmt.Fprintf(b, "test results unavailable: %s\n", cell(testResultsReasonOf(err)))
			return nil
		case behind > 0 && len(res.Legs) == 0:
			// Past the current head, only a run that recorded test timings is worth naming.
			continue
		}
		writeTestResults(b, res, c, behind)
		return nil
	}
	fmt.Fprintf(b, "test results unavailable: no completed push run with %s* artifacts at `%s` or its %d first-parent ancestors read\n",
		forge.TestArtifactPrefix, current, len(commits)-1)
	return nil
}

// testResultsReasonOf is why a read gave no test results: a TestResultsUnavailableError's reason, or the error.
func testResultsReasonOf(err error) string {
	if u, ok := errors.AsType[*forge.TestResultsUnavailableError](err); ok {
		return u.Reason
	}
	return err.Error()
}

// writeTestResults writes res, the run of commit head, behind first-parent commits behind the current head.
func writeTestResults(b *strings.Builder, res forge.TestResults, head string, behind int) {
	where := "the current head"
	switch behind {
	case 0:
	case 1:
		where = "1 first-parent commit behind the current head"
	default:
		where = fmt.Sprintf("%d first-parent commits behind the current head", behind)
	}
	fmt.Fprintf(b, "Run %d (%s) at `%s`, %s; conclusion: %s.\n", res.Run.ID, cell(res.Run.URL), head, where, cell(cmp.Or(res.Run.Conclusion, "none")))
	if res.Unavailable != "" {
		fmt.Fprintf(b, "\ntest results unavailable: %s\n", cell(res.Unavailable))
		return
	}
	legs := slices.Clone(res.Legs)
	slices.SortStableFunc(legs, func(a, c forge.TestLeg) int { return legRank(a.Name) - legRank(c.Name) })
	var read []forge.TestLeg
	var missing []string
	for _, l := range legs {
		if l.Unavailable != "" {
			missing = append(missing, cell(l.Name)+": "+cell(l.Unavailable))
		} else {
			read = append(read, l)
		}
	}
	if len(read) == 0 {
		fmt.Fprintf(b, "\ntest results unavailable: %s\n", strings.Join(missing, "; "))
		return
	}
	b.WriteString("\n")
	for _, m := range missing {
		fmt.Fprintf(b, "unavailable: %s\n", m)
	}
	if len(missing) > 0 {
		b.WriteString("\n")
	}
	writePackageTable(b, read)
	for _, l := range read {
		name := cell(l.Name)
		slowest := l.Summary.Slowest(testResultsSlowest)
		fmt.Fprintf(b, "\n### Slowest tests: %s (%d of %d top-level tests)\n\n", name, len(slowest), len(l.Summary.Tests))
		if len(slowest) == 0 {
			b.WriteString("No top-level test ran.\n")
		} else {
			b.WriteString("| Package | Test | Elapsed |\n|---|---|---|\n")
			for _, t := range slowest {
				fmt.Fprintf(b, "| %s | %s | %.2fs |\n", cell(t.Package), cell(t.Name), t.Elapsed)
			}
		}
		fmt.Fprintf(b, "\n### Skips: %s (%d)\n\n", name, len(l.Summary.Skips))
		if len(l.Summary.Skips) == 0 {
			b.WriteString("No test was skipped.\n")
			continue
		}
		b.WriteString("| Package | Test | Reason |\n|---|---|---|\n")
		for _, s := range l.Summary.Skips {
			fmt.Fprintf(b, "| %s | %s | %s |\n", cell(s.Package), cell(s.Test), cell(cutReason(s.Reason)))
		}
	}
}

// writePackageTable writes one row per package any of legs ran with test files, one column per leg, each cell its
// state and elapsed seconds there, "no tests" when it had no test files there, or — when the leg did not run it; then
// one line counting the packages that have no test files on every leg that ran them, which are not listed.
func writePackageTable(b *strings.Builder, legs []forge.TestLeg) {
	byLeg := make([]map[string]testresults.Package, len(legs))
	var paths []string
	tested := map[string]bool{}
	for i, l := range legs {
		byLeg[i] = map[string]testresults.Package{}
		for _, p := range l.Summary.Packages {
			byLeg[i][p.Path] = p
			if !slices.Contains(paths, p.Path) {
				paths = append(paths, p.Path)
			}
			if p.State != testresults.NoTests {
				tested[p.Path] = true
			}
		}
	}
	slices.Sort(paths)
	untested := 0
	for _, p := range paths {
		if !tested[p] {
			untested++
		}
	}
	listed := len(paths) - untested
	fmt.Fprintf(b, "### Packages (%d)\n\n", listed)
	if listed == 0 {
		b.WriteString("No package with test files ran.\n")
	} else {
		b.WriteString("| Package |")
		for _, l := range legs {
			fmt.Fprintf(b, " %s |", cell(l.Name))
		}
		b.WriteString("\n|---|" + strings.Repeat("---|", len(legs)) + "\n")
		for _, path := range paths {
			if !tested[path] {
				continue
			}
			fmt.Fprintf(b, "| %s |", cell(path))
			for i := range legs {
				p, ok := byLeg[i][path]
				switch {
				case !ok:
					b.WriteString(" — |")
				case p.State == testresults.NoTests:
					fmt.Fprintf(b, " %s |", p.State)
				default:
					fmt.Fprintf(b, " %s %.1fs |", cell(string(p.State)), p.Elapsed)
				}
			}
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(b, "\nPackages with no test files on every leg that ran them: %d.\n", untested)
}

// legRank is where leg name sorts in legOrder; a name it lacks sorts after every one it has.
func legRank(name string) int {
	if i := slices.Index(legOrder, name); i >= 0 {
		return i
	}
	return len(legOrder)
}

// cutReason cuts a skip's reason to testResultsReason bytes at a rune boundary, marking a cut with "…".
func cutReason(reason string) string {
	if len(reason) <= testResultsReason {
		return reason
	}
	cut := testResultsReason
	for cut > 0 && !utf8.RuneStart(reason[cut]) {
		cut--
	}
	return reason[:cut] + "…"
}

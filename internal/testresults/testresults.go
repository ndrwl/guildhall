// Package testresults reads a `go test -json` (test2json) stream into a per-package summary: each package's state
// and elapsed time, its top-level tests' timings and every skipped test or subtest with the first line of its reason
// (docs/contracts/testresults.md). It reads one line at a time with bounded memory, so a stream CI uploaded can be
// summarised without holding it, and it uses only the standard library.
package testresults

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// State is how one package of a stream ended.
type State string

const (
	// Pass: the package's tests ran and passed, or it had test files but no test to run.
	Pass State = "pass"
	// Fail: a test failed, the package timed out or its test binary exited unsuccessfully.
	Fail State = "fail"
	// BuildFailed: the package or its test binary did not build.
	BuildFailed State = "build failed"
	// NoTests: the package has no test files; go test -json reports it as a package-level skip, which is never a
	// skipped test.
	NoTests State = "no tests"
	// NoResult: the stream started the package but ended before its result, as a job killed mid-stream leaves it.
	NoResult State = "no result"
)

// Package is one package of a stream.
type Package struct {
	// Path is the package's import path.
	Path  string
	State State
	// Elapsed is the package result's elapsed seconds, as go test reports it; zero for a build failure, a package with
	// no test files and one with no result.
	Elapsed float64
}

// Test is one top-level test that passed or failed, with its elapsed seconds. Subtests and skipped tests are never
// ranked, as .github/tests/timings does not rank them.
type Test struct {
	Package, Name string
	// Failed reports a test that failed rather than passed.
	Failed  bool
	Elapsed float64
}

// Skip is one skipped test or subtest.
type Skip struct {
	Package string
	// Test is the test's name; a subtest's holds a "/".
	Test string
	// Reason is the first line of the skip's message without its indentation, such as "x_test.go:12: needs docker",
	// cut to at most MaxReason bytes at a rune boundary; empty when the test printed nothing before it skipped.
	Reason string
}

// Summary is a stream's packages, sorted by path, its top-level tests in the order they ended, and its skips in the
// order they happened.
type Summary struct {
	Packages []Package
	Tests    []Test
	Skips    []Skip
}

// Slowest returns at most n of s's top-level tests, slowest first, ties by package and then name.
func (s Summary) Slowest(n int) []Test {
	out := slices.Clone(s.Tests)
	slices.SortStableFunc(out, func(a, b Test) int {
		return cmp.Or(cmp.Compare(b.Elapsed, a.Elapsed), strings.Compare(a.Package, b.Package), strings.Compare(a.Name, b.Name))
	})
	return out[:min(max(n, 0), len(out))]
}

// Count returns how many of s's packages are in state.
func (s Summary) Count(state State) int {
	n := 0
	for _, p := range s.Packages {
		if p.State == state {
			n++
		}
	}
	return n
}

// MaxReason is the most bytes a skip's reason keeps.
const MaxReason = 256

// maxLine is the longest line Parse decodes; a longer one is not a test2json event this summary needs, and is skipped
// without being held.
const maxLine = 1 << 20

// TooLargeError reports a stream longer than the limit Parse was given.
type TooLargeError struct{ Limit int64 }

func (e *TooLargeError) Error() string { return fmt.Sprintf("larger than %d bytes", e.Limit) }

// ErrTooLarge matches every TooLargeError.
var ErrTooLarge = errors.New("larger than its limit")

// Is matches ErrTooLarge.
func (e *TooLargeError) Is(target error) bool { return target == ErrTooLarge }

// event is the part of a test2json event the summary reads.
type event struct {
	Action      string
	Package     string
	ImportPath  string
	Test        string
	Elapsed     float64
	Output      string
	FailedBuild string
}

// Parse reads the test2json stream r, at most limit bytes of it, into a Summary. A line that is not a JSON event,
// such as a line go test printed outside test2json or a last line cut short, is ignored, as is any line longer than
// 1 MiB. A stream longer than limit is a *TooLargeError, and a read error is returned as it is; either way no partial
// summary is returned.
func Parse(r io.Reader, limit int64) (Summary, error) {
	p := parser{pkgs: map[string]*Package{}, open: map[key]*openTest{}, built: map[string]bool{}}
	lr := &io.LimitedReader{R: r, N: limit + 1}
	br := bufio.NewReaderSize(lr, 64<<10)
	var line []byte
	long := false
	for {
		chunk, err := br.ReadSlice('\n')
		switch {
		case long:
		case len(line)+len(chunk) > maxLine:
			long, line = true, line[:0]
		default:
			line = append(line, chunk...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if !long {
			p.line(line)
		}
		line, long = line[:0], false
		if err == io.EOF {
			break
		}
		if err != nil {
			return Summary{}, err
		}
	}
	if lr.N == 0 {
		return Summary{}, &TooLargeError{Limit: limit}
	}
	return p.summary(), nil
}

type key struct{ pkg, test string }

// openTest is a test that started and has not ended: the reason its skip would carry, kept as one line.
type openTest struct {
	first, logged string
}

type parser struct {
	pkgs  map[string]*Package
	open  map[key]*openTest
	built map[string]bool
	tests []Test
	skips []Skip
}

func (p *parser) line(line []byte) {
	text := bytes.TrimRight(line, "\r\n")
	var e event
	if len(text) == 0 || text[0] != '{' || json.Unmarshal(text, &e) != nil || e.Action == "" {
		return
	}
	if e.Action == "build-fail" {
		// Build events carry ImportPath, "pkg [pkg.test]", not Package.
		if path, _, _ := strings.Cut(e.ImportPath, " "); path != "" {
			p.built[path] = true
		}
		return
	}
	if e.Package == "" {
		return
	}
	pkg := p.pkgs[e.Package]
	if pkg == nil {
		pkg = &Package{Path: e.Package, State: NoResult}
		p.pkgs[e.Package] = pkg
	}
	if e.Test == "" {
		switch e.Action {
		case "pass":
			pkg.State, pkg.Elapsed = Pass, e.Elapsed
		case "skip":
			pkg.State, pkg.Elapsed = NoTests, 0
		case "fail":
			pkg.State, pkg.Elapsed = Fail, e.Elapsed
			if e.FailedBuild != "" {
				pkg.State, pkg.Elapsed = BuildFailed, 0
			}
		default:
			return
		}
		// Tests still open when their package ended never finished, as the one running at a timeout.
		for k := range p.open {
			if k.pkg == e.Package {
				delete(p.open, k)
			}
		}
		return
	}
	k := key{e.Package, e.Test}
	switch e.Action {
	case "run":
		p.open[k] = &openTest{}
	case "output":
		t := p.open[k]
		if t == nil {
			t = &openTest{}
			p.open[k] = t
		}
		t.output(e.Output)
	case "pass", "fail", "skip":
		t := p.open[k]
		delete(p.open, k)
		if e.Action == "skip" {
			reason := ""
			if t != nil {
				reason = cmp.Or(t.logged, t.first)
			}
			p.skips = append(p.skips, Skip{Package: e.Package, Test: e.Test, Reason: reason})
			return
		}
		if !strings.Contains(e.Test, "/") {
			p.tests = append(p.tests, Test{Package: e.Package, Name: e.Test, Failed: e.Action == "fail", Elapsed: e.Elapsed})
		}
	}
}

// logLine matches the header go test's t.Log, t.Skip and friends start a message with: "file_test.go:12: ".
var logLine = regexp.MustCompile(`^[^\s:]+\.go:\d+: `)

// output keeps the line a skip's reason would be: the last log line the test printed, or else its first line of
// output; test2json framing and the --- SKIP line itself are not reasons.
func (t *openTest) output(out string) {
	line := strings.TrimSpace(out)
	if line == "" || framing(line) {
		return
	}
	line = reason(line)
	if t.first == "" {
		t.first = line
	}
	if logLine.MatchString(line) {
		t.logged = line
	}
}

// framing reports whether a trimmed test output line is test2json framing rather than what the test printed.
func framing(line string) bool {
	for _, p := range []string{"=== RUN", "=== PAUSE", "=== CONT", "=== NAME", "--- PASS", "--- FAIL", "--- SKIP"} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// reason cuts line to at most MaxReason bytes at a rune boundary.
func reason(line string) string {
	if len(line) <= MaxReason {
		return line
	}
	i := MaxReason
	for i > 0 && !utf8.RuneStart(line[i]) {
		i--
	}
	return line[:i]
}

func (p *parser) summary() Summary {
	// A package whose own build failed is in state BuildFailed even when its result line carries no FailedBuild;
	// a dependency's build-fail names no tested package, so it adds none.
	for path := range p.built {
		if pkg := p.pkgs[path]; pkg != nil && (pkg.State == Fail || pkg.State == NoResult) {
			pkg.State, pkg.Elapsed = BuildFailed, 0
		}
	}
	s := Summary{Packages: make([]Package, 0, len(p.pkgs)), Tests: p.tests, Skips: p.skips}
	for _, pkg := range p.pkgs {
		s.Packages = append(s.Packages, *pkg)
	}
	slices.SortFunc(s.Packages, func(a, b Package) int { return strings.Compare(a.Path, b.Path) })
	return s
}

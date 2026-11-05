package testresults_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"

	"github.com/ndrwl/guildhall/internal/testresults"
)

func read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func parse(t *testing.T, stream string) testresults.Summary {
	t.Helper()
	s, err := testresults.Parse(strings.NewReader(stream), 1<<20)
	if err != nil {
		t.Fatalf("Parse = %v", err)
	}
	return s
}

// TestParseRecorded: each recorded stream, modelled on .github/tests/timings/testdata, gives its packages' states
// and elapsed times, its top-level tests only, and its test-level skips with their reasons; a package with no test
// files is NoTests and never a skip.
func TestParseRecorded(t *testing.T) {
	for _, tc := range []struct {
		file string
		want testresults.Summary
	}{
		{file: "pass.jsonl", want: testresults.Summary{
			Packages: []testresults.Package{{Path: "probe/empty", State: testresults.NoTests}, {Path: "probe/good", State: testresults.Pass, Elapsed: 0.017}},
			Tests:    []testresults.Test{{Package: "probe/good", Name: "TestG"}},
		}},
		{file: "fail.jsonl", want: testresults.Summary{
			Packages: []testresults.Package{{Path: "probe/ok", State: testresults.Fail, Elapsed: 0.009}},
			Tests: []testresults.Test{{Package: "probe/ok", Name: "TestParent", Failed: true}, {Package: "probe/ok", Name: "TestQuiet"},
				{Package: "probe/ok", Name: "TestLogged", Failed: true}},
		}},
		{file: "build.jsonl", want: testresults.Summary{
			Packages: []testresults.Package{{Path: "probe/bad", State: testresults.BuildFailed}, {Path: "probe/good", State: testresults.Pass}},
			Tests:    []testresults.Test{{Package: "probe/good", Name: "TestG"}},
		}},
		// The test running at the timeout never ends, so it has no timing; the package fails.
		{file: "timeout.jsonl", want: testresults.Summary{
			Packages: []testresults.Package{{Path: "probe/hang", State: testresults.Fail, Elapsed: 1.008}},
		}},
		{file: "skip.jsonl", want: testresults.Summary{
			Packages: []testresults.Package{{Path: "probe/skip", State: testresults.Pass, Elapsed: 0.009}},
			Tests:    []testresults.Test{{Package: "probe/skip", Name: "TestTable"}, {Package: "probe/skip", Name: "TestRuns"}},
			Skips: []testresults.Skip{
				{Package: "probe/skip", Test: "TestSkipped", Reason: "skip_test.go:7: needs docker; set GUILDHALL_CI_REQUIRE=docker to fail instead"},
				{Package: "probe/skip", Test: "TestTable/symlink", Reason: "skip_test.go:12: symlinks unsupported:"},
				{Package: "probe/skip", Test: "TestBare"},
			},
		}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			if got := parse(t, read(t, tc.file)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse(%s) =\n%+v\nwant\n%+v", tc.file, got, tc.want)
			}
		})
	}
}

// TestParseTolerates: non-JSON lines, JSON that is not an event, CRLF line ends and a last line cut short are
// ignored; a stream cut mid-package leaves that package with no result; a line over 1 MiB is skipped and the line
// after it still read.
func TestParseTolerates(t *testing.T) {
	pass := read(t, "pass.jsonl")
	want := parse(t, pass)
	noisy := "warning: summariser could not write\n[1, 2]\n{\"Time\":\"x\"}\n\n" + strings.ReplaceAll(pass, "\n", "\r\n") + `{"Action":"pass","Package":"probe/la`
	if got := parse(t, noisy); !reflect.DeepEqual(got, want) {
		t.Errorf("Parse(noisy) = %+v, want %+v", got, want)
	}

	fail := strings.SplitAfter(read(t, "fail.jsonl"), "\n")
	got := parse(t, strings.Join(fail[:12], ""))
	if !reflect.DeepEqual(got.Packages, []testresults.Package{{Path: "probe/ok", State: testresults.NoResult}}) || len(got.Tests) != 0 {
		t.Errorf("Parse(cut mid-package) = %+v, want probe/ok with no result and no tests", got)
	}

	long := `{"Action":"output","Package":"probe/good","Output":"` + strings.Repeat("x", 2<<20) + "\"}\n"
	s, err := testresults.Parse(strings.NewReader(long+pass), 4<<20)
	if err != nil || !reflect.DeepEqual(s, want) {
		t.Errorf("Parse(long line first) = %+v, %v; want %+v", s, err, want)
	}

	// A package whose result line names no FailedBuild, after its own build-fail, is still a build failure; a
	// dependency's build-fail adds no package.
	build := `{"ImportPath":"probe/dep","Action":"build-fail"}` + "\n" +
		`{"ImportPath":"probe/x [probe/x.test]","Action":"build-fail"}` + "\n" +
		`{"Action":"fail","Package":"probe/x","Elapsed":0.5}` + "\n"
	if got := parse(t, build); !reflect.DeepEqual(got.Packages, []testresults.Package{{Path: "probe/x", State: testresults.BuildFailed}}) {
		t.Errorf("Parse(build-fail without FailedBuild) = %+v", got.Packages)
	}
}

// TestParseLimit: a stream of exactly the limit is read; one byte more is a TooLargeError naming the limit, with no
// partial summary; a read error is returned as it is.
func TestParseLimit(t *testing.T) {
	pass := read(t, "pass.jsonl")
	if _, err := testresults.Parse(strings.NewReader(pass), int64(len(pass))); err != nil {
		t.Errorf("Parse at the limit = %v", err)
	}
	s, err := testresults.Parse(strings.NewReader(pass), int64(len(pass)-1))
	var tooLarge *testresults.TooLargeError
	if !errors.As(err, &tooLarge) || !errors.Is(err, testresults.ErrTooLarge) || tooLarge.Limit != int64(len(pass)-1) || len(s.Packages) != 0 {
		t.Errorf("Parse over the limit = %+v, %v; want a TooLargeError", s, err)
	}
	if want := "larger than 10 bytes"; (&testresults.TooLargeError{Limit: 10}).Error() != want {
		t.Errorf("TooLargeError reads %q, want %q", (&testresults.TooLargeError{Limit: 10}).Error(), want)
	}
	boom := errors.New("boom")
	if _, err := testresults.Parse(iotest.ErrReader(boom), 1<<20); !errors.Is(err, boom) {
		t.Errorf("Parse of a failing reader = %v, want boom", err)
	}
}

// TestSkipReason: a skip's reason is the last log line before the skip, or its first output line when none looks
// like a log line, cut to MaxReason bytes at a rune boundary.
func TestSkipReason(t *testing.T) {
	event := func(action, test, output string) string {
		line := `{"Action":"` + action + `","Package":"p","Test":"` + test + `"`
		if output != "" {
			line += `,"Output":` + quote(output)
		}
		return line + "}\n"
	}
	long := "x_test.go:1: " + strings.Repeat("é", testresults.MaxReason)
	stream := event("run", "TestA", "") + event("output", "TestA", "plain words\n") + event("output", "TestA", "more words\n") + event("skip", "TestA", "") +
		event("output", "TestB", long+"\n") + event("skip", "TestB", "")
	got := parse(t, stream).Skips
	if len(got) != 2 || got[0].Reason != "plain words" {
		t.Fatalf("Skips = %+v, want TestA's first output line", got)
	}
	if r := got[1].Reason; len(r) > testresults.MaxReason || !utf8.ValidString(r) || !strings.HasPrefix(long, r) || len(r) < testresults.MaxReason-1 {
		t.Errorf("long reason = %d bytes %q, want a valid prefix of at most %d bytes", len(r), r, testresults.MaxReason)
	}
}

func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

// TestSlowest: top-level tests rank slowest first, ties by package and name, and n bounds the list.
func TestSlowest(t *testing.T) {
	s := testresults.Summary{Tests: []testresults.Test{
		{Package: "b", Name: "TestX", Elapsed: 1}, {Package: "a", Name: "TestY", Elapsed: 3}, {Package: "a", Name: "TestX", Elapsed: 1},
		{Package: "a", Name: "TestZ", Elapsed: 0.5},
	}}
	want := []testresults.Test{{Package: "a", Name: "TestY", Elapsed: 3}, {Package: "a", Name: "TestX", Elapsed: 1}, {Package: "b", Name: "TestX", Elapsed: 1}}
	if got := s.Slowest(3); !reflect.DeepEqual(got, want) {
		t.Errorf("Slowest(3) = %+v, want %+v", got, want)
	}
	if got := s.Slowest(20); len(got) != 4 {
		t.Errorf("Slowest(20) = %d tests, want all 4", len(got))
	}
	if got := s.Slowest(-1); len(got) != 0 {
		t.Errorf("Slowest(-1) = %+v, want none", got)
	}
	if s.Tests[0].Package != "b" {
		t.Error("Slowest reordered the summary's tests")
	}
	if n := parse(t, read(t, "pass.jsonl")).Count(testresults.NoTests); n != 1 {
		t.Errorf("Count(NoTests) = %d, want 1", n)
	}
}

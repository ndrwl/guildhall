package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The testdata/*.jsonl streams are trimmed recordings of real go test -json
// runs over small throwaway packages:
//
//	pass.jsonl     go test -json -count=1 ./good ./empty
//	fail.jsonl     go test -json -count=1 ./ok (failing tests and subtests)
//	timeout.jsonl  go test -json -timeout 1s ./hang (stack trace trimmed)
//	build.jsonl    go test -json ./bad ./good (compile error; good cached)
//	cached.jsonl   go test -json ./good ./ok ./empty (good cached)

type result struct {
	stdout  string
	summary string
	json    []byte
}

func runInput(t *testing.T, input []byte) result {
	t.Helper()
	dir := t.TempDir()
	jsonOut := filepath.Join(dir, "nested", "dir", "test.json")
	summary := filepath.Join(dir, "summary.md")
	var out bytes.Buffer
	run(bytes.NewReader(input), &out, options{jsonOut: jsonOut, summary: summary, title: "Test (example)"})
	sum, err := os.ReadFile(summary)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	raw, err := os.ReadFile(jsonOut)
	if err != nil {
		t.Fatalf("read JSON copy: %v", err)
	}
	return result{stdout: out.String(), summary: string(sum), json: raw}
}

func runFile(t *testing.T, name string) result {
	t.Helper()
	return runInput(t, readTestdata(t, name))
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ev renders one test2json event line.
func ev(action, pkg, test string, elapsed float64, output string) string {
	b, err := json.Marshal(map[string]any{"Action": action, "Package": pkg, "Test": test, "Elapsed": elapsed, "Output": output})
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

func assertContains(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("output lacks %q; got:\n%s", w, got)
		}
	}
}

func assertLacks(t *testing.T, got string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(got, u) {
			t.Errorf("output has %q; got:\n%s", u, got)
		}
	}
}

func TestPassOnlyPrintsPackageLines(t *testing.T) {
	r := runFile(t, "pass.jsonl")
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	if len(lines) != 2 || lines[0] != "?   \tprobe/empty\t[no test files]" || lines[1] != "ok  \tprobe/good\t0.008s" {
		t.Fatalf("stdout = %q, want only the two package lines", r.stdout)
	}
	assertLacks(t, r.stdout, "===", "PASS", "good_test.go", "{")
}

func TestFailingTestPrintsItsOutput(t *testing.T) {
	r := runFile(t, "fail.jsonl")
	assertContains(t, r.stdout,
		"    ok_test.go:14: context before failure\n    ok_test.go:15: want 1, got 2\n--- FAIL: TestLogged (0.00s)\n",
		"FAIL\nFAIL\tprobe/ok\t0.008s\n")
	assertLacks(t, r.stdout, "=== RUN", "quiet passing noise", "--- PASS")
	if !strings.HasSuffix(r.stdout, "FAIL\tprobe/ok\t0.008s\n") {
		t.Errorf("stdout does not end with the package result line:\n%s", r.stdout)
	}
}

func TestFailingSubtestPrintsEachFailingNameOnce(t *testing.T) {
	r := runFile(t, "fail.jsonl")
	for _, s := range []string{"failing detail", "--- FAIL: TestParent/fail (", "--- FAIL: TestParent ("} {
		if n := strings.Count(r.stdout, s); n != 1 {
			t.Errorf("%q printed %d times, want 1:\n%s", s, n, r.stdout)
		}
	}
	if strings.Index(r.stdout, "--- FAIL: TestParent/fail") > strings.Index(r.stdout, "--- FAIL: TestParent (") {
		t.Errorf("parent printed before its failing subtest:\n%s", r.stdout)
	}
	assertLacks(t, r.stdout, "passing sibling noise", "second sibling noise")
}

func TestTimeoutPanicIsPrinted(t *testing.T) {
	r := runFile(t, "timeout.jsonl")
	assertContains(t, r.stdout,
		"    hang_test.go:5: before hang\n",
		"panic: test timed out after 1s\n\trunning tests:\n\t\tTestHang (1s)\n",
		"probe/hang.TestHang(")
	assertLacks(t, r.stdout, "=== RUN")
	if !strings.HasSuffix(r.stdout, "FAIL\tprobe/hang\t1.008s\n") {
		t.Errorf("stdout does not end with the package result line:\n%s", r.stdout)
	}
	assertContains(t, r.summary, "| 1 | probe/hang | 1.01s | fail |")
}

func TestPackageOutputPrintedWhenPackageFails(t *testing.T) {
	in := ev("start", "p", "", 0, "") +
		ev("output", "p", "", 0, "TestMain: GUILDHALL_CI_REQUIRE names pwsh but it is missing\n") +
		ev("output", "p", "", 0, "FAIL\tp\t0.010s\n") +
		ev("fail", "p", "", 0.01, "")
	r := runInput(t, []byte(in))
	if r.stdout != "TestMain: GUILDHALL_CI_REQUIRE names pwsh but it is missing\nFAIL\tp\t0.010s\n" {
		t.Errorf("stdout = %q", r.stdout)
	}
}

func TestBuildFailureIsPrinted(t *testing.T) {
	r := runFile(t, "build.jsonl")
	want := "# probe/bad [probe/bad.test]\n" +
		"bad/bad.go:3:23: cannot use \"x\" (untyped string constant) as int value in return statement\n" +
		"FAIL\tprobe/bad [build failed]\n" +
		"ok  \tprobe/good\t(cached)\n"
	if r.stdout != want {
		t.Errorf("stdout = %q, want %q", r.stdout, want)
	}
}

func TestCachedPackagesAreCountedNotRanked(t *testing.T) {
	r := runFile(t, "cached.jsonl")
	assertContains(t, r.stdout, "ok  \tprobe/good\t(cached)\n", "?   \tprobe/empty\t[no test files]\n")
	assertContains(t, r.summary,
		"Packages: 3 ran, 1 cached (not ranked). Top-level tests: 4 ran.",
		"| 1 | probe/ok |",
		"| TestParent | probe/ok |")
	assertLacks(t, r.summary, "probe/good", "TestG ", "probe/empty")
}

func TestNonJSONLinesPassThrough(t *testing.T) {
	in := "go: downloading example.com/x v1.0.0\n" + string(readTestdata(t, "pass.jsonl")) + "{not json\n"
	r := runInput(t, []byte(in))
	want := "go: downloading example.com/x v1.0.0\n?   \tprobe/empty\t[no test files]\nok  \tprobe/good\t0.008s\n{not json\n"
	if r.stdout != want {
		t.Errorf("stdout = %q, want %q", r.stdout, want)
	}
}

func TestLongLine(t *testing.T) {
	long := strings.Repeat("x", 100<<10) + "\n"
	in := ev("run", "p", "TestBig", 0, "") +
		ev("output", "p", "TestBig", 0, long) +
		ev("output", "p", "TestBig", 0, "--- FAIL: TestBig (0.00s)\n") +
		ev("fail", "p", "TestBig", 0, "") +
		ev("output", "p", "", 0, "FAIL\tp\t0.100s\n") +
		ev("fail", "p", "", 0.1, "")
	r := runInput(t, []byte(in))
	if want := long + "--- FAIL: TestBig (0.00s)\nFAIL\tp\t0.100s\n"; r.stdout != want {
		t.Errorf("stdout has %d bytes, want %d", len(r.stdout), len(want))
	}
	if !bytes.Equal(r.json, []byte(in)) {
		t.Error("JSON copy differs from the input")
	}
}

func TestRanking(t *testing.T) {
	var in strings.Builder
	for i := range 25 {
		pkg := fmt.Sprintf("p%02d", i)
		// Package and test times rise with i, except p00 and p01 tie.
		el := float64(i)
		if i == 0 {
			el = 1
		}
		in.WriteString(ev("run", pkg, "TestTop", 0, ""))
		in.WriteString(ev("run", pkg, "TestTop/sub", 0, ""))
		in.WriteString(ev("pass", pkg, "TestTop/sub", 1000, ""))
		in.WriteString(ev("pass", pkg, "TestTop", el, ""))
		in.WriteString(ev("output", pkg, "", 0, "ok  \t"+pkg+"\t1s\n"))
		in.WriteString(ev("pass", pkg, "", el, ""))
	}
	r := runInput(t, []byte(in.String()))
	assertContains(t, r.summary, "Packages: 25 ran, 0 cached (not ranked). Top-level tests: 25 ran.")
	assertLacks(t, r.summary, "TestTop/sub", "1000.00s")
	pkgRows, testRows := 0, 0
	for _, l := range strings.Split(r.summary, "\n") {
		switch {
		case strings.HasPrefix(l, "| ") && strings.Contains(l, "TestTop"):
			testRows++
		case strings.HasPrefix(l, "| ") && strings.Contains(l, "| p"):
			pkgRows++
		}
	}
	if pkgRows != topN || testRows != topN {
		t.Errorf("rows: %d packages, %d tests, want %d each:\n%s", pkgRows, testRows, topN, r.summary)
	}
	assertContains(t, r.summary, "| 1 | p24 | 24.00s | pass |", "| 1 | TestTop | p24 | 24.00s | pass |")
	// p00 and p01 tie on 1s; slowest keeps input order among ties.
	got := slowest([]timing{{name: "a", elapsed: 1}, {name: "b", elapsed: 2}, {name: "c", elapsed: 1}}, 3)
	if got[0].name != "b" || got[1].name != "a" || got[2].name != "c" {
		t.Errorf("slowest = %v, want b, a, c", got)
	}
}

func TestMarkdownEscaping(t *testing.T) {
	if got, want := escape("Test|a`b`\\"), "Test\\|a\\`b\\`\\\\"; got != want {
		t.Errorf("escape = %q, want %q", got, want)
	}
	in := ev("pass", "p", "TestPipe|`x`", 2, "") + ev("pass", "p", "", 3, "")
	r := runInput(t, []byte(in))
	assertContains(t, r.summary, "| 1 | TestPipe\\|\\`x\\` | p | 2.00s | pass |")
}

func TestSummaryAppends(t *testing.T) {
	dir := t.TempDir()
	summary := filepath.Join(dir, "summary.md")
	in := readTestdata(t, "pass.jsonl")
	run(bytes.NewReader(in), &bytes.Buffer{}, options{summary: summary, title: "Test (one)"})
	run(bytes.NewReader(in), &bytes.Buffer{}, options{summary: summary, title: "Test (two)"})
	b, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, string(b), "## Test (one)\n", "## Test (two)\n", "### 20 slowest packages", "### 20 slowest tests")
}

func TestUnwritableOutputsWarnAndContinue(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	// The summary path is a directory; the JSON path is under a file.
	run(bytes.NewReader(readTestdata(t, "pass.jsonl")), &out, options{
		jsonOut: filepath.Join(file, "test.json"),
		summary: dir,
		title:   "Test",
	})
	got := out.String()
	if n := strings.Count(got, "warning: timings: "); n != 2 {
		t.Errorf("%d warnings, want 2:\n%s", n, got)
	}
	assertContains(t, got, "ok  \tprobe/good\t", "?   \tprobe/empty\t[no test files]\n", "step summary")
}

func TestJSONCopyIsByteForByte(t *testing.T) {
	var in bytes.Buffer
	for _, name := range []string{"pass.jsonl", "fail.jsonl", "timeout.jsonl", "build.jsonl", "cached.jsonl"} {
		in.Write(readTestdata(t, name))
	}
	in.WriteString("not json\r\n\n")
	in.WriteString(ev("output", "p", "", 0, "no trailing newline"))
	in.Truncate(in.Len() - 1)
	r := runInput(t, in.Bytes())
	if !bytes.Equal(r.json, in.Bytes()) {
		t.Errorf("JSON copy has %d bytes, input %d; they differ", len(r.json), in.Len())
	}
}

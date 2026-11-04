// Command summarise reads a `go test -json` stream on stdin, saves it
// unchanged to a file, prints what plain `go test` would print (package
// result lines, compile errors and each failing test's output), and appends
// the 20 slowest packages and top-level tests to the GitHub step summary.
//
// It never decides pass or fail: it exits 0 once it has read all its input,
// so under `bash -eo pipefail` a CI step's status is go test's.
// See docs/decisions/ci-test-timings.md.
//
// Usage:
//
//	go test -json ./... | summarise -json-out timings/test.json -title "Test (linux)"
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// topN is how many packages and tests each summary table ranks.
const topN = 20

func main() {
	jsonOut := flag.String("json-out", "", "file to save the raw go test -json stream to")
	title := flag.String("title", "go test", "heading of the step summary section")
	flag.Parse()
	run(os.Stdin, os.Stdout, options{
		jsonOut: *jsonOut,
		summary: os.Getenv("GITHUB_STEP_SUMMARY"),
		title:   *title,
	})
}

type options struct {
	jsonOut string // raw copy of the stream; empty for none
	summary string // Markdown file the summary is appended to; empty for none
	title   string
}

// event is the part of a test2json event the summariser reads.
type event struct {
	Action  string
	Package string
	Test    string
	Output  string
	Elapsed float64
}

// timing is one ranked package or top-level test.
type timing struct {
	name    string
	pkg     string
	elapsed float64
	result  string
}

// pkgState is what is held for a package until its result event.
type pkgState struct {
	order  []string            // test names in first-output order
	bufs   map[string][]string // non-framing output per full test name
	pkgOut []string            // output with no test name
	tests  []timing            // finished top-level tests
	nTests int
}

type summariser struct {
	out       io.Writer
	pkgs      map[string]*pkgState
	packages  []timing
	tests     []timing
	nPackages int
	nCached   int
	nTests    int
}

// run copies in to opts.jsonOut, prints the plain-text view to out and
// writes the summary. Problems are printed as warning lines; it never fails.
func run(in io.Reader, out io.Writer, opts options) {
	s := &summariser{out: out, pkgs: map[string]*pkgState{}}

	var raw *os.File
	if opts.jsonOut != "" {
		if err := os.MkdirAll(filepath.Dir(opts.jsonOut), 0o755); err != nil {
			fmt.Fprintf(out, "warning: timings: %v\n", err)
		} else if f, err := os.Create(opts.jsonOut); err != nil {
			fmt.Fprintf(out, "warning: timings: %v\n", err)
		} else {
			raw = f
		}
	}
	var rawW *bufio.Writer
	if raw != nil {
		rawW = bufio.NewWriter(raw)
	}

	r := bufio.NewReader(in)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if rawW != nil {
				if _, werr := rawW.Write(line); werr != nil {
					fmt.Fprintf(out, "warning: timings: writing %s: %v\n", opts.jsonOut, werr)
					rawW = nil
				}
			}
			s.line(line)
		}
		if err != nil {
			if err != io.EOF {
				fmt.Fprintf(out, "warning: timings: reading input: %v\n", err)
			}
			break
		}
	}

	if raw != nil {
		if rawW != nil {
			if err := rawW.Flush(); err != nil {
				fmt.Fprintf(out, "warning: timings: writing %s: %v\n", opts.jsonOut, err)
			}
		}
		if err := raw.Close(); err != nil {
			fmt.Fprintf(out, "warning: timings: writing %s: %v\n", opts.jsonOut, err)
		}
	}

	if opts.summary != "" {
		if err := appendFile(opts.summary, s.markdown(opts.title)); err != nil {
			fmt.Fprintf(out, "warning: timings: step summary: %v\n", err)
		}
	}
}

func appendFile(path, text string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := io.WriteString(f, text)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// line handles one input line, newline included.
func (s *summariser) line(line []byte) {
	text := bytes.TrimRight(line, "\r\n")
	var e event
	if len(text) == 0 || text[0] != '{' || json.Unmarshal(text, &e) != nil || e.Action == "" {
		// Not a test2json event: pass it through as go test would print it.
		s.print(string(text) + "\n")
		return
	}
	switch e.Action {
	case "build-output":
		s.print(e.Output)
		return
	case "build-fail":
		return
	}
	if e.Package == "" {
		return
	}
	p := s.pkgs[e.Package]
	if p == nil {
		p = &pkgState{bufs: map[string][]string{}}
		s.pkgs[e.Package] = p
	}
	if e.Test == "" {
		s.packageEvent(e, p)
		return
	}
	switch e.Action {
	case "output":
		if framing(e.Output) {
			return
		}
		if _, ok := p.bufs[e.Test]; !ok {
			p.order = append(p.order, e.Test)
		}
		p.bufs[e.Test] = append(p.bufs[e.Test], e.Output)
	case "pass", "fail", "skip":
		if e.Action == "fail" {
			s.print(strings.Join(p.bufs[e.Test], ""))
		}
		delete(p.bufs, e.Test)
		if !strings.Contains(e.Test, "/") {
			p.nTests++
			if e.Action != "skip" {
				p.tests = append(p.tests, timing{name: e.Test, pkg: e.Package, elapsed: e.Elapsed, result: e.Action})
			}
		}
	}
}

func (s *summariser) packageEvent(e event, p *pkgState) {
	switch e.Action {
	case "output":
		p.pkgOut = append(p.pkgOut, e.Output)
		return
	case "pass", "fail", "skip":
	default:
		return
	}
	cached := false
	for _, o := range p.pkgOut {
		if resultLine(o) && strings.Contains(o, "(cached)") {
			cached = true
		}
	}
	if e.Action == "fail" {
		// Tests that never finished, such as the one running when the
		// package timed out, then the package's own output.
		for _, name := range p.order {
			if b, ok := p.bufs[name]; ok {
				s.print(strings.Join(b, ""))
			}
		}
		s.print(strings.Join(p.pkgOut, ""))
	} else {
		for _, o := range p.pkgOut {
			if resultLine(o) {
				s.print(o)
			}
		}
	}
	s.nPackages++
	s.nTests += p.nTests
	if cached {
		s.nCached++
	} else if e.Action != "skip" {
		s.packages = append(s.packages, timing{name: e.Package, elapsed: e.Elapsed, result: e.Action})
		s.tests = append(s.tests, p.tests...)
	}
	delete(s.pkgs, e.Package)
}

func (s *summariser) print(text string) {
	if text != "" {
		_, _ = io.WriteString(s.out, text)
	}
}

// framing reports whether a test output line is test2json framing that
// plain go test does not print for a failing test.
func framing(output string) bool {
	t := strings.TrimLeft(output, " ")
	for _, p := range []string{"=== RUN", "=== PAUSE", "=== CONT", "=== NAME"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// resultLine reports whether a package output line is the one line plain
// go test prints for a passing or skipped package.
func resultLine(output string) bool {
	return strings.HasPrefix(output, "ok  \t") || strings.HasPrefix(output, "?   \t")
}

// slowest returns the n longest timings, keeping input order among ties.
func slowest(ts []timing, n int) []timing {
	out := append([]timing(nil), ts...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].elapsed > out[j].elapsed })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func (s *summariser) markdown(title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", title)
	fmt.Fprintf(&b, "Packages: %d ran, %d cached (not ranked). Top-level tests: %d ran.\n\n",
		s.nPackages, s.nCached, s.nTests)

	fmt.Fprintf(&b, "### %d slowest packages\n\n", topN)
	if pk := slowest(s.packages, topN); len(pk) == 0 {
		b.WriteString("No uncached package ran.\n\n")
	} else {
		b.WriteString("| # | Package | Time | Result |\n| ---: | --- | ---: | --- |\n")
		for i, t := range pk {
			fmt.Fprintf(&b, "| %d | %s | %.2fs | %s |\n", i+1, escape(t.name), t.elapsed, t.result)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "### %d slowest tests\n\n", topN)
	if ts := slowest(s.tests, topN); len(ts) == 0 {
		b.WriteString("No top-level test ran in an uncached package.\n\n")
	} else {
		b.WriteString("| # | Test | Package | Time | Result |\n| ---: | --- | --- | ---: | --- |\n")
		for i, t := range ts {
			fmt.Fprintf(&b, "| %d | %s | %s | %.2fs | %s |\n", i+1, escape(t.name), escape(t.pkg), t.elapsed, t.result)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// escape makes a name safe inside a Markdown table cell.
func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, "|", `\|`, "`", "\\`").Replace(s)
}

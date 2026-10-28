package fake

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/strictjson"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// ScenarioEnv names the environment variable holding the absolute path of a scenario file. Unset or empty, the
// fake Artisan plays only its prompt's directives. It is for tests and demos (docs/adapters.md#scenarios).
const ScenarioEnv = "GUILDHALL_FAKE_SCENARIO"

// ScenarioSchema identifies a scenario file.
const ScenarioSchema = "guildhall.fake-scenario/1"

const (
	scenarioPrefix  = "guildhall.fake-scenario/"
	scenarioVersion = 1
)

// ScenarioReviewFile is the output path, relative to the working directory, of a scripted review outcome, as
// interptest.ReviewOutcomeFile is the mock agent's.
const ScenarioReviewFile = "review-outcome.json"

// Section names a check may use besides the prompt's headings: the lines before the first heading, and the whole text.
const (
	SectionHeader = "header"
	SectionPrompt = "prompt"
)

// promptSections are the headings render.composePrompt and its output instructions write, in the order they appear.
var promptSections = []string{"Why this is a retry", "Role", "Stage guide", "Instructions", "Inputs", "Standards", "Skills", "Required outputs", "Result template"}

// Scenario is a scenario file: for each launch it allows, the prompt checks and the scripted result.
type Scenario struct {
	Schema   string           `json:"schema"`
	Launches []ScenarioLaunch `json:"launches"`
}

// ScenarioLaunch is one launch, keyed by step and attempt number.
type ScenarioLaunch struct {
	Step    string `json:"step"`
	Attempt int    `json:"attempt"`
	// Checks are what the prompt must say, section by section.
	Checks []Check `json:"checks,omitempty"`
	// Exact, when set, names a file, relative to the scenario file's directory, the prompt must equal byte for byte.
	Exact string `json:"exact,omitempty"`
	// Respond is the result the launch writes once every check passes.
	Respond Respond `json:"respond"`
}

// Check is one assertion on a prompt section: exactly one of Contains, NotContains and Absent.
type Check struct {
	// Section is a heading title, SectionHeader or SectionPrompt.
	Section     string `json:"section"`
	Contains    string `json:"contains,omitempty"`
	NotContains string `json:"not_contains,omitempty"`
	Absent      bool   `json:"absent,omitempty"`
}

// Respond is a scripted result: Status, Summary and Files, or Review alone.
type Respond struct {
	Status  adapter.Status `json:"status,omitempty"`
	Summary string         `json:"summary,omitempty"`
	Files   []ScenarioFile `json:"files,omitempty"`
	// Review writes ScenarioReviewFile and succeeds, as interptest.Review does.
	Review *workflow.ReviewOutcome `json:"review,omitempty"`
}

// ScenarioFile is one file a response writes into the working directory and lists as an output, in order.
type ScenarioFile struct {
	// Path is slash-separated and relative to the working directory.
	Path string `json:"path"`
	Text string `json:"text"`
}

// loadedScenario is a validated scenario with each launch's exact file read.
type loadedScenario struct {
	path     string
	launches []ScenarioLaunch
	exact    map[int][]byte
}

// loadScenario reads and validates the scenario file at path, an absolute path. It refuses another schema, an unknown
// field, a launch keyed twice, a check without a section or with no or two operators, an unknown section, a response
// that is neither a result nor a review, an unsafe or oversized file, an invalid review outcome and an unreadable or
// escaping exact file.
func loadScenario(path string) (*loadedScenario, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("scenario %q is not an absolute path", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("scenario: %w", err)
	}
	var s Scenario
	if err := strictjson.Decode(data, &s, strictjson.Options{Schema: scenarioPrefix, Version: scenarioVersion, MaxBytes: 4 * MaxPromptBytes}); err != nil {
		return nil, fmt.Errorf("scenario %s: %w", path, err)
	}
	l := &loadedScenario{path: path, launches: s.Launches, exact: map[int][]byte{}}
	seen := map[string]bool{}
	var problems []string
	for i, launch := range s.Launches {
		bad := func(format string, args ...any) {
			problems = append(problems, fmt.Sprintf("launch %d (step %q attempt %d): ", i+1, launch.Step, launch.Attempt)+fmt.Sprintf(format, args...))
		}
		if launch.Step == "" || launch.Attempt < 1 {
			bad("needs a step and an attempt from 1")
		}
		key := launchKey(launch.Step, launch.Attempt)
		if seen[key] {
			bad("is listed twice")
		}
		seen[key] = true
		for j, c := range launch.Checks {
			if err := c.validate(); err != nil {
				bad("check %d: %v", j+1, err)
			}
		}
		if launch.Exact != "" {
			if !safePath(launch.Exact) {
				bad("exact %q must be a relative slash-separated path below the scenario's directory", launch.Exact)
			} else if golden, err := os.ReadFile(filepath.Join(filepath.Dir(path), filepath.FromSlash(launch.Exact))); err != nil {
				bad("exact: %v", err)
			} else {
				l.exact[i] = golden
			}
		}
		if err := launch.Respond.validate(); err != nil {
			bad("respond: %v", err)
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("scenario %s: %s", path, strings.Join(problems, "; "))
	}
	return l, nil
}

func (c Check) validate() error {
	if c.Section == "" {
		return errors.New("names no section")
	}
	if c.Section != SectionHeader && c.Section != SectionPrompt && !slices.Contains(promptSections, c.Section) {
		return fmt.Errorf("section %q is not %s, %s or a prompt heading (%s)", c.Section, SectionHeader, SectionPrompt, strings.Join(promptSections, ", "))
	}
	ops := 0
	for _, set := range []bool{c.Contains != "", c.NotContains != "", c.Absent} {
		if set {
			ops++
		}
	}
	if ops != 1 {
		return fmt.Errorf("section %q needs exactly one of contains, not_contains and absent, has %d", c.Section, ops)
	}
	if c.Absent && (c.Section == SectionHeader || c.Section == SectionPrompt) {
		return fmt.Errorf("section %q is always present", c.Section)
	}
	return nil
}

func (r Respond) validate() error {
	if r.Review != nil {
		if r.Status != "" || r.Summary != "" || len(r.Files) > 0 {
			return errors.New("a review response has no status, summary or files")
		}
		return r.Review.Validate()
	}
	if r.Status != adapter.StatusSucceeded && r.Status != adapter.StatusFailed {
		return fmt.Errorf("status %q must be %s or %s", r.Status, adapter.StatusSucceeded, adapter.StatusFailed)
	}
	if strings.TrimSpace(r.Summary) == "" {
		return errors.New("summary is empty")
	}
	seen := map[string]bool{}
	for _, f := range r.Files {
		switch {
		case !safePath(f.Path):
			return fmt.Errorf("file path %q must be a relative slash-separated path with no . or .. segments", f.Path)
		case seen[f.Path]:
			return fmt.Errorf("file path %q is listed twice", f.Path)
		case len(f.Text) > MaxPromptBytes:
			return fmt.Errorf("file %q is larger than %d bytes", f.Path, MaxPromptBytes)
		}
		seen[f.Path] = true
	}
	return nil
}

// safePath reports whether p is a clean relative slash-separated path, as interptest refuses response paths.
func safePath(p string) bool {
	return fs.ValidPath(p) && p != "." && !strings.Contains(p, `\`) && filepath.IsLocal(filepath.FromSlash(p))
}

func launchKey(step string, attempt int) string { return step + "-" + strconv.Itoa(attempt) }

// SplitAttemptID splits an attempt id such as sign-off-2 at its last hyphen into the step and attempt number.
func SplitAttemptID(id string) (string, int, error) {
	i := strings.LastIndexByte(id, '-')
	if i <= 0 {
		return "", 0, fmt.Errorf("attempt id %q is not <step>-<number>", id)
	}
	n, err := strconv.Atoi(id[i+1:])
	if err != nil || n < 1 || id[i+1:] != strconv.Itoa(n) {
		return "", 0, fmt.Errorf("attempt id %q is not <step>-<number>", id)
	}
	return id[:i], n, nil
}

// promptSection returns the body of section name in prompt and whether it is there. Headings are found in the
// order promptSections lists them, outside fenced blocks, so a `## ` line quoted in a fence cannot start or end one.
func promptSection(prompt []byte, name string) (string, bool) {
	if name == SectionPrompt {
		return string(prompt), true
	}
	type found struct {
		name       string
		start, end int
	}
	var heads []found
	next := 0
	var fence string
	offset := 0
	sc := bufio.NewScanner(bytes.NewReader(prompt))
	sc.Buffer(make([]byte, 0, 64*1024), MaxPromptBytes+1)
	for sc.Scan() {
		line := sc.Text()
		lineStart := offset
		offset += len(line) + 1
		trimmed := strings.TrimLeft(line, " ")
		if fence != "" {
			if len(line)-len(trimmed) <= 3 && strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if run := fenceRun(trimmed); run != "" && len(line)-len(trimmed) <= 3 {
			fence = run
			continue
		}
		title, ok := strings.CutPrefix(line, "## ")
		if !ok {
			continue
		}
		for k := next; k < len(promptSections); k++ {
			if promptSections[k] == title {
				heads = append(heads, found{name: title, start: lineStart, end: min(offset, len(prompt))})
				next = k + 1
				break
			}
		}
	}
	if name == SectionHeader {
		if len(heads) == 0 {
			return string(prompt), true
		}
		return string(prompt[:heads[0].start]), true
	}
	for i, h := range heads {
		if h.name != name {
			continue
		}
		stop := len(prompt)
		if i+1 < len(heads) {
			stop = heads[i+1].start
		}
		return string(prompt[h.end:stop]), true
	}
	return "", false
}

// fenceRun is the backtick or tilde run of three or more that opens a fenced block on line, or "".
func fenceRun(line string) string {
	for _, c := range "`~" {
		n := 0
		for n < len(line) && rune(line[n]) == c {
			n++
		}
		if n >= 3 {
			return line[:n]
		}
	}
	return ""
}

// failures are the checks prompt fails, each named, and the exact file's mismatch.
func (l *loadedScenario) failures(i int, prompt []byte) []string {
	launch := l.launches[i]
	var out []string
	for _, c := range launch.Checks {
		body, ok := promptSection(prompt, c.Section)
		switch {
		case c.Absent && ok:
			out = append(out, fmt.Sprintf("section %q is present", c.Section))
		case c.Absent:
		case !ok:
			out = append(out, fmt.Sprintf("section %q is missing", c.Section))
		case c.Contains != "" && !strings.Contains(body, c.Contains):
			out = append(out, fmt.Sprintf("section %q does not contain %q", c.Section, c.Contains))
		case c.NotContains != "" && strings.Contains(body, c.NotContains):
			out = append(out, fmt.Sprintf("section %q contains %q", c.Section, c.NotContains))
		}
	}
	if golden, ok := l.exact[i]; ok && !bytes.Equal(prompt, golden) {
		out = append(out, fmt.Sprintf("prompt does not match %s at line %d", launch.Exact, firstDifferentLine(prompt, golden)))
	}
	return out
}

func firstDifferentLine(a, b []byte) int {
	line := 1
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return line
		}
		if a[i] == '\n' {
			line++
		}
	}
	return line
}

// ScenarioRecord is what one launch made under a scenario records, in <scenario>.records/<step>-<n>.json.
type ScenarioRecord struct {
	Step         string   `json:"step"`
	Attempt      int      `json:"attempt"`
	PromptSHA256 string   `json:"prompt_sha256"`
	Passed       bool     `json:"passed"`
	Failures     []string `json:"failures,omitempty"`
}

// RecordsDir is the directory beside the scenario file at path where launches record themselves.
func RecordsDir(path string) string { return path + ".records" }

// writeRecord creates the record for attemptID exclusively; a second launch with the same id is recorded as a
// failure in a file of its own, <attempt id>.again-<k>.json, so it cannot overwrite the first.
func writeRecord(path, attemptID string, rec ScenarioRecord) error {
	dir := RecordsDir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	first := rec
	for k := 1; ; k++ {
		name := attemptID + ".json"
		if k > 1 {
			name = fmt.Sprintf("%s.again-%d.json", attemptID, k)
			rec.Passed = false
			rec.Failures = append(slices.Clone(first.Failures), fmt.Sprintf("attempt %s was launched %d times", attemptID, k))
		}
		data, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			return err
		}
		switch err := writeNew(filepath.Join(dir, name), append(data, '\n')); {
		case errors.Is(err, adapter.ErrResultExists):
			continue
		case err != nil:
			return err
		case k > 1:
			return errDuplicateLaunch
		}
		return nil
	}
}

var errDuplicateLaunch = errors.New("launched more than once")

// scenarioOutcome is what a launch under a scenario writes: nil leaves the result to the directives.
type scenarioOutcome struct {
	status  adapter.Status
	summary string
	files   []ScenarioFile
}

// errScenario is a scenario that cannot be played: the fake writes a failed result naming it and exits 1.
type errScenario struct{ err error }

func (e errScenario) Error() string { return "fake scenario: " + e.err.Error() }
func (e errScenario) Unwrap() error { return e.err }

// writeScenarioFailure writes the failed result naming the scenario error err, so it shows in the timeline.
func writeScenarioFailure(a Args, err error) error {
	return adapter.WriteResult(a.ResultFile, adapter.Result{Identity: a.Identity, Status: adapter.StatusFailed, Summary: err.Error(), Outputs: []adapter.Output{}})
}

// playScenario matches the launch a against the scenario named by ScenarioEnv, records it, and returns the outcome
// to write: nil with no scenario, the scripted response once every check passes, and a failed result naming the
// unmatched launch or every failing check otherwise. A scenario it cannot load or record, or a matching entry
// beside the output directive, is an errScenario.
func playScenario(a Args, d directives, prompt []byte) (*scenarioOutcome, error) {
	path := os.Getenv(ScenarioEnv)
	if path == "" {
		return nil, nil
	}
	sum := sha256.Sum256(prompt)
	step, n, splitErr := SplitAttemptID(a.AttemptID)
	rec := ScenarioRecord{Step: step, Attempt: n, PromptSHA256: hex.EncodeToString(sum[:])}
	fail := func(err error) (*scenarioOutcome, error) {
		rec.Failures = append(rec.Failures, err.Error())
		if filepath.IsAbs(path) {
			_ = writeRecord(path, a.AttemptID, rec)
		}
		return nil, errScenario{err}
	}
	if splitErr != nil {
		return fail(splitErr)
	}
	l, err := loadScenario(path)
	if err != nil {
		return fail(err)
	}
	i := slices.IndexFunc(l.launches, func(s ScenarioLaunch) bool { return s.Step == step && s.Attempt == n })
	var out *scenarioOutcome
	switch {
	case i < 0:
		rec.Failures = []string{fmt.Sprintf("no launch for step %s attempt %d", step, n)}
		out = &scenarioOutcome{status: adapter.StatusFailed, summary: "fake scenario: " + rec.Failures[0]}
	case d.output != "":
		return fail(fmt.Errorf("step %s attempt %d: the output directive and a scenario launch both script the result", step, n))
	default:
		rec.Failures = l.failures(i, prompt)
		if len(rec.Failures) > 0 {
			out = &scenarioOutcome{status: adapter.StatusFailed, summary: fmt.Sprintf("fake scenario: step %s attempt %d: %s", step, n, strings.Join(rec.Failures, "; "))}
		} else {
			out = l.launches[i].Respond.outcome()
		}
	}
	rec.Passed = len(rec.Failures) == 0
	if err := writeRecord(path, a.AttemptID, rec); errors.Is(err, errDuplicateLaunch) {
		return &scenarioOutcome{status: adapter.StatusFailed, summary: fmt.Sprintf("fake scenario: step %s attempt %d was already launched", step, n)}, nil
	} else if err != nil {
		return nil, errScenario{fmt.Errorf("record: %w", err)}
	}
	return out, nil
}

func (r Respond) outcome() *scenarioOutcome {
	if r.Review != nil {
		data, err := json.MarshalIndent(r.Review, "", "  ")
		if err != nil {
			panic(err) // A validated outcome always marshals.
		}
		return &scenarioOutcome{status: adapter.StatusSucceeded, summary: "review " + string(r.Review.Verdict),
			files: []ScenarioFile{{Path: ScenarioReviewFile, Text: string(data) + "\n"}}}
	}
	return &scenarioOutcome{status: r.Status, summary: r.Summary, files: r.Files}
}

// write writes the outcome's files into the working directory, the current directory, refusing one that aliases
// the result file, and returns them as outputs in order.
func (o *scenarioOutcome) write(resultFile string) ([]adapter.Output, error) {
	result, err := filepath.Abs(resultFile)
	if err != nil {
		return nil, err
	}
	resultInfo, err := os.Stat(result)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, f := range o.files {
		target, err := filepath.Abs(filepath.FromSlash(f.Path))
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(target, result) {
			return nil, fmt.Errorf("file path %q is the result file", f.Path)
		}
		if resultInfo != nil {
			if info, err := os.Stat(target); err == nil && os.SameFile(resultInfo, info) {
				return nil, fmt.Errorf("file path %q is the result file", f.Path)
			}
		}
	}
	outputs := []adapter.Output{}
	for _, f := range o.files {
		p := filepath.FromSlash(f.Path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := atomicfile.Write(p, []byte(f.Text)); err != nil {
			return nil, err
		}
		outputs = append(outputs, adapter.Output{Path: f.Path})
	}
	return outputs, nil
}

// TB is the part of testing.TB CheckScenarioRecords reports through.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
}

// CheckScenarioRecords fails t for every launch of the scenario at path that did not pass, every scenario launch
// never made and every launch the scenario has no entry for.
func CheckScenarioRecords(t TB, path string) {
	t.Helper()
	l, err := loadScenario(path)
	if err != nil {
		t.Errorf("%v", err)
		return
	}
	entries, err := os.ReadDir(RecordsDir(path))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("scenario records: %v", err)
		return
	}
	launched := map[string]bool{}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(RecordsDir(path), e.Name()))
		if err != nil {
			t.Errorf("scenario record %s: %v", e.Name(), err)
			continue
		}
		var rec ScenarioRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			t.Errorf("scenario record %s: %v", e.Name(), err)
			continue
		}
		launched[launchKey(rec.Step, rec.Attempt)] = true
		if !rec.Passed {
			t.Errorf("scenario launch %s did not pass: %s", strings.TrimSuffix(e.Name(), ".json"), strings.Join(rec.Failures, "; "))
		}
	}
	for _, launch := range l.launches {
		if !launched[launchKey(launch.Step, launch.Attempt)] {
			t.Errorf("scenario launch step %s attempt %d never happened", launch.Step, launch.Attempt)
		}
	}
}

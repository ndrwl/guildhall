// Package interptest holds the scripted mock agent, the double for interp.Launcher,
// the shared Launcher contract suite it passes, and helpers that render attempts from
// the fixtures in internal/interp/testdata. It makes no model calls and proves no
// semantic intelligence: it checks what it was given and returns what it was told to.
// See docs/mock-agent.md.
package interptest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// ReviewOutcomeFile is the output path, relative to the working directory, of a scripted review outcome.
const ReviewOutcomeFile = "review-outcome.json"

// ErrScript reports a launch the script did not allow; the script has also failed the test.
var ErrScript = errors.New("mock agent")

var scriptIDs atomic.Uint64

// TB is the part of testing.TB a Script reports through.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Cleanup(func())
}

// File is one file a response writes into the working directory.
type File struct {
	// Path is slash-separated and relative to the working directory.
	Path string
	Data []byte
}

// Response is what the mock agent does for a matched launch.
// Build one with Succeed, Fail, Review, Blocked or Hang.
type Response struct {
	status  adapter.Status
	summary string
	files   []File
	hang    bool
}

// Succeed writes files into the working directory, then a succeeded result listing them as outputs.
func Succeed(summary string, files ...File) Response {
	return Response{status: adapter.StatusSucceeded, summary: summary, files: files}
}

// Fail writes a failed result with no outputs.
func Fail(summary string) Response {
	return Response{status: adapter.StatusFailed, summary: summary}
}

// Review writes outcome to ReviewOutcomeFile, then a succeeded result listing it.
// An outcome that fails workflow.ReviewOutcome.Validate panics, because the script itself is wrong.
func Review(outcome workflow.ReviewOutcome) Response {
	if err := outcome.Validate(); err != nil {
		panic("interptest.Review: " + err.Error())
	}
	return Succeed("review "+string(outcome.Verdict), File{Path: ReviewOutcomeFile, Data: indent(outcome)})
}

// Blocked writes the question to interp.BlockedQuestionFile, then a failed result listing it.
func Blocked(question string, options ...string) Response {
	q := interp.BlockedQuestion{Schema: interp.BlockedQuestionSchema, Question: question, Options: options}
	r := Succeed("blocked: "+question, File{Path: interp.BlockedQuestionFile, Data: indent(q)})
	r.status = adapter.StatusFailed
	return r
}

// Hang writes nothing: the attempt keeps working until it is stopped.
func Hang() Response { return Response{hang: true} }

func indent(v any) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(data, '\n')
}

// Expectation is one launch the script allows, matched against the launched Spec's manifest.
type Expectation struct {
	// Step is the workflow step id the prompt was rendered for.
	Step string
	// Attempt is the attempt number; 0 matches any.
	Attempt int
	// Profile is the profile named in the manifest identity, empty for none.
	Profile string
	// Inputs maps each input port to the digest ("sha256:<hex>") of the artefact revision it must carry.
	Inputs map[string]string
	// Standards are the standard names the prompt must carry, in order.
	Standards []string
	// Skills are the skill names the prompt must carry, in order.
	Skills []string
	// Respond is what the mock agent does once the launch matches.
	Respond Response
}

// Expect returns the expectation that spec's own manifest meets, responding with r.
func Expect(spec interp.Spec, r Response) Expectation {
	m := spec.Manifest
	e := Expectation{Step: m.Identity.Step, Attempt: m.Identity.Attempt, Profile: m.Identity.Profile, Inputs: map[string]string{}, Respond: r}
	for _, in := range m.Inputs {
		e.Inputs[in.Port] = in.Digest
	}
	for _, f := range m.Standards {
		e.Standards = append(e.Standards, f.Path)
	}
	for _, s := range m.Skills {
		e.Skills = append(e.Skills, s.Name)
	}
	return e
}

// Script is a scripted interp.Launcher: each Launch must match the next expectation, in order.
// It fails the test, and refuses the launch with ErrScript, on an adapter other than interp.MockAdapter,
// a launch after the script is exhausted, a launch for another step, attempt or profile,
// a manifest that lacks a required input, standard or skill or carries one not expected,
// an input carrying another artefact revision, and a prompt, input copy or skill file that does
// not match the manifest. Expectations never launched fail the test when it ends.
// A spec that fails interp.Spec.Validate is refused without failing the test, as every Launcher refuses it.
//
// A matched launch writes the response's files into the working directory, each through a
// temporary file renamed into place, then publishes the result with adapter.WriteResult, so a
// reader sees no result or the whole one; an existing result is never replaced.
// Launch returns after writing: the mock runs no process.
type Script struct {
	t       TB
	id      uint64
	mu      sync.Mutex
	next    []Expectation
	handles map[interp.Handle]bool
	specs   []interp.Spec
}

// NewScript returns a Script expecting exps in order.
func NewScript(t TB, exps ...Expectation) *Script {
	s := &Script{t: t, id: scriptIDs.Add(1), next: exps, handles: map[interp.Handle]bool{}}
	t.Cleanup(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.next) > 0 {
			steps := make([]string, len(s.next))
			for i, e := range s.next {
				steps[i] = e.Step
			}
			t.Errorf("mock agent: %d expected launches never happened: %s", len(s.next), strings.Join(steps, ", "))
		}
	})
	return s
}

// Expect appends expectations to the script.
func (s *Script) Expect(exps ...Expectation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next = append(s.next, exps...)
}

// Launched returns the specs launched so far, in order, including refused ones that consumed an expectation.
func (s *Script) Launched() []interp.Spec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.specs)
}

// Launch matches spec against the next expectation and carries out its response.
func (s *Script) Launch(ctx context.Context, spec interp.Spec) (interp.Handle, error) {
	if err := ctx.Err(); err != nil {
		return interp.Handle{}, err
	}
	if err := spec.Validate(); err != nil {
		return interp.Handle{}, fmt.Errorf("mock agent: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.next) == 0 {
		return interp.Handle{}, s.fail("script exhausted: unexpected launch of step %q attempt %d", spec.Manifest.Identity.Step, spec.Manifest.Identity.Attempt)
	}
	// A refused launch still consumes its expectation, so one wrong launch is reported once.
	e := s.next[0]
	s.next = s.next[1:]
	s.specs = append(s.specs, spec)
	if err := interp.CheckAdapter(spec.Adapter); err != nil {
		return interp.Handle{}, s.fail("%v", err)
	}
	if spec.Adapter != interp.MockAdapter {
		return interp.Handle{}, s.fail("spec names adapter %q, want %q", spec.Adapter, interp.MockAdapter)
	}
	if problems := append(match(e, spec.Manifest), verifyFiles(spec)...); len(problems) > 0 {
		return interp.Handle{}, s.fail("%s", strings.Join(problems, "; "))
	}
	h := interp.Handle{Identity: spec.Identity, Ref: fmt.Sprintf("mock-%d-%d", s.id, len(s.specs))}
	if !e.Respond.hang {
		if err := respond(spec, e.Respond); err != nil {
			return interp.Handle{}, fmt.Errorf("mock agent: %w", err)
		}
	}
	s.handles[h] = true
	return h, nil
}

// Stop records the attempt as stopped. A hanging attempt never writes its result.
func (s *Script) Stop(h interp.Handle) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.handles[h]; !ok {
		return fmt.Errorf("mock agent: handle %q was not returned by this script", h.Ref)
	}
	s.handles[h] = false
	return nil
}

func (s *Script) fail(format string, args ...any) error {
	s.t.Helper()
	err := fmt.Errorf("%w: %s", ErrScript, fmt.Sprintf(format, args...))
	s.t.Errorf("%v", err)
	return err
}

// match compares the manifest with the expectation and names every difference.
func match(e Expectation, m render.Manifest) []string {
	id := m.Identity
	if id.Step != e.Step {
		return []string{fmt.Sprintf("unexpected launch: step %q, want step %q", id.Step, e.Step)}
	}
	var problems []string
	if e.Attempt != 0 && id.Attempt != e.Attempt {
		problems = append(problems, fmt.Sprintf("unexpected launch: attempt %d, want %d", id.Attempt, e.Attempt))
	}
	if id.Profile != e.Profile {
		problems = append(problems, fmt.Sprintf("profile %q, want %q", id.Profile, e.Profile))
	}
	got := map[string]string{}
	for _, in := range m.Inputs {
		got[in.Port] = in.Digest
	}
	for _, port := range sortedKeys(e.Inputs) {
		switch digest, ok := got[port]; {
		case !ok:
			problems = append(problems, fmt.Sprintf("manifest lacks required input %q", port))
		case digest != e.Inputs[port]:
			problems = append(problems, fmt.Sprintf("wrong artefact revision on input %q: %s, want %s", port, digest, e.Inputs[port]))
		}
	}
	for _, port := range sortedKeys(got) {
		if _, ok := e.Inputs[port]; !ok {
			problems = append(problems, fmt.Sprintf("unexpected input %q", port))
		}
	}
	var standards, skills []string
	for _, f := range m.Standards {
		standards = append(standards, f.Path)
	}
	for _, sk := range m.Skills {
		skills = append(skills, sk.Name)
	}
	problems = append(problems, compareList("standard", standards, e.Standards)...)
	return append(problems, compareList("skill", skills, e.Skills)...)
}

func compareList(what string, got, want []string) []string {
	var problems []string
	for _, w := range want {
		if !slices.Contains(got, w) {
			problems = append(problems, fmt.Sprintf("manifest lacks required %s %q", what, w))
		}
	}
	for _, g := range got {
		if !slices.Contains(want, g) {
			problems = append(problems, fmt.Sprintf("unexpected %s %q", what, g))
		}
	}
	if len(problems) == 0 && !slices.Equal(got, want) {
		problems = append(problems, fmt.Sprintf("%ss in order %q, want %q", what, got, want))
	}
	return problems
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// verifyFiles checks that the manifest is complete and that the prompt, input copies and
// skill files beside the prompt file are the ones it names.
func verifyFiles(spec interp.Spec) []string {
	m := spec.Manifest
	var problems []string
	for _, f := range []struct {
		what string
		file render.File
	}{{"prompt", m.Prompt}, {"role", m.Role}, {"guide", m.Guide}} {
		if f.file.Path == "" || f.file.Digest == "" {
			problems = append(problems, "manifest lacks the "+f.what)
		}
	}
	dir := filepath.Dir(spec.PromptFile)
	check := func(what, rel, want string) {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", what, err))
		} else if got := digest(data); got != want {
			problems = append(problems, fmt.Sprintf("%s does not match the manifest: %s, manifest names %s", what, got, want))
		}
	}
	if m.Prompt.Digest != "" {
		data, err := os.ReadFile(spec.PromptFile)
		if err != nil {
			problems = append(problems, fmt.Sprintf("prompt file: %v", err))
		} else if got := digest(data); got != m.Prompt.Digest {
			problems = append(problems, fmt.Sprintf("prompt file does not match the manifest: %s, manifest names %s", got, m.Prompt.Digest))
		}
	}
	for _, in := range m.Inputs {
		check("input "+in.Port, path.Join(render.InputsDir, in.Path), in.Digest)
	}
	for _, sk := range m.Skills {
		if len(sk.Files) == 0 {
			problems = append(problems, fmt.Sprintf("manifest lists no files for skill %q", sk.Name))
		}
		for _, f := range sk.Files {
			check("skill "+sk.Name+" file "+f.Path, path.Join(sk.Path, f.Path), f.Digest)
		}
	}
	return problems
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// respond writes the response's files, then its result.
func respond(spec interp.Spec, r Response) error {
	resultInfo, err := os.Stat(spec.ResultFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, f := range r.files {
		if !fs.ValidPath(f.Path) || f.Path == "." || strings.Contains(f.Path, "\\") || !filepath.IsLocal(filepath.FromSlash(f.Path)) {
			return fmt.Errorf("unsafe response path %q", f.Path)
		}
		target := filepath.Join(spec.WorkDir, filepath.FromSlash(f.Path))
		if strings.EqualFold(target, filepath.Clean(spec.ResultFile)) {
			return fmt.Errorf("response path %q collides with result file", f.Path)
		}
		if resultInfo != nil {
			targetInfo, err := os.Stat(target)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if targetInfo != nil && os.SameFile(resultInfo, targetInfo) {
				return fmt.Errorf("response path %q collides with result file", f.Path)
			}
		}
	}
	outputs := []adapter.Output{}
	for _, f := range r.files {
		if err := writeAtomic(filepath.Join(spec.WorkDir, filepath.FromSlash(f.Path)), f.Data); err != nil {
			return err
		}
		outputs = append(outputs, adapter.Output{Path: f.Path})
	}
	return adapter.WriteResult(spec.ResultFile, adapter.Result{Identity: spec.Identity, Status: r.status, Summary: r.summary, Outputs: outputs})
}

func writeAtomic(target string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

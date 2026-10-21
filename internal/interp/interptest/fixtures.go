package interptest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Testdata returns the absolute path of the shared fixture tree, internal/interp/testdata.
func Testdata() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "testdata")
}

func fixture(name string) string { return filepath.Join(Testdata(), filepath.FromSlash(name)) }

// FixtureDigest returns the "sha256:<hex>" digest of the bytes of a fixture, named relative to the tree.
func FixtureDigest(t testing.TB, name string) string {
	t.Helper()
	data, err := os.ReadFile(fixture(name))
	if err != nil {
		t.Fatal(err)
	}
	return string(digest.Of(data))
}

// ReviewFixture returns the review outcome in reviews/<name>.json.
func ReviewFixture(t testing.TB, name string) workflow.ReviewOutcome {
	t.Helper()
	data, err := os.ReadFile(fixture("reviews/" + name + ".json"))
	if err != nil {
		t.Fatal(err)
	}
	o, err := workflow.ParseReviewOutcome(data)
	if err != nil {
		t.Fatal(err)
	}
	return *o
}

// Step returns a step of the given id and kind, version 1, with settings given as JSON (or empty).
func Step(t testing.TB, id, kind, settings string) workflow.Step {
	t.Helper()
	s := workflow.Step{ID: id, Type: kind, Version: 1}
	if settings != "" {
		if err := json.Unmarshal([]byte(settings), &s.Settings); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// Input wires a fixture document, named relative to docs/, to an input port.
type Input struct {
	Port string
	Doc  string
}

// Attempt is one attempt rendered from the fixtures, for task app/t7.
type Attempt struct {
	Step workflow.Step
	// Number is the attempt number; 0 means 1.
	Number int
	Inputs []Input
	// Standards are names under baseline/, such as "standards/go.md".
	Standards []string
	// Role is a name under baseline/; empty means "roles/artisan.md".
	Role string
	// Adapter is the spec's adapter; empty means interp.MockAdapter.
	Adapter string
}

// Render renders the attempt with render.Prompt into a new temporary attempt directory, with
// skill roots release then managed, and returns its Spec: the prompt and result file in the
// attempt directory, a separate temporary working directory, and identity task "t7",
// attempt "<step>-<number>". Every input is recorded as tasks/t7/<doc>.
func (a Attempt) Render(t testing.TB) interp.Spec {
	t.Helper()
	if a.Number == 0 {
		a.Number = 1
	}
	if a.Role == "" {
		a.Role = "roles/artisan.md"
	}
	if a.Adapter == "" {
		a.Adapter = interp.MockAdapter
	}
	source := func(name string) render.Source { return render.Source{Name: name, Path: fixture("baseline/" + name)} }
	baseline := render.Baseline{Role: source(a.Role), Guide: source("stages/" + a.Step.Type + ".md")}
	for _, s := range a.Standards {
		baseline.Standards = append(baseline.Standards, source(s))
	}
	var inputs []render.Input
	for _, in := range a.Inputs {
		d := FixtureDigest(t, "docs/"+in.Doc)
		inputs = append(inputs, render.Input{Port: in.Port, Name: "tasks/t7/" + in.Doc, SHA256: digest.Digest(d).Hex(), Path: fixture("docs/" + in.Doc)})
	}
	roots := []render.SkillRoot{{Name: "release", Dir: fixture("skills/release")}, {Name: "managed", Dir: fixture("skills/managed")}}
	dir := filepath.Join(t.TempDir(), "attempt")
	r, err := render.Prompt(render.Stage{Repository: "app", Task: "t7", Step: a.Step, Attempt: a.Number, Dir: dir}, inputs, baseline, roots)
	if err != nil {
		t.Fatal(err)
	}
	return interp.Spec{
		Identity:   adapter.Identity{TaskID: "t7", AttemptID: artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: a.Step.ID, Attempt: a.Number}.AttemptID()},
		Adapter:    a.Adapter,
		WorkDir:    t.TempDir(),
		PromptFile: r.PromptPath,
		ResultFile: filepath.Join(dir, artisan.ResultFile),
		Manifest:   r.Manifest,
	}
}

// Build is the build attempt the fixtures describe: profile "builder", skills go-tdd and
// guildhall-contributing, input "spec" at doc, and the Go standard.
func Build(t testing.TB, doc string) Attempt {
	return Attempt{
		Step:      Step(t, "build", "build", `{"profile": "builder", "skills": ["go-tdd", "guildhall-contributing"]}`),
		Inputs:    []Input{{Port: "spec", Doc: doc}},
		Standards: []string{"standards/go.md"},
	}
}

// AutomatedReview is the review attempt the fixtures describe: actor "artisan", profile "reviewer-astra",
// the reviewer role, input "spec" at doc, and the Go and review standards.
func AutomatedReview(t testing.TB, doc string) Attempt {
	return Attempt{
		Step:      Step(t, "review", "review", `{"actor": "artisan", "profile": "reviewer-astra"}`),
		Inputs:    []Input{{Port: "spec", Doc: doc}},
		Standards: []string{"standards/go.md", "standards/review.md"},
		Role:      "roles/reviewer.md",
	}
}

package interp

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/spec"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// shippedWorkflows are the workflows Guildhall ships, internal/workflow's and examples/workflows', by file name.
func shippedWorkflows(t *testing.T) map[string]*workflow.Definition {
	t.Helper()
	defs := map[string]*workflow.Definition{}
	for _, f := range []struct {
		name string
		load func() (*workflow.Definition, error)
	}{{"default", workflow.Default}, {"bug", workflow.Bug}, {"hallmark", workflow.Hallmark}} {
		def, err := f.load()
		if err != nil {
			t.Fatal(err)
		}
		defs[f.name] = def
	}
	examples, err := filepath.Glob(filepath.Join("..", "..", "examples", "workflows", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(examples) == 0 {
		t.Fatal("no example workflows found")
	}
	for _, path := range examples {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		def, err := workflow.Parse(data)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		defs[strings.TrimSuffix(filepath.Base(path), ".json")] = def
	}
	return defs
}

// strictSteps are def's steps and the synthetic designer step, which is not in def.Steps.
func strictSteps(def *workflow.Definition) []workflow.Step {
	_, designer := DesignSteps(def)
	return append(slices.Clone(def.Steps), workflow.Step{ID: designer, Type: designKindType, Version: 1})
}

func headings(examples []spec.Example) []string {
	var hs []string
	for _, ex := range examples {
		hs = append(hs, ex.Heading)
	}
	return hs
}

// TestStrictSections: a plan has Slices, and Summary and Open questions too only when a Guildmaster approval shows its
// spec; the designer has Open questions; every other step, the hallmark's included, has none.
func TestStrictSections(t *testing.T) {
	defs := shippedWorkflows(t)
	approved := []string{spec.SummaryHeading, spec.SlicesHeading, spec.OpenQuestionsHeading}
	for _, c := range []struct {
		workflow, step string
		want           []string
	}{
		{"default", "plan", approved},
		{"refute", "plan", approved},
		{"human-review", "plan", approved},
		{"bug", "plan", []string{spec.SlicesHeading}},
		{"default", "build", nil},
		{"default", "accept", nil},
		{"bug", "research", nil},
		{"hallmark", "hallmark", nil},
		{"refute", "refute", nil},
		{"default", "designer", []string{spec.OpenQuestionsHeading}},
	} {
		t.Run(c.workflow+"/"+c.step, func(t *testing.T) {
			def := defs[c.workflow]
			i := slices.IndexFunc(strictSteps(def), func(s workflow.Step) bool { return s.ID == c.step })
			if i < 0 {
				t.Fatalf("workflow %s has no step %s", c.workflow, c.step)
			}
			if got := headings(StrictSections(def, strictSteps(def)[i])); !slices.Equal(got, c.want) {
				t.Errorf("StrictSections = %q, want %q", got, c.want)
			}
		})
	}
	// The plan's list depends on the workflow, not on a step id: a plan no approval takes has Slices alone.
	bare := &workflow.Definition{Steps: []workflow.Step{{ID: "plan", Type: "plan", Version: 1}}}
	if got := headings(StrictSections(bare, bare.Steps[0])); !slices.Equal(got, []string{spec.SlicesHeading}) {
		t.Errorf("StrictSections of an unapproved plan = %q, want Slices alone", got)
	}
}

// TestStrictCheckersCoverSections: every heading StrictSections returns, for any step of a shipped workflow, has an
// intake checker, so checkStrict never meets a section it cannot check.
func TestStrictCheckersCoverSections(t *testing.T) {
	for name, def := range shippedWorkflows(t) {
		for _, s := range strictSteps(def) {
			for _, ex := range StrictSections(def, s) {
				if !slices.ContainsFunc(strictCheckers, func(c strictCheck) bool { return c.heading == ex.Heading }) {
					t.Errorf("%s step %s: strict section %s has no intake checker", name, s.ID, ex.Heading)
				}
			}
		}
	}
}

// promptExample is one strict section a rendered prompt's refused block names.
type promptExample struct{ heading, valid, invalid, err string }

// refusedExamples are the sections the refused Strict sections block of prompt names, in order, none when it has no
// such block.
func refusedExamples(t *testing.T, prompt string) []promptExample {
	t.Helper()
	i := strings.Index(prompt, "\nStrict sections: Guildhall parses these sections of `")
	if i < 0 || !strings.Contains(prompt[i:strings.Index(prompt[i+1:], "\n")+i+1], "intake refuse a result") {
		return nil
	}
	block := prompt[i:]
	// The examples hold `## …` lines themselves, so the block ends at the next output section or the Result template.
	for _, next := range []string{"\n### ", "\n## Result template"} {
		if end := strings.Index(block, next); end >= 0 {
			block = block[:end]
		}
	}
	between := func(s, open, close string) (string, string) {
		a := strings.Index(s, open)
		if a < 0 {
			t.Fatalf("no %q in the block:\n%s", open, s)
		}
		s = s[a+len(open):]
		b := strings.Index(s, close)
		if b < 0 {
			t.Fatalf("no %q after %q", close, open)
		}
		return s[:b], s[b+len(close):]
	}
	var out []promptExample
	for strings.Contains(block, "\n**`") {
		var ex promptExample
		ex.heading, block = between(block, "\n**`", "`.**")
		ex.valid, block = between(block, "Valid:\n\n```text\n", "\n```")
		ex.invalid, block = between(block, "Invalid:\n\n```text\n", "\n```")
		ex.err, block = between(block, "It gets: `", "`\n")
		out = append(out, ex)
	}
	return out
}

// TestShippedPromptsMatchIntake: for every step of every shipped workflow, and the synthetic designer step, the
// prompt rendered as prepare renders it names exactly the sections intake refuses. Each Invalid example it names,
// leading a document whose other named sections are valid, is refused by intake's strict check with a reason that
// starts with the example's error; and a section the prompt does not name is not refused when it alone is broken.
func TestShippedPromptsMatchIntake(t *testing.T) {
	root := t.TempDir()
	if err := baseline.Install(filepath.Join(root, "baseline")); err != nil {
		t.Fatal(err)
	}
	source := func(name string) render.Source {
		return render.Source{Name: name, Path: filepath.Join(root, "baseline", filepath.FromSlash(name))}
	}
	all := []spec.Example{spec.SummaryExample, spec.SlicesExample, spec.OpenQuestionsExample}
	for name, def := range shippedWorkflows(t) {
		for _, s := range strictSteps(def) {
			kind, ok := workflow.LookupKind(s.Type, s.Version)
			if !ok || !slices.Contains(kind.Actors, workflow.ArtisanActor) || s.Actor() != workflow.ArtisanActor {
				continue
			}
			t.Run(name+"/"+s.ID, func(t *testing.T) {
				// The prompt is rendered with no skill roots, so the step's skills go; nothing strict depends on them.
				strict := StrictSections(def, s)
				s.Settings = maps.Clone(s.Settings)
				delete(s.Settings, "skills")
				got, err := render.Prompt(render.Stage{Repository: "repo", Task: "task", Step: s, Attempt: 1, Dir: filepath.Join(t.TempDir(), "attempt"), Strict: strict}, nil,
					render.Baseline{Role: source("roles/artisan.md"), Guide: source("stages/" + s.Type + ".md")}, nil)
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(got.PromptPath)
				if err != nil {
					t.Fatal(err)
				}
				named := refusedExamples(t, string(data))
				var names []string
				for _, n := range named {
					names = append(names, n.heading)
				}
				if want := headings(strict); !slices.Equal(names, want) {
					t.Fatalf("the prompt names %q as refused, want %q", names, want)
				}
				r := &run{base: def}
				// valid is a section that passes intake under def: under no slice range, one slice (research note 8).
				valid := func(ex promptExample) string {
					if ex.heading == spec.SlicesHeading && def.Slices == nil {
						return strings.SplitN(ex.valid, "\n2. ", 2)[0]
					}
					return ex.valid
				}
				for _, ex := range named {
					doc := []string{ex.invalid}
					for _, other := range named {
						if other.heading != ex.heading {
							doc = append(doc, valid(other))
						}
					}
					err := r.checkStrict(s, []byte(strings.Join(doc, "\n\n")+"\n"))
					var rejection *resultRejection
					if !errors.As(err, &rejection) || !strings.HasPrefix(err.Error(), ex.err) {
						t.Errorf("the prompt's Invalid %s got %v from intake, want a rejection starting %q", ex.heading, err, ex.err)
					}
				}
				// The other direction: a section broken alone, the named ones valid, is refused only if the prompt
				// names it.
				for _, ex := range all {
					if slices.ContainsFunc(named, func(n promptExample) bool { return n.heading == ex.Heading }) {
						continue
					}
					doc := []string{ex.Invalid}
					for _, n := range named {
						doc = append(doc, valid(n))
					}
					if err := r.checkStrict(s, []byte(strings.Join(doc, "\n\n")+"\n")); err != nil {
						t.Errorf("intake refuses %s, which the prompt does not name: %v", ex.Heading, err)
					}
				}
			})
		}
	}
}

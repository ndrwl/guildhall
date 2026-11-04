package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	release "github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/spec"
)

// TestPromptStrictSections: every rendered prompt's Result template tells the Artisan to submit with
// `guildhall result submit`. The refused Strict sections block prints exactly the sections Stage.Strict gives
// (interp.StrictSections owns the list), each with its rule, valid and invalid examples and error verbatim from
// internal/spec, whose parser tests run them, and nothing when Strict is empty; a hallmark report shows its
// parsed-only Questions block, which intake does not refuse.
func TestPromptStrictSections(t *testing.T) {
	root := filepath.Join(t.TempDir(), "baseline")
	if err := release.Install(root); err != nil {
		t.Fatal(err)
	}
	shipped := func(name string) Source {
		return Source{Name: name, Path: filepath.Join(root, filepath.FromSlash(name))}
	}
	all := []spec.Example{spec.SummaryExample, spec.SlicesExample, spec.OpenQuestionsExample, spec.ReportQuestionsExample}
	const refusedLine, parsedLine = "` itself, and `guildhall result submit` and intake refuse", "` itself for the ask it shows the Guildmaster"
	for _, c := range []struct {
		name, kind, file string
		strict           []spec.Example
		// shown are the examples the prompt quotes; line is the block's lead-in, empty for no block.
		shown []spec.Example
		line  string
	}{
		{"plan with approval", "plan", "spec.md", []spec.Example{spec.SummaryExample, spec.SlicesExample, spec.OpenQuestionsExample}, []spec.Example{spec.SummaryExample, spec.SlicesExample, spec.OpenQuestionsExample}, refusedLine},
		{"plan without approval", "plan", "spec.md", []spec.Example{spec.SlicesExample}, []spec.Example{spec.SlicesExample}, refusedLine},
		{"plan given nothing", "plan", "spec.md", nil, nil, ""},
		{"design", "design", "design.md", []spec.Example{spec.OpenQuestionsExample}, []spec.Example{spec.OpenQuestionsExample}, refusedLine},
		{"design given nothing", "design", "design.md", nil, nil, ""},
		{"hallmark", "hallmark", "report.md", nil, []spec.Example{spec.ReportQuestionsExample}, parsedLine},
		{"build", "build", "", nil, nil, ""},
		{"research", "research", "", nil, nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			stage := attempt(t, step(t, c.kind, c.kind, ""))
			stage.Strict = c.strict
			r, err := Prompt(stage, nil, Baseline{Role: shipped("roles/artisan.md"), Guide: shipped("stages/" + c.kind + ".md")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(r.PromptPath)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			if !strings.Contains(text, "## Result template\n\n"+SubmitStep) || !strings.Contains(SubmitStep, "`guildhall result submit <draft file>`") {
				t.Error("the Result template does not tell the Artisan to submit with guildhall result submit")
			}
			if c.line == "" {
				if strings.Contains(text, "Strict sections:") {
					t.Error("a stage given no strict sections shows a Strict sections block")
				}
				return
			}
			if !strings.Contains(text, "\nStrict sections: Guildhall parses these sections of `"+c.file+c.line) {
				t.Errorf("the prompt has no Strict sections block for %s led by %q", c.file, c.line)
			}
			for _, ex := range all {
				want := false
				for _, s := range c.shown {
					want = want || s.Heading == ex.Heading && s.Rule == ex.Rule
				}
				if !want {
					if strings.Contains(text, "**`"+ex.Heading+"`.** "+ex.Rule) {
						t.Errorf("the prompt shows %s, which it was not given", ex.Heading)
					}
					continue
				}
				for _, s := range []string{
					"**`" + ex.Heading + "`.** " + ex.Rule,
					"Valid:\n\n```text\n" + ex.Valid + "\n```",
					"Invalid:\n\n```text\n" + ex.Invalid + "\n```",
					"It gets: `" + ex.Error + "`",
				} {
					if !strings.Contains(text, s) {
						t.Errorf("the prompt does not quote %s's example verbatim: %q", ex.Heading, s)
					}
				}
			}
		})
	}
}

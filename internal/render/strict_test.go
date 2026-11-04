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
// `guildhall result submit`, and each kind whose document Guildhall parses shows its strict sections' rules, valid
// and invalid examples and errors verbatim from internal/spec, whose parser tests run them; other kinds show none.
func TestPromptStrictSections(t *testing.T) {
	root := filepath.Join(t.TempDir(), "baseline")
	if err := release.Install(root); err != nil {
		t.Fatal(err)
	}
	shipped := func(name string) Source {
		return Source{Name: name, Path: filepath.Join(root, filepath.FromSlash(name))}
	}
	for _, c := range []struct {
		kind, file string
		examples   []spec.Example
	}{
		{"plan", "spec.md", []spec.Example{spec.SummaryExample, spec.SlicesExample, spec.OpenQuestionsExample}},
		{"design", "design.md", []spec.Example{spec.OpenQuestionsExample}},
		{"hallmark", "report.md", []spec.Example{spec.ReportQuestionsExample}},
		{"build", "", nil},
		{"research", "", nil},
	} {
		t.Run(c.kind, func(t *testing.T) {
			r, err := Prompt(attempt(t, step(t, c.kind, c.kind, "")), nil,
				Baseline{Role: shipped("roles/artisan.md"), Guide: shipped("stages/" + c.kind + ".md")}, nil)
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
			if c.examples == nil {
				if strings.Contains(text, "Strict sections:") {
					t.Error("a kind with no strict sections shows a Strict sections block")
				}
				return
			}
			if !strings.Contains(text, "\nStrict sections: Guildhall parses these sections of `"+c.file+"` itself") {
				t.Errorf("the prompt has no Strict sections block for %s", c.file)
			}
			for _, ex := range c.examples {
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

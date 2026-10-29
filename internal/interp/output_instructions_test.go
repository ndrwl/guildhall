package interp

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// The emitted prompt is an agent-facing protocol, not evidence about whether a
// model understands prose. Execute rendering with every shipped Artisan guide,
// then feed its advertised file examples and result template to the real readers.
func TestBuiltinOutputInstructions(t *testing.T) {
	root := t.TempDir()
	if err := baseline.Install(filepath.Join(root, "baseline")); err != nil {
		t.Fatal(err)
	}
	guides, err := fs.Glob(baseline.Release(), "stages/*.md")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, standards := range []string{"standards-live", ""} {
		for _, kind := range workflow.Kinds() {
			if !slices.Contains(kind.Actors, workflow.ArtisanActor) {
				continue
			}
			t.Run(kind.Type, func(t *testing.T) {
				guideName := "stages/" + kind.Type + ".md"
				seen[guideName] = true
				step := workflow.Step{ID: "step", Type: kind.Type, Version: kind.Version}
				if len(kind.Actors) > 1 {
					step.Settings = map[string]json.RawMessage{"actor": json.RawMessage(`"artisan"`)}
				}
				source := func(name string) render.Source {
					return render.Source{Name: name, Path: filepath.Join(root, "baseline", filepath.FromSlash(name))}
				}
				var revisions *workflow.Revisions
				if kind.Type == "review" || kind.Type == "document-review" {
					revisions = &workflow.Revisions{StandardsRevision: standards}
					if kind.Type == "review" {
						revisions.Subject.CodeHead = strings.Repeat("a", 40)
						revisions.SpecDigest = "sha256:" + strings.Repeat("b", 64)
					} else {
						revisions.Subject.DocumentDigest = "sha256:" + strings.Repeat("c", 64)
					}
				}
				got, err := render.Prompt(render.Stage{Repository: "repo", Task: "task", Step: step, Attempt: 1, Dir: filepath.Join(t.TempDir(), "attempt"), Revisions: revisions}, nil,
					render.Baseline{Role: source("roles/artisan.md"), Guide: source(guideName)}, nil)
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(got.PromptPath)
				if err != nil {
					t.Fatal(err)
				}
				prompt := string(data)
				resultSection := sectionAfter(t, prompt, "## Result template\n")
				example := fencedExample(t, resultSection, "json")
				result, err := adapter.ParseResult([]byte(example), adapter.Identity{TaskID: "task", AttemptID: "step-1"})
				if err != nil {
					t.Fatalf("result example: %v", err)
				}
				outputIndex := 0
				for _, port := range kind.Outputs {
					section := sectionAfter(t, prompt, "### Output port `"+port.Name+"`\n")
					language := "text"
					format := "Format: a UTF-8 Markdown document."
					if port.Type == workflow.ReviewResult {
						language = "json"
						format = "Format: one strict JSON `guildhall.review-outcome/1` object"
					}
					if port.Type != workflow.CodeHead && !strings.Contains(section, format) {
						t.Errorf("port %s does not advertise %s", port.Name, format)
					}
					content := fencedExample(t, section, language)
					if err := checkPort(port.Type, []byte(content)); err != nil {
						t.Fatalf("%s example rejected by engine: %v", port.Name, err)
					}
					if port.Type == workflow.ReviewResult {
						// Every example must pass intake, the finding example included.
						examples := fencedExamples(section, language)
						findings := 0
						for _, content := range examples {
							if err := checkPort(port.Type, []byte(content)); err != nil {
								t.Fatalf("%s example rejected by engine: %v\n%s", port.Name, err, content)
							}
							outcome, err := workflow.ParseReviewOutcome([]byte(content))
							if err != nil {
								t.Fatal(err)
							}
							findings += len(outcome.Findings)
							if err := outcome.BoundTo(*revisions); err != nil {
								t.Fatalf("example changed the bound revisions: %v", err)
							}
							moved := *revisions
							moved.StandardsRevision += "-changed"
							if err := outcome.BoundTo(moved); err == nil {
								t.Fatal("example accepted against different standards")
							}
						}
						if len(examples) < 2 || findings == 0 {
							t.Errorf("port %s has %d examples with %d findings; want an approval and a finding example", port.Name, len(examples), findings)
						}
					}
					if port.Type == workflow.CodeHead {
						if !strings.Contains(section, "automatically") || !strings.Contains(section, "no file") {
							t.Error("code head must explain automatic capture without a file")
						}
						continue
					}
					extension := ".md"
					if port.Type == workflow.ReviewResult {
						extension = ".json"
					}
					path := port.Name + extension
					if !strings.Contains(section, "`"+path+"`") {
						t.Errorf("port %s has no write path", port.Name)
					}
					if outputIndex >= len(result.Outputs) || result.Outputs[outputIndex].Path != path {
						t.Fatalf("result outputs %v do not name %s", result.Outputs, path)
					}
					outputIndex++
				}
				if kind.Type == "rebase" {
					// A rebaser writes rebase.json besides its head, and its example reads.
					section := sectionAfter(t, prompt, "### Also `"+render.RebaseReportFile+"`\n")
					var report struct {
						BehaviourChange *bool  `json:"behaviour_change"`
						Reason          string `json:"reason"`
					}
					if err := json.Unmarshal([]byte(fencedExample(t, section, "json")), &report); err != nil || report.BehaviourChange == nil || report.Reason == "" {
						t.Fatalf("rebase.json example = %+v, %v", report, err)
					}
					if outputIndex >= len(result.Outputs) || result.Outputs[outputIndex].Path != render.RebaseReportFile {
						t.Fatalf("result outputs %v do not name %s", result.Outputs, render.RebaseReportFile)
					}
					outputIndex++
				}
				if len(result.Outputs) != outputIndex {
					t.Errorf("unexpected result outputs: %v", result.Outputs)
				}
			})
		}
	}
	for _, guide := range guides {
		if !seen[guide] {
			t.Errorf("stage guide %s has no exercised Artisan kind", guide)
		}
	}
}

func sectionAfter(t *testing.T, text, heading string) string {
	t.Helper()
	_, rest, ok := strings.Cut(text, heading)
	if !ok {
		t.Fatalf("rendered instructions lack %q", heading)
	}
	for _, next := range []string{"\n## ", "\n### "} {
		if before, _, found := strings.Cut(rest, next); found {
			rest = before
		}
	}
	return rest
}

func fencedExample(t *testing.T, text, language string) string {
	t.Helper()
	_, rest, ok := strings.Cut(text, "```"+language+"\n")
	if !ok {
		t.Fatalf("no %s example in %s", language, text)
	}
	example, _, ok := strings.Cut(rest, "\n```")
	if !ok {
		t.Fatal("unclosed example")
	}
	return example
}

// fencedExamples returns every complete example fenced as language in text, in order.
func fencedExamples(text, language string) []string {
	var out []string
	for {
		_, rest, ok := strings.Cut(text, "```"+language+"\n")
		if !ok {
			return out
		}
		example, after, ok := strings.Cut(rest, "\n```")
		if !ok {
			return out
		}
		out = append(out, example)
		text = after
	}
}

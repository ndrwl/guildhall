package render

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	release "github.com/ndrwl/guildhall/baseline"
)

// TestBriefSurface: a brief's first line names its surface; a brief with no Surface: line reads as none, unfound,
// and a Surface: line naming no known surface is an error.
func TestBriefSurface(t *testing.T) {
	for _, c := range []struct {
		brief string
		want  Surface
		found bool
		err   string
	}{
		{"Surface: web-page\n\nShow it.", SurfaceWebPage, true, ""},
		{"Surface: cli\r\n\r\nShow it.", SurfaceCLI, true, ""},
		{"Surface:  chat  \n\nShow it.", SurfaceChat, true, ""},
		{"Surface: none", SurfaceNone, true, ""},
		{"Show the sort options.", SurfaceNone, false, ""},
		{"", SurfaceNone, false, ""},
		{"surface: cli\n\nShow it.", SurfaceNone, false, ""},
		{"Surface: CLI\n\nShow it.", "", true, `unknown surface "CLI"`},
		{"Surface: web page\n\nShow it.", "", true, `unknown surface "web page"`},
	} {
		got, found, err := BriefSurface([]byte(c.brief))
		if got != c.want || found != c.found || (err == nil) != (c.err == "") || err != nil && !strings.Contains(err.Error(), c.err) {
			t.Errorf("BriefSurface(%q) = %q, %v, %v; want %q, %v, %q", c.brief, got, found, err, c.want, c.found, c.err)
		}
	}
	for _, s := range Surfaces {
		if got, found, err := BriefSurface([]byte(ComposeBrief(s, "Show it.\n"))); got != s || !found || err != nil {
			t.Errorf("BriefSurface(ComposeBrief(%s)) = %q, %v, %v", s, got, found, err)
		}
	}
	if got := ComposeBrief(SurfaceCLI, "Show it.\n"); got != "Surface: cli\n\nShow it.\n" {
		t.Errorf("ComposeBrief = %q", got)
	}
	if _, err := ParseSurface("poster"); err == nil || !strings.Contains(err.Error(), "web-page, cli, chat, none") {
		t.Errorf("ParseSurface(poster) = %v, want the surfaces named", err)
	}
}

// TestDesignerPromptSurface: a designer's prompt names its brief's surface beside the brief and has the "Also"
// section and result outputs of the mock-up that surface asks for, and none for none or a brief recorded before
// surfaces were named. Other kinds' prompts name no surface.
func TestDesignerPromptSurface(t *testing.T) {
	root := filepath.Join(t.TempDir(), "baseline")
	if err := release.Install(root); err != nil {
		t.Fatal(err)
	}
	shipped := func(name string) Source {
		return Source{Name: name, Path: filepath.Join(root, filepath.FromSlash(name))}
	}
	designer := step(t, "designer", "design", `{"profile":"designer"}`)
	for _, c := range []struct {
		name, brief string
		also        []string
		outputs     string
	}{
		{"web-page", "Surface: web-page\n\nShow the sort options.\n", []string{"### Also `mockup.html`", "The brief's surface is `web-page`", "internal/web/testdata/task.golden.html",
			"desktop 1280px wide and phone 390px wide", "no `<script`"}, `"path": "design.md"`},
		{"cli", "Surface: cli\n\nShow the sort options.\n", []string{"### Also `mockup.md`", "The brief's surface is `cli`", "exact sample command-line output", "fenced code block"}, `"path": "design.md"`},
		{"chat", "Surface: chat\n\nShow the sort options.\n", []string{"### Also `mockup.md`", "exact sample chat reply"}, `"path": "design.md"`},
		{"none", "Surface: none\n\nShow the sort options.\n", nil, `"path": "design.md"`},
		{"before surfaces", "Show the sort options.\n", nil, `"path": "design.md"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "brief.md")
			if err := os.WriteFile(file, []byte(c.brief), 0o644); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(c.brief))
			in := Input{Port: BriefPort, Name: "tasks/cli/t7/design-1/brief.md", SHA256: hex.EncodeToString(sum[:]), Path: file}
			r, err := Prompt(attempt(t, designer), []Input{in}, Baseline{Role: shipped("roles/artisan.md"), Guide: shipped("stages/design.md")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(r.PromptPath)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			surface, _, _ := BriefSurface([]byte(c.brief))
			if line := "- brief: inputs/tasks/cli/t7/design-1/brief.md (sha256:" + in.SHA256 + "), surface `" + string(surface) + "`\n"; !strings.Contains(text, line) {
				t.Errorf("prompt does not name the surface beside the brief, %q", line)
			}
			for _, s := range c.also {
				if !strings.Contains(text, s) {
					t.Errorf("prompt lacks %q", s)
				}
			}
			mockup := MockupFile(surface)
			if required := text[strings.Index(text, "## Required outputs"):]; mockup == "" {
				if strings.Contains(required, "### Also") || strings.Contains(required, "mockup.") {
					t.Errorf("a %s designer prompt asks for a mock-up", surface)
				}
				return
			}
			template := text[strings.Index(text, "## Result template"):]
			if i, j := strings.Index(template, `"path": "design.md"`), strings.Index(template, `"path": "`+mockup+`"`); i < 0 || j < i {
				t.Errorf("result template lists outputs %q, want design.md then %s", template, mockup)
			}
		})
	}
	t.Run("other kinds", func(t *testing.T) {
		r, err := Prompt(attempt(t, step(t, "research", "research", "")), nil, baseline("research"), roots())
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(r.PromptPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "surface") || strings.Contains(string(data), "### Also") {
			t.Error("a research prompt names a surface")
		}
	})
	t.Run("unknown surface", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "brief.md")
		brief := []byte("Surface: poster\n\nShow it.\n")
		if err := os.WriteFile(file, brief, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(brief)
		in := Input{Port: BriefPort, Name: "tasks/cli/t7/design-1/brief.md", SHA256: hex.EncodeToString(sum[:]), Path: file}
		stage := attempt(t, designer)
		if _, err := Prompt(stage, []Input{in}, Baseline{Role: shipped("roles/artisan.md"), Guide: shipped("stages/design.md")}, nil); err == nil || !strings.Contains(err.Error(), `unknown surface "poster"`) {
			t.Errorf("Prompt = %v, want the unknown surface refused", err)
		}
		if _, err := os.Stat(stage.Dir); err == nil {
			t.Error("a refused prompt left its directory")
		}
	})
}

// TestDesignQuestionListsTheMockup: a design question lists the mock-up's digest next to the brief and the design.
func TestDesignQuestionListsTheMockup(t *testing.T) {
	ask := DesignQuestion("app", "t7", "design-1", 1, []Input{
		{Port: "brief", Name: "tasks/app/t7/design-1/brief.md", SHA256: strings.Repeat("cd", 32)},
		{Port: "design", Name: "tasks/app/t7/designer-1/design.md", SHA256: strings.Repeat("ef", 32)},
		{Port: "mockup", Name: "tasks/app/t7/designer-1/mockup.html", SHA256: strings.Repeat("ab", 32)},
	})
	if !strings.Contains(ask.Question, "- design: tasks/app/t7/designer-1/design.md (sha256:"+strings.Repeat("ef", 32)+")\n- mockup: tasks/app/t7/designer-1/mockup.html (sha256:"+strings.Repeat("ab", 32)+")\n") {
		t.Errorf("question = %q, want the mock-up listed after the design", ask.Question)
	}
}

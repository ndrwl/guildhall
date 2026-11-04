package interp_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
)

// webMockup is a two-frame web mock-up that intake accepts: a page with an inline style and a data: image, and a
// local link, in desktop and phone srcdoc frames.
var webMockup = []byte(`<!doctype html><html><head><meta charset="utf-8"><style>body{display:flex;flex-wrap:wrap}</style></head><body>` +
	`<iframe width="1280" srcdoc="&lt;p style=&quot;color:red&quot;&gt;Desktop &lt;a href=&quot;/tasks/app/t7&quot;&gt;t7&lt;/a&gt; &lt;img src=&quot;data:image/png;base64,AA==&quot;&gt;"></iframe>` +
	`<iframe width="390" srcdoc="&lt;p&gt;Phone — sort options&lt;/p&gt;"></iframe></body></html>`)

// cliMockup is a Markdown mock-up of command-line output.
var cliMockup = []byte("# Mock-up\n\n```text\n$ guildhall task list --sort due\nt7  open  due 2026-11-04\n```\n")

// surfaceBrief is a brief naming surface, with designBrief's text.
func surfaceBrief(surface string) string {
	return "Surface: " + surface + "\n\nShow the sort options, so the Guildmaster can choose one."
}

// mockupDesigner is designer attempt 1 of a round with brief, answering with files.
func mockupDesigner(brief string, files ...interptest.File) interptest.Expectation {
	e := designerExpectation("designer", 1, brief, nil)
	e.Respond = interptest.Succeed("designed", files...)
	return e
}

// designedEnv grills app/t7 and requests a design round with brief, whose designer answers with files, and returns
// the env with designer attempt 1 running.
func designedEnv(t *testing.T, b backend, brief string, files ...interptest.File) *env {
	t.Helper()
	e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"), mockupDesigner(brief, files...))
	e.grillHeld(t)
	e.release()
	want(t, e.advance(t, designRequested(brief)), interp.Next{Wait: interp.WaitAttempt, Step: "designer", Attempt: 1})
	return e
}

// TestDesignRequestSurface: a request whose brief does not start with a valid Surface: line is refused, recording
// nothing; each valid surface records the brief as sent.
func TestDesignRequestSurface(t *testing.T) {
	for _, c := range []struct{ brief, reason string }{
		{"Show the sort options.", "starts with a Surface: line"},
		{"Surface: poster\n\nShow the sort options.", `unknown surface "poster"`},
		{"Surface:\n\nShow the sort options.", `unknown surface ""`},
		{"\nSurface: cli\n\nShow the sort options.", "starts with a Surface: line"},
	} {
		e := newEnvDefinition(t, backends[1], loadWorkflow(t, "director-grill"))
		e.grillHeld(t)
		before := e.counts(t)
		if err := advanceErr(t, e, designRequested(c.brief)); !errors.Is(err, interp.ErrEvent) || !strings.Contains(err.Error(), c.reason) {
			t.Errorf("request with brief %q = %v, want ErrEvent naming %q", c.brief, err, c.reason)
		}
		if after := e.counts(t); after != before {
			t.Errorf("brief %q: a refused request recorded %v, had %v", c.brief, after, before)
		}
	}
	for _, s := range []string{"web-page", "cli", "chat", "none"} {
		e := newEnvDefinition(t, backends[1], loadWorkflow(t, "director-grill"))
		e.grillHeld(t)
		brief := surfaceBrief(s)
		want(t, e.advance(t, designRequested(brief)), interp.Next{Wait: interp.WaitPaused, Step: "designer"})
		if d, ok := e.documents(t)["design-1-brief"]; !ok || d.SHA256 != sum([]byte(brief)) {
			t.Errorf("surface %s: design-1-brief = %+v, %v; want the composed brief", s, d, ok)
		}
	}
}

// TestDesignMockupAccepted: a designer that writes the mock-up its brief's surface asks for reaches the ask; the
// mock-up is recorded under its named part, designer-1-mockup, and never as designer-1-design-2, the decision stays bound to design.md and its question lists the
// mock-up's digest. A none round records design.md alone.
func TestDesignMockupAccepted(t *testing.T) {
	for _, c := range []struct {
		surface string
		mockup  *interptest.File
	}{
		{"web-page", &interptest.File{Path: "mockup.html", Data: webMockup}},
		{"cli", &interptest.File{Path: "mockup.md", Data: cliMockup}},
		{"chat", &interptest.File{Path: "mockup.md", Data: []byte("Reply:\n\n~~~text\nTask t7 is waiting for your answer.\n~~~\n")}},
		{"none", nil},
	} {
		for _, b := range backends {
			t.Run(c.surface+"/"+b.name, func(t *testing.T) {
				files := []interptest.File{{Path: "design.md", Data: designOne}}
				if c.mockup != nil {
					files = append(files, *c.mockup)
				}
				e := designedEnv(t, b, surfaceBrief(c.surface), files...)
				want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "design", Decision: "design-1"})
				d := e.decision(t, "design-1")
				if d.Subject == nil || d.Subject.Digest != "sha256:"+sum(designOne) {
					t.Errorf("design-1 bound to %+v, want design.md", d.Subject)
				}
				docs := e.documents(t)
				if legacy, ok := docs["designer-1-design-2"]; ok {
					t.Errorf("the mock-up was recorded under its legacy name: %+v", legacy)
				}
				m, ok := docs["designer-1-mockup"]
				if c.mockup == nil {
					if ok || strings.Contains(d.Question, "- mockup:") {
						t.Errorf("a none round recorded a mock-up %+v or asked about one: %q", m, d.Question)
					}
					return
				}
				if !ok || m.SHA256 != sum(c.mockup.Data) || !strings.HasSuffix(m.Path, "/designer-1/"+c.mockup.Path) {
					t.Fatalf("designer-1-mockup = %+v, %v; want the mock-up", m, ok)
				}
				if line := "- mockup: " + m.Path + " (sha256:" + m.SHA256 + ")\n"; !strings.Contains(d.Question, line) {
					t.Errorf("design-1 question = %q, want the line %q", d.Question, line)
				}
			})
		}
	}
}

// TestDesignMockupLegacyReplay: a designer attempt whose mock-up a store already records under its legacy name,
// designer-1-design-2, with its design decision not yet opened, asks with that mock-up on advance, and no
// designer-1-mockup copy is recorded.
func TestDesignMockupLegacyReplay(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := designedEnv(t, b, surfaceBrief("web-page"), interptest.File{Path: "design.md", Data: designOne}, interptest.File{Path: "mockup.html", Data: webMockup})
			const p = "tasks/app/t7/designer-1/mockup.html"
			file := filepath.Join(e.eng.DocumentDir, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, webMockup, 0o644); err != nil {
				t.Fatal(err)
			}
			legacy := store.DocumentRef{Repository: "app", TaskID: "t7", Name: "designer-1-design-2", Path: p, SHA256: sum(webMockup)}
			if _, err := e.h.Records.PutDocument("earlier:designer-1-design-2", legacy); err != nil {
				t.Fatal(err)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "design", Decision: "design-1"})
			docs := e.documents(t)
			if m, ok := docs["designer-1-mockup"]; ok {
				t.Errorf("a legacy mock-up gained a named copy %+v", m)
			}
			if _, ok := docs["designer-1-design"]; !ok {
				t.Errorf("designer-1-design not recorded")
			}
			q := e.decision(t, "design-1").Question
			if line := "- mockup: " + p + " (sha256:" + sum(webMockup) + ")\n"; !strings.Contains(q, line) {
				t.Errorf("design-1 question = %q, want the legacy mock-up line %q", q, line)
			}
		})
	}
}

// TestDesignMockupFromTheGoldenPage:a web mock-up built from the real golden task pages as the stage guide says, each
// page escaped into a desktop and a phone srcdoc frame with its external links pointed at #, passes intake, so the
// page's own markup and CSS raise no false refusal; left unedited, its pull request links are refused.
func TestDesignMockupFromTheGoldenPage(t *testing.T) {
	for _, golden := range []string{"task.golden.html", "task.answering.golden.html"} {
		page, err := os.ReadFile(filepath.Join("..", "web", "testdata", golden))
		if err != nil {
			t.Fatal(err)
		}
		frames := func(page string) []byte {
			esc := strings.NewReplacer("&", "&amp;", `"`, "&quot;").Replace(page)
			return []byte(`<!doctype html><html><head><meta charset="utf-8"><style>body{display:flex;flex-wrap:wrap;gap:16px}</style></head><body>` +
				`<iframe title="Desktop" style="width:1280px;height:900px" srcdoc="` + esc + `"></iframe>` +
				`<iframe title="Phone" style="width:390px;height:900px" srcdoc="` + esc + `"></iframe></body></html>`)
		}
		// Strip exactly Guildhall's own script elements, so any other script that reached the golden still fails intake.
		own, err := filepath.Glob(filepath.Join("..", "web", "scripts", "*.js"))
		if err != nil || len(own) == 0 {
			t.Fatalf("own scripts: %v %v", own, err)
		}
		for _, f := range own {
			js, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			page = []byte(strings.ReplaceAll(string(page), "<script>"+string(js)+"</script>", ""))
		}
		edited := regexp.MustCompile(`href="https?://[^"]*"`).ReplaceAllString(string(page), `href="#"`)
		t.Run(golden, func(t *testing.T) {
			e := designedEnv(t, backends[1], surfaceBrief("web-page"), interptest.File{Path: "design.md", Data: designOne}, interptest.File{Path: "mockup.html", Data: frames(edited)})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "design", Decision: "design-1"})
		})
		if edited != string(page) {
			t.Run(golden+"/unedited links", func(t *testing.T) {
				e := designedEnv(t, backends[1], surfaceBrief("web-page"), interptest.File{Path: "design.md", Data: designOne}, interptest.File{Path: "mockup.html", Data: frames(string(page))})
				assertRejected(t, e, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1}, "loads an external URL")
			})
		}
	}
}

// TestDesignMockupRejected: a designer result whose files do not match its brief's surface fails at intake as the
// result's fault, recording no design, and waits for the Director.
func TestDesignMockupRejected(t *testing.T) {
	design := interptest.File{Path: "design.md", Data: designOne}
	html := func(s string) interptest.File { return interptest.File{Path: "mockup.html", Data: []byte(s)} }
	for _, c := range []struct {
		name, surface string
		files         []interptest.File
		reason        string
	}{
		{"web missing", "web-page", []interptest.File{design}, "lists two outputs, design.md and then mockup.html; got design.md"},
		{"web wrong extension", "web-page", []interptest.File{design, {Path: "mockup.htm", Data: webMockup}}, "and then mockup.html; got design.md, mockup.htm"},
		{"web markdown", "web-page", []interptest.File{design, {Path: "mockup.md", Data: cliMockup}}, "and then mockup.html"},
		{"web extra", "web-page", []interptest.File{design, {Path: "mockup.html", Data: webMockup}, {Path: "notes.md", Data: []byte("x\n")}}, "lists two outputs"},
		{"script", "web-page", []interptest.File{design, html(`<p>x</p><SCRIPT>alert(1)</SCRIPT>`)}, "holds a script element"},
		{"escaped script", "web-page", []interptest.File{design, html(`<iframe srcdoc="&lt;script&gt;x&lt;/script&gt;"></iframe>`)}, "holds a script element"},
		{"event attribute", "web-page", []interptest.File{design, html(`<body onLoad="x()"><p>x</p></body>`)}, "holds an event attribute (onload=)"},
		{"external src", "web-page", []interptest.File{design, html(`<img src="https://example.com/a.png">`)}, "loads an external URL"},
		{"external href", "web-page", []interptest.File{design, html(`<iframe srcdoc="&lt;a href=&quot;http://example.com&quot;&gt;x&lt;/a&gt;"></iframe>`)}, "loads an external URL"},
		{"protocol-relative url", "web-page", []interptest.File{design, html(`<style>p{background:url( //cdn.example/a.png)}</style>`)}, "loads an external URL"},
		{"import", "web-page", []interptest.File{design, html(`<style>@import "https://fonts.example/a.css";</style>`)}, "loads an external URL"},
		{"invalid utf-8", "web-page", []interptest.File{design, {Path: "mockup.html", Data: []byte("<p>\xff</p>")}}, "is not valid UTF-8"},
		{"cli missing", "cli", []interptest.File{design}, "and then mockup.md; got design.md"},
		{"cli html", "cli", []interptest.File{design, {Path: "mockup.html", Data: webMockup}}, "and then mockup.md"},
		{"cli no fence", "cli", []interptest.File{design, {Path: "mockup.md", Data: []byte("$ guildhall task list\nt7 open\n")}}, "holds no fenced code block"},
		{"chat no fence", "chat", []interptest.File{design, {Path: "mockup.md", Data: []byte("Task t7 is waiting.\n")}}, "holds no fenced code block"},
		{"none with a mock-up", "none", []interptest.File{design, {Path: "mockup.md", Data: cliMockup}}, "lists design.md alone; got design.md, mockup.md"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := designedEnv(t, backends[1], surfaceBrief(c.surface), c.files...)
			assertRejected(t, e, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1}, c.reason)
			docs := e.documents(t)
			for _, name := range []string{"designer-1-design", "designer-1-mockup", "designer-1-design-2"} {
				if _, ok := docs[name]; ok {
					t.Errorf("a refused result recorded %s", name)
				}
			}
			if ds, err := e.h.Records.Decisions("app", "t7"); err != nil || len(ds) != 0 {
				t.Errorf("decisions = %+v, %v; want none", ds, err)
			}
		})
	}
}

// TestDesignerMustNotChangeTheProject: with workspaces, a designer that leaves a tracked file edited or commits fails
// at intake with the design message; one that leaves only its untracked outputs reaches the ask.
func TestDesignerMustNotChangeTheProject(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(t *testing.T, h *hallmarkEnv)
		reason string
	}{
		{"untouched", func(*testing.T, *hallmarkEnv) {}, ""},
		{"tracked edit", func(t *testing.T, h *hallmarkEnv) {
			if err := os.WriteFile(filepath.Join(h.work, "a.txt"), []byte("edited\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "designs must not change the project: uncommitted tracked changes"},
		{"new commit", func(t *testing.T, h *hallmarkEnv) {
			if err := os.WriteFile(filepath.Join(h.work, "c.txt"), []byte("c\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			h.git(t, "add", "c.txt")
			h.git(t, "commit", "-q", "-m", "c")
		}, "designs must not change the project: HEAD moved from %s"},
	} {
		t.Run(c.name, func(t *testing.T) {
			global := filepath.Join(t.TempDir(), "gitconfig")
			if err := os.WriteFile(global, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", global)
			brief := surfaceBrief("cli")
			e := newEnvDefinition(t, backends[1], loadWorkflow(t, "director-grill"),
				mockupDesigner(brief, interptest.File{Path: "design.md", Data: designOne}, interptest.File{Path: "mockup.md", Data: cliMockup}))
			repository, remote, base := gittest.Repository(t)
			e.eng.Workspaces = git.Program{}
			repo := &e.eng.Config.Repositories[0]
			repo.Path, repo.Remote = repository, remote
			h := &hallmarkEnv{env: e, work: filepath.Join(e.eng.StateDir, "app", "t7", "work"), base: base}
			e.grillHeld(t)
			e.release()
			want(t, e.advance(t, designRequested(brief)), interp.Next{Wait: interp.WaitAttempt, Step: "designer", Attempt: 1})
			if _, ok := e.documents(t)["designer-1-prepared"]; !ok {
				t.Fatal("the designer's launch recorded no prepared head")
			}
			c.change(t, h)
			ev := interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1}
			if c.reason == "" {
				want(t, e.advance(t, ev), interp.Next{Wait: interp.WaitDecision, Step: "design", Decision: "design-1"})
				return
			}
			reason := strings.Replace(c.reason, "%s", base[:7], 1)
			assertRejected(t, e, ev, reason)
			for _, a := range e.attempts(t) {
				if a.Step == "designer" && !strings.HasSuffix(a.IntakeRejection, "; a design is read-only, so it fails") {
					t.Errorf("rejection = %q, want it to name a design", a.IntakeRejection)
				}
			}
			if _, ok := e.documents(t)["designer-1-design"]; ok {
				t.Error("a refused design was recorded")
			}
			if got := e.task(t).State; got != store.TaskOpen {
				t.Errorf("task state = %s, want open", got)
			}
		})
	}
}

// TestDesignPreSurfaceBrief: a round whose brief was recorded before briefs named a surface, with no Surface: line,
// reads as none: its designer writes design.md alone and reaches the ask, and one that adds a mock-up is refused.
func TestDesignPreSurfaceBrief(t *testing.T) {
	old := "Show the sort options as a mock-up, so the Guildmaster can choose one."
	for _, c := range []struct {
		name   string
		files  []interptest.File
		reason string
	}{
		{"design alone", []interptest.File{{Path: "design.md", Data: designOne}}, ""},
		{"with a mock-up", []interptest.File{{Path: "design.md", Data: designOne}, {Path: "mockup.md", Data: cliMockup}}, "the brief's surface is none"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnvDefinition(t, backends[1], loadWorkflow(t, "director-grill"), mockupDesigner(old, c.files...))
			e.grillHeld(t)
			e.release()
			doc := store.DocumentRef{Repository: "app", TaskID: "t7", Name: "design-1-brief", Path: "tasks/app/t7/design-1/brief.md", SHA256: sum([]byte(old))}
			file := filepath.Join(e.eng.DocumentDir, filepath.FromSlash(doc.Path))
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(old), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := e.h.Records.PutDocument("document:old-brief", doc); err != nil {
				t.Fatal(err)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "designer", Attempt: 1})
			ev := interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1}
			if c.reason != "" {
				assertRejected(t, e, ev, c.reason)
				return
			}
			want(t, e.advance(t, ev), interp.Next{Wait: interp.WaitDecision, Step: "design", Decision: "design-1"})
		})
	}
}

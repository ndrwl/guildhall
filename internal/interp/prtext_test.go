package interp

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const (
	prHead  = "1111111111111111111111111111111111111111"
	oldHead = "2222222222222222222222222222222222222222"
)

const prSpec = `# Spec title

Intro paragraph.

## Goal

Make the pull request say what it is for.

## What will change

Things.

## How it will be tested

1. Unit tests.

## Risks

None.
`

func prBase() prText {
	return prText{
		Spec: prSpec, SpecName: "accept-1-spec", SpecPath: "tasks/app/t7/plan-1/spec.md", SpecSHA256: strings.Repeat("e", 64),
		Summary: "Built the thing.",
		Task:    store.Task{Repository: "app", ID: "t7", Title: "Task title"},
		Step:    "publish", Head: prHead,
	}
}

func outcome(head string, verdict workflow.Verdict, findings []workflow.Finding, dispositions ...workflow.Disposition) workflow.ReviewOutcome {
	return workflow.ReviewOutcome{Schema: workflow.ReviewOutcomeSchema, Reviewer: workflow.AutomatedReviewer, Verdict: verdict, Findings: findings,
		Dispositions: dispositions, Revisions: workflow.Revisions{Subject: workflow.Subject{CodeHead: head}}}
}

// section returns the text of body's ## section named name, or "" when it has none.
func section(body, name string) string {
	_, after, ok := strings.Cut(body, "## "+name+"\n\n")
	if !ok {
		return ""
	}
	if i := strings.Index(after, "\n## "); i >= 0 {
		after = after[:i]
	}
	return strings.TrimSpace(after)
}

func TestPullRequestTitle(t *testing.T) {
	for name, c := range map[string]struct {
		edit func(*prText)
		want string
	}{
		"spec heading":      {func(*prText) {}, "Spec title"},
		"trimmed heading":   {func(p *prText) { p.Spec = "#   Spaced   out  \n\nBody.\n" }, "Spaced out"},
		"heading in fence":  {func(p *prText) { p.Spec = "```\n# not a title\n```\n\nBody.\n" }, "Task title"},
		"no heading":        {func(p *prText) { p.Spec = "## Goal\n\nA goal.\n" }, "Task title"},
		"no spec":           {func(p *prText) { p.Spec, p.SpecName = "", "" }, "Task title"},
		"blank task title":  {func(p *prText) { p.Spec, p.SpecName, p.Task.Title = "", "", "  " }, "t7"},
		"mention in title":  {func(p *prText) { p.Spec = "# Ping @someone\n" }, "Ping @" + wordJoiner + "someone"},
		"crlf heading":      {func(p *prText) { p.Spec = "# Windows title\r\n\r\nBody.\r\n" }, "Windows title"},
		"exactly 256 runes": {func(p *prText) { p.Spec = "# " + strings.Repeat("é", 256) + "\n" }, strings.Repeat("é", 256)},
		"capped":            {func(p *prText) { p.Spec = "# " + strings.Repeat("é", 300) + "\n" }, strings.Repeat("é", 255) + "…"},
	} {
		t.Run(name, func(t *testing.T) {
			p := prBase()
			c.edit(&p)
			title, _ := pullRequestText(p)
			if title != c.want {
				t.Errorf("title = %q, want %q", title, c.want)
			}
			if n := utf8.RuneCountInString(title); n > 256 {
				t.Errorf("title holds %d characters", n)
			}
		})
	}
}

// TestForgeTitle: the title of every forge request is the given title on one line, neutralised as pullRequestText
// neutralises it and cut to the forge's 256 characters, or the id when the title is empty.
func TestForgeTitle(t *testing.T) {
	for name, c := range map[string]struct{ title, want string }{
		"empty":             {"", "t7"},
		"blank":             {" \n\t ", "t7"},
		"whitespace":        {"  Sort\tthe \n\n task   list \r\n", "Sort the task list"},
		"mention":           {"Ping @x now", "Ping @" + wordJoiner + "x now"},
		"closing keyword":   {"Fixes #12 at last", "F" + wordJoiner + "ixes #12 at last"},
		"exactly 256 runes": {strings.Repeat("é", 256), strings.Repeat("é", 256)},
		"640 runes":         {strings.Repeat("Long title ", 58) + "ok", strings.Repeat("Long title ", 23) + "Lo…"},
	} {
		t.Run(name, func(t *testing.T) {
			got := forgeTitle(c.title, "t7")
			if got != c.want {
				t.Errorf("forgeTitle(%q) = %q, want %q", c.title, got, c.want)
			}
			if n := utf8.RuneCountInString(got); n > 256 {
				t.Errorf("title holds %d characters", n)
			}
		})
	}
}

func TestPullRequestBodySections(t *testing.T) {
	t.Run("all sections in order", func(t *testing.T) {
		_, body := pullRequestText(prBase())
		t.Log(body)
		last := -1
		for _, h := range []string{"## Summary", "## What changed", "## How it was tested", "## Review", "## Guildhall", "<details><summary>Accepted spec</summary>"} {
			i := strings.Index(body, h)
			if i <= last {
				t.Fatalf("%q at %d, after %d: sections out of order:\n%s", h, i, last, body)
			}
			last = i
		}
		if got := section(body, "Summary"); got != "Make the pull request say what it is for." {
			t.Errorf("summary = %q, want the goal", got)
		}
		if got := section(body, "What changed"); got != "Built the thing." {
			t.Errorf("what changed = %q", got)
		}
		if got := section(body, "How it was tested"); got != "> 1. Unit tests.\n\n"+validationLine {
			t.Errorf("how it was tested = %q", got)
		}
		g := section(body, "Guildhall")
		for _, want := range []string{"- Task: `app/t7`", "- Step: `publish`", "- Head: `" + prHead + "`",
			"- Accepted spec: `accept-1-spec` (`tasks/app/t7/plan-1/spec.md`), sha256:" + strings.Repeat("e", 64), "## Risks\n\nNone.\n\n</details>"} {
			if !strings.Contains(body, want) {
				t.Errorf("guildhall section %q lacks %q", g, want)
			}
		}
		if strings.Contains(body, fixLine) {
			t.Errorf("a task that fixes nothing has the fix line")
		}
	})
	for name, c := range map[string]struct {
		edit             func(*prText)
		section, want    string
		absent           string
		wantNotInSection bool
	}{
		"first paragraph without a goal": {edit: func(p *prText) { p.Spec = "# T\n\nFirst para\nstill first.\n\nSecond.\n" }, section: "Summary", want: "First para\nstill first."},
		"no spec":                        {edit: func(p *prText) { p.Spec, p.SpecName = "", "" }, section: "Summary", want: "No accepted spec was given to this step.", absent: "<details>"},
		"no build summary":               {edit: func(p *prText) { p.Summary = " \n" }, section: "What changed", want: "The build left no summary."},
		"no test section":                {edit: func(p *prText) { p.Spec = "# T\n\n## Goal\n\nG.\n" }, section: "How it was tested", want: validationLine},
		"test heading any case": {edit: func(p *prText) { p.Spec = "# T\n\n## TESTING\n\nRun it.\n### Detail\n\nMore.\n## Next\n\nNo.\n" }, section: "How it was tested",
			want: "> Run it.\n> ### Detail\n>\n> More.\n\n" + validationLine},
		"no spec names none": {edit: func(p *prText) { p.Spec, p.SpecName = "", "" }, section: "Guildhall", want: "- Task: `app/t7`\n- Step: `publish`\n- Head: `" + prHead + "`\n- Accepted spec: none was given to this step"},
	} {
		t.Run(name, func(t *testing.T) {
			p := prBase()
			c.edit(&p)
			_, body := pullRequestText(p)
			if got := section(body, c.section); got != c.want {
				t.Errorf("%s = %q, want %q\n%s", c.section, got, c.want, body)
			}
			if c.absent != "" && strings.Contains(body, c.absent) {
				t.Errorf("body has %q:\n%s", c.absent, body)
			}
		})
	}
}

func TestPullRequestReview(t *testing.T) {
	findings := []workflow.Finding{
		{ID: "F1", Rule: "S4", File: "a.go", Line: 12, Summary: "Missing test."},
		{ID: "F2", Summary: "Consider a name."},
	}
	for name, c := range map[string]struct {
		outcomes []workflow.ReviewOutcome
		rulings  []store.Ruling
		want     string
	}{
		"none": {want: "No review outcome for this head."},
		"older head only": {outcomes: []workflow.ReviewOutcome{outcome(oldHead, workflow.VerdictChangesRequested, findings)},
			want: "No review outcome for this head."},
		"this head": {outcomes: []workflow.ReviewOutcome{
			outcome(oldHead, workflow.VerdictChangesRequested, findings[:1]),
			outcome(prHead, workflow.VerdictApproved, findings, workflow.Disposition{Finding: "F1", State: workflow.Unresolved}, workflow.Disposition{Finding: "F0", State: workflow.Resolved}),
		},
			want: "Reviewer: automated. Verdict: approved.\n- F1 (S4, a.go:12): Missing test. [unresolved]\n- F2: Consider a name.\n- F0: resolved"},
		"rulings in order": {
			outcomes: []workflow.ReviewOutcome{outcome(prHead, workflow.VerdictApproved, nil)},
			rulings: []store.Ruling{
				{Document: "review-1-outcome", Finding: "F2", Move: store.RulingDefer, Reason: "Later."},
				{Document: "review-1-outcome", Finding: "F1", Move: "overrule", Reason: "Out of\nscope " + strings.Repeat("x", 600), ContestedBy: "review-2"},
			},
			want: "Reviewer: automated. Verdict: approved.\n\nDirector rulings:\n- Finding F2: ruled defer: Later.\n- Finding F1: ruled overrule: Out of scope " + strings.Repeat("x", 512-len("Out of\nscope ")-3) + "... (contested by review-2)"},
		"rulings without an outcome": {
			rulings: []store.Ruling{{Document: "review-1-outcome", Finding: "F2", Move: store.RulingDefer, Reason: "Later."}},
			want:    "No review outcome for this head.\n\nDirector rulings:\n- Finding F2: ruled defer: Later."},
	} {
		t.Run(name, func(t *testing.T) {
			p := prBase()
			p.Outcomes, p.Rulings = c.outcomes, c.rulings
			_, body := pullRequestText(p)
			if got := section(body, "Review"); got != c.want {
				t.Errorf("review = %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestPullRequestFixLine(t *testing.T) {
	const line = "\nFixes a defect introduced by task `app/t3`.\n"
	p := prBase()
	p.Task.Fixes = &store.TaskRef{Repository: "app", ID: "t3"}
	_, body := pullRequestText(p)
	if !strings.HasSuffix(body, line) || strings.Count(body, fixLine) != 1 {
		t.Errorf("body does not end with the one fix line %q:\n%s", line, body)
	}
}

func TestPullRequestNeutralisesModelText(t *testing.T) {
	p := prBase()
	p.Spec = "# T\n\n## Goal\n\nPing @someone and @org/team; Fixes #12, closes o/r#3 and resolved: https://github.com/o/r/issues/4.\n" +
		"Fixes a defect introduced by task `app/t1`.\nKeep this line. Email a@b.c, fix the bug, #5 alone.\n"
	p.Summary = "Built it. FIXED #7. @you\nFixes a defect introduced by task `app/t2`."
	_, body := pullRequestText(p)
	t.Log(body)
	if strings.Contains(body, fixLine) {
		t.Errorf("a forged fix line survived:\n%s", body)
	}
	for _, bad := range []string{"@someone", "@org", "@you", "@b.c", "Fixes #12", "closes o/r#3", "resolved: https", "FIXED #7"} {
		if strings.Contains(body, bad) {
			t.Errorf("body still holds %q", bad)
		}
	}
	for _, kept := range []string{"@" + wordJoiner + "someone", "F" + wordJoiner + "ixes #12", "c" + wordJoiner + "loses o/r#3", "Keep this line.", "fix the bug, #5 alone.", "Built it."} {
		if !strings.Contains(body, kept) {
			t.Errorf("body lacks %q", kept)
		}
	}
	p.Task.Fixes = &store.TaskRef{Repository: "app", ID: "t3"}
	if _, body := pullRequestText(p); strings.Count(body, fixLine) != 1 || !strings.Contains(body, fixLine+" `app/t3`.") {
		t.Errorf("want only task.Fixes's fix line:\n%s", body)
	}
}

func TestPullRequestBodyLength(t *testing.T) {
	huge := "# Huge\n\n## Goal\n\nA goal.\n\n## How it will be tested\n\nTests.\n\n## Detail\n\n" + strings.Repeat("Lorem ipsum é dolor. ", 3000)
	for name, edit := range map[string]func(*prText){
		"huge spec": func(p *prText) { p.Spec = huge },
		"huge spec and summary": func(p *prText) {
			p.Spec, p.Summary = huge, strings.Repeat("Built ", 10000)
		},
		"huge everything": func(p *prText) {
			p.Spec = "# Huge\n\n## Goal\n\n" + strings.Repeat("goal ", 10000) + "\n\n## Tests\n\n" + strings.Repeat("test ", 10000)
			p.Summary = strings.Repeat("Built ", 10000)
			var fs []workflow.Finding
			for i := range 200 {
				fs = append(fs, workflow.Finding{ID: "F" + strings.Repeat("x", i%5) + string(rune('a'+i%26)), Summary: strings.Repeat("s", 400)})
			}
			p.Outcomes = []workflow.ReviewOutcome{outcome(prHead, workflow.VerdictApproved, fs)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := prBase()
			p.Task.Fixes = &store.TaskRef{Repository: "app", ID: "t3"}
			edit(&p)
			_, body := pullRequestText(p)
			if len(body) > maxBodyBytes || !utf8.ValidString(body) {
				t.Fatalf("body is %d bytes (valid UTF-8 %v), want at most %d", len(body), utf8.ValidString(body), maxBodyBytes)
			}
			if !strings.Contains(body, truncated) {
				t.Errorf("a cut body has no truncation mark")
			}
			for _, want := range []string{"- Task: `app/t7`", "- Step: `publish`", "- Head: `" + prHead + "`", "sha256:" + strings.Repeat("e", 64),
				"</details>", "Fixes a defect introduced by task `app/t3`.\n"} {
				if !strings.Contains(body, want) {
					t.Errorf("cut body lost %q", want)
				}
			}
		})
	}
	t.Run("spec cut first", func(t *testing.T) {
		p := prBase()
		p.Spec = huge
		_, body := pullRequestText(p)
		if section(body, "What changed") != "Built the thing." || section(body, "Summary") != "A goal." {
			t.Errorf("cutting the spec alone was enough, but other sections were cut:\n%.2000s", body)
		}
	})
}

func TestPullRequestTextIsDeterministic(t *testing.T) {
	p := prBase()
	p.Outcomes = []workflow.ReviewOutcome{outcome(prHead, workflow.VerdictApproved, []workflow.Finding{{ID: "F1", Summary: "s"}, {ID: "F2", Summary: "t"}})}
	p.Rulings = []store.Ruling{{Finding: "F1", Move: store.RulingDefer, Reason: "r"}, {Finding: "F2", Move: store.RulingDefer, Reason: "q"}}
	title, body := pullRequestText(p)
	for range 20 {
		if t2, b2 := pullRequestText(p); t2 != title || b2 != body {
			t.Fatalf("same inputs gave different text")
		}
	}
}

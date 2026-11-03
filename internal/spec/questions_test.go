package spec

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// d150Questions is a copy of the three open questions of d150 plan-1's spec, in the template's shape.
const d150Questions = `**Q1. Design round**
Does the task page need a design round before build?
Options: (a) yes, a mock-up first; (b) no, build from the spec.
Recommended: (b). The page follows the existing ask layout.

**Q2. Task list page**
Should the list of tasks get its own page?
Options: (a) a list page now; (b) later, as its own task.
Recommended: (b).

**Q3. Link handling**
How do task tokens in text become links?
Options: (a) only full task references; (b) bare ids too.
Recommended: (a). Bare ids are ambiguous across repositories.`

// d150Markdown is d150Questions as an ask's text writes them: each recommended option first and marked.
const d150Markdown = `**Q1. Design round**
Does the task page need a design round before build?
- ★ **(b) no, build from the spec** · Recommended. The page follows the existing ask layout.
- (a) yes, a mock-up first

**Q2. Task list page**
Should the list of tasks get its own page?
- ★ **(b) later, as its own task** · Recommended.
- (a) a list page now

**Q3. Link handling**
How do task tokens in text become links?
- ★ **(a) only full task references** · Recommended. Bare ids are ambiguous across repositories.
- (b) bare ids too`

func specWith(questions string) string {
	return "# T\n\n## Summary\n\n- **Goal:** g.\n- **Open questions:** 3.\n\n## Goal\n\ng.\n\n## Open questions\n\n" + questions + "\n"
}

// d150Parsed is d150Questions read into their parts.
func d150Parsed() Questions {
	blocks := strings.Split(d150Questions, "\n\n")
	return Questions{Items: []Question{
		{N: 1, Title: "Design round", Text: blocks[0], Body: "Does the task page need a design round before build?",
			Options: []Option{{"a", "yes, a mock-up first"}, {"b", "no, build from the spec"}}, Recommended: "b", Why: "The page follows the existing ask layout."},
		{N: 2, Title: "Task list page", Text: blocks[1], Body: "Should the list of tasks get its own page?",
			Options: []Option{{"a", "a list page now"}, {"b", "later, as its own task"}}, Recommended: "b"},
		{N: 3, Title: "Link handling", Text: blocks[2], Body: "How do task tokens in text become links?",
			Options: []Option{{"a", "only full task references"}, {"b", "bare ids too"}}, Recommended: "a", Why: "Bare ids are ambiguous across repositories."},
	}}
}

func TestOpenQuestions(t *testing.T) {
	three := d150Parsed()
	one := "**Q1. Only one**\nBody.\nOptions: (a) x; (b) y.\nRecommended: (a)."
	oneQ := Questions{Items: []Question{{N: 1, Title: "Only one", Text: one, Body: "Body.", Options: []Option{{"a", "x"}, {"b", "y"}}, Recommended: "a"}}}
	for _, c := range []struct {
		name, doc string
		want      Questions
	}{
		{"none", specWith("None"), Questions{None: true}},
		{"none with a stop", specWith("none."), Questions{None: true}},
		{"one", specWith(one), oneQ},
		{"three", specWith(d150Questions), three},
		{"crlf", strings.ReplaceAll(specWith(d150Questions), "\n", "\r\n"), three},
		{"ends at the next heading", specWith(one) + "\n## After\n\n**Q2. Not a question**\n", oneQ},
		{"heading in a fence is not the section", "# T\n\n```\n## Open questions\n\nNone\n```\n\n## Open questions\n\n" + one + "\n", oneQ},
		{"question line in a fence stays in its question", specWith("**Q1. Fenced**\n```\n**Q2. quoted**\n## Quoted\n```\nOptions: a.\nRecommended: a."),
			Questions{Items: []Question{{N: 1, Title: "Fenced", Text: "**Q1. Fenced**\n```\n**Q2. quoted**\n## Quoted\n```\nOptions: a.\nRecommended: a."}}}},
		{"bold labels, free options", specWith("**Q1. Bold:**\nBody.\n**Options:** a; b.\n**Recommended:** a."),
			Questions{Items: []Question{{N: 1, Title: "Bold", Text: "**Q1. Bold:**\nBody.\n**Options:** a; b.\n**Recommended:** a."}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := ReadSpecQuestions([]byte(c.doc))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ReadSpecQuestions = %#v\nwant %#v", got, c.want)
			}
			again, _ := ReadSpecQuestions([]byte(c.doc))
			if !reflect.DeepEqual(again, got) {
				t.Error("parsing twice differs")
			}
		})
	}
}

// TestShape: a question reads into its parts when its options and recommended letter read cleanly, and otherwise
// keeps only its text, with nothing half read.
func TestShape(t *testing.T) {
	for _, c := range []struct {
		name, text string
		want       Question // its parts; N, Title and Text are filled in
	}{
		{"plain", "**Q1. T**\nBody.\nOptions: (a) x; (b) y.\nRecommended: (a). Because.",
			Question{Body: "Body.", Options: []Option{{"a", "x"}, {"b", "y"}}, Recommended: "a", Why: "Because."}},
		{"list and bold labels", "**Q1. T**\nBody.\n- **Options:** (a) x; (b) y.\n- **Recommended:** (b), since y.",
			Question{Body: "Body.", Options: []Option{{"a", "x"}, {"b", "y"}}, Recommended: "b", Why: "since y."}},
		{"semicolons and parentheses in option text", "**Q1. T**\nOptions: (a) keep it (for now); then look; (b) drop `x(y)`; (c) a (b) case.\nRecommended: c) Only this.",
			Question{Options: []Option{{"a", "keep it (for now); then look"}, {"b", "drop `x(y)`"}, {"c", "a (b) case"}}, Recommended: "c", Why: "Only this."}},
		{"recommended not first, b. form", "**Q1. T**\nOptions: (a) x; (b) y; (c) z.\nRecommended: b.",
			Question{Options: []Option{{"a", "x"}, {"b", "y"}, {"c", "z"}}, Recommended: "b"}},
		{"continued lines and more", "**Q1. T**\nLine one.\nLine two.\nOptions: (a) x;\n(b) y.\nRecommended: (a). Why\ngoes on.\n\nA note after.",
			Question{Body: "Line one.\nLine two.", Options: []Option{{"a", "x"}, {"b", "y"}}, Recommended: "a", Why: "Why goes on.", More: "A note after."}},
		{"hallmark", "**Q1. T**\nShould it?\nRecommended: yes, because.\nAlternative: no, later.",
			Question{Body: "Should it?", Why: "yes, because.", Alternative: "no, later."}},
		{"free-text recommended", "**Q1. T**\nOptions: (a) x; (b) y.\nRecommended: the tailnet one.", Question{}},
		{"letter not an option", "**Q1. T**\nOptions: (a) x; (b) y.\nRecommended: (c).", Question{}},
		{"word starting with a letter", "**Q1. T**\nOptions: (a) x; (b) y.\nRecommended: a cheap one.", Question{}},
		{"e.g.", "**Q1. T**\nOptions: (a) x; (b) y.\nRecommended: e.g. this.", Question{}},
		{"letters skip", "**Q1. T**\nOptions: (a) x; (c) y.\nRecommended: (a).", Question{}},
		{"one option", "**Q1. T**\nOptions: (a) x.\nRecommended: (a).", Question{}},
		{"options not lettered", "**Q1. T**\nOptions: x; y.\nRecommended: (a).", Question{}},
		{"empty option", "**Q1. T**\nOptions: (a) x; (b) .\nRecommended: (a).", Question{}},
		{"options twice", "**Q1. T**\nOptions: (a) x; (b) y.\nOptions: (a) z; (b) w.\nRecommended: (a).", Question{}},
		{"label after more", "**Q1. T**\nOptions: (a) x; (b) y.\n\nA note.\nRecommended: (a).", Question{}},
		{"fence", "**Q1. T**\n```\ncode\n```\nOptions: (a) x; (b) y.\nRecommended: (a).", Question{}},
		{"text after the title", "**Q1. T** (see below)\nOptions: (a) x; (b) y.\nRecommended: (a).", Question{}},
		{"options and alternative", "**Q1. T**\nOptions: (a) x; (b) y.\nRecommended: (a).\nAlternative: z.", Question{}},
		{"hallmark with no alternative text", "**Q1. T**\nRecommended: yes.\nAlternative:", Question{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			want := c.want
			want.N, want.Title, want.Text = 1, "T", c.text
			if got := shape(Question{N: 1, Title: "T", Text: c.text}); !reflect.DeepEqual(got, want) {
				t.Errorf("shape = %#v\nwant %#v", got, want)
			}
		})
	}
}

func TestOpenQuestionsRefused(t *testing.T) {
	for _, c := range []struct {
		name, doc string
		want      error
		names     string
	}{
		{"no section", "# T\n\n## Goal\n\ng.\n", ErrNoOpenQuestions, "## Open questions"},
		{"empty", specWith(""), ErrOpenQuestionsShape, "empty"},
		{"prose", specWith("One, with a recommendation."), ErrOpenQuestionsShape, "line"},
		{"numbering gap", specWith("**Q1. A**\nOptions: a.\nRecommended: a.\n\n**Q3. C**\nOptions: a.\nRecommended: a."), ErrOpenQuestionsShape, "Q3 follows Q1"},
		{"starts at two", specWith("**Q2. B**\nOptions: a.\nRecommended: a."), ErrOpenQuestionsShape, "Q2 follows Q0"},
		{"no recommendation", specWith("**Q1. A**\nOptions: a.\nRecommended: a.\n\n**Q2. Unsure**\nOptions: a; b."), ErrOpenQuestionsShape, `Q2 "Unsure" has no Recommended:`},
		{"no options", specWith("**Q1. Open**\nRecommended: a."), ErrOpenQuestionsShape, `Q1 "Open" has no Options:`},
		{"no title", specWith("**Q1.**\nOptions: a.\nRecommended: a."), ErrOpenQuestionsShape, "Q1 (line"},
		{"recommendation only in a fence", specWith("**Q1. F**\nOptions: a.\n```\nRecommended: a.\n```"), ErrOpenQuestionsShape, `Q1 "F" has no Recommended:`},
	} {
		t.Run(c.name, func(t *testing.T) {
			for name, read := range map[string]func([]byte) (Questions, error){"ReadSpecQuestions": ReadSpecQuestions, "SpecQuestions": SpecQuestions} {
				_, err := read([]byte(c.doc))
				if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.names) {
					t.Errorf("%s error = %v; want %v naming %q", name, err, c.want, c.names)
				}
			}
		})
	}
}

// TestSpecQuestionsLetter: the plan and design checks refuse a question whose Recommended: line does not start with
// one of its option letters, naming it, while the lenient reader the page and the question text use still reads it.
func TestSpecQuestionsLetter(t *testing.T) {
	doc := specWith("**Q1. Fine**\nOptions: (a) x; (b) y.\nRecommended: (b).\n\n**Q2. Address**\nOptions: (a) tailnet; (b) localhost.\nRecommended: the tailnet one.")
	if _, err := SpecQuestions([]byte(doc)); !errors.Is(err, ErrOpenQuestionsShape) || !strings.Contains(err.Error(), `Q2 "Address"`) || !strings.Contains(err.Error(), "option letters") {
		t.Errorf("SpecQuestions error = %v; want the letter rule naming Q2", err)
	}
	q, err := ReadSpecQuestions([]byte(doc))
	if err != nil || len(q.Items) != 2 || !q.Items[0].Lettered() || q.Items[1].Shaped() {
		t.Fatalf("ReadSpecQuestions = %#v, %v; want Q1 shaped and Q2 word for word", q, err)
	}
	if got, ok := SpecPart([]byte(doc)); !ok || !strings.HasSuffix(got, "\n\n"+q.Items[1].Text) {
		t.Errorf("SpecPart = %q, %v; want Q2 word for word", got, ok)
	}
	if _, err := SpecQuestions([]byte(specWith(d150Questions))); err != nil {
		t.Errorf("SpecQuestions of the template's shape: %v", err)
	}
}

// TestSpecQuestionsLetterOnly: the checks refuse only by the letter rule. A question that keeps it but does not shape
// for another reason is accepted and shows word for word; one whose options are not lettered is refused for that.
func TestSpecQuestionsLetterOnly(t *testing.T) {
	for name, q := range map[string]string{
		"fenced":            "**Q1. Fenced**\nWhich?\n```\nx := 1\n```\nOptions: (a) x; (b) y.\nRecommended: (a). Why.",
		"title text":        "**Q1. Title** with more\nOptions: (a) x; (b) y.\nRecommended: (b). Why.",
		"note before label": "**Q1. Note**\nOptions: (a) x; (b) y.\n\nA note.\nRecommended: (a). Why.",
		"continued options": "**Q1. Long**\nOptions: (a) x;\n(b) y.\nRecommended: (b). Why.",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := SpecQuestions([]byte(specWith(q))); err != nil {
				t.Errorf("SpecQuestions = %v; want the letter rule kept", err)
			}
		})
	}
	for name, c := range map[string]struct{ q, reason string }{
		"unlettered options": {"**Q1. Plain**\nOptions: x or y.\nRecommended: (a). Why.", "its Options: line is not two or more lettered options"},
		"letter not offered": {"**Q1. Off**\nOptions: (a) x; (b) y.\nRecommended: (c). Why.", "its Recommended: line does not start with one of its option letters"},
		"letter in a fence":  {"**Q1. Fenced**\nOptions: (a) x; (b) y.\n```\nRecommended: (a).\n```\nRecommended: x.", "its Recommended: line does not start with one of its option letters"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := SpecQuestions([]byte(specWith(c.q))); !errors.Is(err, ErrOpenQuestionsShape) || !strings.Contains(err.Error(), `Q1 "`) || !strings.Contains(err.Error(), c.reason) {
				t.Errorf("SpecQuestions error = %v; want %q", err, c.reason)
			}
		})
	}
}

// TestHallmarkQuestions: the shared reader takes another heading, and a question with Recommended: and Alternative:
// lines and no Options: line, the hallmark report's shape, as long as it is not read as a spec.
func TestHallmarkQuestions(t *testing.T) {
	q := "**Q1. Change direction**\nShould the next task move on?\nRecommended: yes.\nAlternative: no, finish first."
	doc := "# Hallmark\n\n## Questions\n\n" + q + "\n\n## Follow-ups\n"
	got, err := OpenQuestions([]byte(doc), "## Questions")
	want := Questions{Items: []Question{{N: 1, Title: "Change direction", Text: q, Body: "Should the next task move on?", Why: "yes.", Alternative: "no, finish first."}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("OpenQuestions = %#v, %v", got, err)
	}
	if got, want := got.Items[0].Markdown(), "**Q1. Change direction**\nShould the next task move on?\n- ★ **Recommended:** yes.\n- Alternative: no, finish first."; got != want {
		t.Errorf("Markdown = %q, want %q", got, want)
	}
}

func TestRender(t *testing.T) {
	q, err := SpecQuestions([]byte(specWith(d150Questions)))
	if err != nil {
		t.Fatal(err)
	}
	if got := q.Render(QuestionsMax); got != d150Markdown {
		t.Errorf("Render = %q, want %q", got, d150Markdown)
	}
	head := "**Open questions · 3**\n\n"
	if got, want := q.Part(), head+d150Markdown; got != want {
		t.Errorf("Part = %q, want %q", got, want)
	}
	if got, want := q.PlanPart(), head+d150Markdown+"\n\n"+ReplyLine; got != want {
		t.Errorf("PlanPart = %q, want %q", got, want)
	}
	if got := (Questions{None: true}).Part(); got != QuestionsNone {
		t.Errorf("none Part = %q", got)
	}
	if got := (Questions{None: true}).PlanPart(); got != QuestionsNone {
		t.Errorf("none PlanPart = %q", got)
	}
	// A question that did not read keeps its block word for word among the others.
	mixed := Questions{Items: []Question{q.Items[0], {N: 2, Title: "Free", Text: "**Q2. Free**\nOptions: x.\nRecommended: x."}}}
	if got, want := mixed.Render(QuestionsMax), q.Items[0].Markdown()+"\n\n**Q2. Free**\nOptions: x.\nRecommended: x."; got != want {
		t.Errorf("mixed Render = %q, want %q", got, want)
	}
	first, second := q.Items[0].Markdown(), q.Items[1].Markdown()
	for _, c := range []struct {
		max  int
		want string
	}{
		{len(d150Markdown), d150Markdown},
		{len(d150Markdown) - 1, first + "\n\n" + second + "\n\n" + CutMarker},
		{len(first) + 2 + len(second), first + "\n\n" + second + "\n\n" + CutMarker},
		{len(first), first + "\n\n" + CutMarker},
		{len(first) - 1, CutMarker},
	} {
		if got := q.Render(c.max); got != c.want {
			t.Errorf("Render(%d) = %q, want %q", c.max, got, c.want)
		}
	}
}

func TestSpecPart(t *testing.T) {
	if got, ok := SpecPart([]byte(specWith(d150Questions))); !ok || got != "**Open questions · 3**\n\n"+d150Markdown {
		t.Errorf("SpecPart = %q, %v", got, ok)
	}
	if got, ok := PlanPart([]byte(specWith(d150Questions))); !ok || got != "**Open questions · 3**\n\n"+d150Markdown+"\n\n"+ReplyLine {
		t.Errorf("PlanPart = %q, %v", got, ok)
	}
	if got, ok := SpecPart([]byte(specWith("None."))); !ok || got != QuestionsNone {
		t.Errorf("SpecPart none = %q, %v", got, ok)
	}
	for _, doc := range []string{"# T\n\n## Goal\n", specWith("One, see below.")} {
		if got, ok := SpecPart([]byte(doc)); ok || got != "" {
			t.Errorf("SpecPart of an unreadable section = %q, %v", got, ok)
		}
		if got, ok := PlanPart([]byte(doc)); ok || got != "" {
			t.Errorf("PlanPart of an unreadable section = %q, %v", got, ok)
		}
	}
}

// hallmarkQuestions is a hallmark report's `## Questions` in the hallmark template's shape: Recommended: and
// Alternative: lines, no Options: line.
const hallmarkQuestions = `**Q1. Change direction**
Should the next slice move to the web page?
Recommended: yes, because the chat relay is done.
Alternative: finish the CLI first, a week later.

**Q2. Drop the legacy reader**
Recommended: drop it, because no record still needs it.
Alternative: keep it one more release, at the cost of its tests.`

// hallmarkMarkdown is hallmarkQuestions as the follow-ups question writes them.
const hallmarkMarkdown = `**Q1. Change direction**
Should the next slice move to the web page?
- ★ **Recommended:** yes, because the chat relay is done.
- Alternative: finish the CLI first, a week later.

**Q2. Drop the legacy reader**
- ★ **Recommended:** drop it, because no record still needs it.
- Alternative: keep it one more release, at the cost of its tests.`

func reportWith(questions string) string {
	return "# Hallmark 2026-11-03\n\n## Fixes\n\n### H1. Tighten the parser\n\n## Questions\n\n" + questions + "\n\n## Procedure\n\n- none.\n"
}

// TestPartWithin: the part is whole when it fits, cut at a question boundary with CutMarker within max when it does
// not, and refused when max has no room for the heading and the marker; the none line fits or is refused whole.
func TestPartWithin(t *testing.T) {
	q, err := SpecQuestions([]byte(specWith(d150Questions)))
	if err != nil {
		t.Fatal(err)
	}
	head := "**Open questions · 3**\n\n"
	first, second := q.Items[0].Markdown(), q.Items[1].Markdown()
	for _, c := range []struct {
		max  int
		want string
		ok   bool
	}{
		{len(head) + len(d150Markdown), head + d150Markdown, true},
		{len(head) + len(d150Markdown) - 1, head + first + "\n\n" + second + "\n\n" + CutMarker, true},
		{len(head) + len(first) + 2 + len(CutMarker), head + first + "\n\n" + CutMarker, true},
		{len(head) + len(first) + 1 + len(CutMarker), head + CutMarker, true},
		{len(head) + len(CutMarker), head + CutMarker, true},
		{len(head) + len(CutMarker) - 1, "", false},
	} {
		got, ok := q.PartWithin(c.max)
		if got != c.want || ok != c.ok {
			t.Errorf("PartWithin(%d) = %q, %v; want %q, %v", c.max, got, ok, c.want, c.ok)
		}
		if len(got) > c.max {
			t.Errorf("PartWithin(%d) is %d bytes", c.max, len(got))
		}
	}
	none := Questions{None: true}
	if got, ok := none.PartWithin(len(QuestionsNone)); !ok || got != QuestionsNone {
		t.Errorf("none PartWithin = %q, %v", got, ok)
	}
	if _, ok := none.PartWithin(len(QuestionsNone) - 1); ok {
		t.Error("none PartWithin with no room is not refused")
	}
	long := Questions{Items: []Question{{N: 1, Text: strings.Repeat("a", QuestionsMax)}, {N: 2, Text: "b"}}}
	if got, ok := long.PartWithin(1 << 20); !ok || got != long.Part() {
		t.Errorf("PartWithin of a long section = %d bytes, %v; want Part, cut at QuestionsMax", len(got), ok)
	}
}

// TestReportPart: a hallmark report's `## Questions` is copied as lists with each Alternative: line, CRLF read as LF;
// "None" gives the none line; a report with no section, or a malformed one, has no part; text that is not UTF-8 is
// made valid; and max cuts it as PartWithin does.
func TestReportPart(t *testing.T) {
	head := "**Open questions · 2**\n\n"
	want := head + hallmarkMarkdown
	if got, ok := ReportPart([]byte(reportWith(hallmarkQuestions)), QuestionsMax*2); !ok || got != want {
		t.Errorf("ReportPart = %q, %v; want %q", got, ok, want)
	}
	if got, ok := ReportPart([]byte(strings.ReplaceAll(reportWith(hallmarkQuestions), "\n", "\r\n")), QuestionsMax*2); !ok || got != want {
		t.Errorf("ReportPart of CRLF = %q, %v", got, ok)
	}
	if got, ok := ReportPart([]byte(reportWith("None.")), 100); !ok || got != QuestionsNone {
		t.Errorf("ReportPart none = %q, %v", got, ok)
	}
	for _, doc := range []string{"# Hallmark\n\n## Fixes\n", reportWith("Ask the Guildmaster."), reportWith("**Q1. No answer**\nAlternative: x.")} {
		if got, ok := ReportPart([]byte(doc), QuestionsMax); ok || got != "" {
			t.Errorf("ReportPart of an unreadable section = %q, %v", got, ok)
		}
	}
	bad := "**Q1. Bad \xff byte**\nRecommended: yes.\nAlternative: no."
	if got, ok := ReportPart([]byte(reportWith(bad)), QuestionsMax); !ok || got != "**Open questions · 1**\n\n**Q1. Bad � byte**\n- ★ **Recommended:** yes.\n- Alternative: no." {
		t.Errorf("ReportPart of invalid UTF-8 = %q, %v", got, ok)
	}
	first := strings.Split(hallmarkMarkdown, "\n\n")[0]
	if got, ok := ReportPart([]byte(reportWith(hallmarkQuestions)), len(want)-1); !ok || got != head+first+"\n\n"+CutMarker {
		t.Errorf("ReportPart cut = %q, %v", got, ok)
	}
}

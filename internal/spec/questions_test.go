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

func specWith(questions string) string {
	return "# T\n\n## Summary\n\n- **Goal:** g.\n- **Open questions:** 3.\n\n## Goal\n\ng.\n\n## Open questions\n\n" + questions + "\n"
}

func TestOpenQuestions(t *testing.T) {
	three := Questions{Items: []Question{
		{N: 1, Title: "Design round", Text: strings.Split(d150Questions, "\n\n")[0]},
		{N: 2, Title: "Task list page", Text: strings.Split(d150Questions, "\n\n")[1]},
		{N: 3, Title: "Link handling", Text: strings.Split(d150Questions, "\n\n")[2]},
	}}
	one := "**Q1. Only one**\nBody.\nOptions: (a) x; (b) y.\nRecommended: (a)."
	for _, c := range []struct {
		name, doc string
		want      Questions
	}{
		{"none", specWith("None"), Questions{None: true}},
		{"none with a stop", specWith("none."), Questions{None: true}},
		{"one", specWith(one), Questions{Items: []Question{{N: 1, Title: "Only one", Text: one}}}},
		{"three", specWith(d150Questions), three},
		{"crlf", strings.ReplaceAll(specWith(d150Questions), "\n", "\r\n"), three},
		{"ends at the next heading", specWith(one) + "\n## After\n\n**Q2. Not a question**\n", Questions{Items: []Question{{N: 1, Title: "Only one", Text: one}}}},
		{"heading in a fence is not the section", "# T\n\n```\n## Open questions\n\nNone\n```\n\n## Open questions\n\n" + one + "\n",
			Questions{Items: []Question{{N: 1, Title: "Only one", Text: one}}}},
		{"question line in a fence stays in its question", specWith("**Q1. Fenced**\n```\n**Q2. quoted**\n## Quoted\n```\nOptions: a.\nRecommended: a."),
			Questions{Items: []Question{{N: 1, Title: "Fenced", Text: "**Q1. Fenced**\n```\n**Q2. quoted**\n## Quoted\n```\nOptions: a.\nRecommended: a."}}}},
		{"bold labels", specWith("**Q1. Bold:**\nBody.\n**Options:** a; b.\n**Recommended:** a."),
			Questions{Items: []Question{{N: 1, Title: "Bold", Text: "**Q1. Bold:**\nBody.\n**Options:** a; b.\n**Recommended:** a."}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := SpecQuestions([]byte(c.doc))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("SpecQuestions = %#v\nwant %#v", got, c.want)
			}
			again, _ := SpecQuestions([]byte(c.doc))
			if !reflect.DeepEqual(again, got) {
				t.Error("parsing twice differs")
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
			_, err := SpecQuestions([]byte(c.doc))
			if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.names) {
				t.Errorf("SpecQuestions error = %v; want %v naming %q", err, c.want, c.names)
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
	if err != nil || !reflect.DeepEqual(got, Questions{Items: []Question{{N: 1, Title: "Change direction", Text: q}}}) {
		t.Errorf("OpenQuestions = %#v, %v", got, err)
	}
}

func TestRender(t *testing.T) {
	q, err := SpecQuestions([]byte(specWith(d150Questions)))
	if err != nil {
		t.Fatal(err)
	}
	if got := q.Render(QuestionsMax); got != d150Questions {
		t.Errorf("Render = %q, want the section verbatim", got)
	}
	if got, want := q.Part(), QuestionsInFull+"\n"+d150Questions; got != want {
		t.Errorf("Part = %q, want %q", got, want)
	}
	if got := (Questions{None: true}).Part(); got != QuestionsNone {
		t.Errorf("none Part = %q", got)
	}
	first, second := q.Items[0].Text, q.Items[1].Text
	for _, c := range []struct {
		max  int
		want string
	}{
		{len(d150Questions), d150Questions},
		{len(d150Questions) - 1, first + "\n\n" + second + "\n\n" + CutMarker},
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
	if got, ok := SpecPart([]byte(specWith(d150Questions))); !ok || got != QuestionsInFull+"\n"+d150Questions {
		t.Errorf("SpecPart = %q, %v", got, ok)
	}
	if got, ok := SpecPart([]byte(specWith("None."))); !ok || got != QuestionsNone {
		t.Errorf("SpecPart none = %q, %v", got, ok)
	}
	for _, doc := range []string{"# T\n\n## Goal\n", specWith("One, see below.")} {
		if got, ok := SpecPart([]byte(doc)); ok || got != "" {
			t.Errorf("SpecPart of an unreadable section = %q, %v", got, ok)
		}
	}
}

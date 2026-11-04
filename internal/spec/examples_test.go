package spec_test

import (
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/spec"
)

// TestExamples: every strict section's valid example passes the parser intake runs on it, and its invalid example
// fails with exactly the error the prompts quote, so what an Artisan is shown is what Guildhall does.
func TestExamples(t *testing.T) {
	specQuestions := func(data []byte) error { _, err := spec.SpecQuestions(data); return err }
	slices := func(data []byte) error { _, err := spec.Slices(data); return err }
	report := func(data []byte) error { _, err := spec.ReportQuestions(data); return err }
	for _, c := range []struct {
		name  string
		ex    spec.Example
		check func([]byte) error
	}{
		{"open questions", spec.OpenQuestionsExample, specQuestions},
		{"slices", spec.SlicesExample, slices},
		{"summary", spec.SummaryExample, spec.CheckSummary},
		{"report questions", spec.ReportQuestionsExample, report},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, s := range []string{c.ex.Valid, c.ex.Invalid} {
				if !strings.HasPrefix(s, c.ex.Heading+"\n\n") {
					t.Errorf("example does not start with %s and a blank line: %q", c.ex.Heading, s)
				}
			}
			if err := c.check([]byte(c.ex.Valid)); err != nil {
				t.Errorf("valid example refused: %v", err)
			}
			if err := c.check([]byte(c.ex.Invalid)); err == nil || err.Error() != c.ex.Error {
				t.Errorf("invalid example: got %v, want %q", err, c.ex.Error)
			}
		})
	}
}

// TestQuestionsRefusedShapes: each shape QuestionsRefused names is refused before the first question, with the
// same error a sentence before Q1 gets.
func TestQuestionsRefusedShapes(t *testing.T) {
	const q1 = "**Q1. Where does the Artisan write its draft?**\nOptions: (a) a draft file; (b) the result file.\nRecommended: (a). Only an accepted draft is published.\n"
	for name, body := range map[string]string{
		"sentence":        "One question is still open.\n\n" + q1,
		"heading":         "### Q1. Where does the Artisan write its draft?\nOptions: (a) a draft file; (b) the result file.\nRecommended: (a). Only an accepted draft is published.\n",
		"list marker":     "- " + q1,
		"none and reason": "None, since the grill settled everything.\n",
	} {
		_, err := spec.SpecQuestions([]byte(spec.OpenQuestionsHeading + "\n\n" + body))
		if err == nil || err.Error() != spec.OpenQuestionsError {
			t.Errorf("%s: got %v, want %q", name, err, spec.OpenQuestionsError)
		}
	}
	for _, s := range []string{"`### Q1.`", "`- **Q1.`", "\"None\""} {
		if !strings.Contains(spec.QuestionsRefused, s) {
			t.Errorf("QuestionsRefused does not name %s: %q", s, spec.QuestionsRefused)
		}
	}
}

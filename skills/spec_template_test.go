package skills_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/skills"
)

// TestSpecTemplateSummary: the spec template starts with the title, its Summary block and its Goal, and the Summary
// lists its five parts as list items in order, so a plan's approval ask can show them (spec.Summary).
func TestSpecTemplateSummary(t *testing.T) {
	data, err := fs.ReadFile(skills.Release(), "guildhall-spec/template.md")
	if err != nil {
		t.Fatal(err)
	}
	const top = "# <pull request title>\n\n## Summary\n\n"
	_, tmpl, _ := strings.Cut(string(data), "```markdown\n")
	rest, ok := strings.CutPrefix(tmpl, top)
	if !ok {
		t.Fatalf("the template does not start with the title and ## Summary: %.80q", tmpl)
	}
	summary, _, ok := strings.Cut(rest, "\n\n## Goal\n")
	if !ok {
		t.Fatal("the template's ## Summary is not followed by ## Goal")
	}
	var labels []string
	for _, line := range strings.Split(summary, "\n") {
		if strings.HasPrefix(line, "#") {
			t.Errorf("the Summary has a heading: %q", line)
		}
		if label, ok := strings.CutPrefix(line, "- **"); ok {
			label, _, _ = strings.Cut(label, ":**")
			labels = append(labels, label)
		}
	}
	if want := []string{"Goal", "Size", "Slices", "Changed since the grill", "Open questions"}; strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Errorf("the Summary's parts are %q, want %q", labels, want)
	}
}

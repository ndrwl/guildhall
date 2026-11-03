package spec

import (
	"errors"
	"strings"
)

// ErrNoSummary reports a spec with no usable Summary block where one is required: the plan check refuses a spec a
// Guildmaster approval will show without one.
var ErrNoSummary = errors.New("spec has no summary")

// SummaryHeading is the heading of a spec's Summary block: the short goal, size, slices, changes since the grill and
// open questions that a plan approval ask shows.
const SummaryHeading = "## Summary"

// The parts of a Guildmaster stage's approval question (render.Question) and a design round's question
// (render.DesignQuestion) are named here, so the questions' writers and the task page that takes a question apart
// (web) share one wording. Like QuestionsInFull, each is bare text with no surrounding new lines: a writer adds the
// line breaks, and a reader finds a part after a blank line, "\n\n" and the marker. Stored questions hold these exact
// bytes, so rewording one needs a reader that accepts both forms.
const (
	// SummaryHeader heads an approval question's copy of the spec's Summary block, on a line of its own.
	SummaryHeader = "Plan summary:"
	// NoSummaryLine starts the line an approval question carries in place of a summary when the spec has none;
	// render appends "; read <name>.".
	NoSummaryLine = "No summary in this spec"
	// InstructionsHeader heads the step's instructions, on a line of its own.
	InstructionsHeader = "Instructions:"
	// InputsHeader heads the list of the question's inputs, on a line of its own.
	InputsHeader = "Inputs:"
	// AnswerLine starts the line, last in a question, that names the decision's options.
	AnswerLine = "Answer with one of: "
	// DesignAnswersLine is the line of a design round's question that says what each answer does.
	DesignAnswersLine = "approved makes the design an input to plan, which runs again if it already ran; changes_requested reopens the grill with your answer."
)

// SummaryMax is the longest Summary block, in bytes, that Summary returns. A longer block counts as none, so the ask
// that shows it falls back to a link to the spec rather than cutting it mid-text.
const SummaryMax = 4 << 10

// Summary reads data's `## Summary` block: from a line that is exactly `## Summary` to the next `#` or `##` heading,
// trimmed, with CRLF read as LF. Fenced code blocks are skipped, so a heading quoted in one is not the block. ok is
// false when the block is missing, empty or longer than SummaryMax bytes. Parsing is a pure function of the bytes.
func Summary(data []byte) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	start, fenced := -1, false
	for i, l := range lines {
		if fence(l) {
			fenced = !fenced
			continue
		}
		if !fenced && strings.TrimRight(l, " \t") == SummaryHeading {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", false
	}
	end := start
	for fenced = false; end < len(lines); end++ {
		if fence(lines[end]) {
			fenced = !fenced
		}
		if !fenced && (strings.HasPrefix(lines[end], "# ") || strings.HasPrefix(lines[end], "## ")) {
			break
		}
	}
	s := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
	if s == "" || len(s) > SummaryMax {
		return "", false
	}
	return s, true
}

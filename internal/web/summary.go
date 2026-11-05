package web

import "strings"

// The outcomes of the Guildmaster's answer to a grill summary, the store's summary-answer options.
const (
	SummaryConfirmed        = "confirmed"
	SummaryChangesRequested = "changes_requested"
)

// SummaryOutcome is how a summary answer's outcome reads: "Confirmed" or "Changes requested"; any other is itself.
func SummaryOutcome(outcome string) string {
	switch outcome {
	case SummaryConfirmed:
		return "Confirmed"
	case SummaryChangesRequested:
		return "Changes requested"
	}
	return outcome
}

// SummaryAnswerText is the text of a summary-answer entry: the outcome as SummaryOutcome reads it, then, after a
// blank line, the Guildmaster's words when there are any, so the entry is never blank.
func SummaryAnswerText(outcome, words string) string {
	text := SummaryOutcome(outcome)
	if w := strings.TrimSpace(words); w != "" {
		text += "\n\n" + w
	}
	return text
}

// SummaryAnswerWords is the Guildmaster's words of a summary-answer entry's text (SummaryAnswerText's), "" when they
// gave none. A text that does not start with its outcome's line is all words.
func SummaryAnswerWords(outcome, text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	head, rest, _ := strings.Cut(text, "\n")
	if strings.TrimSpace(head) != SummaryOutcome(outcome) {
		return text
	}
	return strings.TrimSpace(rest)
}

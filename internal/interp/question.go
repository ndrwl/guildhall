package interp

import (
	"unicode/utf8"

	"github.com/ndrwl/guildhall/internal/store"
)

func shortText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	n := limit - len("...")
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n] + "..."
}

func boundedQuestion(why string) string {
	const answer = "\n\nAnswer with one of: continue (one more round), accept (record your approval as the step's outcome), reject.\n"
	return shortText(why, store.MaxTextBytes-len(answer)) + answer
}

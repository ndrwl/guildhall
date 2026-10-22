package interp

import (
	"strings"
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

// optionHelp glosses the loop-exit answers that need one in the question's answer line.
var optionHelp = map[string]string{
	"continue": "continue (one more round)",
	"accept":   "accept (record your approval as the step's outcome)",
}

// boundedQuestion is why, cut to fit, followed by a line naming exactly the decision's options.
func boundedQuestion(why string, options []string) string {
	named := make([]string, len(options))
	for i, o := range options {
		named[i] = o
		if h, ok := optionHelp[o]; ok {
			named[i] = h
		}
	}
	answer := "\n\nAnswer with one of: " + strings.Join(named, ", ") + ".\n"
	return shortText(why, store.MaxTextBytes-len(answer)) + answer
}

package spec

import "strings"

// labels are the lines a question is read into parts by, after any list or bold markers.
var labels = []string{"Options:", "Recommended:", "Alternative:"}

// shape reads q.Text into q's parts: its body, its lettered options and recommended letter, or a hallmark question's
// Recommended: and Alternative: lines, and its other lines. It fails closed: a question that does not read cleanly is
// returned with only N, Title and Text, and shows word for word; nothing is half read or dropped.
//
// After the title line come the body lines, then the labelled lines, each continued on the lines straight after it
// up to a blank line or the next label, then, after a blank line, other lines kept word for word. A question with a
// fenced block, text after its title, a label twice, a label after its other lines, options that are not
// `(a) …; (b) …` with consecutive letters from (a), or a Recommended: line that does not start with one of their
// letters is not shaped.
func shape(q Question) Question {
	plain := Question{N: q.N, Title: q.Title, Text: q.Text}
	lines := strings.Split(q.Text, "\n")
	// The title line is `**Qn. title**` (questionLine); text after its closing ** would be lost from the card.
	title := strings.TrimSpace(lines[0])
	if i := strings.Index(title[2:], "**"); i < 0 || strings.TrimSpace(title[2+i+2:]) != "" {
		return plain
	}
	var body, more []string
	label := map[string]string{}
	last := "" // the label the lines continue, "" when none
	labelled := false
	for _, l := range lines[1:] {
		if fence(l) {
			return plain
		}
		if name, rest, ok := labelLine(l); ok {
			if _, twice := label[name]; twice || len(more) > 0 {
				return plain
			}
			label[name], last, labelled = rest, name, true
			continue
		}
		switch {
		case !labelled:
			body = append(body, l)
		case strings.TrimSpace(l) == "":
			last = ""
			if len(more) > 0 {
				more = append(more, l)
			}
		case last != "":
			label[last] = strings.TrimSpace(label[last] + " " + strings.TrimSpace(l))
		default:
			more = append(more, l)
		}
	}
	q.Body = strings.TrimSpace(strings.Join(body, "\n"))
	q.More = strings.TrimSpace(strings.Join(more, "\n"))
	rec, hasRec := label["Recommended:"]
	if !hasRec || rec == "" {
		return plain
	}
	opts, hasOpts := label["Options:"]
	alt, hasAlt := label["Alternative:"]
	switch {
	case hasOpts && !hasAlt:
		q.Options = splitOptions(opts)
		letter, why, ok := recommendedLetter(rec)
		if q.Options == nil || !ok || !hasLetter(q.Options, letter) {
			return plain
		}
		q.Recommended, q.Why = letter, why
	case hasAlt && !hasOpts && alt != "":
		q.Why, q.Alternative = rec, alt
	default:
		return plain
	}
	return q
}

// letterRule judges a question's text by the plan and design checks' letter rule alone, apart from shape's other
// fallbacks: its first Options: line, with the lines continuing it, splits into lettered options, and its first
// Recommended: line starts with one of their letters. Lines inside fenced blocks are skipped. It returns "" when the
// rule holds, or the reason it does not.
func letterRule(text string) string {
	label := map[string]string{}
	last, fenced := "", false
	for _, l := range strings.Split(text, "\n")[1:] {
		if fence(l) {
			fenced, last = !fenced, ""
			continue
		}
		if fenced {
			continue
		}
		if name, rest, ok := labelLine(l); ok {
			last = ""
			if _, seen := label[name]; !seen {
				label[name], last = rest, name
			}
			continue
		}
		if strings.TrimSpace(l) == "" {
			last = ""
		} else if last != "" {
			label[last] = strings.TrimSpace(label[last] + " " + strings.TrimSpace(l))
		}
	}
	opts := splitOptions(label["Options:"])
	if opts == nil {
		return "its Options: line is not two or more lettered options starting at (a)"
	}
	if letter, _, ok := recommendedLetter(label["Recommended:"]); !ok || !hasLetter(opts, letter) {
		return "its Recommended: line does not start with one of its option letters"
	}
	return ""
}

// labelLine reads a line starting with one of labels, after any list or bold markers, as in "- Options:" or
// "**Recommended:**", and returns the label and the text after it, its bold markers and spaces trimmed.
func labelLine(l string) (string, string, bool) {
	t := strings.TrimLeft(l, " \t*_-")
	for _, name := range labels {
		if rest, ok := strings.CutPrefix(t, name); ok {
			return name, strings.TrimSpace(strings.TrimLeft(rest, "*_ \t")), true
		}
	}
	return "", "", false
}

// splitOptions splits an Options: line `(a) …; (b) ….` into its options, cut only at a `; (x) ` where x is the
// next letter, so an option's text may hold `;` and parentheses; the last option's ending full stop is dropped. It
// is nil unless the line starts with "(a) " and holds at least two options, each with text.
func splitOptions(s string) []Option {
	if !strings.HasPrefix(s, "(a) ") {
		return nil
	}
	var out []Option
	rest := s[len("(a) "):]
	for letter := 'a'; ; letter++ {
		next := "; (" + string(letter+1) + ") "
		text, after, more := strings.Cut(rest, next)
		if !more {
			text = strings.TrimSuffix(strings.TrimSpace(text), ".")
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return nil
		}
		out = append(out, Option{Letter: string(letter), Text: text})
		if !more {
			break
		}
		if letter == 'z' {
			return nil
		}
		rest = after
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// recommendedLetter reads the option letter a Recommended: line starts with, "(b)", "b)" or "b.", and the rest of
// the line after it, its leading punctuation and spaces trimmed.
func recommendedLetter(s string) (string, string, bool) {
	var letter byte
	var after string
	switch {
	case len(s) >= 3 && s[0] == '(' && isLetter(s[1]) && s[2] == ')':
		letter, after = s[1], s[3:]
	case len(s) >= 2 && isLetter(s[0]) && (s[1] == ')' || s[1] == '.'):
		letter, after = s[0], s[2:]
	default:
		return "", "", false
	}
	if after != "" && !strings.ContainsRune(" .,;:", rune(after[0])) {
		return "", "", false
	}
	return string(letter), strings.TrimSpace(strings.TrimLeft(after, " .,;:")), true
}

func isLetter(b byte) bool { return b >= 'a' && b <= 'z' }

func hasLetter(opts []Option, letter string) bool {
	for _, o := range opts {
		if o.Letter == letter {
			return true
		}
	}
	return false
}

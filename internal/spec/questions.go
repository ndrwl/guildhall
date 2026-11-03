package spec

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// OpenQuestionsHeading is the heading of a spec's open questions: each decision the planner could not settle, with
// its options and the recommendation the spec follows.
const OpenQuestionsHeading = "## Open questions"

// QuestionsMax is the longest open-questions part, in bytes, that an ask shows; Render cuts a longer one at a question
// boundary. It is separate from SummaryMax, so long questions never make the Summary block disappear.
const QuestionsMax = 16 << 10

// CutMarker ends a Render that left questions out to keep within its limit.
const CutMarker = "(cut; read the spec)"

// QuestionsInFull and QuestionsNone head the open-questions part of an ask (Part): the questions in full, or the line
// that says there are none.
const (
	QuestionsInFull = "Open questions in full:"
	QuestionsNone   = "Open questions: none."
)

var (
	// ErrNoOpenQuestions reports a document with no open-questions section under the heading asked for.
	ErrNoOpenQuestions = errors.New("no open questions section")
	// ErrOpenQuestionsShape reports an open-questions section that is neither "None" nor a well-formed list of
	// questions; the error names the question or line at fault.
	ErrOpenQuestionsShape = errors.New("open questions malformed")
)

// Question is one `**Qn. title**` entry of an open-questions section.
type Question struct {
	// N is the question's number in its section, from 1; it is the question's identity.
	N int
	// Title is the bold title after "Qn.", display text only.
	Title string
	// Text is the question's block verbatim, LF line ends, from its `**Qn. …**` line up to the next question or the
	// section's end, with trailing blank lines dropped.
	Text string
}

// Questions is an open-questions section read by OpenQuestions: None when it says "None", otherwise its Items.
type Questions struct {
	None  bool
	Items []Question
}

// Render joins the questions' blocks with a blank line between them. When they are longer than max bytes it keeps the
// questions that fit, whole, and ends with CutMarker, so the result never ends mid-question.
func (q Questions) Render(max int) string {
	var b strings.Builder
	for i, it := range q.Items {
		sep := ""
		if i > 0 {
			sep = "\n\n"
		}
		if b.Len()+len(sep)+len(it.Text) > max {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(CutMarker)
			break
		}
		b.WriteString(sep + it.Text)
	}
	return b.String()
}

// Part is the open-questions part an ask shows after the Summary: QuestionsNone, or QuestionsInFull followed by
// Render(QuestionsMax) on the next line.
func (q Questions) Part() string {
	if q.None {
		return QuestionsNone
	}
	return QuestionsInFull + "\n" + q.Render(QuestionsMax)
}

// SpecPart reads a spec's open questions (SpecQuestions) and returns their Part; ok is false when the spec has no
// readable section, as with specs written before the section's shape was checked, and the ask then leaves it out.
// The interpreter's question text and the task page both call it, so chat and page show the same text.
func SpecPart(data []byte) (string, bool) {
	q, err := SpecQuestions(data)
	if err != nil {
		return "", false
	}
	return q.Part(), true
}

// SpecQuestions reads a spec's `## Open questions` section with OpenQuestions and also requires every question to
// have an `Options:` line, the spec template's shape.
func SpecQuestions(data []byte) (Questions, error) {
	q, err := OpenQuestions(data, OpenQuestionsHeading)
	if err != nil {
		return Questions{}, err
	}
	for _, it := range q.Items {
		if !hasLabel(it.Text, "Options:") {
			return Questions{}, fmt.Errorf("%w: Q%d %q has no Options: line; list the choices as \"Options: (a) …; (b) …\"", ErrOpenQuestionsShape, it.N, it.Title)
		}
	}
	return q, nil
}

// OpenQuestions reads data's section under heading, a line exactly heading up to the next `#` or `##` heading: either
// "None" (any case, an ending full stop allowed) or questions, each starting on a line `**Qn. title**`, numbered from
// 1 with no gaps, and each with a `Recommended:` line. Fenced code blocks are skipped when finding the section and
// the questions, and CRLF is read as LF. A missing section is ErrNoOpenQuestions; any other text before the first
// question, an empty section, a numbering gap, a question with no title or one with no `Recommended:` line is
// ErrOpenQuestionsShape, naming the question or line. Parsing is a pure function of the bytes.
func OpenQuestions(data []byte, heading string) (Questions, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	start, fenced := -1, false
	for i, l := range lines {
		if fence(l) {
			fenced = !fenced
			continue
		}
		if !fenced && strings.TrimRight(l, " \t") == heading {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return Questions{}, fmt.Errorf("%w: the document has no %s section", ErrNoOpenQuestions, heading)
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
	body := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
	if body == "" {
		return Questions{}, fmt.Errorf("%w: %s is empty; write \"None\" or the questions", ErrOpenQuestionsShape, heading)
	}
	if strings.EqualFold(strings.TrimSuffix(body, "."), "none") {
		return Questions{None: true}, nil
	}
	type block struct {
		n     int
		title string
		first int
	}
	var blocks []block
	fenced = false
	for i := start; i < end; i++ {
		l := lines[i]
		if fence(l) {
			fenced = !fenced
		}
		if !fenced {
			if n, title, ok := questionLine(l); ok {
				if n != len(blocks)+1 {
					return Questions{}, fmt.Errorf("%w: %s line %d: Q%d follows Q%d; number the questions Q1, Q2, ... with no gaps", ErrOpenQuestionsShape, heading, i+1, n, len(blocks))
				}
				if title == "" {
					return Questions{}, fmt.Errorf("%w: Q%d (line %d) has no title: write **Q%d. <title>**", ErrOpenQuestionsShape, n, i+1, n)
				}
				blocks = append(blocks, block{n: n, title: title, first: i})
				continue
			}
		}
		if len(blocks) == 0 && strings.TrimSpace(l) != "" {
			return Questions{}, fmt.Errorf("%w: %s line %d is neither \"None\" nor a **Qn. title** question", ErrOpenQuestionsShape, heading, i+1)
		}
	}
	out := Questions{Items: make([]Question, 0, len(blocks))}
	for k, b := range blocks {
		last := end
		if k+1 < len(blocks) {
			last = blocks[k+1].first
		}
		text := strings.TrimRight(strings.Join(lines[b.first:last], "\n"), " \t\n")
		if !hasLabel(text, "Recommended:") {
			return Questions{}, fmt.Errorf("%w: Q%d %q has no Recommended: line; say which answer the document follows", ErrOpenQuestionsShape, b.n, b.title)
		}
		out.Items = append(out.Items, Question{N: b.n, Title: b.title, Text: text})
	}
	return out, nil
}

// questionLine reads a question's first line, `**Qn. title**` with anything after the closing `**`.
func questionLine(l string) (int, string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(l), "**Q")
	if !ok {
		return 0, "", false
	}
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits >= len(rest) || rest[digits] != '.' {
		return 0, "", false
	}
	n, err := strconv.Atoi(rest[:digits])
	if err != nil {
		return 0, "", false
	}
	title, _, ok := strings.Cut(rest[digits+1:], "**")
	if !ok {
		return 0, "", false
	}
	return n, strings.TrimSpace(strings.TrimRight(strings.TrimSpace(title), ".:")), true
}

// hasLabel reports whether a line of text, outside fenced code, starts with label, allowing list or bold markers
// before it, as in "- Options:" or "**Recommended:**".
func hasLabel(text, label string) bool {
	fenced := false
	for _, l := range strings.Split(text, "\n") {
		if fence(l) {
			fenced = !fenced
			continue
		}
		if !fenced && strings.HasPrefix(strings.TrimLeft(l, " \t*_-"), label) {
			return true
		}
	}
	return false
}

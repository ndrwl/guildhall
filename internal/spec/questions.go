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

// QuestionsNone is the open-questions part of an ask (Part) whose document says it has none.
const QuestionsNone = "Open questions: none."

// QuestionsHeadingPrefix starts the heading of an ask's open-questions part, QuestionsHeading.
const QuestionsHeadingPrefix = "**Open questions · "

// QuestionsHeading heads an ask's open-questions part when there are n questions: "**Open questions · n**".
func QuestionsHeading(n int) string { return QuestionsHeadingPrefix + strconv.Itoa(n) + "**" }

// OldQuestionsInFull headed the open-questions part of asks recorded before the part listed each question's options
// (d153). Stored questions are immutable, so the task page still recognises it in an ask's text.
const OldQuestionsInFull = "Open questions in full:"

// ReplyLine ends a plan approval's open-questions part (PlanPart): what approve means when the questions carry
// recommendations, and how to answer one differently.
const ReplyLine = `Reply **approve** to take every recommendation, or name the answers to change, such as "Q2 (a)".`

// Recommend is the chat marker of a question's recommended option, at the start of its line.
const Recommend = "★"

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

	// The fields below are the question read into its parts (Shaped); all are empty when it does not read cleanly,
	// and the question then shows Text word for word.

	// Body is the question's lines between its title and its first Options:, Recommended: or Alternative: line.
	Body string
	// Options are the choices of its `Options: (a) …; (b) ….` line, in letter order from (a).
	Options []Option
	// Recommended is the letter of the option the `Recommended:` line starts with, such as "b" for "(b)", "b)" or
	// "b."; it is always one of Options' letters.
	Recommended string
	// Why is the rest of the Recommended: line after its letter, or on a hallmark question the whole line.
	Why string
	// Alternative is a hallmark question's Alternative: line, which it has in place of options.
	Alternative string
	// More is the question's lines after its labelled lines and a blank line, word for word.
	More string
}

// Option is one choice of a question's Options: line: its letter, such as "a", and its text, inline Markdown.
type Option struct {
	Letter, Text string
}

// Shaped says the question read cleanly into its parts: lettered options with a recommended letter among them, or
// a hallmark question's Recommended: and Alternative: lines.
func (q Question) Shaped() bool { return len(q.Options) > 0 || q.Alternative != "" }

// Lettered says the question has lettered options, one of them recommended.
func (q Question) Lettered() bool { return len(q.Options) > 0 }

// Pick is the recommended option first, then the others in letter order; letters are kept as written, so "Q2 (a)"
// means the same option wherever it shows. It is empty when the question has no lettered options.
func (q Question) Pick() []Option {
	var out []Option
	for _, o := range q.Options {
		if o.Letter == q.Recommended {
			out = append(out, o)
		}
	}
	for _, o := range q.Options {
		if o.Letter != q.Recommended {
			out = append(out, o)
		}
	}
	return out
}

// Markdown is the question as an ask's text writes it: when Shaped, its `**Qn. title**` line, its body, the
// recommended option first as `- ★ **(b) text** · Recommended. why` and the others as `- (a) text`, or on a hallmark
// question `- ★ **Recommended:** …` and `- Alternative: …`, then its other lines word for word; otherwise Text.
func (q Question) Markdown() string {
	if !q.Shaped() {
		return q.Text
	}
	lines := []string{fmt.Sprintf("**Q%d. %s**", q.N, q.Title)}
	if q.Body != "" {
		lines = append(lines, q.Body)
	}
	if q.Lettered() {
		for i, o := range q.Pick() {
			if i == 0 {
				l := fmt.Sprintf("- %s **(%s) %s** · Recommended.", Recommend, o.Letter, o.Text)
				if q.Why != "" {
					l += " " + q.Why
				}
				lines = append(lines, l)
				continue
			}
			lines = append(lines, fmt.Sprintf("- (%s) %s", o.Letter, o.Text))
		}
	} else {
		lines = append(lines, "- "+Recommend+" **Recommended:** "+q.Why, "- Alternative: "+q.Alternative)
	}
	if q.More != "" {
		lines = append(lines, "", q.More)
	}
	return strings.Join(lines, "\n")
}

// Questions is an open-questions section read by OpenQuestions: None when it says "None", otherwise its Items.
type Questions struct {
	None  bool
	Items []Question
}

// Fit is how many of the questions Render(max) keeps whole: the most, from the first, whose blocks (Markdown) joined
// by blank lines fit within max bytes.
func (q Questions) Fit(max int) int {
	n := 0
	for i, it := range q.Items {
		if i > 0 {
			n += 2
		}
		n += len(it.Markdown())
		if n > max {
			return i
		}
	}
	return len(q.Items)
}

// Render joins the questions' blocks (Markdown) with a blank line between them. When they are longer than max bytes
// it keeps the questions that fit, whole (Fit), and ends with CutMarker, so the result never ends mid-question.
func (q Questions) Render(max int) string {
	kept := q.Fit(max)
	blocks := make([]string, 0, kept+1)
	for _, it := range q.Items[:kept] {
		blocks = append(blocks, it.Markdown())
	}
	if kept < len(q.Items) {
		blocks = append(blocks, CutMarker)
	}
	return strings.Join(blocks, "\n\n")
}

// head is the part's heading line and the blank line after it.
func (q Questions) head() string { return QuestionsHeading(len(q.Items)) + "\n\n" }

// Part is the open-questions part an ask shows after the Summary: QuestionsNone, or QuestionsHeading, a blank line
// and Render(QuestionsMax).
func (q Questions) Part() string {
	if q.None {
		return QuestionsNone
	}
	return q.head() + q.Render(QuestionsMax)
}

// PlanPart is a plan approval's part: Part, and after questions a blank line and ReplyLine.
func (q Questions) PlanPart() string {
	if q.None {
		return QuestionsNone
	}
	return q.Part() + "\n\n" + ReplyLine
}

// PartWithin is Part kept within max bytes: the none line, or QuestionsHeading and the questions that fit within both
// max and QuestionsMax, whole, ending with CutMarker when any are left out. ok is false when max leaves no room for
// even the heading and CutMarker, and the ask then leaves the part out.
func (q Questions) PartWithin(max int) (string, bool) {
	if q.None {
		return QuestionsNone, len(QuestionsNone) <= max
	}
	head := q.head()
	if part := q.Part(); len(part) <= max {
		return part, true
	}
	room := max - len(head)
	if room < len(CutMarker) {
		return "", false
	}
	// Render ends a cut with "\n\n" and CutMarker after the questions it kept, so keep that much room for them.
	return head + q.Render(min(room-2-len(CutMarker), QuestionsMax)), true
}

// ReportQuestionsHeading is the heading of a hallmark report's questions: each direction change it asks about, with a
// Recommended: and an Alternative: line, read by OpenQuestions with no Options: rule.
const ReportQuestionsHeading = "## Questions"

// ReportPart reads a hallmark report's `## Questions` section (OpenQuestions with ReportQuestionsHeading), its text
// made valid UTF-8 first, and returns its part kept within max bytes (PartWithin); ok is false when the report has
// no readable section, as with reports written before the section was copied, or max leaves no room, and the ask then
// leaves the part out. The follow-ups question text and the task page both call it, so chat and page show the same
// questions.
func ReportPart(data []byte, max int) (string, bool) {
	q, err := ReportQuestions(data)
	if err != nil {
		return "", false
	}
	return q.PartWithin(max)
}

// ReportQuestions reads a hallmark report's `## Questions` section (OpenQuestions with ReportQuestionsHeading), its
// text made valid UTF-8 first; ReportPart and the task page's cards both read it.
func ReportQuestions(data []byte) (Questions, error) {
	return OpenQuestions([]byte(strings.ToValidUTF8(string(data), "�")), ReportQuestionsHeading)
}

// SpecPart reads a spec's open questions (ReadSpecQuestions) and returns their Part; ok is false when the spec has no
// readable section, as with specs written before the section's shape was checked, and the ask then leaves it out.
// A design ask's question text and the task page both call it, so chat and page show the same text.
func SpecPart(data []byte) (string, bool) {
	q, err := ReadSpecQuestions(data)
	if err != nil {
		return "", false
	}
	return q.Part(), true
}

// PlanPart is SpecPart for a plan approval: the spec's PlanPart, which ends with ReplyLine.
func PlanPart(data []byte) (string, bool) {
	q, err := ReadSpecQuestions(data)
	if err != nil {
		return "", false
	}
	return q.PlanPart(), true
}

// ReadSpecQuestions reads a spec's `## Open questions` section with OpenQuestions and also requires every question
// to have an `Options:` line, the spec template's shape. It is lenient about the Recommended: line, so documents
// recorded before SpecQuestions' letter rule still read, their questions shown word for word.
func ReadSpecQuestions(data []byte) (Questions, error) {
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

// SpecQuestions is ReadSpecQuestions with the plan and design checks' letter rule: every question's Options: line
// splits into lettered options `(a) …; (b) …`, and its Recommended: line starts with one of their letters, so its
// recommended option can be marked. Only that rule refuses; a question that keeps the rule but does not shape for
// another reason, such as a fenced block, is accepted and shows word for word.
func SpecQuestions(data []byte) (Questions, error) {
	q, err := ReadSpecQuestions(data)
	if err != nil {
		return Questions{}, err
	}
	for _, it := range q.Items {
		if reason := letterRule(it.Text); reason != "" {
			return Questions{}, fmt.Errorf("%w: Q%d %q: %s; write \"Options: (a) …; (b) ….\" and \"Recommended: (<letter>). <why>\"", ErrOpenQuestionsShape, it.N, it.Title, reason)
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
		out.Items = append(out.Items, shape(Question{N: b.n, Title: b.title, Text: text}))
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

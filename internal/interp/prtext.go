package interp

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// maxBodyBytes caps a pull request body publish writes, so the title, the body and gh's other arguments fit in
// Windows's 32,767-unit command line and well under GitHub's own body limit.
const maxBodyBytes = 24000

// truncated marks text cut to fit the body.
const truncated = "… (truncated)"

// fixLine is the sentence rework (C5) and change failure (E2) count from (docs/quality.md); only task.Fixes writes it.
const fixLine = "Fixes a defect introduced by task"

// validationLine is what the body says of validation, which runs after publish.
const validationLine = "Validation runs after publish; its result is in this pull request's checks."

// prText is what a pull request's title and body are made from: records Guildhall already holds.
type prText struct {
	// Spec is the accepted spec's text, SpecName its document name, SpecPath its path in the document directory
	// and SpecSHA256 its digest; all empty when the step was given no spec.
	Spec, SpecName, SpecPath, SpecSHA256 string
	// Summary is the build attempt's result summary, empty when it left none that reads.
	Summary string
	// Outcomes are the review outcomes the step was given; only those bound to Head are told.
	Outcomes []workflow.ReviewOutcome
	// Rulings are the Director's rulings on the task's findings, in the order the records hold them.
	Rulings []store.Ruling
	Task    store.Task
	// Step is the publish step's id and Head the head it publishes.
	Step, Head string
}

// pullRequestText returns the title and body of the pull request for p. It is deterministic: the same p gives
// the same text, so a replayed publish asks the forge for no edit. Model-written text is neutralised before it is
// placed, and the body is cut to maxBodyBytes, cutting the inlined spec first, then the build summary, then the
// review list; the Guildhall section's names, head and fix line are never cut.
func pullRequestText(p prText) (title, body string) {
	spec := strings.ReplaceAll(p.Spec, "\r\n", "\n")
	parts := parseSpec(spec)

	title = parts.title
	if title == "" {
		title = strings.TrimSpace(p.Task.Title)
	}
	if title == "" {
		title = p.Task.ID
	}
	title = capRunes(strings.Join(strings.Fields(neutralise(title)), " "), forge.MaxTitle)

	summary := parts.goal
	if summary == "" {
		summary = parts.first
	}
	if summary == "" {
		summary = "No accepted spec was given to this step."
		if p.SpecName != "" {
			summary = "The accepted spec has no goal to quote."
		}
	}
	changed := strings.TrimSpace(p.Summary)
	if changed == "" {
		changed = "The build left no summary."
	}
	tested := validationLine
	if parts.tested != "" {
		tested = quote(neutralise(parts.tested)) + "\n\n" + tested
	}

	// The body's pieces, in order; the pieces named below are the ones that may be cut.
	pieces := []string{
		"## Summary\n\n", neutralise(summary), "\n\n",
		"## What changed\n\n", neutralise(changed), "\n\n",
		"## How it was tested\n\n", tested, "\n\n",
		"## Review\n\n", neutralise(review(p)), "\n\n",
		"## Guildhall\n\n", guildhall(p),
		"", "", "", // the inlined spec
		"", // the fix line
	}
	const summaryPiece, changedPiece, testedPiece, reviewPiece, specPiece, fixPiece = 1, 4, 7, 10, 15, 17
	if p.SpecName != "" {
		pieces[specPiece-1] = "\n<details><summary>Accepted spec</summary>\n\n"
		pieces[specPiece] = strings.TrimSpace(neutralise(spec))
		pieces[specPiece+1] = "\n\n</details>\n"
	}
	if f := p.Task.Fixes; f != nil {
		pieces[fixPiece] = fmt.Sprintf("\n%s `%s/%s`.\n", fixLine, f.Repository, f.ID)
	}
	// The goal and test sections are cut last, only when a spec has made them too long on their own.
	for _, i := range []int{specPiece, changedPiece, reviewPiece, testedPiece, summaryPiece} {
		over := len(strings.Join(pieces, "")) - maxBodyBytes
		if over <= 0 {
			break
		}
		pieces[i] = cut(pieces[i], len(pieces[i])-over)
	}
	return title, strings.Join(pieces, "")
}

// specParts are the parts of an accepted spec the text quotes: its title, its goal section, its first paragraph
// after the title and its first section about testing, each empty when the spec has none.
type specParts struct {
	title, goal, first, tested string
}

// heading reads a Markdown ATX heading: its level and its text, or level 0 when line is not one.
func heading(line string) (int, string) {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n == len(line) || line[n] != ' ' && line[n] != '\t' {
		return 0, ""
	}
	return n, strings.TrimSpace(strings.TrimRight(strings.TrimSpace(line[n:]), "#"))
}

// parseSpec finds the parts of spec, skipping fenced code blocks.
func parseSpec(spec string) specParts {
	var out specParts
	lines := strings.Split(spec, "\n")
	levels := make([]int, len(lines))
	texts := make([]string, len(lines))
	fenced := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fenced = !fenced
			continue
		}
		if !fenced {
			levels[i], texts[i] = heading(l)
		}
	}
	// section returns the trimmed text under the heading at line i, up to the next heading of its level or above.
	section := func(i int) string {
		end := i + 1
		for end < len(lines) && (levels[end] == 0 || levels[end] > levels[i]) {
			end++
		}
		return strings.TrimSpace(strings.Join(lines[i+1:end], "\n"))
	}
	start := 0
	for i, n := range levels {
		if n == 1 {
			out.title, start = texts[i], i+1
			break
		}
	}
	// The first paragraph after the title: the first run of non-blank lines that are not headings.
	for i := start; i < len(lines) && out.first == ""; i++ {
		if strings.TrimSpace(lines[i]) == "" || levels[i] > 0 {
			continue
		}
		end := i
		for end < len(lines) && strings.TrimSpace(lines[end]) != "" && levels[end] == 0 {
			end++
		}
		out.first = strings.TrimSpace(strings.Join(lines[i:end], "\n"))
	}
	for i, n := range levels {
		switch {
		case n < 2:
		case out.goal == "" && strings.EqualFold(texts[i], "goal"):
			out.goal = section(i)
		case out.tested == "" && strings.Contains(strings.ToLower(texts[i]), "test"):
			out.tested = section(i)
		}
	}
	return out
}

// review is the Review section's text: each outcome given that is bound to the head, with its findings and
// dispositions, then the Director's rulings.
func review(p prText) string {
	var b strings.Builder
	for _, o := range p.Outcomes {
		if o.Revisions.Subject.CodeHead != p.Head {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Reviewer: %s. Verdict: %s.\n", o.Reviewer, o.Verdict)
		listed := map[string]bool{}
		for _, f := range o.Findings {
			listed[f.ID] = true
			var where []string
			if f.Rule != "" {
				where = append(where, shortText(f.Rule, 1024))
			}
			if f.File != "" {
				loc := shortText(f.File, 256)
				if f.Line > 0 {
					loc += fmt.Sprintf(":%d", f.Line)
				}
				where = append(where, loc)
			}
			line := "- " + f.ID
			if len(where) > 0 {
				line += " (" + strings.Join(where, ", ") + ")"
			}
			line += ": " + oneLine(shortText(f.Summary, 512))
			for _, d := range o.Dispositions {
				if d.Finding == f.ID {
					line += " [" + string(d.State) + "]"
				}
			}
			b.WriteString(line + "\n")
		}
		for _, d := range o.Dispositions {
			if !listed[d.Finding] {
				fmt.Fprintf(&b, "- %s: %s\n", d.Finding, d.State)
			}
		}
	}
	if b.Len() == 0 {
		b.WriteString("No review outcome for this head.\n")
	}
	if len(p.Rulings) > 0 {
		b.WriteString("\nDirector rulings:\n")
	}
	for _, r := range p.Rulings {
		line := fmt.Sprintf("- Finding %s: ruled %s: %s", r.Finding, r.Move, oneLine(shortText(r.Reason, 512)))
		if r.ContestedBy != "" {
			line += fmt.Sprintf(" (contested by %s)", r.ContestedBy)
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimSpace(b.String())
}

// guildhall is the Guildhall section's list: the task, the step, the head and the accepted spec, by name and digest.
func guildhall(p prText) string {
	spec := "none was given to this step"
	if p.SpecName != "" {
		spec = fmt.Sprintf("`%s` (`%s`), sha256:%s", p.SpecName, p.SpecPath, p.SpecSHA256)
	}
	return fmt.Sprintf("- Task: `%s/%s`\n- Step: `%s`\n- Head: `%s`\n- Accepted spec: %s\n", p.Task.Repository, p.Task.ID, p.Step, p.Head, spec)
}

var (
	// mention is an @ that GitHub could read as a mention of a user or team.
	mention = regexp.MustCompile(`@([A-Za-z0-9])`)
	// closing is a GitHub closing keyword followed by an issue or pull request reference.
	closing = regexp.MustCompile(`(?i)\b(clos(?:e|es|ed)|fix(?:es|ed)?|resolv(?:e|es|ed))(:?[ \t]+)((?:[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)?#[0-9]+|https?://[^\s]+/(?:issues|pull)/[0-9]+)`)
)

// wordJoiner is an invisible character that breaks a mention or keyword without changing how it reads.
const wordJoiner = string(rune(0x2060))

// neutralise makes model-written text safe to place in a pull request: mentions notify nobody, closing keywords
// close nothing on merge, and no line can pass for the fix line only task.Fixes writes.
func neutralise(text string) string {
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if !strings.Contains(l, fixLine) {
			kept = append(kept, l)
		}
	}
	text = strings.Join(kept, "\n")
	text = mention.ReplaceAllString(text, "@"+wordJoiner+"$1")
	return closing.ReplaceAllStringFunc(text, func(m string) string {
		_, size := utf8.DecodeRuneInString(m)
		return m[:size] + wordJoiner + m[size:]
	})
}

// quote quotes text as a Markdown block quote.
func quote(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight("> "+l, " ")
	}
	return strings.Join(lines, "\n")
}

// oneLine joins text's lines, so it stays on one list item.
func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }

// capRunes cuts text to at most n runes, ending it with … when it was cut.
func capRunes(text string, n int) string {
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	r := []rune(text)
	return string(r[:n-1]) + "…"
}

// cut cuts text to at most n bytes on a rune boundary, marking the cut with truncated; text that fits is unchanged.
func cut(text string, n int) string {
	if len(text) <= n || text == "" {
		return text
	}
	n = max(0, n-len(truncated)-1)
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n] + "\n" + truncated
}

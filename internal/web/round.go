package web

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// This file holds a grill round's text: the questions the Director puts on the page (ParseRound, in the
// guildhall-grill skill's format) and the answers the Guildmaster sends (RoundAnswersText, ParseRoundAnswers), so the
// page, grill round open and grill round show read them the same way.

// MaxRoundQuestions bounds a round: a round that asks more is better split, and the page's form stays one screenful
// of cards per question.
const MaxRoundQuestions = 30

// RoundQuestion is one question of a grill round: its number from 1, its title, its Markdown body ("" for none) and
// the answer the Director recommends.
type RoundQuestion struct {
	N                        int
	Title, Body, Recommended string
}

// roundHead is a question's first line, **Q<n>. <title>**.
var roundHead = regexp.MustCompile(`^\*\*Q([0-9]+)\. (.+)\*\*$`)

// recommendedPrefix starts a question's last line, its recommended answer.
const recommendedPrefix = "Recommended: "

// ParseRound reads a grill round in the guildhall-grill format: one block per question, numbered from Q1 in order,
// each a **Q<n>. <title>** line, its body and a last line "Recommended: <answer>", with nothing before the first
// question. It refuses any other text, so the round on the page is the round the Director wrote.
func ParseRound(text string) ([]RoundQuestion, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []RoundQuestion
	var body []string
	finish := func() error {
		if len(out) == 0 {
			return nil
		}
		q := &out[len(out)-1]
		for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
			body = body[:len(body)-1]
		}
		if len(body) == 0 || !strings.HasPrefix(body[len(body)-1], recommendedPrefix) {
			return fmt.Errorf("question Q%d has no last line %q giving your recommended answer", q.N, recommendedPrefix+"<answer>")
		}
		q.Recommended = strings.TrimSpace(strings.TrimPrefix(body[len(body)-1], recommendedPrefix))
		if q.Recommended == "" {
			return fmt.Errorf("question Q%d recommends nothing", q.N)
		}
		q.Body = strings.TrimSpace(strings.Join(body[:len(body)-1], "\n"))
		body = nil
		return nil
	}
	fence := false
	for _, l := range lines {
		if strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~") {
			fence = !fence
		}
		m := roundHead.FindStringSubmatch(strings.TrimRight(l, " \t"))
		if fence || m == nil {
			if len(out) == 0 && strings.TrimSpace(l) != "" {
				return nil, errors.New("a round starts with its first question, **Q1. <title>**")
			}
			body = append(body, l)
			continue
		}
		if err := finish(); err != nil {
			return nil, err
		}
		n, _ := strconv.Atoi(m[1])
		if n != len(out)+1 {
			return nil, fmt.Errorf("question %q is numbered Q%d; the questions are numbered from Q1 in order", m[2], n)
		}
		out = append(out, RoundQuestion{N: n, Title: strings.TrimSpace(m[2])})
	}
	if err := finish(); err != nil {
		return nil, err
	}
	switch {
	case len(out) == 0:
		return nil, errors.New("a round asks at least one question, **Q1. <title>**")
	case len(out) > MaxRoundQuestions:
		return nil, fmt.Errorf("a round asks at most %d questions, not %d; ask the rest in the next round", MaxRoundQuestions, len(out))
	}
	return out, nil
}

// RoundAnswer is the Guildmaster's answer to one question of a round: Accepted says they accepted its recommendation,
// and Words are their words, the answer itself when they did not accept, or words given with the acceptance. On a
// plan approval's open question, Pick is the letter of the option picked on its card as the answer, with any words
// after it.
type RoundAnswer struct {
	Accepted bool
	Words    string
	Pick     string
}

// The lines a question's answer starts with in a round's answers.
const (
	acceptedPrefix = "Accepted: "
	wordsLine      = "Your words:"
)

// RoundAnswersText writes a round's answers, one per question in order, as the Markdown a round-answers entry
// holds and grill round show prints: each question's head line, then "Accepted: <recommendation>", or "Your
// words:", then the words.
func RoundAnswersText(qs []RoundQuestion, answers []RoundAnswer) string {
	var b strings.Builder
	for i, q := range qs {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "**Q%d. %s**\n", q.N, q.Title)
		a := answers[i]
		words := strings.TrimSpace(a.Words)
		switch {
		case a.Accepted && words == "":
			b.WriteString(acceptedPrefix + q.Recommended)
		case a.Accepted:
			b.WriteString(acceptedPrefix + q.Recommended + "\n\n" + words)
		default:
			b.WriteString(wordsLine + "\n\n" + words)
		}
	}
	return b.String()
}

// ParseRoundAnswers reads the answers RoundAnswersText wrote for qs, one per question; a question whose block it
// cannot find, as in an entry not written by it, is read as words "".
func ParseRoundAnswers(qs []RoundQuestion, text string) []RoundAnswer {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	out := make([]RoundAnswer, len(qs))
	// Each block runs from its question's head to the next question's head. The heads are found from the last
	// back, each the last before the next, so words that quote another question's head stay in their block.
	heads := make([]int, len(qs)+1)
	bodies := make([]int, len(qs))
	heads[len(qs)] = len(text)
	for i := len(qs) - 1; i >= 0; i-- {
		head := fmt.Sprintf("**Q%d. %s**\n", qs[i].N, qs[i].Title)
		j := strings.LastIndex(text[:heads[i+1]], head)
		if j < 0 {
			return out
		}
		heads[i], bodies[i] = j, j+len(head)
	}
	for i, q := range qs {
		block := strings.TrimSpace(text[bodies[i]:heads[i+1]])
		// The words line goes first, so words that begin with the accepted line stay words.
		if rest, ok := strings.CutPrefix(block, wordsLine); ok {
			out[i] = RoundAnswer{Words: strings.TrimSpace(rest)}
			continue
		}
		if rest, ok := strings.CutPrefix(block, acceptedPrefix+q.Recommended); ok {
			out[i] = RoundAnswer{Accepted: true, Words: strings.TrimSpace(rest)}
			continue
		}
		out[i] = RoundAnswer{Words: strings.TrimSpace(strings.TrimPrefix(block, wordsLine))}
	}
	return out
}

// ParseGivenAnswers reads the answers the Guildmaster gave to a round's questions qs outside the page, in chat, as
// the Director writes them for grill round answer: one block per question, numbered from Q1 in order like the round,
// each a **Q<n>. <title>** line (the title is the Director's copy and is not checked), then either a line "Accepted"
// or "Accepted: <the recommendation>", with any words given with it after, or the Guildmaster's words, after an
// optional "Your words:" line. The text RoundAnswersText writes reads back as the answers it wrote. It refuses a
// block count other than the round's, a question with no answer and an Accepted line naming another answer than the
// recommendation.
func ParseGivenAnswers(qs []RoundQuestion, text string) ([]RoundAnswer, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var blocks [][]string
	fence := false
	for _, l := range lines {
		if strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~") {
			fence = !fence
		}
		m := givenHead.FindStringSubmatch(strings.TrimRight(l, " \t"))
		if fence || m == nil {
			if len(blocks) == 0 {
				if strings.TrimSpace(l) != "" {
					return nil, errors.New("the answers start with the first question's line, **Q1. <title>**")
				}
				continue
			}
			blocks[len(blocks)-1] = append(blocks[len(blocks)-1], l)
			continue
		}
		if n, _ := strconv.Atoi(m[1]); n != len(blocks)+1 {
			return nil, fmt.Errorf("an answer is headed Q%d; the answers are numbered from Q1 in order, as the round's questions are", n)
		}
		blocks = append(blocks, nil)
	}
	if len(blocks) != len(qs) {
		return nil, fmt.Errorf("the answers answer %d %s; the round asks %d", len(blocks), plural(len(blocks), "question", "questions"), len(qs))
	}
	out := make([]RoundAnswer, len(qs))
	for i, q := range qs {
		block := strings.TrimSpace(strings.Join(blocks[i], "\n"))
		first, rest, _ := strings.Cut(block, "\n")
		first = strings.TrimSpace(first)
		if named, ok := strings.CutPrefix(first, "Accepted"); ok && (named == "" || named == "." || strings.HasPrefix(named, ":")) {
			named = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(named, "."), ":"))
			if named != "" && named != q.Recommended {
				return nil, fmt.Errorf("the answer to Q%d is Accepted: %q, but the round recommends %q; give an answer other than the recommendation as the Guildmaster's words", q.N, named, q.Recommended)
			}
			out[i] = RoundAnswer{Accepted: true, Words: strings.TrimSpace(rest)}
			continue
		}
		if first == wordsLine {
			block = rest
		}
		words := strings.TrimSpace(block)
		if words == "" {
			return nil, fmt.Errorf("question Q%d has no answer: write Accepted, or the Guildmaster's words", q.N)
		}
		out[i] = RoundAnswer{Words: words}
	}
	return out, nil
}

// givenHead is an answer's first line in ParseGivenAnswers, **Q<n>. <title>** with the title not checked.
var givenHead = regexp.MustCompile(`^\*\*Q([0-9]+)\.(.*)\*\*$`)

// RoundTally says how a round's answers went, such as "3 accepted, 1 your words".
func RoundTally(answers []RoundAnswer) string {
	accepted := 0
	for _, a := range answers {
		if a.Accepted {
			accepted++
		}
	}
	var parts []string
	if accepted > 0 {
		parts = append(parts, strconv.Itoa(accepted)+" accepted")
	}
	if words := len(answers) - accepted; words > 0 {
		parts = append(parts, strconv.Itoa(words)+" your words")
	}
	return strings.Join(parts, ", ")
}

// RoundID is the message id of the grill-answered message for one round, the conversation entry number of the
// round, sent once since a round is sent once.
func RoundID(repo, task string, round int) string {
	return "page-round." + AnsweredID(repo, task, "round:"+strconv.Itoa(round))[len("page-answered."):]
}

// Unanswered names the questions a round still waits on for the refusal Send round gives, such as "Q9 and Q10".
func Unanswered(ns []int) string {
	names := make([]string, len(ns))
	for i, n := range ns {
		names[i] = "Q" + strconv.Itoa(n)
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

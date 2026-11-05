package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// grillSummary runs `guildhall grill summary open|show` (docs/web.md#grill-rounds): the grill's closing summary,
// put to the Guildmaster on the task page to Confirm or Request changes, and its answer read back.
func (a *App) grillSummary(sub string, args []string) int {
	name := "grill summary " + sub
	usage := func(msg string) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s\n%s\n", msg, grillUsage)
		return 2
	}
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	path := fl.String("store", "", "store file")
	var cp copyFlags
	cp.register(fl)
	repo := fl.String("repo", "", "repository id")
	task := fl.String("task", "", "task id")
	attempt := fl.String("attempt", "", "the Director stage attempt, such as grill-2")
	file := fl.String("summary-file", "", "the summary, or - for standard input")
	k := fl.Int("summary", 0, "the attempt's summary to show, from 1")
	if err := fl.Parse(args); err != nil {
		return usage(err.Error())
	}
	switch err := cp.apply(a, path, nil, nil); {
	case fl.NArg() > 0:
		return usage(name + " takes no arguments")
	case err != nil:
		return usage(err.Error())
	case cp.searched:
		return usage(cp.missing(a).Error())
	case *path == "" || *repo == "" || *task == "" || *attempt == "":
		return usage(name + " requires --store, --repo, --task and --attempt")
	case !workflow.ValidID(*repo) || !workflow.ValidID(*task) || !workflow.ValidID(*attempt):
		return usage("--repo, --task and --attempt must be ids: lowercase letters, digits and hyphens, starting with a letter")
	case sub == "open" && *file == "":
		return usage(name + " requires --summary-file")
	case sub == "open" && *k != 0:
		return usage("--summary goes with summary show")
	case sub == "show" && *file != "":
		return usage("--summary-file goes with summary open")
	case *k < 0:
		return usage("--summary counts the attempt's summaries from 1")
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s: %v\n", name, err)
		return 1
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(*path))
	}
	if sub == "show" {
		out, err := withStore(*path, true, func(s *store.Store) (string, error) {
			return showSummary(s, *repo, *task, *attempt, *k)
		})
		if err != nil {
			return fail(err)
		}
		if _, err := fmt.Fprint(a.Stdout, out); err != nil {
			return 1
		}
		return 0
	}
	text, err := a.readText("summary", *file, store.MaxTextBytes)
	if err != nil {
		return fail(err)
	}
	rc, err := withStore(*path, false, func(s *store.Store) (store.Receipt, error) {
		// The id counts the attempt's answered summaries, so a rerun before this summary is answered replays its
		// receipt and a revised summary after Request changes, even one reading the same, is a new summary. A talk
		// that does not read counts none; checkAskOpen gives the refusal.
		talk, _ := s.Conversation(*repo, *task)
		mutation := mutationID("grill-summary", *repo, *task, *attempt, strconv.Itoa(answeredSummaries(talk, *attempt)), digest.Of([]byte(text)).Hex())
		if err := checkAskOpen(s, *repo, *task, *attempt, mutation, "the summary"); err != nil {
			return store.Receipt{}, err
		}
		return s.AddConversation(mutation, *repo, *task, store.Saying{Author: store.ConversationDirector, Tag: *attempt, Kind: store.ConversationSummary, Text: text})
	})
	if err != nil {
		return fail(err)
	}
	if err := json.NewEncoder(a.Stdout).Encode(rc); err != nil {
		return 1
	}
	return 0
}

// latestSummary is the last summary entry of talk under tag.
func latestSummary(talk []store.ConversationEntry, tag string) (store.ConversationEntry, bool) {
	var last store.ConversationEntry
	found := false
	for _, e := range talk {
		if e.Tag == tag && e.Kind == store.ConversationSummary {
			last, found = e, true
		}
	}
	return last, found
}

// answeredSummaries counts tag's summaries that a summary-answer entry answers.
func answeredSummaries(talk []store.ConversationEntry, tag string) int {
	k := 0
	for _, e := range talk {
		if e.Tag == tag && e.Kind == store.ConversationSummary && summaryAnswered(talk, e.Number) {
			k++
		}
	}
	return k
}

// summaryAnswered says a summary-answer entry answers summary number n.
func summaryAnswered(talk []store.ConversationEntry, n int) bool {
	_, ok := summaryAnswer(talk, n)
	return ok
}

// summaryAnswer is the first summary-answer entry answering summary number n: a summary is answered once.
func summaryAnswer(talk []store.ConversationEntry, n int) (store.ConversationEntry, bool) {
	for _, e := range talk {
		if e.Kind == store.ConversationSummaryAnswer && e.Round == n {
			return e, true
		}
	}
	return store.ConversationEntry{}, false
}

// summaryOrdinal is summary's place among its tag's summaries, from 1.
func summaryOrdinal(talk []store.ConversationEntry, summary store.ConversationEntry) int {
	k := 0
	for _, e := range talk {
		if e.Tag == summary.Tag && e.Kind == store.ConversationSummary && e.Number <= summary.Number {
			k++
		}
	}
	return k
}

// showSummary is summary k of tag, or with k 0 its latest answered summary, with its outcome, time and the
// Guildmaster's words, as Markdown.
func showSummary(s *store.Store, repo, task, tag string, k int) (string, error) {
	if _, err := s.Task(repo, task); err != nil {
		return "", err
	}
	talk, err := s.Conversation(repo, task)
	if err != nil {
		return "", err
	}
	var pick *store.ConversationEntry
	n := 0
	for i := range talk {
		e := &talk[i]
		if e.Tag != tag || e.Kind != store.ConversationSummary {
			continue
		}
		n++
		if (k == 0 && summaryAnswered(talk, e.Number)) || n == k {
			pick = e
		}
	}
	switch {
	case pick == nil && k > 0:
		return "", fmt.Errorf("%w: %s of %s/%s has %d %s, not a summary %d", store.ErrNotFound, tag, repo, task, n, plural(n, "summary", "summaries"), k)
	case pick == nil:
		return "", fmt.Errorf("%w: no grill summary of %s of %s/%s is answered yet", store.ErrNotFound, tag, repo, task)
	}
	k = summaryOrdinal(talk, *pick)
	summary := strings.TrimSpace(pick.Text)
	a, answered := summaryAnswer(talk, pick.Number)
	if !answered {
		return fmt.Sprintf("# Grill summary %d of %s · not answered yet\n\n%s\n", k, tag, summary), nil
	}
	words := web.SummaryAnswerWords(a.Option, a.Text)
	if words == "" {
		words = "None."
	}
	return fmt.Sprintf("# Grill summary %d of %s · %s · %s\n\n%s\n\n## The Guildmaster's words\n\n%s\n", k, tag, web.SummaryOutcome(a.Option),
		a.At.UTC().Format("2006-01-02T15:04:05Z"), summary, words), nil
}

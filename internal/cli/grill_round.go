package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const grillUsage = `usage: guildhall grill round open --store <path> --repo <id> --task <id> --attempt <step-n> --questions-file <path|->
       guildhall grill round show --store <path> --repo <id> --task <id> --attempt <step-n> [--round <k>]
--copy <dir>, or the copy found from the current directory, may stand in for --store: the copy's director.store
round open puts a grill round on the task page, under the running Director stage attempt --attempt names, such as
grill-2: the questions file is the round in the guildhall-grill format, each question a **Q<n>. <title>** line, its
body and a last line "Recommended: <answer>", numbered from Q1; - reads it from standard input. The page asks it as
one form, and the Guildmaster's Send round sends a grill-answered message. A new round opens only once the
attempt's previous round is sent. Running the same command again prints the original receipt and writes nothing.
round show prints the attempt's latest sent round, or its round k, with the Guildmaster's answers, as Markdown.`

// grill runs `guildhall grill round open|show` (docs/web.md#grill-rounds). It exits 0 on success, 1 when the store
// refuses or fails and 2 on a usage error.
func (a *App) grill(args []string) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		if _, err := fmt.Fprintln(a.Stdout, grillUsage); err != nil {
			return 1
		}
		return 0
	}
	if len(args) < 2 || args[0] != "round" || (args[1] != "open" && args[1] != "show") {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: grill takes round open or round show\n%s\n", grillUsage)
		return 2
	}
	return a.grillRound(args[1], args[2:])
}

func (a *App) grillRound(sub string, args []string) int {
	name := "grill round " + sub
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
	questions := fl.String("questions-file", "", "the round's questions, or - for standard input")
	round := fl.Int("round", 0, "the attempt's round to show, from 1")
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
	case sub == "open" && *questions == "":
		return usage(name + " requires --questions-file")
	case sub == "open" && *round != 0:
		return usage("--round goes with round show")
	case sub == "show" && *questions != "":
		return usage("--questions-file goes with round open")
	case *round < 0:
		return usage("--round counts the attempt's rounds from 1")
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
			return showRound(s, *repo, *task, *attempt, *round)
		})
		if err != nil {
			return fail(err)
		}
		if _, err := fmt.Fprint(a.Stdout, out); err != nil {
			return 1
		}
		return 0
	}
	text, err := a.readText("questions", *questions, store.MaxTextBytes)
	if err != nil {
		return fail(err)
	}
	if _, err := web.ParseRound(text); err != nil {
		return fail(fmt.Errorf("%w: the questions are not a round: %v", store.ErrInvalid, err))
	}
	rc, err := withStore(*path, false, func(s *store.Store) (store.Receipt, error) {
		// The id counts the attempt's sent rounds, so a rerun before this round is sent replays its receipt and a
		// later round asking the same questions as a sent one is a new round. A talk that does not read counts
		// none; checkRoundOpen gives the refusal.
		talk, _ := s.Conversation(*repo, *task)
		mutation := mutationID("grill-round", *repo, *task, *attempt, strconv.Itoa(sentRounds(talk, *attempt)), digest.Of([]byte(text)).Hex())
		if err := checkRoundOpen(s, *repo, *task, *attempt, mutation); err != nil {
			return store.Receipt{}, err
		}
		return s.AddConversation(mutation, *repo, *task, store.Saying{Author: store.ConversationDirector, Tag: *attempt, Kind: store.ConversationRound, Text: text})
	})
	if err != nil {
		return fail(err)
	}
	if err := json.NewEncoder(a.Stdout).Encode(rc); err != nil {
		return 1
	}
	return 0
}

// checkRoundOpen refuses a round unless tag names a running Director stage attempt of the open task repo/task and
// the attempt's previous round, if any, is sent. A rerun of the round already opened under mutation passes, so it
// replays its receipt.
func checkRoundOpen(s *store.Store, repo, task, tag, mutation string) error {
	if r, err := s.Receipt(mutation); err == nil && r.Repository == repo && r.TaskID == task {
		return nil
	}
	t, err := s.Task(repo, task)
	if err != nil {
		return err
	}
	if t.State != store.TaskOpen {
		return fmt.Errorf("%w: task %s/%s is %s; its talk takes nothing new", store.ErrInvalid, repo, task, t.State)
	}
	if err := runningDirectorAttempt(s, repo, task, tag); err != nil {
		return err
	}
	talk, err := s.Conversation(repo, task)
	if err != nil {
		return err
	}
	if last, ok := latestRound(talk, tag); ok && !roundSent(talk, last.Number) {
		return fmt.Errorf("%w: round %d of %s is not sent yet; a new round opens once the Guildmaster sends it", store.ErrInvalid, roundOrdinal(talk, last), tag)
	}
	return nil
}

// runningDirectorAttempt refuses tag unless it names a running Director stage attempt, such as grill-2, of the task.
func runningDirectorAttempt(s *store.Store, repo, task, tag string) error {
	attempts, err := s.Attempts(repo, task)
	if err != nil {
		return err
	}
	for _, at := range attempts {
		if interp.DecisionID(at.Step, at.Number, false) != tag {
			continue
		}
		switch {
		case !at.Director:
			return fmt.Errorf("%w: attempt %s of %s/%s is an Artisan's; a round goes under a Director stage, such as a grill", store.ErrInvalid, tag, repo, task)
		case at.Outcome != "":
			return fmt.Errorf("%w: attempt %s of %s/%s has finished (%s); a round goes under the running attempt", store.ErrInvalid, tag, repo, task, at.Outcome)
		}
		return nil
	}
	return fmt.Errorf("%w: task %s/%s has no attempt %q; name the running grill attempt, such as grill-1", store.ErrInvalid, repo, task, tag)
}

// latestRound is the last round entry of talk under tag.
func latestRound(talk []store.ConversationEntry, tag string) (store.ConversationEntry, bool) {
	var last store.ConversationEntry
	found := false
	for _, e := range talk {
		if e.Tag == tag && e.Kind == store.ConversationRound {
			last, found = e, true
		}
	}
	return last, found
}

// sentRounds counts tag's rounds that a round-answers entry answers.
func sentRounds(talk []store.ConversationEntry, tag string) int {
	k := 0
	for _, e := range talk {
		if e.Tag == tag && e.Kind == store.ConversationRound && roundSent(talk, e.Number) {
			k++
		}
	}
	return k
}

// roundSent says a round-answers entry answers round number n.
func roundSent(talk []store.ConversationEntry, n int) bool {
	_, ok := roundAnswers(talk, n)
	return ok
}

func roundAnswers(talk []store.ConversationEntry, n int) (store.ConversationEntry, bool) {
	for _, e := range talk {
		if e.Kind == store.ConversationRoundAnswers && e.Round == n {
			return e, true
		}
	}
	return store.ConversationEntry{}, false
}

// roundOrdinal is round's place among its tag's rounds, from 1.
func roundOrdinal(talk []store.ConversationEntry, round store.ConversationEntry) int {
	k := 0
	for _, e := range talk {
		if e.Tag == round.Tag && e.Kind == store.ConversationRound && e.Number <= round.Number {
			k++
		}
	}
	return k
}

// showRound is round k of tag, or with k 0 its latest sent round, with the Guildmaster's answers, as Markdown.
func showRound(s *store.Store, repo, task, tag string, k int) (string, error) {
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
		if e.Tag != tag || e.Kind != store.ConversationRound {
			continue
		}
		n++
		if (k == 0 && roundSent(talk, e.Number)) || n == k {
			pick = e
		}
	}
	switch {
	case pick == nil && k > 0:
		return "", fmt.Errorf("%w: %s of %s/%s has %d rounds, not a round %d", store.ErrNotFound, tag, repo, task, n, k)
	case pick == nil:
		return "", fmt.Errorf("%w: no round of %s of %s/%s is sent yet", store.ErrNotFound, tag, repo, task)
	}
	k = roundOrdinal(talk, *pick)
	qs, err := web.ParseRound(pick.Text)
	if err != nil {
		return "", fmt.Errorf("round %d of %s does not read: %w", k, tag, err)
	}
	a, sent := roundAnswers(talk, pick.Number)
	if !sent {
		return fmt.Sprintf("# Round %d of %s · %d questions · not sent yet\n\n%s\n", k, tag, len(qs), strings.TrimSpace(pick.Text)), nil
	}
	answers := web.ParseRoundAnswers(qs, a.Text)
	return fmt.Sprintf("# Round %d of %s · %d questions · sent %s · %s\n\n%s\n", k, tag, len(qs), a.At.UTC().Format("2006-01-02T15:04:05Z"),
		web.RoundTally(answers), strings.TrimSpace(a.Text)), nil
}

// Round implements web.Answerer: every box the form showed is saved as a draft of its question first, in one open
// of the store with the rest, so no press loses words; then the button's mark is saved, or Send round records the
// answers and the grill-answered message in one transaction and wakes the Director.
func (w webAnswerer) Round(p web.RoundPost) error {
	boxes := map[int]string{}
	for n, text := range p.Boxes {
		t, err := normaliseText(fmt.Sprintf("your words on Q%d", n), []byte(text), store.MaxTextBytes)
		if err != nil {
			return web.Refuse(http.StatusRequestEntityTooLarge, err.Error())
		}
		if strings.TrimSpace(t) == "" {
			t = ""
		}
		boxes[n] = t
	}
	sent, err := withRecords(storeRecords{path: w.c.store, wait: webLockWait}, false, nil, func(s *store.Store) (string, error) {
		return pageRound(s, p, boxes)
	})
	if err != nil {
		return webBusy(err)
	}
	if sent != "" {
		w.wake(sent)
	}
	return nil
}

// pageRound does one press of a round's button in s, returning the id of the grill-answered message it sent, "" for
// any other press and a replayed Send round.
func pageRound(s *store.Store, p web.RoundPost, boxes map[int]string) (string, error) {
	t, err := s.Task(p.Repository, p.TaskID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "", web.Refuse(http.StatusNotFound, "there is no task "+p.Repository+"/"+p.TaskID)
	case err != nil:
		return "", err
	}
	talk, err := s.Conversation(p.Repository, p.TaskID)
	if err != nil {
		return "", err
	}
	var round *store.ConversationEntry
	for i := range talk {
		if e := &talk[i]; e.Number == p.Round && e.Kind == store.ConversationRound && e.Tag == p.Tag {
			round = e
		}
	}
	if round == nil {
		return "", web.Refuse(http.StatusNotFound, "this task has no such round under "+p.Tag)
	}
	if roundSent(talk, round.Number) {
		if p.Act == web.RoundSend {
			// A second Send round of a round already sent replays: it was sent once.
			return "", nil
		}
		return "", web.Refuse(http.StatusConflict, "already sent: this round's answers reached the Director, so nothing more is saved")
	}
	switch last, _ := latestRound(talk, p.Tag); {
	case t.State != store.TaskOpen:
		return "", web.Refuse(http.StatusConflict, "this task is closed; its round was never sent and takes nothing new")
	case last.Number != round.Number:
		return "", web.Refuse(http.StatusConflict, "changed since you loaded the page: the Director opened a newer round; reload it")
	}
	if err := runningDirectorAttempt(s, p.Repository, p.TaskID, p.Tag); errors.Is(err, store.ErrInvalid) {
		return "", web.Refuse(http.StatusConflict, "changed since you loaded the page: "+p.Tag+" is no longer running, so its round cannot be answered")
	} else if err != nil {
		return "", err
	}
	qs, err := web.ParseRound(round.Text)
	if err != nil {
		return "", fmt.Errorf("round %d does not read: %w", round.Number, err)
	}
	if p.Question > len(qs) {
		return "", web.Refuse(http.StatusBadRequest, fmt.Sprintf("this round has no Q%d", p.Question))
	}
	saved, err := s.Drafts(p.Repository, p.TaskID)
	if err != nil {
		return "", err
	}
	// version is the highest draft number of this round, which the page's form posts as p.Drafts.
	old := map[int]store.Draft{}
	version := 0
	for _, e := range saved {
		if e.Tag == p.Tag && e.Round == round.Number {
			old[e.Question] = e.Draft
			version = max(version, e.Number)
		}
	}
	drafts := make([]store.Draft, len(qs))
	changed := false
	for i, q := range qs {
		d, ok := old[q.N]
		if !ok {
			d = store.Draft{Tag: p.Tag, Round: round.Number, Question: q.N}
		}
		if words, ok := boxes[q.N]; ok {
			d.Text = words
		}
		if q.N == p.Question {
			switch p.Act {
			case web.RoundAccept:
				d.Mark = store.DraftAccept
			case web.RoundWords:
				if d.Text != "" {
					d.Mark = store.DraftWords
				}
			case web.RoundChange:
				d.Mark = store.DraftUnmarked
			}
		}
		if d.Mark == store.DraftWords && d.Text == "" {
			d.Mark = store.DraftUnmarked
		}
		drafts[i] = d
		// A question never saved and still empty is no change.
		changed = changed || (d != old[q.N] && (ok || d.Mark != "" || d.Text != ""))
	}
	// The mutation names the drafts the page rendered, so the same words saved again after others replay nothing.
	body, err := json.Marshal(drafts)
	if err != nil {
		return "", err
	}
	mutation := mutationID("round-drafts", p.Repository, p.TaskID, p.Tag, strconv.Itoa(round.Number), strconv.Itoa(p.Drafts), digest.Of(body).Hex())
	if version > p.Drafts {
		// The page predates a draft of this round saved since, from another tab or window: its boxes and marks are
		// not the round's, so the press writes nothing, unless it is a retry of the very post that saved.
		if r, err := s.Receipt(mutation); err != nil || r.Repository != p.Repository || r.TaskID != p.TaskID {
			return "", web.Refuse(http.StatusConflict, "changed since you loaded the page: its answers were saved from another tab or window; reload, your words there are kept")
		}
		changed = false
	}
	if changed {
		if _, err := s.SaveDrafts(mutation, p.Repository, p.TaskID, drafts); err != nil {
			if errors.Is(err, store.ErrInvalid) {
				return "", web.Refuse(http.StatusConflict, err.Error())
			}
			return "", err
		}
	}
	if p.Act == web.RoundWords && drafts[p.Question-1].Mark != store.DraftWords {
		return "", web.Refuse(http.StatusBadRequest, fmt.Sprintf("Q%d has no words to answer with; write them in its box first. Your other words are saved", p.Question))
	}
	if p.Act != web.RoundSend {
		return "", nil
	}
	var left []int
	answers := make([]web.RoundAnswer, len(qs))
	for i, d := range drafts {
		switch d.Mark {
		case store.DraftAccept:
			answers[i] = web.RoundAnswer{Accepted: true, Words: d.Text}
		case store.DraftWords:
			answers[i] = web.RoundAnswer{Words: d.Text}
		default:
			left = append(left, qs[i].N)
		}
	}
	if len(left) > 0 {
		verb := "has"
		if len(left) > 1 {
			verb = "have"
		}
		return "", web.Refuse(http.StatusConflict, web.Unanswered(left)+" "+verb+" no answer yet; your words are saved")
	}
	text := web.RoundAnswersText(qs, answers)
	if len(text) > store.MaxTextBytes {
		return "", web.Refuse(http.StatusRequestEntityTooLarge, fmt.Sprintf("the round's answers are over the limit of %d bytes (64 KiB); shorten some and send again. Your words are saved", store.MaxTextBytes))
	}
	k := roundOrdinal(talk, *round)
	x := web.Answer{Repository: p.Repository, TaskID: p.TaskID}
	env := webEnvelope(x, web.RoundID(p.Repository, p.TaskID, round.Number), store.MessageGrillAnswered,
		fmt.Sprintf("Round %d of %s of %s/%s was sent on the task page: %d %s, %s; read the answers with guildhall grill round show --repo %s --task %s --attempt %s, then open the next round or finish the stage.",
			k, p.Tag, p.Repository, p.TaskID, len(qs), plural(len(qs), "question", "questions"), web.RoundTally(answers), p.Repository, p.TaskID, p.Tag))
	say := &store.Saying{Author: store.ConversationGuildmasterPage, Tag: p.Tag, Kind: store.ConversationRoundAnswers, Text: text, Round: round.Number}
	if err := webSendDirector(s, env, say); err != nil {
		return "", err
	}
	return env.ID, nil
}

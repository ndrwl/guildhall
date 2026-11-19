package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// directorAcceptChoice is the option a Director acceptance answers: the plan approved.
const directorAcceptChoice = string(workflow.VerdictApproved)

// decideAccept runs `guildhall decide accept <repo>/<task> <decision> --director --reason <text>`: under the task
// lock it judges the decision again as the advance that opened it did (interp.Engine.DirectorAcceptance) and, when
// the Director may accept it, records the answer approved with the reason as its note, AnsweredVia director, and a
// conversation entry by the Director carrying the reason, in one transaction (docs/decisions-cli.md#decide-accept).
// It prints the receipt as one JSON document. The flags may come before or after the two arguments. The mutation id
// is decide answer's, so a retry replays and a decision the Guildmaster already answered conflicts, without judging
// again. Like decide answer, it never advances the task. It exits 0 on success, 1 when the decision is refused or
// the store fails and 2 on a usage error.
func (a *App) decideAccept(args []string) int {
	const name = "decide accept"
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var f advanceFlags
	f.register(fl)
	director := fl.Bool("director", false, "the Director accepts the plan itself, telling the Guildmaster")
	reason := fl.String("reason", "", "why the Director accepts it")
	if err := parseInterspersed(fl, args); err != nil {
		return a.decideUsage(err.Error())
	}
	pos := fl.Args()
	if len(pos) != 2 {
		return a.decideUsage(name + " takes <repo>/<task> and <decision>")
	}
	repo, task, ok := strings.Cut(pos[0], "/")
	switch {
	case !ok || !workflow.ValidID(repo) || !workflow.ValidID(task):
		return a.decideUsage(fmt.Sprintf("%s: %q must be <repo>/<task>", name, pos[0]))
	case !workflow.ValidID(pos[1]):
		return a.decideUsage(fmt.Sprintf("%s: decision %q must be an id", name, pos[1]))
	case !*director:
		// Only the Director's own acceptance has this command; the Guildmaster's words go through decide answer.
		return a.decideUsage(name + " requires --director; the Guildmaster answers with decide answer")
	case strings.TrimSpace(*reason) == "":
		return a.decideUsage(name + " requires --reason")
	}
	if f.repo != "" && f.repo != repo {
		return a.decideUsage(fmt.Sprintf("%s: --repo %s is not the task's repository %s", name, f.repo, repo))
	}
	f.repo = repo
	if err := f.resolve(a); err != nil {
		return a.decideUsage(name + ": " + err.Error())
	}
	id := pos[1]
	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s: %v\n", name, err)
		return 1
	}
	// The reason is both the answer's note and part of the conversation entry, so the longer wrapper bounds it.
	text, err := normaliseText("reason", []byte(*reason), store.MaxTextBytes-max(len(directorAcceptChoice)+1, len(directorAcceptedText(id, ""))))
	if err != nil {
		return fail(err)
	}
	text = strings.TrimSpace(text)
	if err := f.listed(); err != nil {
		return fail(err)
	}
	if _, err := os.Stat(f.store); errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(f.store))
	}
	rc, err := a.directorAccept(f, task, id, text)
	if err != nil {
		return fail(err)
	}
	if err := json.NewEncoder(a.Stdout).Encode(rc); err != nil {
		return 1
	}
	return 0
}

// directorAccept is decide accept's work under task's lock: a decision already answered goes straight to the store,
// which replays the Director's own acceptance and refuses any other answer as a conflict; an open one is judged
// first, and refused with every reason when the verdict is no.
func (a *App) directorAccept(f advanceFlags, task, id, reason string) (store.Receipt, error) {
	cfg, repo, state, release, err := lockTask(f, task)
	if err != nil {
		return store.Receipt{}, err
	}
	defer release()
	records := storeRecords{path: f.store, wait: f.wait}
	d, err := withRecords(records, true, nil, func(s *store.Store) (store.Decision, error) { return findDecision(s, f.repo, task, id) })
	if err != nil {
		return store.Receipt{}, err
	}
	if !d.Answered {
		// The verdict reads the task's overrides as the advance does, from the copy's config.
		overrides, err := interp.ApplyTaskOverrides(records, cfg, f.repo, task)
		if err != nil {
			return store.Receipt{}, fmt.Errorf("task overrides: %w", err)
		}
		e := a.reconcileEngine(f, overrides.Config, repo, state)
		e.Overrides = &overrides
		v, err := e.DirectorAcceptance(context.Background(), f.repo, task, id)
		if err != nil {
			return store.Receipt{}, err
		}
		if !v.OK {
			return store.Receipt{}, fmt.Errorf("decision %s stays with the Guildmaster: %s", id, strings.Join(v.Reasons, "; "))
		}
	}
	answer := directorAcceptChoice + "\n" + reason
	say := &store.Saying{Author: store.ConversationDirector, Tag: id, Kind: store.ConversationSay, Text: directorAcceptedText(id, reason)}
	return withRecords(records, false, nil, func(s *store.Store) (store.Receipt, error) {
		return answerDecisionSaying(s, f.repo, task, id, answer, store.AnsweredViaDirector, "", say)
	})
}

// directorAcceptedText is the conversation entry a Director acceptance records: what it did, the reason, and how the
// Guildmaster takes it back.
func directorAcceptedText(id, reason string) string {
	return fmt.Sprintf("Accepted by the Director, %s approved: %s\n\nTo take it back, send the task back to plan with guildhall step send-back.", id, reason)
}

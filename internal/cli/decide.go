package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const decideUsage = `usage:
  guildhall decide list --store <path> [--repo <id>]
  guildhall decide show --store <path> --repo <id> --task <id> --decision <id>
  guildhall decide answer --store <path> --repo <id> --task <id> --decision <id> --answer-file <path|->
  guildhall decide defer --store <path> --repo <id> --task <id> --decision <id> --until-merged <repo>/<task>... --reason <text>
  guildhall decide undefer --store <path> --repo <id> --task <id> --decision <id>
  guildhall decide open --store <path> --repo <id> --task <id> (--question <text> | --question-file <path|->) [--option <text>]...
--copy <dir>, or the copy found from the current directory, may stand in for --store: the copy's director.store`

// decide runs a decide subcommand against an existing store and prints its result as one JSON document
// (docs/decisions-cli.md). list prints the open decisions, the per-machine inbox (R1.20), by repository,
// task and decision id; show prints one decision; answer records the Guildmaster's words with the mutation
// "decision-answer:<repo>:<task>:<decision>" (or "decision-answer:sha256:<hex digest>" of that string if
// over 128 bytes), so a retry of the same words prints the original receipt. Answering never advances a task.
// It exits 0 on success, 1 when the answer or the store refuses or fails and 2 on a usage error.
func (a *App) decide(args []string) int {
	if len(args) == 0 {
		return a.decideUsage("missing decide subcommand")
	}
	sub := args[0]
	if sub == "defer" || sub == "undefer" {
		return a.decideDefer(sub, args[1:])
	}
	if sub == "open" {
		return a.decideOpen(args[1:])
	}
	flags := flag.NewFlagSet("decide "+sub, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("store", "", "store file")
	repo := flags.String("repo", "", "repository id")
	task := flags.String("task", "", "task id")
	id := flags.String("decision", "", "decision id")
	answerFile := flags.String("answer-file", "", "answer file, or - for standard input")
	var cp copyFlags
	cp.register(flags)
	var required, allowed []string
	switch sub {
	case "list":
		required, allowed = []string{"store"}, []string{"store", "copy", "repo"}
	case "show":
		required = []string{"store", "repo", "task", "decision"}
		allowed = append(required, "copy")
	case "answer":
		required = []string{"store", "repo", "task", "decision", "answer-file"}
		allowed = append(required, "copy")
	default:
		return a.decideUsage(fmt.Sprintf("unknown decide subcommand %q", sub))
	}
	if err := flags.Parse(args[1:]); err != nil {
		return a.decideUsage(err.Error())
	}
	if flags.NArg() > 0 {
		return a.decideUsage(fmt.Sprintf("decide %s takes no arguments", sub))
	}
	var usageErr string
	flags.Visit(func(f *flag.Flag) {
		if !slices.Contains(allowed, f.Name) {
			usageErr = fmt.Sprintf("--%s does not apply to decide %s", f.Name, sub)
		}
	})
	if err := cp.apply(a, path, nil, nil); err != nil {
		return a.decideUsage(err.Error())
	}
	if cp.searched {
		return a.decideUsage(cp.missing(a).Error())
	}
	for _, name := range required {
		if flags.Lookup(name).Value.String() == "" {
			usageErr = fmt.Sprintf("decide %s requires --%s", sub, name)
		}
	}
	for _, v := range []string{*repo, *task, *id} {
		if v != "" && !workflow.ValidID(v) {
			usageErr = fmt.Sprintf("id %q must be lowercase letters, digits and hyphens, starting with a letter", v)
		}
	}
	if usageErr != "" {
		return a.decideUsage(usageErr)
	}

	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: decide %s: %v\n", sub, err)
		return 1
	}
	var answer string
	if sub == "answer" {
		// A rollout approval is the Guildmaster's own: an agent in a session, the Steward's among them, cannot record
		// it for them (docs/steward.md#approval-in-a-steward-session).
		if *repo == stewardRepository && *task == stewardRolloutsTask && filepath.Base(*path) == stewardStoreFile && a.inSession() {
			return fail(fmt.Errorf("decision %s/%s/%s approves a rollout, and only the Guildmaster answers it, outside any Guildhall session", *repo, *task, *id))
		}
		var err error
		if answer, err = a.readAnswer(*answerFile); err != nil {
			return fail(err)
		}
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(*path))
	}
	s, err := store.Open(*path, store.Options{ReadOnly: sub != "answer", RequireStore: true})
	if err != nil {
		return fail(err)
	}
	var out any
	switch sub {
	case "list":
		out, err = openDecisions(s, *repo)
	case "show":
		out, err = findDecision(s, *repo, *task, *id)
	case "answer":
		out, err = answerDecision(s, *repo, *task, *id, answer, "", "")
	}
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(err)
	}
	if err := json.NewEncoder(a.Stdout).Encode(out); err != nil {
		return 1
	}
	return 0
}

// openDecisions lists unanswered decisions of one repository, or of every repository when repo is empty.
func openDecisions(s *store.Store, repo string) ([]store.Decision, error) {
	repos := []string{repo}
	if repo == "" {
		var err error
		if repos, err = s.Repositories(); err != nil {
			return nil, err
		}
	}
	out := []store.Decision{}
	for _, r := range repos {
		all, err := s.AllDecisions(r)
		if err != nil {
			return nil, err
		}
		for _, d := range all {
			if d.Open() {
				out = append(out, d)
			}
		}
	}
	return out, nil
}

// answerDecision records an answer unless the decision's task is cancelled: a cancelled task's decisions are void
// and nothing waits for them (docs/tasks.md#task-cancel). A done task's decisions, such as its audit, are answered
// as any other. via and by are where the answer was given and the login that gave it, both "" for decide answer and
// store.AnsweredViaPage with the Tailscale login for the task page; the mutation id is the same for both, so an
// answer is recorded once whichever way it comes, and a retry of the same answer from the same place replays it.
func answerDecision(s *store.Store, repo, task, id, answer, via, by string) (store.Receipt, error) {
	t, err := s.Task(repo, task)
	if err != nil {
		return store.Receipt{}, err
	}
	if t.State == store.TaskCancelled {
		return store.Receipt{}, fmt.Errorf("task %s/%s is cancelled (%s); its decisions are not answered", repo, task, t.CancelReason)
	}
	return s.AnswerDecisionVia(mutationID("decision-answer", repo, task, id), repo, task, id, answer, via, by)
}

func findDecision(s *store.Store, repo, task, id string) (store.Decision, error) {
	all, err := s.Decisions(repo, task)
	if err != nil {
		return store.Decision{}, err
	}
	for _, d := range all {
		if d.ID == id {
			return d, nil
		}
	}
	return store.Decision{}, fmt.Errorf("decision %q of %s/%s: %w", id, repo, task, store.ErrNotFound)
}

// readAnswer reads an answer from a file, or from standard input for "-", with readText's rules; the store
// enforces its limit on the result.
func (a *App) readAnswer(name string) (string, error) {
	return a.readText("answer", name, store.MaxTextBytes)
}

// readText reads the text named what from a file, or from standard input for "-".
// As for message envelopes, a leading UTF-8 byte order mark is dropped and UTF-16 is refused;
// CRLF line endings become LF, so the same words record the same text on every platform.
// It reads at most enough for limit bytes of LF text; the caller checks the result's size.
func (a *App) readText(what, name string, limit int) (string, error) {
	r := a.Stdin
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return "", fmt.Errorf("%s: %w", what, err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, int64(textReadLimit(limit))+1))
	if err != nil {
		return "", fmt.Errorf("%s: %w", what, err)
	}
	return normaliseText(what, data, limit)
}

// textReadLimit is the most bytes worth reading for limit bytes of LF text: CRLF endings may double the size.
func textReadLimit(limit int) int { return 2*limit + 3 }

// normaliseText is readText's rules on bytes already read, which the task page applies to its form's text too:
// more than textReadLimit bytes and UTF-16 are refused, a leading UTF-8 byte order mark is dropped and CRLF line
// endings become LF. The caller checks the result's size and encoding.
func normaliseText(what string, data []byte, limit int) (string, error) {
	switch {
	case len(data) > textReadLimit(limit):
		return "", fmt.Errorf("%s is larger than the limit of %d bytes", what, limit)
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		return "", fmt.Errorf("%s is UTF-16; write it as UTF-8, for example with Set-Content -Encoding utf8", what)
	}
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	return string(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))), nil
}

func (a *App) decideUsage(msg string) int {
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s\n%s\n", msg, decideUsage)
	return 2
}

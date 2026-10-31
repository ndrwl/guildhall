package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// questionPrefix starts the id of every question decision the Director opens: q<n>. An id with no "-" is never
// read as a step round (<step>-<n>) or loop exit (<step>-x<n>), and cannot clash with the engine's own decisions.
const questionPrefix = "q"

// decideOpen runs `guildhall decide open`: it opens a question decision on a task that is not closed, with the
// next free id q<n>, and prints it as `decide show` would. An open question with the same text and options on the
// task is printed instead and nothing is written, so a retry opens nothing new. It sends no message: the Director
// asked the question. It exits 0 on success, 1 when refused and 2 on a usage error (docs/decisions-cli.md#decide-open).
func (a *App) decideOpen(args []string) int {
	const name = "decide open"
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	path := fl.String("store", "", "store file")
	var cp copyFlags
	cp.register(fl)
	repo := fl.String("repo", "", "repository id")
	task := fl.String("task", "", "task id")
	question := fl.String("question", "", "the question")
	questionFile := fl.String("question-file", "", "question file, or - for standard input")
	var options stringList
	fl.Var(&options, "option", "an option the Guildmaster may name; repeatable")
	if err := fl.Parse(args); err != nil {
		return a.decideUsage(err.Error())
	}
	set := map[string]bool{}
	fl.Visit(func(f *flag.Flag) { set[f.Name] = true })
	switch err := cp.apply(a, path, nil, nil); {
	case fl.NArg() > 0:
		return a.decideUsage(name + " takes no arguments")
	case err != nil:
		return a.decideUsage(err.Error())
	case cp.searched:
		return a.decideUsage(cp.missing(a).Error())
	case *path == "" || *repo == "" || *task == "":
		return a.decideUsage(name + " requires --store, --repo and --task")
	case !workflow.ValidID(*repo) || !workflow.ValidID(*task):
		return a.decideUsage("--repo and --task must be ids")
	case set["question"] == set["question-file"]:
		return a.decideUsage(name + " requires one of --question or --question-file")
	case set["question"] && strings.TrimSpace(*question) == "":
		return a.decideUsage(name + " requires a non-blank --question")
	case set["question-file"] && *questionFile == "":
		return a.decideUsage(name + " requires a --question-file path, or - for standard input")
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s: %v\n", name, err)
		return 1
	}
	text := *question
	if set["question-file"] {
		var err error
		if text, err = a.readText("question", *questionFile, store.MaxTextBytes); err != nil {
			return fail(err)
		}
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(*path))
	}
	d, err := withStore(*path, false, func(s *store.Store) (store.Decision, error) {
		return openQuestion(s, *repo, *task, text, options)
	})
	if err != nil {
		return fail(err)
	}
	if err := json.NewEncoder(a.Stdout).Encode(d); err != nil {
		return 1
	}
	return 0
}

// openQuestion opens a question decision on repo/task, refusing a closed (done or cancelled) task, and returns it;
// an open question of the task with the same text and options is returned instead, with nothing written.
func openQuestion(s *store.Store, repo, task, question string, options []string) (store.Decision, error) {
	t, err := s.Task(repo, task)
	if err != nil {
		return store.Decision{}, err
	}
	switch t.State {
	case store.TaskDone:
		return store.Decision{}, fmt.Errorf("task %s/%s is done; a closed task takes no new questions", repo, task)
	case store.TaskCancelled:
		return store.Decision{}, fmt.Errorf("task %s/%s is cancelled (%s); a closed task takes no new questions", repo, task, t.CancelReason)
	}
	all, err := s.Decisions(repo, task)
	if err != nil {
		return store.Decision{}, err
	}
	next := 1
	for _, d := range all {
		if d.Kind == store.DecisionQuestion && d.Open() && d.Question == question && slices.Equal(d.Options, options) {
			return d, nil
		}
		if n, ok := questionNumber(d.ID); ok && n >= next {
			next = n + 1
		}
	}
	d := store.Decision{Repository: repo, TaskID: task, ID: questionPrefix + strconv.Itoa(next), Kind: store.DecisionQuestion,
		Options: options, Question: question}
	if _, err := s.OpenDecision(mutationID("decision-open", repo, task, d.ID), d); err != nil {
		return store.Decision{}, err
	}
	return findDecision(s, repo, task, d.ID)
}

// questionNumber is n of a question decision id q<n>, n a positive decimal without leading zeros.
func questionNumber(id string) (int, bool) {
	digits, ok := strings.CutPrefix(id, questionPrefix)
	if !ok || digits == "" || digits[0] == '0' || strings.Trim(digits, "0123456789") != "" || len(digits) > 9 {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	return n, err == nil
}

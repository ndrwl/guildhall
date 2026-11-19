package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// questionPrefix starts the id of every question decision the Director opens: q<n>. An id with no "-" is never
// read as a step round (<step>-<n>) or loop exit (<step>-x<n>), and cannot clash with the engine's own decisions.
const questionPrefix = "q"

// grantFlags are decide open's flags of the one grant a question may propose, all given together or none.
var grantFlags = []string{"grant-option", "grant-step", "grant-tool", "grant-reason"}

// decideOpen runs `guildhall decide open`: it opens a question decision on a task that is not closed, with the
// next free id q<n>, and prints it as `decide show` would. An open question with the same text, options,
// recommendation and proposed grant on the task is printed instead and nothing is written, so a retry opens nothing
// new. With the grant flags, one of its options proposes the exact task grant they name, recorded pending with the
// question in one mutation; the Guildmaster's answer choosing that option approves it (docs/tasks.md#task-grants).
// It sends no message: the Director asked the question. It exits 0 on success, 1 when refused and 2 on a usage
// error (docs/decisions-cli.md#decide-open).
func (a *App) decideOpen(args []string) int {
	const name = "decide open"
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	path := fl.String("store", "", "store file")
	cfgPath := fl.String("config", "", "Director config file, with a grant only")
	var cp copyFlags
	cp.register(fl)
	repo := fl.String("repo", "", "repository id")
	task := fl.String("task", "", "task id")
	question := fl.String("question", "", "the question")
	questionFile := fl.String("question-file", "", "question file, or - for standard input")
	var options stringList
	fl.Var(&options, "option", "an option the Guildmaster may name; repeatable")
	recommend := fl.String("recommend", "", "the option the Director recommends, one of --option")
	grantOption := fl.String("grant-option", "", "the option whose choice approves the proposed grant, one of --option")
	grantStep := fl.String("grant-step", "", "the proposed grant's step id, or all")
	var grantTools stringList
	fl.Var(&grantTools, "grant-tool", "an allowed_tools rule of the proposed grant; repeatable")
	grantUntil := fl.String("grant-until", string(store.GrantUntilAttempt), "attempt or done")
	grantReason := fl.String("grant-reason", "", "the proposed grant's reason")
	if err := fl.Parse(args); err != nil {
		return a.decideUsage(err.Error())
	}
	set := map[string]bool{}
	fl.Visit(func(f *flag.Flag) { set[f.Name] = true })
	granting := set["grant-until"]
	for _, f := range grantFlags {
		granting = granting || set[f]
	}
	// Only a grant needs the config, to resolve the task's bound workflow; a copy supplies it then.
	var copyConfig *string
	if granting {
		copyConfig = cfgPath
	}
	switch err := cp.apply(a, path, copyConfig, nil); {
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
	case set["recommend"] && !slices.Contains(options, *recommend):
		return a.decideUsage(fmt.Sprintf("--recommend %q must be one of the --option values", *recommend))
	case !granting && set["config"]:
		return a.decideUsage("--config applies to " + name + " only with a grant")
	}
	if granting {
		if msg := grantFlagsUsage(set, options, *grantOption, *grantStep, *grantUntil, *cfgPath); msg != "" {
			return a.decideUsage(msg)
		}
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
	var grant *proposedGrant
	if granting {
		cfg, r, err := loadRepository(*cfgPath, *repo)
		if err != nil {
			return fail(err)
		}
		grant = &proposedGrant{
			grant: store.TaskGrant{Repository: *repo, TaskID: *task, Step: *grantStep, Rules: grantTools, Reason: *grantReason,
				Until: store.GrantUntil(*grantUntil), ApprovingOption: *grantOption},
			dir: filepath.Join(filepath.Dir(*cfgPath), "workflows"), cfg: cfg, repo: r,
		}
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(*path))
	}
	d, err := withStore(*path, false, func(s *store.Store) (store.Decision, error) {
		return openQuestion(s, *repo, *task, text, options, *recommend, grant)
	})
	if err != nil {
		return fail(err)
	}
	if err := json.NewEncoder(a.Stdout).Encode(shownDecision{Decision: d, SubjectDigest: store.SubjectDigestOf(d)}); err != nil {
		return 1
	}
	return 0
}

// grantFlagsUsage is the usage error of decide open's grant flags, set being the flags given, or "" when they
// name one grant: --grant-option, --grant-step, --grant-tool and --grant-reason together, an option of options,
// --grant-until attempt or done, done for every step, and the config the step is resolved with.
func grantFlagsUsage(set map[string]bool, options []string, option, step, until, cfgPath string) string {
	for _, f := range grantFlags {
		if !set[f] {
			return fmt.Sprintf("a proposed grant requires --%s: --grant-option, --grant-step, --grant-tool and --grant-reason go together", f)
		}
	}
	switch {
	case !slices.Contains(options, option):
		return fmt.Sprintf("--grant-option %q must be one of the --option values", option)
	case until != string(store.GrantUntilAttempt) && until != string(store.GrantUntilDone):
		return fmt.Sprintf("--grant-until %q must be %s or %s", until, store.GrantUntilAttempt, store.GrantUntilDone)
	case step == store.GrantAllSteps && until != string(store.GrantUntilDone):
		return "--grant-step all needs --grant-until done: the next attempt of every step has no clear meaning"
	case cfgPath == "":
		return "a proposed grant requires --config, or --copy, to resolve the task's workflow steps"
	}
	return ""
}

// proposedGrant is the grant a question proposes, its id and decision unset, with what its task's bound workflow
// is resolved from to check its step (grantSteps).
type proposedGrant struct {
	grant store.TaskGrant
	dir   string
	cfg   *config.Config
	repo  config.Repository
}

// openQuestion opens a question decision on repo/task, refusing a closed (done or cancelled) task, and returns it;
// an open question of the task with the same text, options, recommended option and proposed grant is returned
// instead, with nothing written. recommend, when not "", is the option the Director recommends, which the task page
// marks. grant, when not nil, is the exact grant one option proposes, recorded with the question under the task's
// next grant id by store.OpenQuestionGranting, which appends the sentence naming it to the question.
func openQuestion(s *store.Store, repo, task, question string, options []string, recommend string, grant *proposedGrant) (store.Decision, error) {
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
	grants, err := s.TaskGrantStatuses(repo, task)
	if err != nil {
		return store.Decision{}, err
	}
	next := 1
	for _, d := range all {
		if d.Kind == store.DecisionQuestion && d.Open() && slices.Equal(d.Options, options) && sameProposal(d, question, grant, grants) {
			switch rec, err := questionRecommended(s, d); {
			case err != nil:
				return store.Decision{}, err
			case rec == recommend:
				return d, nil
			}
		}
		if n, ok := questionNumber(d.ID); ok && n >= next {
			next = n + 1
		}
	}
	d := store.Decision{Repository: repo, TaskID: task, ID: questionPrefix + strconv.Itoa(next), Kind: store.DecisionQuestion,
		Options: options, Question: question}
	// The question as recorded: with a grant, the store appends the sentence naming it, which the conversation
	// entry and the recommendation's digest are made against too.
	shown := d
	var g store.TaskGrant
	if grant != nil {
		g = grant.grant
		g.ID = nextGrantID(grants)
		shown.Question = store.QuestionWithGrant(question, g)
	}
	// The question is the Director's words on the task page too, tagged with the new decision, and its recommendation
	// the decision's one record, made against the question it opens; both are written with it.
	say := store.Saying{Author: store.ConversationDirector, Tag: d.ID, Kind: store.ConversationSay, Text: shown.Question}
	var rec *store.Recommendation
	if recommend != "" {
		rec = &store.Recommendation{Option: recommend, SubjectDigest: store.SubjectDigestOf(shown)}
	}
	mid := mutationID("decision-open", repo, task, d.ID)
	if grant == nil {
		_, err = s.OpenDecisionSaying(mid, d, &say, rec)
	} else {
		var steps []string
		if steps, err = grantSteps(s, grant.dir, grant.cfg, grant.repo, t); err != nil {
			return store.Decision{}, err
		}
		_, err = s.OpenQuestionGranting(mid, d, g, steps, &say, rec)
	}
	if err != nil {
		return store.Decision{}, err
	}
	return findDecision(s, repo, task, d.ID)
}

// sameProposal reports whether open question d asks question with grant, nil for none, as proposed: with no
// grant, d's text is question and no grant names d; with one, a grant of the task names d with the same approving
// option, step, rules in order, until and reason, and d's text is question with that grant's sentence.
func sameProposal(d store.Decision, question string, grant *proposedGrant, grants []store.TaskGrantStatus) bool {
	var proposed *store.TaskGrant
	for i := range grants {
		if grants[i].Decision == d.ID && grants[i].ApprovingOption != "" {
			proposed = &grants[i].TaskGrant
		}
	}
	switch {
	case grant == nil:
		return proposed == nil && d.Question == question
	case proposed == nil:
		return false
	}
	return proposed.ApprovingOption == grant.grant.ApprovingOption && sameGrant(*proposed, grant.grant) &&
		d.Question == store.QuestionWithGrant(question, *proposed)
}

// questionRecommended is the option the Director recommends on question d: its recommendation record's, or for a
// question opened before the record, its own recommended field; "" for none.
func questionRecommended(s *store.Store, d store.Decision) (string, error) {
	r, err := s.DecisionRecommendation(d.Repository, d.TaskID, d.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return d.Recommended, nil
	case err != nil:
		return "", err
	}
	return r.Option, nil
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

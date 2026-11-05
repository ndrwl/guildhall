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
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const taskSayUsage = `usage: guildhall task say --store <path> --repo <id> --task <id> --tag <attempt|decision> --text-file <path|-> [--author director|guildmaster-chat] [--recommend <option> --subject-digest <digest> [--words-file <path|->]]
--copy <dir>, or the copy found from the current directory, may stand in for --store: the copy's director.store
Write on the task page's conversation, under the attempt (such as grill-2) or decision (such as accept-1) of the task
that --tag names: the Director's own words, or with --author guildmaster-chat the Guildmaster's words in chat about
the task, quoted. The text is Markdown; - reads it from standard input. It answers nothing and sends no message.
With --recommend the text is the Director's reading of the Guildmaster's words on the open decision --tag names,
and the option is its recommended answer, one of the decision's options: it becomes the decision's one
recommendation, and the page shows it under the ask with a Confirm that answers with that option in one click.
--subject-digest is the subject_digest decide show prints, as decide explain takes; a recommendation made against
an earlier digest is shown as stale and cannot be confirmed. --words-file pre-fills the ask's Your words box, which
otherwise holds the Guildmaster's latest words on the ask.
A closed task's conversation stays readable and takes nothing new. Running the same command again prints the
original receipt and writes nothing.`

// taskSay runs `guildhall task say`: it appends one entry to an open task's conversation (docs/web.md#the-conversation)
// with the mutation "task-say:<repo>:<task>:<tag>:<author>:<sha256 of the text>", hashed as mutationID does, and
// prints its receipt; with --recommend the entry is a recommendation, written with the decision's recommendation
// record, and its mutation also names the option, the sha256 of the words and the subject digest. It exits 0 on success, 1 when the store refuses or fails and 2 on a usage error.
func (a *App) taskSay(args []string) int {
	const name = "task say"
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		if _, err := fmt.Fprintln(a.Stdout, taskSayUsage); err != nil {
			return 1
		}
		return 0
	}
	usage := func(msg string) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s\n%s\n", msg, taskSayUsage)
		return 2
	}
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	path := fl.String("store", "", "store file")
	var cp copyFlags
	cp.register(fl)
	repo := fl.String("repo", "", "repository id")
	task := fl.String("task", "", "task id")
	tag := fl.String("tag", "", "the attempt or decision the words are about")
	textFile := fl.String("text-file", "", "Markdown text file, or - for standard input")
	author := fl.String("author", store.ConversationDirector, "director, or guildmaster-chat for the Guildmaster's words in chat")
	recommend := fl.String("recommend", "", "the option of the decision --tag names that the Director recommends")
	wordsFile := fl.String("words-file", "", "the words the ask's Your words box is pre-filled with, or - for standard input")
	subjectDigest := fl.String("subject-digest", "", "with --recommend, the digest of what the Director read")
	if err := fl.Parse(args); err != nil {
		return usage(err.Error())
	}
	set := map[string]bool{}
	fl.Visit(func(f *flag.Flag) { set[f.Name] = true })
	switch err := cp.apply(a, path, nil, nil); {
	case fl.NArg() > 0:
		return usage(name + " takes no arguments")
	case err != nil:
		return usage(err.Error())
	case cp.searched:
		return usage(cp.missing(a).Error())
	case *path == "" || *repo == "" || *task == "" || *tag == "" || *textFile == "":
		return usage(name + " requires --store, --repo, --task, --tag and --text-file")
	case !workflow.ValidID(*repo) || !workflow.ValidID(*task) || !workflow.ValidID(*tag):
		return usage("--repo, --task and --tag must be ids: lowercase letters, digits and hyphens, starting with a letter")
	case *author != store.ConversationDirector && *author != store.ConversationGuildmasterChat:
		return usage(fmt.Sprintf("--author %q is not director or guildmaster-chat; the page writes the Guildmaster's page words itself", *author))
	case set["recommend"] && strings.TrimSpace(*recommend) == "":
		return usage("--recommend needs the option recommended")
	case set["recommend"] && *author != store.ConversationDirector:
		return usage("--recommend is the Director's; it takes no --author")
	case set["words-file"] && !set["recommend"]:
		return usage("--words-file goes with --recommend")
	case set["subject-digest"] && !set["recommend"]:
		return usage("--subject-digest goes with --recommend")
	case set["recommend"] && strings.TrimSpace(*subjectDigest) == "":
		return usage("--recommend requires --subject-digest, the digest of what you read")
	case set["words-file"] && *wordsFile == "":
		return usage("--words-file needs a path, or - for standard input")
	case *textFile == "-" && set["words-file"] && *wordsFile == "-":
		return usage("--text-file and --words-file cannot both read standard input")
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s: %v\n", name, err)
		return 1
	}
	text, err := a.readText("text", *textFile, store.MaxTextBytes)
	if err != nil {
		return fail(err)
	}
	if strings.TrimSpace(text) == "" {
		return fail(errors.New("the text is empty"))
	}
	var words string
	if set["words-file"] {
		if words, err = a.readText("words", *wordsFile, store.MaxTextBytes); err != nil {
			return fail(err)
		}
		if strings.TrimSpace(words) == "" {
			words = ""
		}
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(*path))
	}
	say := store.Saying{Author: *author, Tag: *tag, Kind: store.ConversationSay, Text: text}
	mutation := mutationID("task-say", *repo, *task, *tag, *author, digest.Of([]byte(text)).Hex())
	if set["recommend"] {
		say.Kind, say.Option, say.Words = store.ConversationRecommendation, *recommend, words
		mutation = mutationID("task-say", *repo, *task, *tag, *author, digest.Of([]byte(text)).Hex(), "recommend", *recommend,
			digest.Of([]byte(words)).Hex(), *subjectDigest)
	}
	rc, err := withStore(*path, false, func(s *store.Store) (store.Receipt, error) {
		if set["recommend"] {
			// checkRecommend words the refusals for the Director; the store enforces them again in its transaction.
			if err := checkRecommend(s, *repo, *task, *tag, *recommend); err != nil {
				return store.Receipt{}, err
			}
			return s.SayRecommendation(mutation, *repo, *task, say, *subjectDigest)
		} else if err := checkTag(s, *repo, *task, *tag); err != nil {
			return store.Receipt{}, err
		}
		return s.AddConversation(mutation, *repo, *task, say)
	})
	if err != nil {
		return fail(err)
	}
	if err := json.NewEncoder(a.Stdout).Encode(rc); err != nil {
		return 1
	}
	return 0
}

// checkRecommend refuses a recommendation unless tag names an open decision of task repo/task and option is one of
// its options, so the page's Confirm can answer with it.
func checkRecommend(s *store.Store, repo, task, tag, option string) error {
	if _, err := s.Task(repo, task); err != nil {
		return err
	}
	d, err := findDecision(s, repo, task, tag)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: task %s/%s has no decision %q; --recommend tags the decision it recommends an answer to", store.ErrInvalid, repo, task, tag)
	}
	if err != nil {
		return err
	}
	switch {
	case !d.Open():
		return fmt.Errorf("%w: decision %s of %s/%s is no longer open, so there is nothing to recommend", store.ErrInvalid, tag, repo, task)
	case len(d.Options) == 0:
		return fmt.Errorf("%w: decision %s of %s/%s has no options to recommend; reply without --recommend", store.ErrInvalid, tag, repo, task)
	case !slices.Contains(d.Options, option):
		return fmt.Errorf("%w: %q is not one of decision %s's options %v", store.ErrInvalid, option, tag, d.Options)
	}
	return nil
}

// checkTag refuses a tag that names neither a decision nor an attempt (<step>-<n>) of task repo/task, so every
// entry sits under something the task page shows.
func checkTag(s *store.Store, repo, task, tag string) error {
	if _, err := s.Task(repo, task); err != nil {
		return err
	}
	decisions, err := s.Decisions(repo, task)
	if err != nil {
		return err
	}
	for _, d := range decisions {
		if d.ID == tag {
			return nil
		}
	}
	attempts, err := s.Attempts(repo, task)
	if err != nil {
		return err
	}
	for _, at := range attempts {
		if interp.DecisionID(at.Step, at.Number, false) == tag {
			return nil
		}
	}
	return fmt.Errorf("%w: task %s/%s has no attempt or decision %q; tag the words with one, such as grill-1 or accept-1", store.ErrInvalid, repo, task, tag)
}

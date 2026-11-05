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

// decideExplain runs `guildhall decide explain <repo>/<task> <decision> --subject-digest <digest> (--text <md> |
// --file <path|->) [--recommend <option>]`: it records the Director's Markdown explanation of an open decision, which
// the task page shows above the ask's summary, and prints the explanation record as one JSON document
// (docs/decisions-cli.md#decide-explain). The flags may come before or after the two arguments. Explaining again
// replaces the text under the record's next revision; repeating the stored explanation word for word writes nothing.
// It exits 0 on success, 1 when the store refuses or fails and 2 on a usage error.
func (a *App) decideExplain(args []string) int {
	const name = "decide explain"
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	path := fl.String("store", "", "store file")
	var cp copyFlags
	cp.register(fl)
	digest := fl.String("subject-digest", "", "the subject digest the explanation was written against")
	text := fl.String("text", "", "the explanation, Markdown")
	file := fl.String("file", "", "a file holding the explanation, or - for standard input")
	recommend := fl.String("recommend", "", "the option the Director recommends")
	if err := parseInterspersed(fl, args); err != nil {
		return a.decideUsage(err.Error())
	}
	pos := fl.Args()
	var given []string
	fl.Visit(func(f *flag.Flag) { given = append(given, f.Name) })
	hasText, hasFile := slices.Contains(given, "text"), slices.Contains(given, "file")
	if err := cp.apply(a, path, nil, nil); err != nil {
		return a.decideUsage(err.Error())
	}
	if cp.searched {
		return a.decideUsage(cp.missing(a).Error())
	}
	if len(pos) != 2 {
		return a.decideUsage(name + " takes <repo>/<task> and <decision>")
	}
	repo, task, ok := strings.Cut(pos[0], "/")
	switch {
	case !ok || !workflow.ValidID(repo) || !workflow.ValidID(task):
		return a.decideUsage(fmt.Sprintf("%s: %q must be <repo>/<task>", name, pos[0]))
	case !workflow.ValidID(pos[1]):
		return a.decideUsage(fmt.Sprintf("%s: decision %q must be an id", name, pos[1]))
	case *path == "":
		return a.decideUsage(name + " requires --store or --copy")
	case strings.TrimSpace(*digest) == "":
		return a.decideUsage(name + " requires --subject-digest")
	case hasText == hasFile:
		return a.decideUsage(name + " requires one of --text or --file")
	}
	id := pos[1]
	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s: %v\n", name, err)
		return 1
	}
	body, err := *text, error(nil)
	if hasFile {
		if body, err = a.readText("explanation", *file, store.MaxTextBytes); err != nil {
			return fail(err)
		}
	} else if body, err = normaliseText("explanation", []byte(body), store.MaxTextBytes); err != nil {
		return fail(err)
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(*path))
	}
	e, err := withStore(*path, false, func(s *store.Store) (store.DecisionExplanation, error) {
		return explainDecision(s, repo, task, id, body, *digest, *recommend)
	})
	if err != nil {
		return fail(err)
	}
	if err := json.NewEncoder(a.Stdout).Encode(e); err != nil {
		return 1
	}
	return 0
}

// explainDecision records an explanation under the mutation id of the explanation record's current revision, so a
// retry of the same command replays it; an explanation that already stands word for word, with the same digest and
// recommendation, is returned as it is.
func explainDecision(s *store.Store, repo, task, id, text, digest, recommend string) (store.DecisionExplanation, error) {
	prior, err := s.DecisionExplanation(repo, task, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.DecisionExplanation{}, err
	}
	if prior.Revision > 0 && prior.Text == text && prior.SubjectDigest == digest && prior.Recommend == recommend {
		// Only while the decision is open: an answered or void one is refused below, as any explanation of it is.
		// A stored digest that is not current, from before the store refused one, falls through and is refused there.
		if d, err := findDecision(s, repo, task, id); err == nil && d.Open() && prior.Current(d) {
			return prior, nil
		}
	}
	if _, err := s.ExplainDecision(mutationID("decision-explain", repo, task, id, strconv.Itoa(prior.Revision)), repo, task, id, text, digest, recommend,
		prior.Revision); err != nil {
		return store.DecisionExplanation{}, err
	}
	return s.DecisionExplanation(repo, task, id)
}

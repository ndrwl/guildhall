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
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const workflowUsage = `usage:
  guildhall workflow approve --store <path> --file <workflow.json> --words-file <path|->
  guildhall workflow approve --store <path> --config <director.toml> --repo <id> --words-file <path|->
  guildhall workflow approvals --store <path> [--json]
--copy <dir>, or GUILDHALL_COPY, may stand in for --store, and for --config with --repo: the copy's director.store
and config.toml`

// listedApproval is one line of workflow approvals: a definition shipped with this release
// ("release") or one the Guildmaster approved in this store ("guildmaster").
type listedApproval struct {
	Source     string     `json:"source"`
	Name       string     `json:"name"`
	Digest     string     `json:"digest"`
	Words      string     `json:"words,omitempty"`
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
}

// workflowCmd runs a workflow subcommand against an existing store.
// approve validates the definition, computes its digest and records the Guildmaster's words before printing the receipt;
// with --config and --repo the definition is the repository's effective workflow, resolved as task new resolves it.
// Explicit approvals of shipped definitions are recorded like any other approval.
// approvals lists the shipped definitions as approved by release, then the recorded approvals.
// It exits 0 on success, 1 when the definition, words or store refuse or fail and 2 on a usage error.
func (a *App) workflowCmd(args []string) int {
	if len(args) == 0 {
		return a.workflowUsage("missing workflow subcommand")
	}
	sub := args[0]
	if sub != "approve" && sub != "approvals" {
		return a.workflowUsage(fmt.Sprintf("unknown workflow subcommand %q", sub))
	}
	flags := flag.NewFlagSet("workflow "+sub, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("store", "", "store file")
	file := flags.String("file", "", "workflow definition")
	cfgPath := flags.String("config", "", "Director config file")
	repo := flags.String("repo", "", "repository id")
	wordsFile := flags.String("words-file", "", "approval words file, or - for standard input")
	asJSON := flags.Bool("json", false, "print JSON")
	var cp copyFlags
	cp.register(flags)
	if err := flags.Parse(args[1:]); err != nil {
		return a.workflowUsage(err.Error())
	}
	// A copy's config is the definition only for approve of a repository's effective workflow.
	var copyConfig *string
	if sub == "approve" && *file == "" && *repo != "" {
		copyConfig = cfgPath
	}
	cerr := cp.apply(a.Getenv, path, copyConfig, nil)
	switch {
	case cerr != nil:
		return a.workflowUsage(cerr.Error())
	case *path == "":
		return a.workflowUsage("--store is required, or --copy")
	case flags.NArg() != 0:
		return a.workflowUsage(fmt.Sprintf("workflow %s takes no arguments", sub))
	case sub == "approve" && *file != "" && *cfgPath != "":
		return a.workflowUsage("--file and --config cannot be used together")
	case sub == "approve" && (*cfgPath == "") != (*repo == ""):
		return a.workflowUsage("--config and --repo go together")
	case sub == "approve" && *file == "" && *cfgPath == "":
		return a.workflowUsage("--file is required, or --config and --repo")
	case sub == "approve" && *wordsFile == "":
		return a.workflowUsage("--words-file is required")
	case sub == "approve" && *asJSON:
		return a.workflowUsage("--json applies only to workflow approvals")
	case sub == "approvals" && (*file != "" || *wordsFile != ""):
		return a.workflowUsage("--file and --words-file apply only to workflow approve")
	case sub == "approvals" && (*cfgPath != "" || *repo != ""):
		return a.workflowUsage("--config and --repo apply only to workflow approve")
	}

	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: workflow %s: %v\n", sub, err)
		return 1
	}
	shipped, err := shippedDefinitions()
	if err != nil {
		return fail(err)
	}
	var approval store.WorkflowApproval
	if sub == "approve" {
		var def *workflow.Definition
		if *file != "" {
			def, err = readDefinition(*file)
		} else {
			def, err = repositoryEffective(*cfgPath, *repo)
		}
		if err == nil {
			approval, err = a.readApproval(def, *wordsFile)
		}
		if err != nil {
			return fail(err)
		}
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(*path))
	}
	s, err := store.Open(*path, store.Options{ReadOnly: sub == "approvals", RequireStore: true})
	if err != nil {
		return fail(err)
	}
	var out any
	var recorded []store.WorkflowApproval
	if sub == "approve" {
		out, err = s.ApproveWorkflow(approval)
	} else {
		recorded, err = s.WorkflowApprovals()
	}
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(err)
	}
	if sub == "approvals" {
		list := make([]listedApproval, 0, len(shipped)+len(recorded))
		for _, def := range shipped {
			rev := def.Revision()
			list = append(list, listedApproval{Source: "release", Name: rev.Name, Digest: rev.Digest})
		}
		for _, r := range recorded {
			list = append(list, listedApproval{Source: "guildmaster", Name: r.Name, Digest: r.Digest, Words: r.Words, RecordedAt: &r.RecordedAt})
		}
		if !*asJSON {
			return a.printApprovals(list)
		}
		out = list
	}
	if err := json.NewEncoder(a.Stdout).Encode(out); err != nil {
		return 1
	}
	return 0
}

// shippedDefinitions returns the definitions this release ships, which count as approved by release.
func shippedDefinitions() ([]*workflow.Definition, error) {
	def, err := workflow.Default()
	if err != nil {
		return nil, fmt.Errorf("shipped default workflow: %w", err)
	}
	return []*workflow.Definition{def}, nil
}

// readDefinition parses and validates a workflow file.
func readDefinition(file string) (*workflow.Definition, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("workflow file: %w", err)
	}
	def, err := workflow.Parse(data)
	if err == nil {
		err = workflow.Validate(def)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return def, nil
}

// repositoryEffective returns repository repo's effective workflow from the Director config at cfgPath.
func repositoryEffective(cfgPath, repo string) (*workflow.Definition, error) {
	cfg, r, err := loadRepository(cfgPath, repo)
	if err != nil {
		return nil, err
	}
	def, err := effectiveWorkflow(filepath.Join(filepath.Dir(cfgPath), "workflows"), cfg, r)
	if err != nil {
		return nil, fmt.Errorf("repository %s: %w", repo, err)
	}
	return def, nil
}

// readApproval reads the words for approving def, a validated definition, so nothing is written for refused words.
func (a *App) readApproval(def *workflow.Definition, wordsFile string) (store.WorkflowApproval, error) {
	rev := def.Revision()
	var words []byte
	var err error
	if wordsFile == "-" {
		words, err = io.ReadAll(a.Stdin)
	} else {
		words, err = os.ReadFile(wordsFile)
	}
	if err != nil {
		return store.WorkflowApproval{}, fmt.Errorf("words file: %w", err)
	}
	if strings.TrimSpace(string(words)) == "" {
		return store.WorkflowApproval{}, errors.New("approval words must not be empty")
	}
	return store.WorkflowApproval{Name: rev.Name, Digest: rev.Digest, Words: string(words)}, nil
}

// printApprovals prints one aligned line per approval: source, name, digest and, when recorded, the time.
func (a *App) printApprovals(list []listedApproval) int {
	w := tabwriter.NewWriter(a.Stdout, 0, 0, 2, ' ', 0)
	for _, l := range list {
		line := l.Source + "\t" + l.Name + "\t" + l.Digest
		if l.RecordedAt != nil {
			line += "\t" + l.RecordedAt.Format(time.RFC3339)
		}
		_, _ = fmt.Fprintln(w, line)
	}
	if err := w.Flush(); err != nil {
		return 1
	}
	return 0
}

func (a *App) workflowUsage(msg string) int {
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s\n%s\n", msg, workflowUsage)
	return 2
}

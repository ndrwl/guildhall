package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const workflowUsage = `usage:
  guildhall workflow approve --store <path> --file <workflow.json> --words-file <path|->
  guildhall workflow approvals --store <path> [--json]`

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
// explicit approvals of shipped definitions are recorded like any other approval.
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
	wordsFile := flags.String("words-file", "", "approval words file, or - for standard input")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return a.workflowUsage(err.Error())
	}
	switch {
	case *path == "":
		return a.workflowUsage("--store is required")
	case flags.NArg() != 0:
		return a.workflowUsage(fmt.Sprintf("workflow %s takes no arguments", sub))
	case sub == "approve" && *file == "":
		return a.workflowUsage("--file is required")
	case sub == "approve" && *wordsFile == "":
		return a.workflowUsage("--words-file is required")
	case sub == "approve" && *asJSON:
		return a.workflowUsage("--json applies only to workflow approvals")
	case sub == "approvals" && (*file != "" || *wordsFile != ""):
		return a.workflowUsage("--file and --words-file apply only to workflow approve")
	}

	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: workflow %s: %v\n", sub, err)
		return 1
	}
	shipped, err := shippedRevisions()
	if err != nil {
		return fail(err)
	}
	var approval store.WorkflowApproval
	if sub == "approve" {
		if approval, err = a.readApproval(*file, *wordsFile); err != nil {
			return fail(err)
		}
	}
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return fail(fmt.Errorf("no store at %s", *path))
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
		for _, rev := range shipped {
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

// shippedRevisions returns the definitions this release ships, which count as approved by release.
func shippedRevisions() ([]workflow.Revision, error) {
	def, err := workflow.Default()
	if err != nil {
		return nil, fmt.Errorf("shipped default workflow: %w", err)
	}
	return []workflow.Revision{def.Revision()}, nil
}

// readApproval parses and validates the definition and reads the words, so nothing is written for a refused definition.
func (a *App) readApproval(file, wordsFile string) (store.WorkflowApproval, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return store.WorkflowApproval{}, fmt.Errorf("workflow file: %w", err)
	}
	def, err := workflow.Parse(data)
	if err == nil {
		err = workflow.Validate(def)
	}
	if err != nil {
		return store.WorkflowApproval{}, fmt.Errorf("%s: %w", file, err)
	}
	rev := def.Revision()
	var words []byte
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

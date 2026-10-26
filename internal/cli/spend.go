package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
)

const spendHelp = `Usage: guildhall spend report --copy <dir> [--repo <name>] [--task <id>] [--since <RFC3339>] [--json]
  --copy <dir>       Director copy (or GUILDHALL_COPY)
  --store <path>     explicit existing store instead of --copy
  --state <dir>      attempt state directory (required with --store)
  --repo <name>      repository filter (default: all)
  --task <id>        task filter (default: all matching repositories)
  --since <RFC3339>  inclusive attempt start time (default: all)
  --json            JSON instead of a human-readable table
  --help            show this help
Examples:
  guildhall spend report --copy ./director
  guildhall spend report --copy ./director --repo app --task t-1 --json
  guildhall spend report --copy ./director --since 2026-01-01T00:00:00Z --json
`

type spendAmounts struct {
	InputTokens              int64   `json:"input_tokens"`
	OutputTokens             int64   `json:"output_tokens"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64   `json:"cache_read_input_tokens"`
	CostUSD                  float64 `json:"cost_usd"`
}
type spendSummary struct {
	Attempts      int          `json:"attempts"`
	Measured      spendAmounts `json:"measured"`
	Estimated     spendAmounts `json:"estimated"`
	TokensUnknown int          `json:"tokens_unknown"`
	CostUnknown   int          `json:"cost_unknown"`
	Partial       bool         `json:"partial"`
}
type spendGroup struct {
	Repository string `json:"repository"`
	Task       string `json:"task"`
	Step       string `json:"step"`
	Profile    string `json:"profile"`
	Role       string `json:"role"`
	Adapter    string `json:"adapter"`
	Model      string `json:"model"`
}
type spendRow struct {
	spendGroup
	spendSummary
}
type spendReport struct {
	Rows                 []spendRow   `json:"rows"`
	Total                spendSummary `json:"total"`
	ExcludedUnknownStart int          `json:"excluded_unknown_start"`
	Unavailable          []string     `json:"unavailable"`
}

func (s *spendSummary) add(u store.Spend) {
	s.Attempts++
	var tokens, cost *spendAmounts
	switch u.Tokens {
	case store.SpendMeasured:
		tokens = &s.Measured
	case store.SpendEstimated:
		tokens = &s.Estimated
	default:
		s.TokensUnknown++
	}
	switch u.Cost {
	case store.SpendMeasured:
		cost = &s.Measured
	case store.SpendEstimated:
		cost = &s.Estimated
	default:
		s.CostUnknown++
	}
	if tokens != nil {
		tokens.InputTokens += u.InputTokens
		tokens.OutputTokens += u.OutputTokens
		tokens.CacheCreationInputTokens += u.CacheCreationInputTokens
		tokens.CacheReadInputTokens += u.CacheReadInputTokens
	}
	if cost != nil {
		cost.CostUSD += u.CostUSD
	}
	s.Partial = s.TokensUnknown > 0 || s.CostUnknown > 0
}

// spend reads existing attempt evidence only; its behavioural contract is docs/spend.md.
func (a *App) spend(args []string) int {
	if len(args) == 0 || args[0] != "report" {
		return a.usageError("spend: use guildhall spend report --help")
	}
	fl := flag.NewFlagSet("spend report", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	path := fl.String("store", "", "existing store")
	state := fl.String("state", "", "attempt state directory")
	repo := fl.String("repo", "", "repository")
	task := fl.String("task", "", "task")
	since := fl.String("since", "", "inclusive start time")
	js := fl.Bool("json", false, "JSON")
	if err := fl.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		_, err = io.WriteString(a.Stdout, spendHelp)
		if err != nil {
			return 1
		}
		return 0
	} else if err != nil {
		return a.usageError(err.Error() + "\n" + spendHelp)
	}
	if fl.NArg() != 0 {
		return a.usageError("spend report: unexpected arguments\n" + spendHelp)
	}
	var start time.Time
	if *since != "" {
		var err error
		start, err = time.Parse(time.RFC3339, *since)
		if err != nil {
			return a.usageError("spend report: --since must be RFC3339")
		}
	}
	if err := cp.apply(a.Getenv, path, nil, state); err != nil {
		return a.usageError(err.Error())
	}
	if *path == "" || *state == "" {
		return a.usageError("spend report: --copy or both --store and --state required\n" + spendHelp)
	}
	s, err := store.Open(*path, store.Options{ReadOnly: true, RequireStore: true})
	if err != nil {
		return a.fail(err)
	}
	report, err := readSpend(s, *state, *repo, *task, start)
	err = errors.Join(err, s.Close())
	if err != nil {
		return a.fail(err)
	}
	if *js {
		return a.printJSON(report)
	}
	return a.spendTable(report)
}

type spendStore interface {
	Repositories() ([]string, error)
	Tasks(string) ([]store.Task, error)
	Attempts(string, string) ([]store.StageAttempt, error)
	Receipts() ([]store.Receipt, error)
}

func readSpend(s spendStore, state, repository, taskID string, since time.Time) (spendReport, error) {
	out := spendReport{Rows: []spendRow{}, Unavailable: []string{"prompt_only_tokens", "tool_schema_tokens", "file_read_tokens", "adapter", "model"}}
	groups := map[spendGroup]spendSummary{}
	starts := map[[3]string]time.Time{}
	if !since.IsZero() {
		receipts, err := s.Receipts()
		if err != nil {
			return out, err
		}
		for _, r := range receipts {
			if r.Operation == "start-attempt" || r.Operation == "start-owned-attempt" {
				starts[[3]string{r.Repository, r.TaskID, r.Record}] = r.CommittedAt
			}
		}
	}
	repos, err := s.Repositories()
	if err != nil {
		return out, err
	}
	for _, repo := range repos {
		if repository != "" && repository != repo {
			continue
		}
		tasks, err := s.Tasks(repo)
		if err != nil {
			return out, err
		}
		for _, task := range tasks {
			if taskID != "" && taskID != task.ID {
				continue
			}
			attempts, err := s.Attempts(repo, task.ID)
			if err != nil {
				return out, err
			}
			for _, at := range attempts {
				if !since.IsZero() {
					record := fmt.Sprintf("attempt/%s/%d", at.Step, at.Number)
					started := starts[[3]string{repo, task.ID, record}]
					if started.IsZero() {
						out.ExcludedUnknownStart++
						continue
					}
					if started.Before(since) {
						continue
					}
				}
				dir := (artisan.AttemptKey{Repository: repo, TaskID: task.ID, Step: at.Step, Attempt: at.Number}).Dir(state)
				var u store.Spend
				if at.Spend != nil {
					u = *at.Spend
				} else {
					usage, err := adapter.ReadUsage(filepath.Join(dir, artisan.ResultFile))
					if err != nil {
						return out, fmt.Errorf("spend %s/%s/%s/%d: %w", repo, task.ID, at.Step, at.Number, err)
					}
					u = store.Spend{Tokens: store.Measure(usage.Tokens), InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheCreationInputTokens: usage.CacheCreationInputTokens, CacheReadInputTokens: usage.CacheReadInputTokens, Cost: store.Measure(usage.Cost), CostUSD: usage.CostUSD}
				}
				key := spendGroup{repo, task.ID, at.Step, "unavailable", "unavailable", "unavailable", "unavailable"}
				if err := spendManifest(dir, at, &key); err != nil {
					return out, err
				}
				sum := groups[key]
				sum.add(u)
				groups[key] = sum
				out.Total.add(u)
			}
		}
	}
	for k, v := range groups {
		out.Rows = append(out.Rows, spendRow{k, v})
	}
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i].spendGroup, out.Rows[j].spendGroup
		aa := []string{a.Repository, a.Task, a.Step, a.Profile, a.Role, a.Adapter, a.Model}
		bb := []string{b.Repository, b.Task, b.Step, b.Profile, b.Role, b.Adapter, b.Model}
		for n := range aa {
			if aa[n] != bb[n] {
				return aa[n] < bb[n]
			}
		}
		return false
	})
	return out, nil
}

func spendManifest(dir string, at store.StageAttempt, key *spendGroup) error {
	f, err := os.Open(filepath.Join(dir, "prompt", render.ManifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	var m render.Manifest
	if err := json.NewDecoder(io.LimitReader(f, 4<<20)).Decode(&m); err != nil {
		return fmt.Errorf("spend manifest %s: %w", dir, err)
	}
	id := m.Identity
	if m.Schema != render.ManifestSchema || id.Repository != at.Repository || id.Task != at.TaskID || id.Step != at.Step || id.Attempt != at.Number {
		return fmt.Errorf("spend manifest %s: identity or schema mismatch", dir)
	}
	if id.Profile != "" {
		key.Profile = id.Profile
	}
	if m.Role.Path != "" {
		key.Role = m.Role.Path
	}
	return nil
}

func (a *App) spendTable(r spendReport) int {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "REPO\tTASK\tSTEP\tPROFILE\tROLE\tADAPTER\tMODEL\tMEASURE\tINPUT\tOUTPUT\tCACHE-CREATE\tCACHE-READ\tUSD\tCOVERAGE")
	row := func(k spendGroup, s spendSummary) {
		coverage := "complete"
		if s.Partial {
			coverage = "partial"
		}
		for _, m := range []struct {
			name string
			v    spendAmounts
		}{{"measured", s.Measured}, {"estimated", s.Estimated}} {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%.6f\t%s\n", k.Repository, k.Task, k.Step, k.Profile, k.Role, k.Adapter, k.Model, m.name, m.v.InputTokens, m.v.OutputTokens, m.v.CacheCreationInputTokens, m.v.CacheReadInputTokens, m.v.CostUSD, coverage)
		}
		_, _ = fmt.Fprintf(w, "\t\t\t\t\t\t\tunknown\ttokens=%d\tcost=%d\t\t\t\t%d attempts\n", s.TokensUnknown, s.CostUnknown, s.Attempts)
	}
	for _, v := range r.Rows {
		row(v.spendGroup, v.spendSummary)
	}
	row(spendGroup{Repository: "TOTAL"}, r.Total)
	_ = w.Flush()
	_, _ = fmt.Fprintf(&b, "Excluded unknown start: %d\nUnavailable: %s\n", r.ExcludedUnknownStart, strings.Join(r.Unavailable, ", "))
	if _, err := io.WriteString(a.Stdout, b.String()); err != nil {
		return 1
	}
	return 0
}

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

const spendHelp = `Usage: guildhall spend report --copy <dir> [--repo <name>] [--task <id>] [--since <RFC3339>] [--by day] [--json]
  --copy <dir>       Director copy (default: the copy found from the current directory)
  --store <path>     explicit existing store instead of --copy
  --state <dir>      attempt state directory (required with --store)
  --repo <name>      repository filter (default: all)
  --task <id>        task filter (default: all matching repositories)
  --since <RFC3339>  inclusive attempt start time (default: all)
  --by day           one row per UTC day of each attempt's start receipt (default: by task, step, rerun, role and outcome)
  --json            JSON instead of a human-readable table
  --help            show this help
Examples:
  guildhall spend report --copy ./director
  guildhall spend report --copy ./director --repo app --task t-1 --json
  guildhall spend report --copy ./director --since 2026-01-01T00:00:00Z --json
  guildhall spend report --copy ./director --by day
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
	Rerun      string `json:"rerun"`
	Profile    string `json:"profile"`
	Role       string `json:"role"`
	Adapter    string `json:"adapter"`
	Model      string `json:"model"`
	Outcome    string `json:"outcome"`
}
type spendRow struct {
	spendGroup
	spendSummary
}
type spendReport struct {
	Rows                 []spendRow              `json:"rows"`
	Total                spendSummary            `json:"total"`
	ByRerun              map[string]spendSummary `json:"by_rerun"`
	ExcludedUnknownStart int                     `json:"excluded_unknown_start"`
	Unavailable          []string                `json:"unavailable"`
	// Days holds the --by day rows in place of Rows; spendDayReport is their JSON form.
	Days []spendDayRow `json:"-"`
}

// spendUnknownDay is the --by day row of attempts without a start receipt; it sorts after every date.
const spendUnknownDay = "unknown"

type spendDayRow struct {
	Day string `json:"day"`
	spendSummary
}

// spendDayReport is the --by day JSON: the same top-level fields as spendReport, with day rows.
type spendDayReport struct {
	Rows                 []spendDayRow           `json:"rows"`
	Total                spendSummary            `json:"total"`
	ByRerun              map[string]spendSummary `json:"by_rerun"`
	ExcludedUnknownStart int                     `json:"excluded_unknown_start"`
	Unavailable          []string                `json:"unavailable"`
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
	by := fl.String("by", "", "grouping dimension")
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
	if *by != "" && *by != "day" {
		return a.usageError("spend report: --by must be day\n" + spendHelp)
	}
	var start time.Time
	if *since != "" {
		var err error
		start, err = time.Parse(time.RFC3339, *since)
		if err != nil {
			return a.usageError("spend report: --since must be RFC3339")
		}
	}
	if err := cp.apply(a, path, nil, state); err != nil {
		return a.usageError(err.Error())
	}
	if cp.searched {
		return a.usageError("spend report: " + cp.missing(a).Error())
	}
	if *path == "" || *state == "" {
		return a.usageError("spend report: --copy or both --store and --state required\n" + spendHelp)
	}
	s, err := store.Open(*path, store.Options{ReadOnly: true, RequireStore: true})
	if err != nil {
		return a.fail(err)
	}
	report, err := readSpend(s, *state, *repo, *task, start, *by)
	err = errors.Join(err, s.Close())
	if err != nil {
		return a.fail(err)
	}
	if *by == "day" {
		if *js {
			return a.printJSON(spendDayReport{Rows: report.Days, Total: report.Total, ByRerun: report.ByRerun, ExcludedUnknownStart: report.ExcludedUnknownStart, Unavailable: report.Unavailable})
		}
		rows := make([]spendTableRow, 0, len(report.Days))
		for _, d := range report.Days {
			rows = append(rows, spendTableRow{[]string{d.Day}, d.spendSummary})
		}
		return a.spendTable([]string{"DAY"}, -1, rows, report)
	}
	if *js {
		return a.printJSON(report)
	}
	rows := make([]spendTableRow, 0, len(report.Rows))
	for _, r := range report.Rows {
		k := r.spendGroup
		rows = append(rows, spendTableRow{[]string{k.Repository, k.Task, k.Step, k.Rerun, k.Profile, k.Role, k.Adapter, k.Model, k.Outcome}, r.spendSummary})
	}
	return a.spendTable([]string{"REPO", "TASK", "STEP", "RERUN", "PROFILE", "ROLE", "ADAPTER", "MODEL", "OUTCOME"}, 3, rows, report)
}

type spendStore interface {
	Repositories() ([]string, error)
	Tasks(string) ([]store.Task, error)
	Attempts(string, string) ([]store.StageAttempt, error)
	Receipts() ([]store.Receipt, error)
}

// Rerun labels name what came before an attempt: attempt N-1 of the same task and step (docs/spend.md).
const (
	rerunFirst        = "first"
	rerunAfterRefusal = "after-refusal"
	rerunAfterFailure = "after-failure"
	rerunAfterStop    = "after-stop"
	rerunSendBack     = "send-back"
	rerunUnknown      = "unknown"
)

// rerunLabel labels at by its predecessor in byStep, the task's full attempt list keyed by step and number.
// A refusal is checked before the outcome, since it is stored as failed over any launcher outcome.
func rerunLabel(byStep map[string]map[int]store.StageAttempt, at store.StageAttempt) string {
	if at.Number <= 1 {
		return rerunFirst
	}
	prev, ok := byStep[at.Step][at.Number-1]
	switch {
	case !ok:
		return rerunUnknown
	case prev.IntakeRejection != "":
		return rerunAfterRefusal
	case prev.Outcome == store.AttemptFailed:
		return rerunAfterFailure
	case prev.Outcome == store.AttemptStopped:
		return rerunAfterStop
	case prev.Outcome == store.AttemptSucceeded:
		return rerunSendBack
	}
	return rerunUnknown
}

// readSpend reads the selected attempts' spend; by is "" for the grouped Rows or "day" for Days,
// bucketed by the UTC day of each attempt's start receipt (spendUnknownDay without one).
func readSpend(s spendStore, state, repository, taskID string, since time.Time, by string) (spendReport, error) {
	out := spendReport{Rows: []spendRow{}, ByRerun: map[string]spendSummary{}, Unavailable: []string{"prompt_only_tokens", "tool_schema_tokens", "file_read_tokens", "adapter", "model"}}
	byDay := by == "day"
	if byDay {
		out.Days = []spendDayRow{}
	}
	groups := map[spendGroup]spendSummary{}
	days := map[string]spendSummary{}
	starts := map[[3]string]time.Time{}
	if !since.IsZero() || byDay {
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
			// Built before the --since filter, so a filter never changes a label.
			byStep := map[string]map[int]store.StageAttempt{}
			for _, at := range attempts {
				if byStep[at.Step] == nil {
					byStep[at.Step] = map[int]store.StageAttempt{}
				}
				byStep[at.Step][at.Number] = at
			}
			for _, at := range attempts {
				var started time.Time
				if !since.IsZero() || byDay {
					started = starts[[3]string{repo, task.ID, fmt.Sprintf("attempt/%s/%d", at.Step, at.Number)}]
				}
				if !since.IsZero() {
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
				rerun := rerunLabel(byStep, at)
				key := spendGroup{Repository: repo, Task: task.ID, Step: at.Step, Rerun: rerun, Profile: "unavailable", Role: "unavailable", Adapter: "unavailable", Model: "unavailable", Outcome: spendOutcome(at)}
				if err := spendManifest(dir, at, &key); err != nil {
					return out, err
				}
				if byDay {
					day := spendUnknownDay
					if !started.IsZero() {
						day = started.UTC().Format(time.DateOnly)
					}
					sum := days[day]
					sum.add(u)
					days[day] = sum
				} else {
					sum := groups[key]
					sum.add(u)
					groups[key] = sum
				}
				out.Total.add(u)
				byRerun := out.ByRerun[rerun]
				byRerun.add(u)
				out.ByRerun[rerun] = byRerun
			}
		}
	}
	for k, v := range groups {
		out.Rows = append(out.Rows, spendRow{k, v})
	}
	for k, v := range days {
		out.Days = append(out.Days, spendDayRow{k, v})
	}
	// "unknown" sorts after every YYYY-MM-DD date, since letters follow digits.
	sort.Slice(out.Days, func(i, j int) bool { return out.Days[i].Day < out.Days[j].Day })
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i].spendGroup, out.Rows[j].spendGroup
		aa := []string{a.Repository, a.Task, a.Step, a.Rerun, a.Profile, a.Role, a.Adapter, a.Model, a.Outcome}
		bb := []string{b.Repository, b.Task, b.Step, b.Rerun, b.Profile, b.Role, b.Adapter, b.Model, b.Outcome}
		for n := range aa {
			if aa[n] != bb[n] {
				return aa[n] < bb[n]
			}
		}
		return false
	})
	return out, nil
}

// spendOutcome classifies an attempt as succeeded, failed, stopped, refused or running.
// Refusal comes first: RejectAttempt stores a refused attempt as failed.
func spendOutcome(at store.StageAttempt) string {
	switch {
	case at.IntakeRejection != "":
		return "refused"
	case at.Outcome == "":
		return "running"
	default:
		return string(at.Outcome)
	}
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

type spendTableRow struct {
	key []string
	sum spendSummary
}

// spendTable prints the header's key columns and the measures for each row, then the TOTAL row and the footer.
// rerunColumn is the header index of RERUN, under which one TOTAL row per rerun label follows; -1 omits them.
func (a *App) spendTable(header []string, rerunColumn int, rows []spendTableRow, r spendReport) int {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, strings.Join(header, "\t")+"\tMEASURE\tINPUT\tOUTPUT\tCACHE-CREATE\tCACHE-READ\tUSD\tCOVERAGE")
	blank := strings.Repeat("\t", len(header))
	row := func(k []string, s spendSummary) {
		coverage := "complete"
		if s.Partial {
			coverage = "partial"
		}
		key := strings.Join(k, "\t")
		for _, m := range []struct {
			name string
			v    spendAmounts
		}{{"measured", s.Measured}, {"estimated", s.Estimated}} {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%.6f\t%s\n", key, m.name, m.v.InputTokens, m.v.OutputTokens, m.v.CacheCreationInputTokens, m.v.CacheReadInputTokens, m.v.CostUSD, coverage)
		}
		_, _ = fmt.Fprintf(w, "%sunknown\ttokens=%d\tcost=%d\t\t\t\t%d attempts\n", blank, s.TokensUnknown, s.CostUnknown, s.Attempts)
	}
	for _, v := range rows {
		row(v.key, v.sum)
	}
	total := make([]string, len(header))
	total[0] = "TOTAL"
	row(total, r.Total)
	if rerunColumn >= 0 {
		labels := make([]string, 0, len(r.ByRerun))
		for label := range r.ByRerun {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		for _, label := range labels {
			total := make([]string, len(header))
			total[0], total[rerunColumn] = "TOTAL", label
			row(total, r.ByRerun[label])
		}
	}
	_ = w.Flush()
	_, _ = fmt.Fprintf(&b, "Excluded unknown start: %d\nUnavailable: %s\n", r.ExcludedUnknownStart, strings.Join(r.Unavailable, ", "))
	if _, err := io.WriteString(a.Stdout, b.String()); err != nil {
		return 1
	}
	return 0
}

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/store"
)

// stewardInformsOnly ends every Steward message's text: what the Director may and may not do with it.
const stewardInformsOnly = "This Steward message informs only: it is not dispatch, a task or a push of configuration. " +
	"Tell the Guildmaster if it matters to them, and acknowledge it with guildhall messages ack --sender steward director <id> once you have."

// failedRollout reports whether a rollout receipt status is one a Steward message reports.
func failedRollout(status string) bool {
	return status == steward.StatusFailed || status == steward.StatusVerifyFailed || status == steward.StatusReloadFailed
}

// stewardReports finds the Steward messages a scheduled run sends (docs/steward.md#steward-messages): a rollout-failed
// report for each enrolled copy whose newest rollout receipt under copyDir's rollouts/ failed, and an
// unreachable-recovered report for each copy round reached that had been unreachable for at least span. It records
// each copy's reach from round in the Steward store at storePath at now; a nil round records none.
func stewardReports(roster *steward.Roster, copyDir, storePath string, round *collectRound, span time.Duration, now time.Time) ([]steward.Report, []string) {
	var out []steward.Report
	receipts, problems := latestReceipts(filepath.Join(copyDir, stewardRollouts))
	for _, key := range slices.Sorted(maps.Keys(receipts)) {
		r := receipts[key]
		machine, director, _ := strings.Cut(key, "/")
		if !failedRollout(r.Status) {
			continue
		}
		if _, _, ok := roster.DirectorOf(machine, director); !ok {
			continue
		}
		plan := r.PlanDigest
		if d := strings.TrimPrefix(plan, "sha256:"); len(d) >= 12 {
			plan = d[:12]
		}
		text := fmt.Sprintf("The Steward's newest rollout to this copy, plan %s finished %s, ended %s", r.PlanDigest, r.FinishedAt.UTC().Format(time.RFC3339), r.Status)
		if r.Error != "" {
			text += ": " + r.Error
		}
		text += ".\nThe Steward's receipt is " + r.Receipt + " on the Steward's machine; the Guildmaster decides what follows.\n" + stewardInformsOnly
		out = append(out, steward.Report{Machine: machine, Director: director, Kind: store.ReportRolloutFailed,
			Key: r.PlanDigest + "\n" + r.FinishedAt.UTC().Format(time.RFC3339Nano), Subject: fmt.Sprintf("Rollout %s to this copy ended %s", plan, r.Status),
			Text: truncateUTF8(text, store.MaxMessageTextBytes)})
	}
	if round == nil {
		return out, problems
	}
	for _, cc := range round.Copies {
		name := steward.Destination(cc.Machine, cc.Director)
		since, err := withStore(storePath, false, func(s *store.Store) (time.Time, error) { return s.MarkCopyReach(name, cc.Error == "", now) })
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: reach: %v", name, err))
			continue
		}
		if cc.Error != "" || since.IsZero() || now.Sub(since) < span {
			continue
		}
		down := now.Sub(since).Round(time.Minute)
		text := fmt.Sprintf("The Steward could not reach this copy from %s until %s, %s, and reaches it again now. "+
			"Anything the Steward sent meanwhile was kept and is delivered now.\n%s",
			since.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339), down, stewardInformsOnly)
		out = append(out, steward.Report{Machine: cc.Machine, Director: cc.Director, Kind: store.ReportUnreachableRecovered,
			Key: since.UTC().Format(time.RFC3339Nano), Subject: fmt.Sprintf("This copy was unreachable from the Steward for %s and is reachable again", down),
			Text: text})
	}
	return out, problems
}

// queueReports records each report as a pending outbox entry in the Steward store at storePath, once: a report
// found again is the same entry, whatever state it reached.
func queueReports(storePath string, reports []steward.Report) []string {
	var problems []string
	for _, r := range reports {
		env, err := r.Envelope()
		if err == nil {
			_, err = withStore(storePath, false, func(s *store.Store) (store.Receipt, error) {
				return s.RecordOutbox(steward.ReportID(r), steward.Destination(r.Machine, r.Director), env)
			})
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s: %v", steward.Destination(r.Machine, r.Director), r.Kind, err))
		}
	}
	return problems
}

// deliverOutbox delivers every pending outbox entry of the Steward store at storePath, one at a time, with t and run
// to the copies of roster: each attempt is recorded, delivered with the copy's receipt, refused with its reason, which
// is never retried, or kept pending with the error, which the next run retries.
func deliverOutbox(ctx context.Context, roster *steward.Roster, t steward.Copier, run steward.Runner, storePath, tmpDir string) []string {
	entries, err := withStore(storePath, true, func(s *store.Store) ([]store.OutboxEntry, error) { return s.Outbox(false) })
	if err != nil {
		return []string{fmt.Sprintf("outbox: %v", err)}
	}
	var problems []string
	for _, e := range entries {
		if e.State != store.OutboxPending {
			continue
		}
		var receipt []byte
		var reason, failure string
		machine, director, _ := strings.Cut(e.To, "/")
		if m, d, ok := roster.DirectorOf(machine, director); !ok {
			failure = fmt.Sprintf("%s is not in the roster", e.To)
		} else if res, err := steward.Deliver(ctx, t, run, m, d, e.Envelope, tmpDir); err != nil {
			failure = oneLine(err.Error())
		} else if res.Status == messaging.ReceiveRefused {
			reason = res.Reason
		} else if receipt, err = json.Marshal(res.Receipt); err != nil {
			failure = err.Error()
		}
		if failure != "" {
			problems = append(problems, fmt.Sprintf("%s: %s: %s", e.To, e.ID, failure))
		}
		if _, err := withStore(storePath, false, func(s *store.Store) (store.Receipt, error) {
			return s.OutboxAttempt(e.ID, e.Attempts+1, receipt, reason, truncateUTF8(failure, store.MaxMessageTextBytes))
		}); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s: record attempt: %v", e.To, e.ID, err))
		}
		if ctx.Err() != nil {
			break
		}
	}
	return problems
}

// runStewardMessages is the scheduled run's Steward messages, after its collection round: it queues the reports the
// run finds and delivers every pending outbox entry, deterministic code with no model turn
// (docs/steward.md#steward-messages). It returns what went wrong, for the supervisor's standard error.
func (a *App) runStewardMessages(ctx context.Context, c *steward.Collector, cfg *config.Config, storePath string, round *collectRound) []string {
	span := config.DefaultReportUnreachableAfter
	if cfg.Steward != nil {
		var err error
		if span, err = cfg.Steward.ReportUnreachableAfterDuration(); err != nil {
			return []string{err.Error()}
		}
	}
	reports, problems := stewardReports(c.Roster, filepath.Dir(storePath), storePath, round, span, a.now())
	problems = append(problems, queueReports(storePath, reports)...)
	tmp, err := os.MkdirTemp("", "guildhall-steward-messages-")
	if err != nil {
		return append(problems, err.Error())
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	return append(problems, deliverOutbox(ctx, c.Roster, c.Transport, c.Runner, storePath, tmp)...)
}

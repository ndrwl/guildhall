package cli

import (
	"cmp"
	"context"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
)

// Stats reads the task list's stats strip: the status feed's running attempts, start queue, merge queues and
// today's spend, the config's running cap, the merge records recorded today, and the machine's vitals. "Today" is the UTC day, the spend
// limit's (docs/director.md#spend). It reads the store read-only with the page's bounded wait.
func (w webSource) Stats() (web.Stats, error) {
	records := storeRecords{path: w.c.store, wait: webLockWait}
	st, err := readDirectorStatusRecords(context.Background(), records, w.c.state, w.c.config, w.a.Now)
	if err != nil {
		return web.Stats{}, webBusy(err)
	}
	now := time.Now().UTC()
	if w.a.Now != nil {
		now = w.a.Now().UTC()
	}
	g := st.Session.GuardRails
	day, err := time.Parse(time.DateOnly, g.Day)
	if err != nil {
		day = utcDay(now)
	}
	out := web.Stats{ReadAt: now, Running: g.RunningAttempts, StartQueue: len(st.Session.StartQueue), Day: day, Spend: webSpend(g.SpendToday)}
	// The cap is the config's, as director status reports it; a copy with no readable config shows no cap.
	if rails, _, _ := statusConfig(w.c.config, false); rails != nil {
		out.RunningLimit = rails.MaxRunningAttempts
	}
	for _, q := range st.MergeQueues {
		out.MergeQueue += len(q.Entries)
		for _, e := range q.Entries {
			if e.Front {
				out.Fronts = append(out.Fronts, web.TaskRef{Repository: q.Repository, ID: e.Task})
			}
		}
	}
	slices.SortStableFunc(out.Fronts, func(a, b web.TaskRef) int { return cmp.Compare(a.Repository, b.Repository) })
	out.MergedToday, err = withRecords(records, true, w.a.Now, func(s *store.Store) (int, error) {
		receipts, err := s.Receipts()
		if err != nil {
			return 0, err
		}
		return mergesOn(receipts, day), nil
	})
	if err != nil {
		return web.Stats{}, webBusy(err)
	}
	out.Machine = machineVitals(w.c.state)
	return out, nil
}

// mergesOn counts the merge records recorded on the UTC day day: one per put-document receipt of an <id>-merge
// document (interp.IsMergeRecord), so each merged pull request counts once, a multi-PR task's once per slice. The
// reserved repositories' tasks are left out, as the status feed leaves them out.
func mergesOn(receipts []store.Receipt, day time.Time) int {
	start := utcDay(day)
	end := start.Add(24 * time.Hour)
	n := 0
	for _, r := range receipts {
		name, ok := strings.CutPrefix(r.Record, "document/")
		if r.Operation != "put-document" || !ok || !interp.IsMergeRecord(name) || reservedRepository(r.Repository) {
			continue
		}
		if at := r.CommittedAt.UTC(); !at.Before(start) && at.Before(end) {
			n++
		}
	}
	return n
}

// utcDay is the UTC day of at, at its midnight.
func utcDay(at time.Time) time.Time {
	y, m, d := at.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// webSpend is a day's spend as the strip shows it: its attempts, every token counted, its cost and the cost's
// measure.
func webSpend(s store.SpendTotal) web.Spend {
	return web.Spend{Attempts: s.Attempts, Tokens: s.InputTokens + s.OutputTokens + s.CacheCreationInputTokens + s.CacheReadInputTokens,
		CostUSD: s.CostUSD, CostMeasure: string(cmp.Or(s.Cost, store.SpendUnknown))}
}

// machineVitals reads this machine's vitals, the disk's on the volume holding dir; the core count is always known,
// and each value the OS does not give without cgo stays unavailable (osVitals).
func machineVitals(dir string) web.Vitals {
	v := osVitals(dir)
	v.Cores = runtime.NumCPU()
	return v
}

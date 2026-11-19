package cli

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strings"
	"sync"
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
	out.Days, err = withRecords(records, true, w.a.Now, func(s *store.Store) ([]web.DayTotal, error) {
		receipts, err := s.Receipts()
		if err != nil {
			return nil, err
		}
		out.MergedToday = mergesOn(receipts, day)
		return w.days.totals(receipts, s.Attempts, day)
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

// dayCache keeps the strip's charts' totals for past UTC days, so each read works out only today and the days not
// yet settled (docs/web.md#the-task-list). A past day is kept once every attempt started on it has finished, so its
// spend can no longer change; its merges cannot, since a receipt is committed at the time it is written. A nil cache
// keeps nothing.
type dayCache struct {
	mu   sync.Mutex
	days map[time.Time]web.DayTotal
}

func newDayCache() *dayCache { return &dayCache{days: map[time.Time]web.DayTotal{}} }

// attemptLister lists one task's stage attempts, as store.Store.Attempts does.
type attemptLister func(repository, taskID string) ([]store.StageAttempt, error)

// totals are the web.ChartDays UTC days ending with today's, oldest first: each day's merge records, counted as
// mergesOn counts them, and the spend of the attempts started on it, summed as the spend limit sums today's
// (store.CountUsage). Cached past days are taken from c, and the attempts are listed only for the tasks that started
// one on a day worked out afresh.
func (c *dayCache) totals(receipts []store.Receipt, attempts attemptLister, today time.Time) ([]web.DayTotal, error) {
	last := utcDay(today)
	first := last.AddDate(0, 0, -(web.ChartDays - 1))
	out := make([]web.DayTotal, web.ChartDays)
	fresh := make([]bool, web.ChartDays)
	running := make([]bool, web.ChartDays)
	if c != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		for d := range c.days {
			if d.Before(first) {
				delete(c.days, d)
			}
		}
	}
	for i := range out {
		day := first.AddDate(0, 0, i)
		if c != nil && i < len(out)-1 {
			if t, ok := c.days[day]; ok {
				out[i] = t
				continue
			}
		}
		out[i] = web.DayTotal{Day: day, Merges: mergesOn(receipts, day), Spend: webSpend(store.NoSpend)}
		fresh[i] = true
	}
	// started holds each attempt started on a fresh day, by repository, task and record name, with its day's index.
	started := map[[3]string]int{}
	tasks := map[[2]string]bool{}
	for _, r := range receipts {
		if r.Operation != "start-attempt" && r.Operation != "start-owned-attempt" {
			continue
		}
		at := r.CommittedAt.UTC()
		if at.Before(first) || !at.Before(last.Add(24*time.Hour)) {
			continue
		}
		i := int(utcDay(at).Sub(first) / (24 * time.Hour))
		if !fresh[i] {
			continue
		}
		started[[3]string{r.Repository, r.TaskID, r.Record}] = i
		tasks[[2]string{r.Repository, r.TaskID}] = true
	}
	sums := make([]store.SpendTotal, web.ChartDays)
	for i := range sums {
		sums[i] = store.NoSpend
	}
	keys := slices.SortedFunc(maps.Keys(tasks), func(a, b [2]string) int {
		return cmp.Or(cmp.Compare(a[0], b[0]), cmp.Compare(a[1], b[1]))
	})
	for _, k := range keys {
		list, err := attempts(k[0], k[1])
		if err != nil {
			return nil, err
		}
		for _, a := range list {
			i, ok := started[[3]string{a.Repository, a.TaskID, fmt.Sprintf("attempt/%s/%d", a.Step, a.Number)}]
			if !ok {
				continue
			}
			sums[i].AddAttempt(a)
			running[i] = running[i] || a.Outcome == ""
		}
	}
	for i := range out {
		if !fresh[i] {
			continue
		}
		out[i].Spend = webSpend(sums[i])
		if c != nil && i < len(out)-1 && !running[i] {
			c.days[out[i].Day] = out[i]
		}
	}
	return out, nil
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

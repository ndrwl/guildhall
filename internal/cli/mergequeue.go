package cli

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
)

// mergeQueueEntry is one task's place in its repository's merge queue as director status and task list print it
// (docs/interpreter.md#the-merge-queue). WaitingOn is what the task waits for: the front waits on a rebase while a
// rebaser or narrowed review runs, on a decision while one of its decisions is open, and otherwise on the checks of
// its head; every other task waits on the queue.
type mergeQueueEntry struct {
	Task      string         `json:"task"`
	Position  int            `json:"position"`
	Priority  store.Priority `json:"priority"`
	ReadyAt   time.Time      `json:"ready_at"`
	PR        string         `json:"pr,omitempty"`
	Held      bool           `json:"held"`
	Front     bool           `json:"front"`
	WaitingOn string         `json:"waiting_on"`
}

// mergeQueue is one repository's merge queue, front first and then in queue order.
type mergeQueue struct {
	Repository string            `json:"repository"`
	Entries    []mergeQueueEntry `json:"entries"`
}

// mergeQueues reads the merge queues of repos, leaving out the empty ones; the result is never nil.
func mergeQueues(s *store.Store, repos []string) ([]mergeQueue, error) {
	out := []mergeQueue{}
	for _, repo := range repos {
		q, err := s.MergeQueue(repo)
		if err != nil {
			return nil, err
		}
		if len(q.Entries) == 0 {
			continue
		}
		mq := mergeQueue{Repository: repo, Entries: []mergeQueueEntry{}}
		for i, e := range q.Entries {
			entry := mergeQueueEntry{Task: e.TaskID, Position: i + 1, Priority: e.Priority, ReadyAt: e.ReadyAt, PR: e.PR, Held: e.Held, Front: e.Front, WaitingOn: "queue"}
			if e.Front {
				if entry.WaitingOn, err = frontWait(s, repo, e.TaskID); err != nil {
					return nil, err
				}
			}
			mq.Entries = append(mq.Entries, entry)
		}
		out = append(out, mq)
	}
	return out, nil
}

// frontWait is what the front task waits on, from its records alone.
func frontWait(s *store.Store, repo, id string) (string, error) {
	attempts, err := s.Attempts(repo, id)
	if err != nil {
		return "", err
	}
	if slices.ContainsFunc(attempts, func(a store.StageAttempt) bool {
		return a.Outcome == "" && (strings.HasSuffix(a.Step, "-rebase") || strings.HasSuffix(a.Step, "-rebase-review"))
	}) {
		return "rebase", nil
	}
	decisions, err := s.Decisions(repo, id)
	if err != nil {
		return "", err
	}
	// A Director's question decision holds nothing.
	if slices.ContainsFunc(decisions, func(d store.Decision) bool { return d.Open() && d.Kind != store.DecisionQuestion }) {
		return "decision", nil
	}
	return "ci", nil
}

// queueEntries maps each queued task of repos, by "<repo>/<task>", to its merge-queue entry.
func queueEntries(s *store.Store, repos []string) (map[string]*mergeQueueEntry, error) {
	qs, err := mergeQueues(s, repos)
	if err != nil {
		return nil, err
	}
	out := map[string]*mergeQueueEntry{}
	for _, q := range qs {
		for i := range q.Entries {
			out[q.Repository+"/"+q.Entries[i].Task] = &q.Entries[i]
		}
	}
	return out, nil
}

// toggleMergeHold holds the task's merge, or releases a held one, with the mutation "merge-hold:<repo>:<task>:<n>", n
// the change's number, and prints its receipt. A held task stays authorized and keeps its ready time, but the merge
// queue skips it; the deliver step releases the front at its next advance. Each call toggles, so a retry of a call
// that succeeded toggles again.
func toggleMergeHold(s *store.Store, repo, id string) (store.Receipt, error) {
	h, err := s.MergeHold(repo, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Receipt{}, err
	}
	return s.SetMergeHold(mutationID("merge-hold", repo, id, strconv.Itoa(len(h.History)+1)), repo, id, !h.Held)
}

package store

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	bolt "go.etcd.io/bbolt"
)

// DirectorView is everything the Director's status feed reads from the store (docs/director.md#status).
// Its contents are read in one transaction and describe exactly the store state AsOf names.
// Its lists are empty rather than nil when the store has no such records.
type DirectorView struct {
	AsOf Snapshot
	// Tasks are every task, in repository then task id order.
	Tasks []Task
	// Attempts are every stage attempt, in repository, task, step and number order.
	Attempts []StageAttempt
	// Owners are the ownership records not yet released, in repository, task, step and attempt order.
	Owners []Ownership
	// OpenDecisions are the decisions not yet answered, in repository, task and decision id order.
	OpenDecisions []Decision
	// Parks are every attempt's park record, parked or unparked, in repository, task, step and attempt order.
	Parks []AttemptPark
	// Deferrals are every decision's deferral record, deferred or undeferred, in repository, task and decision order.
	Deferrals []DecisionDeferral
	// FollowUps are the follow-ups not yet done, in id order.
	FollowUps []FollowUp
	// Grants are the task grants whose derived state is active, with their approval times, in repository, task and
	// grant order (docs/decisions/task-grants.md).
	Grants []TaskGrantStatus
	// Unread are the Director's unacknowledged messages, in send order.
	Unread []Message
	// Receipts are the receipts that name a task, in sequence order.
	Receipts []Receipt
	// Session is the newest Director session, or nil when the store has none.
	Session *DirectorSession
	// LastWake is the newest wake, or nil when the store has none.
	LastWake *Wake
	// Usage is what the attempt caps count (AttemptUsage), by the store's clock.
	Usage AttemptUsage
	// Reload is the newest reload request, nil when there is none.
	Reload *DirectorReload
	// Maintenance is what says when Director maintenance is due (docs/director.md#maintenance).
	Maintenance MaintenanceView
}

// DirectorView reads the Director's view of the whole store without writing.
// Like TaskHistory it scans every receipt, so its cost grows with the store's mutation count.
func (s *Store) DirectorView() (DirectorView, error) {
	v := DirectorView{Tasks: []Task{}, Attempts: []StageAttempt{}, Owners: []Ownership{}, OpenDecisions: []Decision{}, Parks: []AttemptPark{}, Deferrals: []DecisionDeferral{}, Grants: []TaskGrantStatus{},
		Unread: []Message{}, Receipts: []Receipt{}}
	err := s.view(func(tx *bolt.Tx) error {
		var repos []string
		_ = tx.Bucket(bucketRepos).ForEachBucket(func(k []byte) error { repos = append(repos, string(k)); return nil })
		for _, repo := range repos {
			var owners []Ownership
			var decisions []Decision
			for _, err := range []error{
				scan(repoBucket(tx, repo, bucketTasks), nil, &v.Tasks),
				scan(repoBucket(tx, repo, bucketAttempts), nil, &v.Attempts),
				scan(repoBucket(tx, repo, bucketOwners), nil, &owners),
				scan(repoBucket(tx, repo, bucketDecisions), nil, &decisions),
				scan(repoBucket(tx, repo, bucketAttemptParks), nil, &v.Parks),
				scan(repoBucket(tx, repo, bucketDecisionDeferrals), nil, &v.Deferrals),
			} {
				if err != nil {
					return err
				}
			}
			for _, o := range owners {
				if o.State != OwnerReleased {
					v.Owners = append(v.Owners, o)
				}
			}
			for _, d := range decisions {
				if d.Open() {
					v.OpenDecisions = append(v.OpenDecisions, d)
				}
			}
			inbox := repoBucket(tx, repo, bucketInbox)
			if inbox == nil {
				continue
			}
			prefix := inboxPrefix(Recipient{Role: RecipientDirector})
			c := inbox.Cursor()
			for k, id := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, id = c.Next() {
				var m Message
				if err := getJSON(repoBucket(tx, repo, bucketMessages), id, &m); err != nil {
					return fmt.Errorf("inbox message %q: %w", id, err)
				}
				v.Unread = append(v.Unread, m)
			}
		}
		for _, t := range v.Tasks {
			grants, err := taskGrantStatuses(tx, t)
			if err != nil {
				return err
			}
			for _, g := range grants {
				if g.State == GrantActive {
					v.Grants = append(v.Grants, g)
				}
			}
		}
		if len(v.Grants) > 0 {
			approved, err := grantApprovals(tx)
			if err != nil {
				return err
			}
			for i, g := range v.Grants {
				if at, ok := approved[g.Repository+"/"+g.TaskID+"/"+g.Decision]; ok {
					v.Grants[i].ApprovedAt = &at
				}
			}
		}
		follows, err := followUps(tx, false)
		if err != nil {
			return err
		}
		v.FollowUps = follows
		u, err := attemptUsage(tx, s.now())
		if err != nil {
			return err
		}
		v.Usage = u
		switch d, err := latestSession(tx); {
		case err == nil:
			v.Session = &d
		case !errors.Is(err, ErrNotFound):
			return err
		}
		var reload DirectorReload
		switch err := lastJSON(directorBucket(tx, bucketReloads), &reload); {
		case err == nil:
			v.Reload = &reload
		case !errors.Is(err, ErrNotFound):
			return err
		}
		if v.Maintenance, err = maintenanceView(tx); err != nil {
			return err
		}
		var wake Wake
		switch err := lastJSON(directorBucket(tx, bucketWakes), &wake); {
		case err == nil:
			v.LastWake = &wake
		case !errors.Is(err, ErrNotFound):
			return err
		}
		return tx.Bucket(bucketReceipts).ForEach(func(k, raw []byte) error {
			var r Receipt
			if err := json.Unmarshal(raw, &r); err != nil {
				return fmt.Errorf("receipt %q: %w", k, err)
			}
			if r.Sequence > v.AsOf.Sequence {
				v.AsOf = Snapshot{Sequence: r.Sequence, CommittedAt: r.CommittedAt}
			}
			if r.TaskID != "" {
				v.Receipts = append(v.Receipts, r)
			}
			return nil
		})
	})
	if err != nil {
		return DirectorView{}, fmt.Errorf("store: director view: %w", err)
	}
	slices.SortFunc(v.Unread, func(a, b Message) int { return cmp.Compare(a.Sequence, b.Sequence) })
	slices.SortFunc(v.Receipts, func(a, b Receipt) int { return cmp.Compare(a.Sequence, b.Sequence) })
	return v, nil
}

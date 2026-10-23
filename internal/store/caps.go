package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"slices"
	"time"

	bolt "go.etcd.io/bbolt"
)

// ErrAttemptCap reports a start refused because the Director is at an attempt cap; the refusal is a *CapRefusal.
var ErrAttemptCap = errors.New("attempt cap reached")

// Cap names one attempt cap in a CapRefusal.
type Cap string

const (
	// CapRunning caps the attempts running at once across the store.
	CapRunning Cap = "running"
	// CapDaily caps the attempts started in one UTC day across the store.
	CapDaily Cap = "daily"
)

// ErrDirectorHeld reports a start refused, writing nothing, because the newest Director session holds new attempts:
// it is paused or stopping, or a stop's sweep of its attempts has not finished (AttemptCaps.Hold).
var ErrDirectorHeld = errors.New("the Director holds new attempts")

// AttemptCaps bounds StartCappedAttempt across the whole store; a zero or negative field is no cap.
// Hold refuses every new attempt with ErrDirectorHeld while the newest Director session holds them
// (DirectorSession.Holds).
type AttemptCaps struct {
	MaxRunning, MaxPerDay int
	Hold                  bool
}

// CapRefusal is the error a capped start returns at a cap: Count is what the cap counted, which has reached
// Limit, on the UTC day starting at Day. It wraps ErrAttemptCap.
type CapRefusal struct {
	Cap          Cap
	Count, Limit int
	Day          time.Time
}

func (e *CapRefusal) Error() string {
	if e.Cap == CapRunning {
		return fmt.Sprintf("%v: %d attempts are running, the cap is %d", ErrAttemptCap, e.Count, e.Limit)
	}
	return fmt.Sprintf("%v: %d attempts were started on %s (UTC), the cap is %d", ErrAttemptCap, e.Count, e.Day.Format(time.DateOnly), e.Limit)
}

func (e *CapRefusal) Unwrap() error { return ErrAttemptCap }

// AttemptUsage is what the attempt caps count, read at one instant: Running is every stage attempt started and
// not finished, in any repository, which includes every attempt with an unreleased ownership record; Today is
// every attempt started (a start-attempt or start-owned-attempt receipt) since Day, the start of the current UTC
// day by the store's clock (Options.Now). Spend sums the spend of the attempts started since Day
// (SpendTotal.AddAttempt).
type AttemptUsage struct {
	Running int        `json:"running"`
	Today   int        `json:"today"`
	Day     time.Time  `json:"day"`
	Spend   SpendTotal `json:"spend"`
}

// AttemptUsage reads what the caps count without writing. Like DirectorView it scans every receipt.
func (s *Store) AttemptUsage() (AttemptUsage, error) {
	var u AttemptUsage
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		u, err = attemptUsage(tx, s.now())
		return err
	})
	if err != nil {
		return AttemptUsage{}, fmt.Errorf("store: attempt usage: %w", err)
	}
	return u, nil
}

func attemptUsage(tx *bolt.Tx, now time.Time) (AttemptUsage, error) {
	var receipts []Receipt
	err := tx.Bucket(bucketReceipts).ForEach(func(k, raw []byte) error {
		var r Receipt
		if err := json.Unmarshal(raw, &r); err != nil {
			return fmt.Errorf("receipt %q: %w", k, err)
		}
		receipts = append(receipts, r)
		return nil
	})
	if err != nil {
		return AttemptUsage{}, err
	}
	var attempts []StageAttempt
	err = tx.Bucket(bucketRepos).ForEachBucket(func(repo []byte) error {
		var in []StageAttempt
		if err := scan(repoBucket(tx, string(repo), bucketAttempts), nil, &in); err != nil {
			return err
		}
		attempts = append(attempts, in...)
		return nil
	})
	if err != nil {
		return AttemptUsage{}, err
	}
	return CountUsage(slices.Values(receipts), slices.Values(attempts), now), nil
}

// CountUsage is the one rule for what the attempt caps count (AttemptUsage), given every receipt and every stage
// attempt of a store at the instant now: Running counts the attempts with no outcome, Today the start-attempt and
// start-owned-attempt receipts committed since now's UTC day began, and Spend the attempts those receipts started.
func CountUsage(receipts iter.Seq[Receipt], attempts iter.Seq[StageAttempt], now time.Time) AttemptUsage {
	y, m, d := now.UTC().Date()
	u := AttemptUsage{Day: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Spend: NoSpend}
	// startedToday holds each attempt started today by its repository, task and record name.
	startedToday := map[[3]string]bool{}
	for r := range receipts {
		if (r.Operation == "start-attempt" || r.Operation == "start-owned-attempt") && !r.CommittedAt.Before(u.Day) {
			u.Today++
			startedToday[[3]string{r.Repository, r.TaskID, r.Record}] = true
		}
	}
	for a := range attempts {
		if a.Outcome == "" {
			u.Running++
		}
		if startedToday[[3]string{a.Repository, a.TaskID, attemptRecord(a.Step, a.Number)}] {
			u.Spend.AddAttempt(a)
		}
	}
	return u
}

// StartCappedAttempt is StartAttempt refused, writing nothing, with ErrDirectorHeld when caps.Hold is set and the
// newest Director session holds new attempts (DirectorSession.Holds), or else with a *CapRefusal when caps.MaxRunning attempts are
// already running or caps.MaxPerDay were already started today (AttemptUsage). The hold, the count and the start
// are one mutation under the store's single-writer lock, so concurrent starts never exceed a cap and no start
// commits after a pause or stop has, until the stop's sweep has finished: a start either commits before the session
// move, and is seen by whatever reads the store after it, such as the stop's sweep, or is refused. The caps are not part of the request: a replayed mutation id returns its
// original receipt whatever the caps and the session are now, so an attempt already recorded is never refused.
// StartAttempt is StartCappedAttempt with no caps.
func (s *Store) StartCappedAttempt(mutationID, repository, taskID, step string, caps AttemptCaps) (Receipt, error) {
	if err := validateIDs(repository, taskID, step); err != nil {
		return Receipt{}, err
	}
	req := StageAttempt{Repository: repository, TaskID: taskID, Step: step}
	return s.mutate(mutationID, "start-attempt", req, func(tx *bolt.Tx, r *Receipt) error {
		if caps.Hold {
			switch d, err := latestSession(tx); {
			case errors.Is(err, ErrNotFound):
			case err != nil:
				return err
			case d.State == DirectorPaused || d.State == DirectorStopping:
				return fmt.Errorf("%w: session %d is %s", ErrDirectorHeld, d.Number, d.State)
			case d.Holds():
				return fmt.Errorf("%w: session %d's stop is sweeping its attempts", ErrDirectorHeld, d.Number)
			}
		}
		if caps.MaxRunning > 0 || caps.MaxPerDay > 0 {
			if _, err := attemptTask(tx, repository, taskID, step); err != nil {
				return err
			}
			u, err := attemptUsage(tx, s.now())
			if err != nil {
				return err
			}
			switch {
			case caps.MaxRunning > 0 && u.Running >= caps.MaxRunning:
				return &CapRefusal{Cap: CapRunning, Count: u.Running, Limit: caps.MaxRunning, Day: u.Day}
			case caps.MaxPerDay > 0 && u.Today >= caps.MaxPerDay:
				return &CapRefusal{Cap: CapDaily, Count: u.Today, Limit: caps.MaxPerDay, Day: u.Day}
			}
		}
		_, err := startAttempt(tx, r, repository, taskID, step)
		return err
	})
}

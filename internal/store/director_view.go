package store

import "errors"

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
	// Unread are the Director's unacknowledged messages, in send order.
	Unread []Message
	// Receipts are the receipts that name a task, in sequence order.
	Receipts []Receipt
}

// DirectorView reads the Director's view of the whole store without writing.
// Like TaskHistory it scans every receipt, so its cost grows with the store's mutation count.
func (s *Store) DirectorView() (DirectorView, error) {
	return DirectorView{}, errors.New("store: director view is not implemented")
}

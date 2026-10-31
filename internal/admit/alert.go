package admit

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// The Guildmaster's decisions on observed-untrusted managed.toml digests live on one reserved task of the Director
// store, under the repository id config validation refuses for a real repository, as a Steward store keeps its
// rollout approvals (docs/director-modes.md#guard-rails). Task listings and prune leave the task out; its open
// decisions reach needs_me through the status feed's decision items.
const (
	AlertRepository = config.ManagedAlertRepository
	AlertTask       = "untrusted"
)

// An alert decision's options: only AlertApprove authorizes the digest, recording a guildmaster-approved revision
// naming the decision; AlertReject keeps the refusal until a reload authorizes the digest or the copy's managed.toml
// is replaced with an authorized one.
const (
	AlertApprove = "approve"
	AlertReject  = "reject"
)

// alertTaskDigest stands in for the reserved task's workflow and config digests: it runs no workflow.
var alertTaskDigest = string(digest.Of([]byte("guildhall managed-mode reserved task")))

// AlertDecisionOptions returns the option sets of the alert decisions the boundary opens, by decision kind, for
// readers such as the task page's option-meanings table.
func AlertDecisionOptions() map[store.DecisionKind][][]string {
	return map[store.DecisionKind][][]string{store.DecisionApproval: {{AlertApprove, AlertReject}}}
}

// AlertDecisionID is the id of the alert decision on managed digest d observed under enrolment event enrolment:
// "untrusted-<enrolment>-<first 12 hex digits>", so enrolling again starts with no answered alert.
func AlertDecisionID(enrolment uint64, d string) string {
	return "untrusted-" + strconv.FormatUint(enrolment, 10) + "-" + digest.Digest(d).Hex()[:12]
}

// Alert is the observed-untrusted state of the managed.toml on disk, when the store recorded it observed and it is
// still not authorized.
type Alert struct {
	// Digest is the observed digest and ObservedAt when it was first recorded observed.
	Digest     string    `json:"digest"`
	ObservedAt time.Time `json:"observed_at"`
	// Decision is the alert decision's id on the reserved task, and State "waiting" for the Guildmaster's answer,
	// "rejected" when they rejected it, "approved" when they approved it and the approval is not recorded as a
	// revision yet, or "not opened" when the observation's decision is not recorded yet.
	Decision string `json:"decision"`
	State    string `json:"state"`
}

// Alert states.
const (
	AlertWaiting   = "waiting"
	AlertRejected  = "rejected"
	AlertApproved  = "approved"
	AlertNotOpened = "not opened"
)

// alerted sets a's Untrusted from the managed facts v and the reserved task's decisions, and extends its refusal to
// say what was recorded and what answers it: a managed copy whose on-disk digest is not authorized and was recorded
// observed-untrusted under the current enrolment.
func (a *Admission) alerted(v store.ManagedView, decisions []store.Decision) {
	if a.Mode != ModeManaged || a.Authorized || a.ManagedDigest == "" || v.Enrolment == nil {
		return
	}
	var observed *store.ManagedRevision
	for i := range v.Revisions {
		if r := v.Revisions[i]; r.Source == store.SourceObservedUntrusted && r.Digest == a.ManagedDigest {
			observed = &v.Revisions[i]
			break
		}
	}
	if observed == nil {
		return
	}
	al := &Alert{Digest: observed.Digest, ObservedAt: observed.Time, Decision: AlertDecisionID(v.Enrolment.Sequence, a.ManagedDigest), State: AlertNotOpened}
	ref := fmt.Sprintf("%s/%s/%s", AlertRepository, AlertTask, al.Decision)
	more := "; it is recorded as observed-untrusted, and its alert decision is not opened yet: any command that needs effective settings opens it"
	for _, d := range decisions {
		if d.ID != al.Decision {
			continue
		}
		switch {
		case d.Answered && d.Choice == AlertReject:
			al.State = AlertRejected
			more = fmt.Sprintf("; the Guildmaster rejected it (decision %s), so it stays refused until guildhall config reload authorizes it or managed.toml is replaced with an authorized revision", ref)
		case d.Answered && d.Choice == AlertApprove:
			// Approved but its revision not recorded yet, as after an interrupted answer: the refusing form records it.
			a.approved, al.State = true, AlertApproved
			more = fmt.Sprintf("; the Guildmaster approved it (decision %s), and the next command that needs effective settings records that approval", ref)
		case d.Open():
			al.State = AlertWaiting
			more = fmt.Sprintf("; it is recorded as observed-untrusted and waits for the Guildmaster's answer to decision %s: approve authorizes this exact digest, reject keeps the refusal; guildhall config reload also authorizes it", ref)
		}
	}
	a.Untrusted = al
	if a.Refusal != "" {
		a.Refusal += more
	}
}

// alertDecisions reads the reserved task's decisions from s: none when the task does not exist.
func alertDecisions(s *store.Store) ([]store.Decision, error) {
	ds, err := s.Decisions(AlertRepository, AlertTask)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return ds, err
}

// ensureAlertTask records, once, the reserved task the alert decisions belong to; recording it again changes nothing.
func ensureAlertTask(s *store.Store) error {
	t := store.Task{Repository: AlertRepository, ID: AlertTask, Title: "Untrusted managed revisions",
		Detail:       "The Guildmaster's decisions on managed.toml digests this copy found with no authorization recorded, one per digest and enrolment.",
		Workflow:     workflow.Revision{Name: "managed-mode", Digest: alertTaskDigest},
		ConfigDigest: alertTaskDigest}
	_, err := s.CreateTask("managed-alert-task", t)
	return err
}

// previousDigest is the digest the copy applied before d: the newest one this enrolment authorized, unless it is d.
func previousDigest(v store.ManagedView, d string) string {
	previous := ""
	for _, r := range v.Revisions {
		if r.Source.Authorizes() {
			previous = r.Digest
		}
	}
	if previous == d {
		return ""
	}
	return previous
}

// ApplyAnswer records, in s, what the answered alert decision d authorizes: for an approve answered under the
// copy's current enrolment, a guildmaster-approved revision naming d and its exact subject digest, with the digest
// applied before. Any other decision, a reject and an answer from an earlier enrolment record nothing. Recording it
// again replays the event, so a retry after an interruption finishes the approval.
func ApplyAnswer(s *store.Store, d store.Decision) error {
	if d.Repository != AlertRepository || d.TaskID != AlertTask || !d.Answered || d.Choice != AlertApprove || d.Subject == nil {
		return nil
	}
	v, err := s.ManagedView()
	if err != nil {
		return err
	}
	if v.Enrolment == nil || v.Enrolment.Event != store.Enrolled || AlertDecisionID(v.Enrolment.Sequence, d.Subject.Digest) != d.ID {
		return nil
	}
	_, _, err = s.RecordManagedRevision(store.ManagedRevision{Source: store.SourceGuildmasterApproved, Digest: d.Subject.Digest,
		Previous: previousDigest(v, d.Subject.Digest), Decision: d.ID})
	return err
}

// VoidAlerts voids the reserved task's open alert decisions that nothing waits for any more, with reason: those of
// digest d, or every one when d is empty. It is for config reload, which authorizes d, and leave, which ends the
// enrolment.
func VoidAlerts(s *store.Store, d, reason string) error {
	ds, err := alertDecisions(s)
	if err != nil {
		return err
	}
	for _, dec := range ds {
		if !dec.Open() || d != "" && (dec.Subject == nil || dec.Subject.Digest != d) {
			continue
		}
		if _, err := s.VoidDecision("managed-alert-void:"+dec.ID, AlertRepository, AlertTask, dec.ID, reason); err != nil {
			return err
		}
	}
	return nil
}

// observe records, in the director.store of the copy whose config is at cfgPath, that the copy found managed digest
// a.ManagedDigest with no authorization: one observed-untrusted revision per digest and enrolment, and one
// approve/reject decision on the reserved task, opened once. An alert already answered approve is applied
// (ApplyAnswer) instead, so the digest becomes authorized. It returns the managed facts and alert decisions as they
// stand after recording.
func observe(cfgPath string, a Admission) (store.ManagedView, []store.Decision, error) {
	s, err := store.Open(filepath.Join(filepath.Dir(cfgPath), StoreFile), store.Options{RequireStore: true, LockTimeout: config.DefaultLockWait})
	if err != nil {
		return store.ManagedView{}, nil, err
	}
	v, decisions, err := record(s, cfgPath, a)
	return v, decisions, errors.Join(err, s.Close())
}

func record(s *store.Store, cfgPath string, a Admission) (store.ManagedView, []store.Decision, error) {
	v, err := s.ManagedView()
	if err != nil {
		return v, nil, err
	}
	// Another command may have enrolled, left or authorized meanwhile: only the state Judge saw is recorded.
	if v.Enrolment == nil || v.Enrolment.Event != store.Enrolled || v.Authorization(a.ManagedDigest) != nil {
		ds, err := alertDecisions(s)
		return v, ds, err
	}
	d := a.ManagedDigest
	id := AlertDecisionID(v.Enrolment.Sequence, d)
	ds, err := alertDecisions(s)
	if err != nil {
		return v, nil, err
	}
	opened := false
	for _, dec := range ds {
		if dec.ID != id {
			continue
		}
		if dec.Answered && dec.Choice == AlertApprove {
			if err := ApplyAnswer(s, dec); err != nil {
				return v, ds, err
			}
			v, err = s.ManagedView()
			return v, ds, err
		}
		opened = true
	}
	if _, _, err := s.RecordManagedRevision(store.ManagedRevision{Source: store.SourceObservedUntrusted, Digest: d, Previous: previousDigest(v, d)}); err != nil {
		return v, ds, err
	}
	if opened {
		v, err = s.ManagedView()
		return v, ds, err
	}
	if err := ensureAlertTask(s); err != nil {
		return v, ds, err
	}
	copyDir, _ := filepath.Abs(filepath.Dir(cfgPath))
	dec := store.Decision{Repository: AlertRepository, TaskID: AlertTask, ID: id, Kind: store.DecisionApproval,
		Subject: &store.Subject{Kind: store.SubjectDocument, Digest: d}, Options: []string{AlertApprove, AlertReject},
		Question: fmt.Sprintf("Approve managed.toml %s on Director copy %s, enrolled with steward %q? This copy's store records no reload or approval of that digest, "+
			"so its settings are not applied and commands that need them refuse. Approve only if you made or expect this exact change; reject keeps the refusal "+
			"until guildhall config reload authorizes it or managed.toml is replaced with an authorized revision. "+
			"Answer: guildhall decide answer --copy %s --repo %s --task %s --decision %s --answer-file -",
			d, copyDir, v.Enrolment.Steward, copyDir, AlertRepository, AlertTask, id)}
	if _, err := s.OpenDecision("managed-alert:"+strconv.FormatUint(v.Enrolment.Sequence, 10)+":"+digest.Digest(d).Hex(), dec); err != nil && !errors.Is(err, store.ErrExists) {
		return v, ds, err
	}
	if v, err = s.ManagedView(); err != nil {
		return v, ds, err
	}
	ds, err = alertDecisions(s)
	return v, ds, err
}

package store

import (
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// bucketWorkflowApprovals is a top-level bucket keyed by workflow digest.
// It is created by the first approval, so an existing schema 1 file accepts approvals unchanged.
var bucketWorkflowApprovals = []byte("workflow-approvals")

const opApproveWorkflow = "approve-workflow"

// WorkflowApproval records the Guildmaster's approval of one exact workflow revision for this Director
// (docs/configuration.md#workflow-edit-authority).
// It approves content, not a name: an edited definition has a new digest and needs its own approval.
// Recording an approval never changes a task; enforcing approvals is a later layer's job.
type WorkflowApproval struct {
	Name string `json:"name"`
	// Digest is the definition's "sha256:<hex>" digest, as workflow.Definition.Digest computes it.
	Digest string `json:"digest"`
	// Words are the Guildmaster's approval exactly as given.
	Words string `json:"words"`
	// RecordedAt is set by the store, in UTC, when the approval commits.
	RecordedAt time.Time `json:"recorded_at"`
}

// ApproveWorkflow records an approval once per digest, with the mutation id "workflow-approval:<digest>".
// Repeating the same approval returns the original receipt and changes nothing;
// different words or name for an approved digest return ErrMutationConflict.
// The caller is responsible for having validated the definition the digest names.
func (s *Store) ApproveWorkflow(a WorkflowApproval) (Receipt, error) {
	if !workflow.ValidID(a.Name) {
		return Receipt{}, invalid("workflow name %q is not a valid id", a.Name)
	}
	if _, err := digest.Parse(a.Digest); err != nil {
		return Receipt{}, invalid(`workflow digest %q must be "sha256:<64 lowercase hex>"`, a.Digest)
	}
	if err := validateText("words", a.Words, MaxTextBytes); err != nil {
		return Receipt{}, err
	}
	if !a.RecordedAt.IsZero() {
		return Receipt{}, invalid("recorded_at is set by the store")
	}
	req := struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
		Words  string `json:"words"`
	}{a.Name, a.Digest, a.Words}
	return s.mutate("workflow-approval:"+a.Digest, opApproveWorkflow, req, func(tx *bolt.Tx, _ *Receipt) error {
		approvals, err := tx.CreateBucketIfNotExists(bucketWorkflowApprovals)
		if err != nil {
			return err
		}
		if approvals.Get([]byte(a.Digest)) != nil {
			return fmt.Errorf("%w: workflow approval %s", ErrExists, a.Digest)
		}
		a.RecordedAt = s.now().UTC()
		return putJSON(approvals, []byte(a.Digest), a)
	})
}

// WorkflowApprovals lists the recorded approvals in digest order.
func (s *Store) WorkflowApprovals() ([]WorkflowApproval, error) {
	var out []WorkflowApproval
	err := s.view(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketWorkflowApprovals)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var a WorkflowApproval
			if err := decode(k, v, &a); err != nil {
				return err
			}
			out = append(out, a)
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: workflow approvals: %w", err)
	}
	return out, nil
}

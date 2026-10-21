package store

import (
	"errors"
	"strings"
	"testing"
)

func sampleApproval(c byte) WorkflowApproval {
	return WorkflowApproval{Name: "default", Digest: sampleDigest(c), Words: "Approved: two reviews before deliver.\r\n"}
}

func TestApproveWorkflowRecordsOnce(t *testing.T) {
	s, _ := newStore(t)
	a := sampleApproval('a')
	r, err := s.ApproveWorkflow(a)
	if err != nil {
		t.Fatal(err)
	}
	if r.MutationID != "workflow-approval:"+a.Digest || r.Operation != "approve-workflow" || r.Sequence != 1 {
		t.Fatalf("receipt = %+v", r)
	}

	// Replaying the same approval returns the original receipt and writes nothing.
	again, err := s.ApproveWorkflow(a)
	if err != nil || again != r {
		t.Fatalf("replay = %+v, %v; want %+v", again, err, r)
	}
	// Different words for the same digest conflict.
	other := a
	other.Words = "Approved, but only for the pilot."
	if _, err := s.ApproveWorkflow(other); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("different words: err = %v, want ErrMutationConflict", err)
	}
	if n := countReceipts(t, s); n != 1 {
		t.Fatalf("receipts = %d, want 1", n)
	}

	got, err := s.WorkflowApprovals()
	want := a
	want.RecordedAt = testTime
	if err != nil || len(got) != 1 || got[0] != want {
		t.Fatalf("approvals = %+v, %v; want [%+v]", got, err, want)
	}
}

func TestApproveWorkflowValidates(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*WorkflowApproval)
	}{
		{"bad name", func(a *WorkflowApproval) { a.Name = "Default" }},
		{"bare hex digest", func(a *WorkflowApproval) { a.Digest = strings.Repeat("a", 64) }},
		{"short digest", func(a *WorkflowApproval) { a.Digest = "sha256:abc" }},
		{"upper-case digest", func(a *WorkflowApproval) { a.Digest = "sha256:" + strings.Repeat("A", 64) }},
		{"blank words", func(a *WorkflowApproval) { a.Words = " \n" }},
		{"long words", func(a *WorkflowApproval) { a.Words = strings.Repeat("x", MaxTextBytes+1) }},
		{"invalid UTF-8 words", func(a *WorkflowApproval) { a.Words = "ok \xff" }},
		{"preset time", func(a *WorkflowApproval) { a.RecordedAt = testTime }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStore(t)
			a := sampleApproval('a')
			tc.mutate(&a)
			if _, err := s.ApproveWorkflow(a); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if n := countReceipts(t, s); n != 0 {
				t.Fatalf("receipts = %d, want 0", n)
			}
		})
	}
}

func TestWorkflowApprovalsListAndSurviveReopen(t *testing.T) {
	s, path := newStore(t)
	// An existing store without the bucket lists nothing.
	if got, err := s.WorkflowApprovals(); err != nil || len(got) != 0 {
		t.Fatalf("empty approvals = %+v, %v", got, err)
	}
	for _, c := range []byte{'c', 'a', 'b'} {
		if _, err := s.ApproveWorkflow(sampleApproval(c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	ro := testOptions()
	ro.ReadOnly = true
	r, err := Open(path, ro)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got, err := r.WorkflowApprovals()
	if err != nil || len(got) != 3 {
		t.Fatalf("approvals = %+v, %v", got, err)
	}
	for i, c := range []byte{'a', 'b', 'c'} {
		if got[i].Digest != sampleDigest(c) {
			t.Errorf("approval %d digest = %s, want %s (digest order)", i, got[i].Digest, sampleDigest(c))
		}
	}
	rep, err := r.Doctor()
	if err != nil || rep.WorkflowApprovals != 3 || len(rep.Problems) != 0 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
	if _, err := r.ApproveWorkflow(sampleApproval('d')); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only approve: err = %v, want ErrReadOnly", err)
	}
}

package store

import (
	"errors"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/digest"
)

var (
	managedA = string(digest.Of([]byte("managed a")))
	managedB = string(digest.Of([]byte("managed b")))
)

func enrol(steward, d string) Enrolment {
	return Enrolment{Event: Enrolled, Steward: steward, Digest: d}
}

func leave(d string) Enrolment {
	return Enrolment{Event: Left, Digest: d, Revision: RevisionName(digest.Digest(d))}
}

// TestEnrolmentEvents: enrol and leave are ordered events with content-derived mutation ids; recording the newest
// event again replays it, and the newest event decides the view.
func TestEnrolmentEvents(t *testing.T) {
	s, path := newStore(t)
	if v, err := s.ManagedView(); err != nil || v.Enrolment != nil || len(v.Revisions) != 0 || v.Revisions == nil {
		t.Fatalf("empty view = %#v, %v", v, err)
	}
	e, r, err := s.RecordEnrolment(enrol("hq", managedA))
	if err != nil {
		t.Fatal(err)
	}
	wantID := "director-enrol:1:hq:" + digest.Digest(managedA).Hex()[:16]
	if e.Sequence != 1 || e.Receipt != r.Sequence || e.Time.IsZero() || r.MutationID != wantID ||
		r.Operation != "record-enrolment" || r.Record != "director/enrolment/1" {
		t.Fatalf("enrolled = %+v, receipt %+v", e, r)
	}
	if again, ar, err := s.RecordEnrolment(enrol("hq", managedA)); err != nil || again != e || ar != r {
		t.Fatalf("replayed enrol = %+v, %+v, %v; want the first", again, ar, err)
	}
	if _, _, err := s.RecordEnrolment(enrol("hq", managedB)); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "already enrolled") {
		t.Fatalf("second enrol = %v, want already enrolled", err)
	}
	l, lr, err := s.RecordEnrolment(leave(managedA))
	if err != nil {
		t.Fatal(err)
	}
	if l.Sequence != 2 || lr.MutationID != "director-leave:2:"+digest.Digest(managedA).Hex()[:16] || l.Revision != "managed.toml.rev-"+digest.Digest(managedA).Hex()[:16] {
		t.Fatalf("left = %+v, receipt %+v", l, lr)
	}
	if again, ar, err := s.RecordEnrolment(leave(managedA)); err != nil || again != l || ar != lr {
		t.Fatalf("replayed leave = %+v, %+v, %v; want the first", again, ar, err)
	}
	// Enrolling again after leaving is a new event, even with the same Steward and digest.
	e3, _, err := s.RecordEnrolment(enrol("hq", managedA))
	if err != nil || e3.Sequence != 3 {
		t.Fatalf("re-enrol = %+v, %v; want event 3", e3, err)
	}
	_ = s.Close()
	s = openTest(t, path)
	if v, err := s.ManagedView(); err != nil || v.Enrolment == nil || *v.Enrolment != e3 {
		t.Fatalf("view = %+v, %v; want the re-enrolment", v, err)
	}
	if rep, err := s.Doctor(); err != nil || len(rep.Problems) != 0 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
}

func TestEnrolmentRefusals(t *testing.T) {
	s, _ := newStore(t)
	if _, _, err := s.RecordEnrolment(leave(managedA)); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "not enrolled") {
		t.Fatalf("leave unenrolled = %v", err)
	}
	for name, e := range map[string]Enrolment{
		"unknown event":   {Event: "joined", Digest: managedA},
		"bad label":       enrol("HQ!", managedA),
		"bad digest":      enrol("hq", "sha256:abc"),
		"store set":       {Event: Enrolled, Steward: "hq", Digest: managedA, Sequence: 4},
		"enrol revision":  {Event: Enrolled, Steward: "hq", Digest: managedA, Revision: RevisionName(digest.Digest(managedA))},
		"leave steward":   {Event: Left, Steward: "hq", Digest: managedA, Revision: RevisionName(digest.Digest(managedA))},
		"leave wrong rev": {Event: Left, Digest: managedA, Revision: RevisionName(digest.Digest(managedB))},
	} {
		if _, _, err := s.RecordEnrolment(e); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if v, err := s.ManagedView(); err != nil || v.Enrolment != nil {
		t.Fatalf("view after refusals = %+v, %v", v, err)
	}
}

// TestManagedRevisions: revisions belong to the enrolment they were recorded under, are one per source and
// digest, and only reload and Guildmaster approval authorize a digest.
func TestManagedRevisions(t *testing.T) {
	s, _ := newStore(t)
	if _, _, err := s.RecordManagedRevision(ManagedRevision{Source: SourceReload, Digest: managedA}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("revision unenrolled = %v, want ErrInvalid", err)
	}
	if _, _, err := s.RecordEnrolment(enrol("hq", managedA)); err != nil {
		t.Fatal(err)
	}
	v, _ := s.ManagedView()
	if v.Authorization(managedA) != nil {
		t.Fatal("enrolment authorized its digest")
	}
	obs, _, err := s.RecordManagedRevision(ManagedRevision{Source: SourceObservedUntrusted, Digest: managedA})
	if err != nil || obs.Sequence != 1 || obs.Enrolment != 1 {
		t.Fatalf("observed = %+v, %v", obs, err)
	}
	if v, _ := s.ManagedView(); v.Authorization(managedA) != nil || v.Latest() == nil || *v.Latest() != obs {
		t.Fatalf("observation authorized %s: %+v", managedA, v)
	}
	rel, r, err := s.RecordManagedRevision(ManagedRevision{Source: SourceReload, Digest: managedA, Previous: managedB})
	if err != nil {
		t.Fatal(err)
	}
	if rel.Sequence != 2 || r.MutationID != "managed-revision:1:reload:"+digest.Digest(managedA).Hex() || r.Record != "director/managed-revision/2" || rel.Previous != managedB {
		t.Fatalf("reload = %+v, receipt %+v", rel, r)
	}
	// The same reload again, whatever previous digest it names, returns the one recorded.
	if again, ar, err := s.RecordManagedRevision(ManagedRevision{Source: SourceReload, Digest: managedA}); err != nil || again != rel || ar != r {
		t.Fatalf("replayed reload = %+v, %+v, %v", again, ar, err)
	}
	if v, _ := s.ManagedView(); v.Authorization(managedA) == nil || *v.Authorization(managedA) != rel || v.Authorization(managedB) != nil {
		t.Fatalf("authorization = %+v", v)
	}
	app, _, err := s.RecordManagedRevision(ManagedRevision{Source: SourceGuildmasterApproved, Digest: managedB, Decision: "managed-b"})
	if err != nil || app.Decision != "managed-b" {
		t.Fatalf("approved = %+v, %v", app, err)
	}
	// Leaving drops every authorization; a re-enrolment starts with none.
	if _, _, err := s.RecordEnrolment(leave(managedA)); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.ManagedView(); v.Authorization(managedA) != nil || len(v.Revisions) != 0 {
		t.Fatalf("view after leave = %+v", v)
	}
	if _, _, err := s.RecordEnrolment(enrol("hq", managedA)); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.ManagedView(); v.Authorization(managedA) != nil || len(v.Revisions) != 0 {
		t.Fatalf("view after re-enrol = %+v", v)
	}
	if rel2, _, err := s.RecordManagedRevision(ManagedRevision{Source: SourceReload, Digest: managedA}); err != nil || rel2.Enrolment != 3 || rel2.Sequence != 4 {
		t.Fatalf("reload under the new enrolment = %+v, %v", rel2, err)
	}
	for name, m := range map[string]ManagedRevision{
		"unknown source":       {Source: "rollout", Digest: managedA},
		"approval no decision": {Source: SourceGuildmasterApproved, Digest: managedA},
		"reload with decision": {Source: SourceReload, Digest: managedA, Decision: "d-1"},
		"bad digest":           {Source: SourceReload, Digest: "sha256:x"},
		"bad previous":         {Source: SourceReload, Digest: managedA, Previous: "x"},
		"store set":            {Source: SourceReload, Digest: managedA, Enrolment: 1},
	} {
		if _, _, err := s.RecordManagedRevision(m); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

// TestDoctorDecodesManagedRecords: store doctor decodes both managed-mode buckets and reports a record that does
// not decode.
func TestDoctorDecodesManagedRecords(t *testing.T) {
	for _, bucket := range [][]byte{bucketEnrolments, bucketManagedRevisions} {
		s, _ := newStore(t)
		if err := s.db.Update(func(tx *bolt.Tx) error {
			b, err := createDirectorBucket(tx, bucket)
			if err != nil {
				return err
			}
			return b.Put(seqKey(1), []byte("{not json"))
		}); err != nil {
			t.Fatal(err)
		}
		rep, err := s.Doctor()
		if err != nil || len(rep.Problems) != 1 || !strings.Contains(rep.Problems[0], "director "+string(bucket)) {
			t.Fatalf("doctor of a bad %s record = %+v, %v", bucket, rep, err)
		}
	}
}

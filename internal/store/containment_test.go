package store

import (
	"errors"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/artisan"
)

var worker = artisan.Identity{PID: 303, Token: "boot:3"}

// running returns launcherA's running record of attempt 1 contained by c.
func running(c Containment) Ownership {
	o := sampleOwnership(launcherA)
	o.Attempt, o.State, o.Containment = 1, OwnerRunning, c
	return o
}

func TestContainmentRoundTrips(t *testing.T) {
	for _, c := range []Containment{
		{Kind: ContainmentJob, Name: `Local\guildhall-a1`},
		{Kind: ContainmentCgroup, Name: "/sys/fs/cgroup/app.slice/guildhall-a1", Reason: "cgroup v2 delegated", Worker: worker},
		{Kind: ContainmentSubreaper, Reason: "no cgroup v2 mounted at /sys/fs/cgroup", Worker: worker},
		{Kind: ContainmentGroup, Worker: worker},
	} {
		t.Run(c.Kind, func(t *testing.T) {
			if err := c.Recoverable(); err != nil {
				t.Fatalf("Recoverable() = %v", err)
			}
			s, _ := newStore(t)
			ownedAttempt(t, s)
			want := running(c)
			if _, err := s.UpdateOwnership("m-run", want); err != nil {
				t.Fatalf("UpdateOwnership: %v", err)
			}
			if got := mustOwnership(t, s, 1); got != want {
				t.Fatalf("ownership = %+v, want %+v", got, want)
			}
		})
	}
}

func TestContainmentRequiresRecoveryEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    Containment
	}{
		{"unknown kind", Containment{Kind: "vm", Name: "guildhall-a1", Worker: worker}},
		{"no kind", Containment{Name: "4242"}},
		{"job without name", Containment{Kind: ContainmentJob, Worker: worker}},
		{"cgroup without name", Containment{Kind: ContainmentCgroup, Reason: "cgroup v2 delegated", Worker: worker}},
		{"group without worker", Containment{Kind: ContainmentGroup}},
		{"subreaper without worker", Containment{Kind: ContainmentSubreaper, Reason: "no cgroup v2"}},
		{"group with a group id for a name", Containment{Kind: ContainmentGroup, Name: "4242", Worker: worker}},
		{"subreaper with a name", Containment{Kind: ContainmentSubreaper, Name: "4242"}},
		{"worker without pid", Containment{Kind: ContainmentGroup, Worker: artisan.Identity{Token: "boot:3"}}},
		{"worker without token", Containment{Kind: ContainmentGroup, Worker: artisan.Identity{PID: 303}}},
		{"blank name", Containment{Kind: ContainmentJob, Name: " "}},
		{"long name", Containment{Kind: ContainmentCgroup, Name: strings.Repeat("n", MaxContainmentBytes+1)}},
		{"invalid reason", Containment{Kind: ContainmentGroup, Reason: "\xff", Worker: worker}},
		{"long reason", Containment{Kind: ContainmentGroup, Reason: strings.Repeat("r", MaxContainmentBytes+1), Worker: worker}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.c.Recoverable(); !errors.Is(err, ErrUnrecoverable) {
				t.Fatalf("Recoverable() = %v, want ErrUnrecoverable", err)
			}
			s, _ := newStore(t)
			ownedAttempt(t, s)
			_, err := s.UpdateOwnership("m-run", running(tc.c))
			if !errors.Is(err, ErrInvalid) || !errors.Is(err, ErrUnrecoverable) {
				t.Fatalf("UpdateOwnership: err = %v, want ErrInvalid and ErrUnrecoverable", err)
			}
			if got := mustOwnership(t, s, 1); got.Containment != (Containment{}) {
				t.Fatalf("refused update recorded %+v", got.Containment)
			}
		})
	}
}

// putRaw replaces attempt 1's ownership record with raw JSON, as an older
// Guildhall wrote it.
func putRaw(t *testing.T, s *Store, raw string) {
	t.Helper()
	err := s.db.Update(func(tx *bolt.Tx) error {
		owners, err := createRepoBucket(tx, "app", bucketOwners)
		if err != nil {
			return err
		}
		return owners.Put(attemptKey("t-1", "build", 1), []byte(raw))
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestLegacyContainmentRecords: records whose containment held only a kind and
// a name still read; a named one stays recoverable, and a process group named
// by its group id, with no worker identity, refuses a take-over even though
// its launcher has exited.
func TestLegacyContainmentRecords(t *testing.T) {
	for _, tc := range []struct {
		name, containment string
		want              Containment
		recoverable       bool
	}{
		{"job", `{"kind":"job","name":"Local\\guildhall-a1"}`, Containment{Kind: ContainmentJob, Name: `Local\guildhall-a1`}, true},
		{"group id", `{"kind":"pgroup","name":"4242"}`, Containment{Kind: ContainmentGroup, Name: "4242"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStore(t)
			ownedAttempt(t, s)
			putRaw(t, s, `{"repository":"app","task_id":"t-1","step":"build","attempt":1,`+
				`"launcher_identity":{"pid":101,"token":"boot:1"},"containment":`+tc.containment+`,"state":"running"}`)
			got := mustOwnership(t, s, 1)
			if got.Containment != tc.want {
				t.Fatalf("containment = %+v, want %+v", got.Containment, tc.want)
			}
			if rep, err := s.Doctor(); err != nil || len(rep.Problems) != 0 {
				t.Fatalf("doctor = %+v, %v", rep, err)
			}
			calls := 0
			_, err := s.TakeOwnership("m-take", takeB(1), probeSays(artisan.Gone, nil, &calls))
			if tc.recoverable {
				if err != nil {
					t.Fatalf("take from an exited launcher: %v", err)
				}
				return
			}
			if !errors.Is(got.Containment.Recoverable(), ErrUnrecoverable) {
				t.Errorf("Recoverable() = %v, want ErrUnrecoverable", got.Containment.Recoverable())
			}
			if !errors.Is(err, ErrOwnerUnknown) || !errors.Is(err, ErrUnrecoverable) {
				t.Fatalf("take: err = %v, want ErrOwnerUnknown and ErrUnrecoverable", err)
			}
			if calls != 0 {
				t.Errorf("probe ran %d times; incomplete evidence refuses without probing", calls)
			}
			if after := mustOwnership(t, s, 1); after != got {
				t.Fatalf("refused take changed the record to %+v", after)
			}
		})
	}
}

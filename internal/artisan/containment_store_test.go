package artisan_test

import (
	"crypto/rand"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// recoveryRecord is the launcher's field-for-field copy of a unit's
// containment and its worker into the store's recovery description.
func recoveryRecord(c artisan.Containment, worker artisan.Identity) store.Containment {
	return store.Containment{Kind: c.Kind, Name: c.Name, Reason: c.Reason, Worker: store.ProcessIdentity(worker)}
}

// launcherProbe is the launcher composition's owner probe: the store's
// conservative policy over Probe, mapping its verdict to the store's word.
var launcherProbe = store.ProbeLauncher(func(id store.ProcessIdentity) (store.Liveness, error) {
	lv, err := artisan.Probe(artisan.Identity(id))
	return store.Liveness(lv.String()), err
})

// TestStoreLivenessIsProbeVerdict pins that the store's liveness values are
// the words Probe reports, so a launcher composition maps a verdict by name.
func TestStoreLivenessIsProbeVerdict(t *testing.T) {
	for lv, want := range map[artisan.Liveness]store.Liveness{
		artisan.Gone:       store.ProbeGone,
		artisan.Alive:      store.ProbeAlive,
		artisan.Unverified: store.ProbeUnverified,
	} {
		if got := store.Liveness(lv.String()); got != want {
			t.Errorf("artisan %d maps to store %q, want %q", lv, got, want)
		}
	}
}

// ownedStore opens a store holding attempt 1 of app/t-1 build, owned and
// started by self.
func ownedStore(t *testing.T, self artisan.Identity) (*store.Store, store.Ownership) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "guildhall.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	digest := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := st.CreateTask("m-task", store.Task{Repository: "app", ID: "t-1", Title: "Containment", Workflow: workflow.Revision{Name: "default", Digest: digest}, ConfigDigest: digest}); err != nil {
		t.Fatal(err)
	}
	own := store.Ownership{Repository: "app", TaskID: "t-1", Step: "build", LauncherIdentity: store.ProcessIdentity(self)}
	if _, err := st.StartOwnedAttempt("m-start", own); err != nil {
		t.Fatal(err)
	}
	own.Attempt = 1
	return st, own
}

// persist records c as the running owner's containment and reads it back.
func persist(t *testing.T, st *store.Store, own store.Ownership, c store.Containment) {
	t.Helper()
	own.Containment, own.State = c, store.OwnerRunning
	if _, err := st.UpdateOwnership("m-run", own); err != nil {
		t.Fatalf("UpdateOwnership(%+v): %v", c, err)
	}
	got, err := st.Ownership("app", "t-1", "build", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Containment != c {
		t.Fatalf("stored containment = %+v, want %+v", got.Containment, c)
	}
	if err := got.Containment.Recoverable(); err != nil {
		t.Fatalf("stored containment is not recoverable: %v", err)
	}
}

// TestContainmentPersistsAsOwnership: every declared backend's containment,
// as the backend reports it, is stored losslessly as an ownership record.
func TestContainmentPersistsAsOwnership(t *testing.T) {
	self := mustSelf(t)
	worker := artisan.Identity{PID: self.PID + 1, Token: self.Token + "-worker"}
	for _, c := range []artisan.Containment{
		{Kind: artisan.KindJob, Name: `Local\guildhall-a1`},
		{Kind: artisan.KindCgroup, Name: "/sys/fs/cgroup/app.slice/guildhall-a1", Reason: "cgroup v2 delegated by the systemd user manager"},
		{Kind: artisan.KindSubreaper, Reason: "no cgroup v2 mounted at /sys/fs/cgroup"},
		{Kind: artisan.KindGroup},
	} {
		t.Run(c.Kind, func(t *testing.T) {
			st, own := ownedStore(t, self)
			persist(t, st, own, recoveryRecord(c, worker))
		})
	}
}

// TestPlatformUnitPersistsAsOwnership stores this platform's backend's
// containment and its started worker's identity.
func TestPlatformUnitPersistsAsOwnership(t *testing.T) {
	// The inventory dir is created first so its removal runs after Close
	// has stopped the worker that writes into it.
	dir := t.TempDir()
	u, err := artisan.NewUnit("persist-" + rand.Text()[:8])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := u.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	worker, err := u.Start(artisantest.Command(dir, artisantest.Worker{}))
	if err != nil {
		t.Fatal(err)
	}
	st, own := ownedStore(t, mustSelf(t))
	persist(t, st, own, recoveryRecord(u.Containment(), worker))
}

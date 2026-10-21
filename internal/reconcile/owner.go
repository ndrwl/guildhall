package reconcile

import (
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/store"
)

// Owner is what the owner-gone rule finds for an ownership record: the one
// place that probes the recorded launcher, translates the record's
// containment and asks artisan.Contained for its survivors. Take-over
// (launch.ProbeOwner), classification and the resume sweep all read it.
type Owner struct {
	// Launcher is the recorded launcher identity's liveness, with the
	// probe's error.
	Launcher artisan.Liveness
	ProbeErr error
	// Recorded is whether a containment is recorded; Unit and Worker are it.
	Recorded bool
	Unit     artisan.Containment
	Worker   artisan.Identity
	// Survivors are the unit's live processes and UnitErr why they could not
	// be listed; both are checked only once the launcher is gone and a
	// containment is recorded.
	Survivors []artisan.Identity
	UnitErr   error
}

// ProbeOwner applies the rule to o.
func ProbeOwner(o store.Ownership) Owner {
	var w Owner
	w.Launcher, w.ProbeErr = artisan.Probe(artisan.Identity(o.LauncherIdentity))
	c := o.Containment
	w.Recorded = c != (store.Containment{})
	w.Unit, w.Worker = artisan.Containment{Kind: c.Kind, Name: c.Name, Reason: c.Reason}, artisan.Identity(c.Worker)
	if w.Launcher == artisan.Gone && w.ProbeErr == nil && w.Recorded {
		w.Survivors, w.UnitErr = artisan.Contained(w.Unit, w.Worker)
	}
	return w
}

// Gone reports that the owner is proven exited: its launcher is gone and its
// recorded unit is proven empty. Missing containment proves nothing.
func (w Owner) Gone() bool {
	return w.Launcher == artisan.Gone && w.ProbeErr == nil && w.Recorded && w.UnitErr == nil && len(w.Survivors) == 0
}

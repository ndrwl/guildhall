package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/reconcile"
)

// PaneLauncherFile is the file the pane bridge (Present) writes in the attempt directory as soon as it has
// started its one owned launcher: that launcher's artisan.Identity, created exclusively and never rewritten. A
// claim is typed into its pane once, so this records the only launcher a bound PaneClaimFile claim can have; once
// it probes artisan.Gone with no ownership recorded, no launcher of that claim can still own the attempt (d221).
const PaneLauncherFile = "pane-launcher.json"

// AbandonPaneClaimFlag is the `artisan resume` flag by which the operator asserts that no launcher of a bound pane
// claim will appear, so resume sets the claim aside although its launcher is not proven gone.
const AbandonPaneClaimFlag = "--abandon-pane-claim"

// The states of the launcher a bound pane claim's bridge recorded, as PaneLauncherState reports them.
const (
	// PaneLauncherGone is a recorded launcher proven exited.
	PaneLauncherGone = "gone"
	// PaneLauncherUnrecorded is a claim whose bridge recorded no launcher: the typed line never ran, or ran on a
	// release before PaneLauncherFile.
	PaneLauncherUnrecorded = "unrecorded"
	// PaneLauncherLive is a recorded launcher still running.
	PaneLauncherLive = "live"
	// PaneLauncherUnverified is a record that cannot be read or a launcher whose probe failed.
	PaneLauncherUnverified = "unverified"
)

// The ResumeReport.PaneClaim values.
const (
	// PaneClaimAbandoned is a bound pane claim with no owner that resume set aside, so the next start launches.
	PaneClaimAbandoned = "abandoned"
)

// recordPaneLauncher writes id as dir's PaneLauncherFile, refusing one already there.
func recordPaneLauncher(dir string, id artisan.Identity) error {
	b, err := json.Marshal(id)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, PaneLauncherFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return errors.Join(err, f.Close())
}

// PaneLauncherState reports the state of the launcher dir's PaneLauncherFile records, one of the PaneLauncher
// constants; the error says why it is PaneLauncherUnverified.
func PaneLauncherState(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, PaneLauncherFile))
	if errors.Is(err, fs.ErrNotExist) {
		return PaneLauncherUnrecorded, nil
	}
	if err != nil {
		return PaneLauncherUnverified, err
	}
	var id artisan.Identity
	if err := json.Unmarshal(b, &id); err != nil || id.PID <= 0 || id.Token == "" {
		return PaneLauncherUnverified, fmt.Errorf("%s does not record a launcher identity: %s", PaneLauncherFile, strings.TrimSpace(string(b)))
	}
	switch live, err := artisan.Probe(id); {
	case err != nil:
		return PaneLauncherUnverified, err
	case live == artisan.Gone:
		return PaneLauncherGone, nil
	case live == artisan.Alive:
		return PaneLauncherLive, nil
	}
	return PaneLauncherUnverified, nil
}

// boundPaneClaim is at's PaneClaimFile when it holds a tab binding, nil when there is none or it is any other claim.
func boundPaneClaim(at Attempt) *adapter.TabRef {
	c, err := ReadPaneClaim(filepath.Join(at.Dir(), PaneClaimFile))
	if err != nil {
		return nil
	}
	return c.Bound
}

// HeldPaneClaimReason is the classification reason of an attempt with no ownership record and a bound pane claim
// whose launcher is in state, as PaneLauncherState reported it with err.
func HeldPaneClaimReason(state string, err error) string {
	const held = "pane claim held, no owner"
	switch state {
	case PaneLauncherGone:
		return held + "; its launcher was proven gone"
	case PaneLauncherUnrecorded:
		return held + "; its launcher was never recorded"
	case PaneLauncherLive:
		return held + "; its launcher is still live"
	}
	if err != nil {
		return fmt.Sprintf("%s; its launcher could not be verified: %v", held, err)
	}
	return held + "; its launcher could not be verified"
}

// HeldPaneClaim reports whether at has a bound pane claim, and if so the state of the launcher its bridge recorded
// and the classification reason naming it. It reads only files; the caller knows at has no ownership record.
func HeldPaneClaim(at Attempt) (held bool, state, reason string) {
	if boundPaneClaim(at) == nil {
		return false, "", ""
	}
	state, err := PaneLauncherState(at.Dir())
	return true, state, HeldPaneClaimReason(state, err)
}

// PaneClaimAge is how long ago at's bound pane claim was last written, false when it has none.
func PaneClaimAge(at Attempt, now time.Time) (time.Duration, bool) {
	if boundPaneClaim(at) == nil {
		return 0, false
	}
	st, err := os.Stat(filepath.Join(at.Dir(), PaneClaimFile))
	if err != nil {
		return 0, false
	}
	return now.Sub(st.ModTime()), true
}

// ResumeFlags are the `artisan resume` flags that name at.
func ResumeFlags(at Attempt) string {
	return fmt.Sprintf("--repo %s --task %s --step %s --attempt %d", at.Key.Repository, at.Key.TaskID, at.Key.Step, at.Key.Attempt)
}

// errUnrecoveredClaim is the refusal of a bound claim with no owner that cannot be recovered automatically, naming
// why and the command that recovers it.
func errUnrecoveredClaim(at Attempt, why string) error {
	return fmt.Errorf("the bound pane claim was not recovered automatically: %s; once no launcher of it can appear, run `guildhall artisan resume %s %s`", why, ResumeFlags(at), AbandonPaneClaimFlag)
}

// unrecoverableBound is why a bound claim with no owner whose launcher is in state is not recovered automatically.
func unrecoverableBound(state, reason string) string {
	if state == PaneLauncherUnrecorded {
		return "its launcher was never recorded, so one may still start from the typed line"
	}
	return strings.TrimPrefix(reason, "pane claim held, no owner; ")
}

// abandonedName is the first free "<base>.abandoned-<n><ext>" name in dir for both claim files, n from 1.
func abandonedNames(dir string) (claim, launcher string) {
	for n := 1; ; n++ {
		claim = filepath.Join(dir, fmt.Sprintf("pane-launch.abandoned-%d.json", n))
		launcher = filepath.Join(dir, fmt.Sprintf("pane-launcher.abandoned-%d.json", n))
		_, ce := os.Lstat(claim)
		_, le := os.Lstat(launcher)
		if errors.Is(ce, fs.ErrNotExist) && errors.Is(le, fs.ErrNotExist) {
			return claim, launcher
		}
	}
}

// setAside keeps dir's bound claim, and its launcher record if any, as abandoned evidence. With relaunch the claim
// is then overwritten with a fresh launching claim, so it is never absent; otherwise it is removed, so the next
// start claims afresh. The caller holds the task and attempt locks.
func setAside(dir string, relaunch bool) error {
	claimPath := filepath.Join(dir, PaneClaimFile)
	b, err := os.ReadFile(claimPath)
	if err != nil {
		return err
	}
	abandonedClaim, abandonedLauncher := abandonedNames(dir)
	f, err := os.OpenFile(abandonedClaim, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(dir, PaneLauncherFile), abandonedLauncher); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if relaunch {
		return os.WriteFile(claimPath, []byte(launchingClaim), 0o600)
	}
	return os.Remove(claimPath)
}

// checkUnowned takes at's attempt lock and reports, under it, whether at has no ownership record and the state of
// its bound claim's recorded launcher; release frees the lock and is nil on an error.
func checkUnowned(ctx context.Context, at Attempt) (release func(), state, reason string, err error) {
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	release, err = artisan.LockAttempt(lockCtx, at.Dir())
	if err != nil {
		return nil, "", "", fmt.Errorf("attempt lock: %w", err)
	}
	o, err := at.owner()
	switch {
	case err != nil:
		release()
		return nil, "", "", err
	case o != nil:
		release()
		return nil, "", "", fmt.Errorf("%s is owned (%s): its pane claim is kept", at.Key, o.State)
	}
	held, state, reason := HeldPaneClaim(at)
	if !held {
		release()
		return nil, "", "", fmt.Errorf("%s holds no bound pane claim", at.Key)
	}
	return release, state, reason, nil
}

// closeBound closes the workspace of ref, a bound claim's tab, best effort and bounded by abandonedCloseTimeout,
// as closeAbandoned closes a launching claim's.
func (pr *Presentation) closeBound(ctx context.Context, at Attempt, ref adapter.TabRef) (bool, string) {
	return pr.closeAbandoned(ctx, at, Launching{Workspace: ref.WorkspaceID, Tabs: []adapter.TabRef{ref}})
}

// recoverBound recovers at's bound claim for a relaunch by a caller holding the task lock, once, under the attempt
// lock, no ownership is recorded and the claim's recorded launcher is proven gone: it closes the claim's workspace,
// sets the claims aside with a fresh launching claim in their place, and reports the recovery. ok is false, with
// why, when the claim is not recoverable.
func (p *Process) recoverBound(ctx context.Context, at Attempt, pr *Presentation, ref adapter.TabRef) (ok bool, why string, err error) {
	release, state, reason, err := checkUnowned(ctx, at)
	if err != nil {
		return false, err.Error(), nil
	}
	defer release()
	if state != PaneLauncherGone {
		return false, unrecoverableBound(state, reason), nil
	}
	closed, closeReason := pr.closeBound(ctx, at, ref)
	if err := setAside(at.Dir(), true); err != nil {
		return false, "", err
	}
	pr.recoveredBound(at, ref.WorkspaceID, closed, closeReason)
	return true, "", nil
}

// recoveredBound warns that at's bound claim, whose launcher was proven gone before owning, was recovered, and
// what became of its workspace, and appends that to at's events.
func (pr *Presentation) recoveredBound(at Attempt, workspace string, closed bool, reason string) {
	if closed {
		pr.warn("recovered a bound pane claim for %s: its launcher was proven gone before owning the attempt; closed the Herdr workspace %s it was typed in", at.Key, workspace)
	} else {
		pr.warn("recovered a bound pane claim for %s: its launcher was proven gone before owning the attempt; left open the Herdr workspace %s %q it was typed in (%s), so close it by hand", at.Key, workspace, at.workspaceLabel(), reason)
	}
	if err := artisan.LogPaneClaimRecovered(at.Dir(), at.workspaceLabel(), workspace, closed, reason); err != nil {
		pr.warn("record pane claim recovery for %s: %v", at.Key, err)
	}
}

// PaneClaimReport is what AbandonPaneClaim found and did.
type PaneClaimReport struct {
	// Launcher is the claim's recorded launcher's state, one of the PaneLauncher constants.
	Launcher string
	// Workspace is the claim's Herdr workspace, and Closed whether it is closed or already gone; CloseReason says
	// why it was left open.
	Workspace   string
	Closed      bool
	CloseReason string
}

// AbandonPaneClaim sets aside at's bound pane claim, which has no owner, so the next start launches the attempt;
// the caller holds the task lock. Under the attempt lock it re-checks that no ownership is recorded. A claim whose
// recorded launcher is proven gone is set aside, its workspace closed first, best effort, through pr when it is
// not nil. Any other claim is refused unless force, the operator's assertion that no launcher will appear; then pr
// must close the workspace and confirm the bound pane is gone, or the claim is refused. It never removes an owner
// and never launches.
func AbandonPaneClaim(ctx context.Context, at Attempt, pr *Presentation, force bool) (PaneClaimReport, error) {
	var rep PaneClaimReport
	ref := boundPaneClaim(at)
	if ref == nil {
		return rep, fmt.Errorf("launch: %s holds no bound pane claim", at.Key)
	}
	rep.Workspace = ref.WorkspaceID
	release, state, reason, err := checkUnowned(ctx, at)
	if err != nil {
		return rep, fmt.Errorf("launch: abandon pane claim of %s: %w", at.Key, err)
	}
	defer release()
	rep.Launcher = state
	if state != PaneLauncherGone && !force {
		return rep, fmt.Errorf("launch: abandon pane claim of %s: %s; if no launcher of it can appear, run again with %s", at.Key, unrecoverableBound(state, reason), AbandonPaneClaimFlag)
	}
	if pr != nil && pr.Host != nil {
		rep.Closed, rep.CloseReason = pr.closeBound(ctx, at, *ref)
	} else {
		rep.CloseReason = "no Herdr session is configured to close it in"
	}
	if state != PaneLauncherGone {
		if pr == nil || pr.Host == nil {
			return rep, fmt.Errorf("launch: abandon pane claim of %s: the bound pane %s cannot be confirmed gone: %s", at.Key, ref.PaneID, rep.CloseReason)
		}
		if gone, why := paneGone(ctx, pr.Host, *ref); !gone {
			return rep, fmt.Errorf("launch: abandon pane claim of %s: the bound pane %s is not confirmed gone: %s", at.Key, ref.PaneID, why)
		}
	}
	if err := setAside(at.Dir(), false); err != nil {
		return rep, fmt.Errorf("launch: abandon pane claim of %s: %w", at.Key, err)
	}
	if err := artisan.LogPaneClaimRecovered(at.Dir(), at.workspaceLabel(), ref.WorkspaceID, rep.Closed, rep.CloseReason); err != nil {
		return rep, fmt.Errorf("launch: record pane claim abandonment of %s: %w", at.Key, err)
	}
	return rep, nil
}

// paneGone reports whether host confirms ref's workspace is no longer listed, and otherwise why not.
func paneGone(ctx context.Context, host adapter.TabHost, ref adapter.TabRef) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, abandonedCloseTimeout)
	defer cancel()
	list, err := host.Workspaces(ctx)
	if err != nil {
		return false, err.Error()
	}
	for _, w := range list {
		if w.ID == ref.WorkspaceID {
			return false, fmt.Sprintf("its Herdr workspace %s is still listed", ref.WorkspaceID)
		}
	}
	return true, ""
}

// classifyFresh is Status for diagnosing a launch that failed, run with its own short context, so a caller's
// expired deadline does not turn the classification into an attempt-lock timeout.
func classifyFresh(ctx context.Context, at Attempt) (reconcile.Classification, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return Status(ctx, at)
}

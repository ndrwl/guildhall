package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/launch"
)

// statusLockWait is the lock_wait of the config at cfgPath, config.DefaultLockWait when there is none or it does
// not read.
func statusLockWait(cfgPath string) time.Duration {
	if cfgPath != "" {
		if cfg, err := reportedConfig(cfgPath); err == nil {
			if w, err := cfg.LockWait(); err == nil {
				return w
			}
		}
	}
	return config.DefaultLockWait
}

// staleHeldPaneClaim reports whether at, an attempt with no ownership record, holds a bound pane claim written
// longer ago than budget by the clock now (nil is wall time), so it is no launch still starting.
func staleHeldPaneClaim(at launch.Attempt, budget time.Duration, now func() time.Time) bool {
	clock := time.Now
	if now != nil {
		clock = now
	}
	age, ok := launch.PaneClaimAge(at, clock())
	return ok && age >= budget
}

// heldPaneClaimCommand is the command that recovers at's bound pane claim with no owner: `artisan resume` with
// flags, and launch.AbandonPaneClaimFlag unless the claim's launcher is proven gone.
func heldPaneClaimCommand(at launch.Attempt, flags string) string {
	command := "guildhall artisan resume " + flags
	if _, state, _ := launch.HeldPaneClaim(at); state != launch.PaneLauncherGone {
		command += " " + launch.AbandonPaneClaimFlag
	}
	return command
}

// resumePaneClaim is `artisan resume`'s recovery of a bound pane claim with no owner, after launch.Resume found no
// ownership record in rep (docs/artisan.md#a-pane-claim-with-no-owner). With no bound claim it changes nothing.
// Otherwise, under the task lock taken with the copy's lock_wait, as task start takes it, it sets the claim aside
// (launch.AbandonPaneClaim), closing its workspace in the copy's Herdr session when one is configured, and reports
// launch.PaneClaimAbandoned; abandon is the operator's launch.AbandonPaneClaimFlag. The next task start launches.
func (a *App) resumePaneClaim(ctx context.Context, at launch.Attempt, copyDir string, abandon bool, rep launch.ResumeReport) (launch.ResumeReport, error) {
	if held, _, _ := launch.HeldPaneClaim(at); !held {
		if abandon {
			return rep, fmt.Errorf("artisan resume: %s: %s holds no bound pane claim", launch.AbandonPaneClaimFlag, at.Key)
		}
		return rep, nil
	}
	var cfg *config.Config
	wait := config.DefaultLockWait
	if copyDir != "" {
		c, err := admitDirector(filepath.Join(copyDir, "config.toml"))
		if err != nil {
			return rep, fmt.Errorf("artisan resume: config: %w", err)
		}
		if wait, err = c.LockWait(); err != nil {
			return rep, fmt.Errorf("artisan resume: config: %w", err)
		}
		cfg = c
	}
	taskDir := filepath.Join(at.StateDir, at.Key.Repository, at.Key.TaskID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return rep, err
	}
	lockCtx, cancel := context.WithTimeout(ctx, wait)
	release, err := artisan.LockAttempt(lockCtx, taskDir)
	cancel()
	if err != nil {
		return rep, fmt.Errorf("lock task %s/%s: %w", at.Key.Repository, at.Key.TaskID, err)
	}
	defer release()
	var pr *launch.Presentation
	if cfg != nil {
		if pr, err = artisanPresentation(ctx, cfg, at.Store, at.StateDir, a.Stderr); err != nil {
			// Closing is best effort for a proven gone launcher; AbandonPaneClaim refuses an unproven one without it.
			a.notef("guildhall: warning: Herdr session unavailable to close the claim's workspace: %v\n", err)
			pr = nil
		}
	}
	pc, err := launch.AbandonPaneClaim(ctx, at, pr, abandon)
	if err != nil {
		return rep, err
	}
	rep.PaneClaim = launch.PaneClaimAbandoned
	if !pc.Closed {
		a.notef("guildhall: warning: left open the Herdr workspace %s of %s's abandoned pane claim (%s), so close it by hand\n", pc.Workspace, at.Key, pc.CloseReason)
	}
	rep.Classification, err = launch.Status(ctx, at)
	return rep, err
}

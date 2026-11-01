package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
)

// managedIncoming is where a rollout copies the binary of the release a managed.toml names before config reload
// installs it: releases/.incoming/<version>/guildhall (guildhall.exe on Windows) under the Guildhall home.
func managedIncoming(h release.Home, version string) string {
	return filepath.Join(h.IncomingDir(), version, release.BinaryName(h.GOOS))
}

// managedTarget is the release the authorized revision cfg names, verified (docs/releases.md#the-pin): already
// installed intact with release_sha256, or else installed by release install from the rollout's copy under
// releases/.incoming/<version>/ against release_sha256. A release installed with another sha256, or damaged, is
// refused, since a release directory is never changed, and so is one with neither an install nor an .incoming copy.
// installed says whether this run installed it.
func (a *App) managedTarget(h release.Home, version, sum string) (r release.Release, installed bool, err error) {
	_, err = h.Check(version)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		from := managedIncoming(h, version)
		if _, err := os.Stat(from); err != nil {
			return release.Release{}, false, fmt.Errorf("release %s is not installed at %s and the rollout left no copy of it at %s to install: %w", version, h.ReleaseDir(version), from, err)
		}
		res, err := h.Install(context.Background(), version, from, sum, a.releaseProbe, a.now(), releaseLockWait)
		if err != nil {
			return release.Release{}, false, fmt.Errorf("install release %s from %s: %w", version, from, err)
		}
		return res.Release, !res.Already, nil
	case err != nil:
		return release.Release{}, false, err
	}
	r, err = h.Runnable(version)
	if err != nil {
		return release.Release{}, false, err
	}
	if r.SHA256 != sum {
		return release.Release{}, false, fmt.Errorf("release %s is installed at %s with sha256 %s, not the %s %s names; a release directory is never changed", version, h.ReleaseDir(version), r.SHA256, sum, config.ReleaseSHA256Key)
	}
	return r, false, nil
}

// applyManagedRelease pins the managed copy in dir, whose store is at storePath, to the release its just authorized
// revision cfg names, from source reload, printing what it did; with no release keys in cfg it changes no pin and
// prints nothing. The target is verified, or installed, first (managedTarget), and only then is the release-pin
// event recorded and release.pin written (setPin), under the store-schema rule; a pin change is followed by prune.
func (a *App) applyManagedRelease(dir, storePath string, cfg *config.Config) error {
	version, sum, ok := cfg.ManagedRelease()
	if !ok {
		return nil
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	h, err := a.releaseHome()
	if err != nil {
		return err
	}
	r, installed, err := a.managedTarget(h, version, sum)
	if err != nil {
		return err
	}
	if installed {
		if _, err := fmt.Fprintf(a.Stdout, "installed release %s (%s) at %s from %s\n", r.Version, shortSHA(r.SHA256), h.ReleaseDir(r.Version), managedIncoming(h, version)); err != nil {
			return err
		}
	}
	ch, err := a.setPin(h, dir, storePath, r, store.PinReload)
	if err != nil {
		return err
	}
	p := ch.Pin
	var line string
	switch {
	case !ch.Changed:
		line = fmt.Sprintf("copy %s already pins %s (%s); its pin is unchanged\n", dir, p.Version, shortSHA(p.SHA256))
	case p.PreviousVersion == "":
		line = fmt.Sprintf("copy %s pins %s (%s), its first pin; new tasks run on it, and guildhall director reload --copy %s moves a running Director onto it\n", dir, p.Version, shortSHA(p.SHA256), dir)
	default:
		line = fmt.Sprintf("copy %s pins %s (%s), was %s; new tasks run on it, and guildhall director reload --copy %s moves a running Director onto it\n", dir, p.Version, shortSHA(p.SHA256), p.PreviousVersion, dir)
	}
	if _, err := fmt.Fprint(a.Stdout, line); err != nil {
		return err
	}
	if ch.Changed {
		a.pruneAfterPin(h, storePath)
	}
	return nil
}

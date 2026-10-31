package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/release"
)

// releaseLockWait bounds each wait for the Guildhall home's lock.
const releaseLockWait = 10 * time.Second

const releaseUsage = `usage: guildhall release install <version> --from <file> --sha256 <hex>
       guildhall release status [--json]`

// releaseCmd runs `guildhall release install|status` (docs/contracts/cli.md#release).
func (a *App) releaseCmd(args []string) int {
	if len(args) == 0 {
		return a.usageError(releaseUsage)
	}
	switch args[0] {
	case "install":
		return a.releaseInstall(args[1:])
	case "status":
		return a.releaseStatus(args[1:])
	}
	return a.usageError(fmt.Sprintf("release: unknown subcommand %q\n%s", args[0], releaseUsage))
}

// releaseHome is the Guildhall home this binary uses (release.Locate).
func (a *App) releaseHome() (release.Home, error) {
	var exe string
	if a.Executable != nil {
		if p, err := a.Executable(); err == nil {
			exe = p
		}
	}
	return release.Locate(a.Getenv, exe, Version(), runtime.GOOS)
}

// releaseProbe runs a binary as `guildhall version --json` through App.Output, bounded by binaryPreflightTimeout.
func (a *App) releaseProbe(ctx context.Context, path string) (release.Report, error) {
	ctx, cancel := context.WithTimeout(ctx, binaryPreflightTimeout)
	defer cancel()
	out, err := a.Output(ctx, path, "version", "--json")
	if err != nil {
		return release.Report{}, errors.New(refusalReason("run", err, out))
	}
	var r release.Report
	if err := json.NewDecoder(strings.NewReader(string(out))).Decode(&r); err != nil || r.Version == "" {
		return release.Report{}, errors.Join(errors.New("no version reported"), err)
	}
	return r, nil
}

// releaseInstall runs `guildhall release install <version> --from <file> --sha256 <hex>`.
func (a *App) releaseInstall(args []string) int {
	fl := flag.NewFlagSet("release install", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	from := fl.String("from", "", "the binary to install")
	sum := fl.String("sha256", "", "its expected sha256")
	if err := parseInterspersed(fl, args); err != nil {
		return a.usageError("release install: " + err.Error())
	}
	switch {
	case fl.NArg() != 1:
		return a.usageError("release install: want one version\n" + releaseUsage)
	case *from == "" || *sum == "":
		return a.usageError("release install: --from and --sha256 are required\n" + releaseUsage)
	}
	h, err := a.releaseHome()
	if err != nil {
		return a.fail(fmt.Errorf("release install: %w", err))
	}
	res, err := h.Install(context.Background(), fl.Arg(0), *from, *sum, a.releaseProbe, a.now(), releaseLockWait)
	if err != nil {
		return a.fail(fmt.Errorf("release install: %w", err))
	}
	r := res.Release
	line := fmt.Sprintf("installed %s (sha256 %s) at %s\n", r.Version, r.SHA256, h.ReleaseDir(r.Version))
	if res.Already {
		line = fmt.Sprintf("%s is already installed (sha256 %s) at %s; nothing changed\n", r.Version, r.SHA256, h.ReleaseDir(r.Version))
	}
	if _, err := io.WriteString(a.Stdout, line); err != nil {
		return 1
	}
	return 0
}

// releaseStatusReport is what `release status --json` prints.
type releaseStatusReport struct {
	Home     string            `json:"home"`
	Releases []release.Release `json:"releases"`
	Copies   []string          `json:"copies"`
}

// releaseStatus runs `guildhall release status [--json]`: the home, every installed release, newest install first,
// with any damage, and the inventoried copies. It reads only, never creating the home.
func (a *App) releaseStatus(args []string) int {
	fl := flag.NewFlagSet("release status", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	asJSON := fl.Bool("json", false, "print JSON")
	if err := fl.Parse(args); err != nil {
		return a.usageError("release status: " + err.Error())
	}
	if fl.NArg() > 0 {
		return a.usageError(fmt.Sprintf("release status: unexpected argument %q", fl.Arg(0)))
	}
	h, err := a.releaseHome()
	if err != nil {
		return a.fail(fmt.Errorf("release status: %w", err))
	}
	releases, err := h.List()
	if err != nil {
		return a.fail(fmt.Errorf("release status: %w", err))
	}
	copies, err := h.Copies()
	if err != nil {
		return a.fail(fmt.Errorf("release status: %w", err))
	}
	rep := releaseStatusReport{Home: h.Dir, Releases: releases, Copies: copies}
	if rep.Releases == nil {
		rep.Releases = []release.Release{}
	}
	if rep.Copies == nil {
		rep.Copies = []string{}
	}
	if *asJSON {
		return a.printJSON(rep)
	}
	if _, err := io.WriteString(a.Stdout, rep.plain()); err != nil {
		return 1
	}
	return 0
}

// plain is release status's terminal form.
func (r releaseStatusReport) plain() string {
	var b strings.Builder
	fmt.Fprintf(&b, "home: %s\n", r.Home)
	if len(r.Releases) == 0 {
		b.WriteString("releases: none\n")
	} else {
		b.WriteString("releases:\n")
	}
	for _, rel := range r.Releases {
		if rel.Damage != "" {
			fmt.Fprintf(&b, "  %s  damaged, never run: %s\n", rel.Version, rel.Damage)
			continue
		}
		fmt.Fprintf(&b, "  %s  %s  installed %s  store schema %d  release contract %d\n",
			rel.Version, shortSHA(rel.SHA256), rel.InstalledAt.UTC().Format(time.RFC3339), rel.StoreSchema, rel.ReleaseContract)
	}
	if len(r.Copies) == 0 {
		b.WriteString("copies: none\n")
	} else {
		b.WriteString("copies:\n")
	}
	for _, c := range r.Copies {
		fmt.Fprintf(&b, "  %s\n", c)
	}
	return b.String()
}

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/admit"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
)

// releaseLockWait bounds each wait for the Guildhall home's lock.
const releaseLockWait = 10 * time.Second

const releaseUsage = `usage: guildhall release install <version> --from <file> --sha256 <hex>
       guildhall release install --launcher [--from <file>]
       guildhall release adopt [--copy <dir>] [--launcher <file>]
       guildhall release use <version> [--copy <dir>]
       guildhall release status [--json] [--copy <dir>]`

// releaseCmd runs `guildhall release install|adopt|use|status` (docs/contracts/cli.md#release).
func (a *App) releaseCmd(args []string) int {
	if len(args) == 0 {
		return a.usageError(releaseUsage)
	}
	switch args[0] {
	case "install":
		return a.releaseInstall(args[1:])
	case "adopt":
		return a.releaseAdopt(args[1:])
	case "use":
		return a.releaseUse(args[1:])
	case "status":
		return a.releaseStatus(args[1:])
	}
	return a.usageError(fmt.Sprintf("release: unknown subcommand %q\n%s", args[0], releaseUsage))
}

// CheckReleaseEnv refuses to run this binary when GUILDHALL_RELEASE, which the PATH launcher sets to the release it
// selected, names a version other than its own (docs/releases.md#the-path-launcher), so a mislabelled release
// directory cannot run as another release. Unset, it admits any binary, as one run by its path.
func CheckReleaseEnv(getenv func(string) string) error {
	want := getenv(release.Env)
	if want == "" || want == Version() {
		return nil
	}
	return fmt.Errorf("%s=%s, but this binary is release %s; the launcher selected another release than the one it found, so this release refuses to run", release.Env, want, Version())
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
	launcher := fl.Bool("launcher", false, "install the PATH launcher")
	if err := parseInterspersed(fl, args); err != nil {
		return a.usageError("release install: " + err.Error())
	}
	switch {
	case *launcher && (fl.NArg() != 0 || *sum != ""):
		return a.usageError("release install --launcher takes only --from <file>\n" + releaseUsage)
	case *launcher:
		return a.releaseInstallLauncher(*from)
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
	Home     string               `json:"home"`
	Releases []releaseStatusEntry `json:"releases"`
	Copies   []string             `json:"copies"`
	// Copy is the selected copy's pin, null when the command selected no copy.
	Copy *releaseCopyStatus `json:"copy"`
}

// releaseStatusEntry is one installed release and the inventoried copies whose pin names it.
type releaseStatusEntry struct {
	release.Release
	PinnedBy []string `json:"pinned_by"`
}

// releaseCopyStatus is the selected copy's pin: the version its release.pin names ("" for none), its newest
// release-pin event's version and sha256, and Problem when the two disagree or cannot be read, as the
// configuration-admission boundary refuses them.
type releaseCopyStatus struct {
	Dir          string `json:"dir"`
	Pin          string `json:"pin"`
	EventVersion string `json:"event_version"`
	EventSHA256  string `json:"event_sha256"`
	Problem      string `json:"problem"`
}

// releaseStatus runs `guildhall release status [--json] [--copy <dir>]`: the home, every installed release, newest
// install first, with any damage and the copies pinning it, the inventoried copies and, when a copy is selected,
// its pin. It reads only, never creating the home.
func (a *App) releaseStatus(args []string) int {
	fl := flag.NewFlagSet("release status", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	asJSON := fl.Bool("json", false, "print JSON")
	var cp copyFlags
	cp.register(fl)
	if err := parseInterspersed(fl, args); err != nil {
		return a.usageError("release status: " + err.Error())
	}
	if fl.NArg() > 0 {
		return a.usageError(fmt.Sprintf("release status: unexpected argument %q", fl.Arg(0)))
	}
	var storePath string
	if err := cp.apply(a, &storePath, nil, nil); err != nil {
		return a.fail(fmt.Errorf("release status: %w", err))
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
	rep := releaseStatusReport{Home: h.Dir, Releases: []releaseStatusEntry{}, Copies: copies}
	pins := map[string][]string{}
	for _, c := range copies {
		if v, err := release.ReadPin(c); err == nil {
			pins[v] = append(pins[v], c)
		}
	}
	for _, r := range releases {
		rep.Releases = append(rep.Releases, releaseStatusEntry{Release: r, PinnedBy: append([]string{}, pins[r.Version]...)})
	}
	if rep.Copies == nil {
		rep.Copies = []string{}
	}
	if cp.dir != "" {
		cs, err := readCopyPin(cp.dir, storePath)
		if err != nil {
			return a.fail(fmt.Errorf("release status: %w", err))
		}
		rep.Copy = &cs
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
		for _, c := range rel.PinnedBy {
			fmt.Fprintf(&b, "    pinned by %s\n", c)
		}
	}
	if len(r.Copies) == 0 {
		b.WriteString("copies: none\n")
	} else {
		b.WriteString("copies:\n")
	}
	for _, c := range r.Copies {
		fmt.Fprintf(&b, "  %s\n", c)
	}
	if c := r.Copy; c != nil {
		if c.Pin == "" {
			fmt.Fprintf(&b, "copy: %s has no release pin\n", c.Dir)
		} else {
			fmt.Fprintf(&b, "copy: %s pins %s\n", c.Dir, c.Pin)
		}
		if c.Problem != "" {
			fmt.Fprintf(&b, "  problem: %s\n", c.Problem)
		}
	}
	return b.String()
}

// readCopyPin reads the pin of the copy in dir, whose store is at storePath, and its newest release-pin event,
// read-only; a copy with no store has no event.
func readCopyPin(dir, storePath string) (releaseCopyStatus, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return releaseCopyStatus{}, err
	}
	cs := releaseCopyStatus{Dir: abs}
	cs.Pin, _ = release.ReadPin(abs)
	var newest *store.ReleasePin
	if _, err := os.Stat(storePath); err == nil {
		p, err := withStore(storePath, true, func(s *store.Store) (store.ReleasePin, error) { return s.LatestReleasePin() })
		switch {
		case err == nil:
			newest = &p
			cs.EventVersion, cs.EventSHA256 = p.Version, p.SHA256
		case !errors.Is(err, store.ErrNotFound):
			return releaseCopyStatus{}, err
		}
	}
	cs.Problem = admit.PinRefusal(abs, newest)
	return cs, nil
}

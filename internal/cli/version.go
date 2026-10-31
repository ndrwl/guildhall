package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// VersionGit is the git a version report reads Guildhall's own repository with (docs/contracts/cli.md#version);
// git.Program is the real one.
type VersionGit interface {
	// Subject returns the subject line of the commit id rev, full or a pseudo-version's abbreviation, in the
	// repository at repo.
	Subject(ctx context.Context, repo, rev string) (string, error)
	// FetchDefault records the commit remote's default branch is at as ref in the repository at repo.
	FetchDefault(ctx context.Context, repo, remote, ref string) error
	// Behind returns how many first-parent commits ref has after rev, with their subjects, newest first.
	Behind(ctx context.Context, repo, rev, ref string) (int, []string, error)
}

// versionGitTimeout bounds each git run of a version report, including the fetch of the remote's default branch.
const versionGitTimeout = 5 * time.Second

// versionMainRef is the private ref version fetches the remote's default branch into.
const versionMainRef = "refs/guildhall/version/main"

// versionFetchFile, under the copy's state directory, records when version last fetched versionMainRef.
const versionFetchFile = "version-fetch.json"

// versionNamedPRs is how many missing pull requests the text forms name before "and N more".
const versionNamedPRs = 10

// versionInfo is what `guildhall version --json` prints and `director status --json` shows as version: missing
// values are null or "", never absent.
type versionInfo struct {
	Version    string      `json:"version"`
	Revision   string      `json:"revision"`
	Time       string      `json:"time"`
	Modified   bool        `json:"modified"`
	GoVersion  string      `json:"go_version"`
	Platform   string      `json:"platform"`
	PR         *int        `json:"pr"`
	Task       *string     `json:"task"`
	BehindMain *behindMain `json:"behind_main"`
	// StoreSchema is the newest store schema this binary reads and writes, store.SchemaVersion, and ReleaseContract
	// the release contract it implements, release.Contract; release install records both (docs/releases.md).
	StoreSchema     int `json:"store_schema"`
	ReleaseContract int `json:"release_contract"`
	// repository is the id of Guildhall's own repository in the copy's config, "" when it lists none.
	repository string
	// pin is the copy's release pin, "" for a copy that has not adopted releases, and pinBehind how far that
	// pinned release is behind main, nil when not known.
	pin       string
	pinBehind *behindMain
}

// behindMain is how far the binary's revision is behind the repository's default branch as last fetched.
type behindMain struct {
	Count      int   `json:"count"`
	MissingPRs []int `json:"missing_prs"`
	// AsOf is when the default branch was last fetched, RFC 3339 in UTC, or "" when no fetch was recorded.
	AsOf string `json:"as_of"`
	// stale is set when this report's fetch failed, so the count is as of AsOf rather than now.
	stale bool
}

// versionCmd runs `guildhall version [--startup | --json] [--copy <dir>]` (docs/contracts/cli.md#version).
func (a *App) versionCmd(args []string) int {
	fl := flag.NewFlagSet("version", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	startup := fl.Bool("startup", false, "print the compact chat form")
	asJSON := fl.Bool("json", false, "print JSON")
	if err := fl.Parse(args); err != nil {
		return a.usageError("version: " + err.Error())
	}
	switch {
	case fl.NArg() > 0:
		return a.usageError(fmt.Sprintf("version: unexpected argument %q", fl.Arg(0)))
	case *startup && *asJSON:
		return a.usageError("version: --startup and --json cannot be used together")
	}
	var storePath, cfgPath, state string
	if err := cp.apply(a, &storePath, &cfgPath, &state); err != nil {
		// A copy that does not resolve gives no repository fields; version never fails for it.
		cfgPath, state, storePath = "", "", ""
	}
	v := a.versionInfo(context.Background(), cfgPath, state, !*asJSON)
	if *asJSON {
		return a.printJSON(v)
	}
	out := v.plain()
	if *startup {
		out = v.startup(a.supervisorBinary(storePath))
	}
	if _, err := io.WriteString(a.Stdout, out); err != nil {
		return 1
	}
	return 0
}

// versionInfo reports the running binary from its build info and, when cfgPath's config lists Guildhall's own
// repository, its latest merge, the task that delivered it from state and how far it is behind the repository's
// default branch: fetched first when fetch is set, else as last fetched. Every repository failure leaves the
// repository fields empty.
func (a *App) versionInfo(ctx context.Context, cfgPath, state string, fetch bool) versionInfo {
	v := versionInfo{Version: Version(), GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
		StoreSchema: store.SchemaVersion, ReleaseContract: release.Contract}
	info, ok := a.buildInfo()
	if !ok {
		return v
	}
	if info.GoVersion != "" {
		v.GoVersion = info.GoVersion
	}
	goos, goarch := runtime.GOOS, runtime.GOARCH
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			v.Revision = s.Value
		case "vcs.time":
			v.Time = s.Value
		case "vcs.modified":
			v.Modified = s.Value == "true"
		case "GOOS":
			goos = s.Value
		case "GOARCH":
			goarch = s.Value
		}
	}
	v.Platform = goos + "/" + goarch
	if v.Revision == "" {
		// A module-mode build, `go install …@<pseudo-version>`, stamps no vcs.* but names its commit and time in
		// its version; the module proxy builds from a clean tree, so it is not modified.
		v.Revision, v.Time = pseudoVersionCommit(info.Main.Version)
	}
	if v.Revision == "" || cfgPath == "" {
		return v
	}
	repo, ok := ownRepository(cfgPath, info.Main.Path)
	if !ok {
		return v
	}
	v.repository = repo.ID
	g := a.versionGit()
	if subject, err := g.Subject(ctx, repo.Path, v.Revision); err == nil {
		if pr, ok := subjectPR(subject); ok {
			v.PR = &pr
			if task, ok := deliveredTask(state, repo.ID, pr); ok {
				v.Task = &task
			}
		}
	}
	fetched := filepath.Join(state, versionFetchFile)
	stale := fetch && !a.fetchMain(ctx, g, repo, state)
	count, subjects, err := g.Behind(ctx, repo.Path, v.Revision, versionMainRef)
	if err != nil {
		return v
	}
	v.BehindMain = newBehind(count, subjects, readFetchTime(fetched, repo.Path), stale)
	// An adopted copy's new work runs its pin, which may not be this binary until the Director reloads: the install
	// item compares the pin with main (docs/releases.md).
	if pin, err := release.ReadPin(filepath.Dir(cfgPath)); err == nil {
		v.pin = pin
		switch rev, _ := pseudoVersionCommit(pin); {
		case pin == v.Version:
			v.pinBehind = v.BehindMain
		case rev != "":
			if count, subjects, err := g.Behind(ctx, repo.Path, rev, versionMainRef); err == nil {
				v.pinBehind = newBehind(count, subjects, v.BehindMain.AsOf, stale)
			}
		}
	}
	return v
}

// newBehind is a behindMain of count first-parent commits with their subjects, naming the pull requests they merged.
func newBehind(count int, subjects []string, asOf string, stale bool) *behindMain {
	m := &behindMain{Count: count, MissingPRs: []int{}, AsOf: asOf, stale: stale}
	for _, s := range subjects {
		if pr, ok := subjectPR(s); ok {
			m.MissingPRs = append(m.MissingPRs, pr)
		}
	}
	return m
}

// pseudoVersion matches a Go pseudo-version in its three forms, vX.0.0-yyyymmddhhmmss-abcdef123456,
// vX.Y.Z-pre.0.yyyymmddhhmmss-abcdef123456 and vX.Y.(Z+1)-0.yyyymmddhhmmss-abcdef123456, with any build suffix.
var pseudoVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+-(?:(?:[0-9A-Za-z-]+\.)*0\.)?([0-9]{14})-([0-9a-f]{12})(?:\+[0-9A-Za-z.-]+)?$`)

// pseudoVersionCommit returns the abbreviated commit and, in RFC 3339 UTC, the commit time a Go pseudo-version
// names; "" for both when version is not one.
func pseudoVersionCommit(version string) (revision, at string) {
	m := pseudoVersion.FindStringSubmatch(version)
	if m == nil {
		return "", ""
	}
	t, err := time.Parse("20060102150405", m[1])
	if err != nil {
		return "", ""
	}
	return m[2], t.UTC().Format(time.RFC3339)
}

// buildInfo is the running binary's build information, from the App's BuildInfo, nil being debug.ReadBuildInfo.
func (a *App) buildInfo() (*debug.BuildInfo, bool) {
	read := a.BuildInfo
	if read == nil {
		read = debug.ReadBuildInfo
	}
	info, ok := read()
	return info, ok && info != nil
}

// versionGit is the App's VersionGit; nil is git.Program bounded by versionGitTimeout.
func (a *App) versionGit() VersionGit {
	if a.VersionGit != nil {
		return a.VersionGit
	}
	return git.Program{Timeout: versionGitTimeout}
}

// fetchMain fetches the default branch of repo, Guildhall's own repository, into versionMainRef with g and records
// when in state's versionFetchFile; it reports whether the fetch succeeded.
func (a *App) fetchMain(ctx context.Context, g VersionGit, repo config.Repository, state string) bool {
	if err := g.FetchDefault(ctx, repo.Path, repo.Remote, versionMainRef); err != nil {
		return false
	}
	writeFetchTime(filepath.Join(state, versionFetchFile), repo.Path, a.now())
	return true
}

// refreshVersionAfterMerge fetches the default branch of Guildhall's own repository when task of repository repoID,
// which an advance just left done, merged into it: repoID is the repository the config at cfgPath lists for the
// running binary's module and the task has a merge.json document under state. Every failure is ignored; the
// fetch lets the next director status count the merge (docs/director.md#status).
func (a *App) refreshVersionAfterMerge(ctx context.Context, cfgPath, state, repoID, task string) {
	info, ok := a.buildInfo()
	if !ok {
		return
	}
	repo, ok := ownRepository(cfgPath, info.Main.Path)
	if !ok || repo.ID != repoID {
		return
	}
	if merges, err := filepath.Glob(filepath.Join(state, "documents", "tasks", repoID, task, "*", "merge.json")); err != nil || len(merges) == 0 {
		return
	}
	a.fetchMain(ctx, a.versionGit(), repo, state)
}

// installItem is the needs_me item saying the binary is behind its repository's default branch as last fetched,
// so it is time for the Guildmaster to install (docs/director.md#status); none when it is not behind or that is not
// known.
func (v versionInfo) installItem() (needsMeItem, bool) {
	m := v.BehindMain
	if v.pin != "" {
		m = v.pinBehind
	}
	if m == nil || m.Count <= 0 || v.repository == "" {
		return needsMeItem{}, false
	}
	if v.pin != "" {
		return needsMeItem{Kind: installKind, Repository: v.repository, Count: m.Count,
			Reason: fmt.Sprintf("this copy's pinned release %s is %d %s behind main%s; on the Guildmaster's word, install the new build with "+
				"guildhall release install <version> --from <file> --sha256 <hex>, pin it with guildhall release use <version>, then run guildhall director reload",
				v.pin, m.Count, plural(m.Count, "merge", "merges"), m.prs()),
			Command: "guildhall release install"}, true
	}
	return needsMeItem{Kind: installKind, Repository: v.repository, Count: m.Count,
		Reason: fmt.Sprintf("the installed guildhall (%s) is %d %s behind main%s; ask the Guildmaster to install",
			v.Revision[:min(8, len(v.Revision))], m.Count, plural(m.Count, "merge", "merges"), m.prs()),
		Command: "guildhall version"}, true
}

// now is the App's clock; nil is wall time.
func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// ownRepository is the one repository of the config at cfgPath whose remote names module, the binary's module
// path; none, more than one or a config that does not load gives none.
func ownRepository(cfgPath, module string) (config.Repository, bool) {
	if module == "" {
		return config.Repository{}, false
	}
	cfg, err := reportedConfig(cfgPath)
	if err != nil {
		return config.Repository{}, false
	}
	var found []config.Repository
	for _, r := range cfg.Repositories {
		if remoteModule(r.Remote) == strings.ToLower(module) {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		return config.Repository{}, false
	}
	return found[0], true
}

// remoteModule writes a git remote as a module path, host/owner/name in lower case: the scheme of a URL, a user,
// the colon of an scp-like git@host:path and a trailing .git or slash are dropped.
func remoteModule(remote string) string {
	r := strings.TrimSpace(remote)
	if scheme, rest, ok := strings.Cut(r, "://"); ok && scheme != "" {
		r = rest
		host, path, _ := strings.Cut(r, "/")
		if _, h, ok := strings.Cut(host, "@"); ok {
			host = h
		}
		r = host + "/" + path
	} else if host, path, ok := strings.Cut(r, ":"); ok && host != "" && !strings.ContainsAny(host, `/\`) {
		if _, h, ok := strings.Cut(host, "@"); ok {
			host = h
		}
		r = host + "/" + strings.TrimLeft(path, "/")
	}
	r = strings.TrimSuffix(strings.TrimSuffix(r, "/"), ".git")
	return strings.ToLower(r)
}

// prSuffix is the " (#N)" a squash merge's subject ends with.
var prSuffix = regexp.MustCompile(`\(#([0-9]+)\)\s*$`)

// subjectPR is the pull request number a commit subject ends with, if any.
func subjectPR(subject string) (int, bool) {
	m := prSuffix.FindStringSubmatch(subject)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// deliveredTask is the task whose pull-request.json document under state records pull request pr of repository
// repo, the first in path order; none when this copy did not deliver it.
func deliveredTask(state, repo string, pr int) (string, bool) {
	if state == "" {
		return "", false
	}
	matches, err := filepath.Glob(filepath.Join(state, "documents", "tasks", repo, "*", "*", "pull-request.json"))
	if err != nil {
		return "", false
	}
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var doc struct {
			Number int `json:"number"`
		}
		if json.Unmarshal(b, &doc) == nil && doc.Number == pr {
			return filepath.Base(filepath.Dir(filepath.Dir(m))), true
		}
	}
	return "", false
}

// fetchRecord is versionFetchFile: when the default branch of the repository at Repository was last fetched.
type fetchRecord struct {
	Repository string    `json:"repository"`
	FetchedAt  time.Time `json:"fetched_at"`
}

// writeFetchTime records a fetch of repo's default branch at t; a record that cannot be written is left out.
func writeFetchTime(path, repo string, t time.Time) {
	if b, err := json.Marshal(fetchRecord{Repository: repo, FetchedAt: t.UTC().Truncate(time.Second)}); err == nil {
		_ = atomicfile.Write(path, append(b, '\n'))
	}
}

// readFetchTime is when repo's default branch was last fetched, RFC 3339 in UTC, or "" with no record of it.
func readFetchTime(path, repo string) string {
	b, err := atomicfile.Read(path)
	if err != nil {
		return ""
	}
	var r fetchRecord
	if json.Unmarshal(b, &r) != nil || r.Repository != repo || r.FetchedAt.IsZero() {
		return ""
	}
	return r.FetchedAt.UTC().Format(time.RFC3339)
}

// startupSupervisor is what `version --startup` compares itself with: the release and binary sha256 the
// supervisor of its session recorded, its own image's sha256, and the guildhall binary that supervisor refused to
// move onto at the reload that started the session, if it refused one.
type startupSupervisor struct {
	Release, SHA256, OwnSHA256 string
	Refused                    store.ReloadRefusal
}

// supervisorBinary reads, read-only from the store at storePath, what the supervisor of the session
// GUILDHALL_SESSION names recorded of its binary; nothing outside a session or when it cannot be read.
func (a *App) supervisorBinary(storePath string) startupSupervisor {
	if storePath == "" || a.Getenv == nil {
		return startupSupervisor{}
	}
	n, err := strconv.Atoi(a.Getenv(session.NumberEnv))
	if err != nil || n < 1 {
		return startupSupervisor{}
	}
	if _, err := os.Stat(storePath); errors.Is(err, fs.ErrNotExist) || err != nil {
		return startupSupervisor{}
	}
	sup, err := withStore(storePath, true, func(s *store.Store) (startupSupervisor, error) {
		d, err := s.DirectorSession(n)
		if err != nil {
			return startupSupervisor{}, err
		}
		sup := startupSupervisor{Release: d.Release, SHA256: d.BinarySHA256}
		if r, err := s.LatestDirectorReload(); err == nil && r.State == store.ReloadApplied && r.NewSession == n {
			sup.Refused = store.ReloadRefusal{SHA256: r.RefusedSHA256, Reason: r.RefusedReason}
		}
		return sup, nil
	})
	if err != nil {
		return startupSupervisor{}
	}
	if sup.SHA256 != "" {
		// An image that cannot be hashed is compared by release, as with a supervisor that recorded no sha256.
		if sup.OwnSHA256, err = a.imageSHA256(); err != nil {
			sup.SHA256 = ""
		}
	}
	return sup
}

// plain is the terminal form: the release line, then the revision, latest merge and behind-main lines it has.
func (v versionInfo) plain() string {
	var b strings.Builder
	fmt.Fprintf(&b, "guildhall %s %s %s\n", v.Version, v.Platform, v.GoVersion)
	if v.Revision != "" {
		var notes []string
		if v.Time != "" {
			notes = append(notes, v.Time)
		}
		if v.Modified {
			notes = append(notes, "modified")
		}
		fmt.Fprintf(&b, "revision: %s", v.Revision)
		if len(notes) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(notes, ", "))
		}
		b.WriteString("\n")
	}
	if v.PR != nil {
		fmt.Fprintf(&b, "latest merge: #%d", *v.PR)
		if v.Task != nil {
			fmt.Fprintf(&b, " (task %s)", *v.Task)
		}
		b.WriteString("\n")
	}
	if m := v.BehindMain; m != nil {
		fmt.Fprintf(&b, "behind main: %d %s%s%s\n", m.Count, plural(m.Count, "merge", "merges"), m.prs(), m.asOf())
	}
	return b.String()
}

// startup is the chat form: one line, then one warning line when the binary is behind main, modified, or not the
// binary its session's supervisor sup recorded, by sha256 or, for a supervisor that recorded none, by release; or
// when that supervisor refused to move onto a new binary at the reload that started the session.
func (v versionInfo) startup(sup startupSupervisor) string {
	parts := []string{"guildhall", v.Version}
	if v.Revision != "" {
		parts[1] = v.Revision[:min(8, len(v.Revision))]
	}
	if v.PR != nil {
		parts = append(parts, fmt.Sprintf("#%d", *v.PR))
	}
	if v.Task != nil {
		parts = append(parts, *v.Task)
	}
	if t, err := time.Parse(time.RFC3339, v.Time); err == nil {
		parts = append(parts, t.UTC().Format(time.DateOnly))
	}
	line := strings.Join(parts, " ")
	if v.Modified {
		line += ", modified"
	}
	var warnings []string
	if m := v.BehindMain; m != nil {
		if m.Count == 0 {
			line += ", up to date with main" + m.asOf()
		} else {
			line += fmt.Sprintf(", %d behind main%s%s", m.Count, m.prs(), m.asOf())
			warnings = append(warnings, fmt.Sprintf("this binary is %d %s behind main", m.Count, plural(m.Count, "merge", "merges")))
		}
	}
	if v.Modified {
		warnings = append(warnings, "this binary was built with uncommitted or untracked files")
	}
	switch {
	case sup.SHA256 != "" && sup.SHA256 != sup.OwnSHA256:
		warnings = append(warnings, fmt.Sprintf("this binary is not the one running this session's supervisor (%s %s)", sup.Release, shortSHA(sup.SHA256)))
	case sup.SHA256 == "" && sup.Release != "" && sup.Release != v.Version:
		warnings = append(warnings, fmt.Sprintf("this binary is not the one running this session's supervisor (%s), which predates moving binaries at a reload; "+
			"run guildhall director stop, then guildhall director start", sup.Release))
	}
	if sup.Refused.Reason != "" {
		warnings = append(warnings, fmt.Sprintf("this session's supervisor refused to move onto guildhall %s at its reload, so it still runs its old binary: %s",
			shortSHA(sup.Refused.SHA256), sup.Refused.Reason))
	}
	line += "\n"
	if len(warnings) > 0 {
		line += "warning: " + strings.Join(warnings, "; ") + "\n"
	}
	return line
}

// prs names the missing pull requests, at most versionNamedPRs of them, as " (#1, #2 and 3 more)"; "" for none.
func (m behindMain) prs() string {
	if len(m.MissingPRs) == 0 {
		return ""
	}
	var names []string
	for _, pr := range m.MissingPRs[:min(versionNamedPRs, len(m.MissingPRs))] {
		names = append(names, fmt.Sprintf("#%d", pr))
	}
	s := strings.Join(names, ", ")
	if more := len(m.MissingPRs) - len(names); more > 0 {
		s += fmt.Sprintf(" and %d more", more)
	}
	return " (" + s + ")"
}

// asOf says when the default branch was fetched, when this report's fetch failed; "" otherwise.
func (m behindMain) asOf() string {
	switch {
	case !m.stale:
		return ""
	case m.AsOf == "":
		return " as of an earlier fetch"
	}
	return " as of " + m.AsOf
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

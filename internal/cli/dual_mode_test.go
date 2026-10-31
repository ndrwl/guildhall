package cli

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
)

// directorModeEnv chooses the mode the Director suites run their copies in (docs/director-modes.md#testing-both-ways):
// unset or "standalone" leaves every copy as its helper makes it, and "managed" makes each copy the shared copy
// helpers return a managed copy with the same effective settings. CI runs this package once each way.
const directorModeEnv = "GUILDHALL_TEST_DIRECTOR_MODE"

// directorModeFlag is directorModeEnv as a test flag, `go test ./internal/cli -director-mode=managed`; the
// environment variable, when set, wins, so a test that sets it empty always runs standalone.
var directorModeFlag = flag.String("director-mode", "", "run the Director suites' copies standalone or managed")

// managedRun reports whether this run's Director suites run their copies managed.
func managedRun() bool {
	if v, ok := os.LookupEnv(directorModeEnv); ok {
		return v == modeManaged
	}
	return *directorModeFlag == modeManaged
}

// maybeManage makes the copy in dir managed, as manageCopy does, when this run is managed, and returns dir.
func maybeManage(t *testing.T, dir string) string {
	t.Helper()
	if managedRun() {
		manageCopy(t, dir)
	}
	return dir
}

// manageCopy turns the standalone copy in dir, its config.toml and a director.store it creates when missing, into a
// managed copy with the same effective settings: each [local] setting moves to managed.toml as an overridable
// managed key with no local value, the store records the copy enrolled with steward "hq" and a reload authorizing that
// managed.toml, as a rollout's `config reload` would. A [local] key is moved by removing its line, so the fixtures
// it is used on keep their local settings one per line.
func manageCopy(t *testing.T, dir string) {
	t.Helper()
	cfgPath := filepath.Join(dir, "config.toml")
	local, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	before, err := config.Parse(local)
	if err != nil {
		t.Fatalf("manageCopy: %v", err)
	}
	settings := map[string]config.ManagedSetting{}
	for k, v := range before.Local {
		// Overridable, as the key was local: a task override of it, which a pinned key refuses, still applies.
		settings[k] = config.ManagedSetting{Value: v, Overridable: true}
		local = regexp.MustCompile(`(?m)^[ \t]*`+regexp.QuoteMeta(k)+`[ \t]*=.*\r?\n`).ReplaceAll(local, nil)
	}
	managed, err := config.EncodeManaged(settings)
	if err != nil {
		t.Fatal(err)
	}
	after, err := config.ParseCombined(local, managed)
	if err != nil {
		t.Fatalf("manageCopy: the managed copy does not load: %v", err)
	}
	for k := range before.Local {
		if want, _ := before.Setting(k); func() string { v, _ := after.Setting(k); return v }() != want {
			t.Fatalf("manageCopy: %s is not %q in the managed copy", k, want)
		}
	}
	writeFile(t, cfgPath, string(local))
	writeFile(t, filepath.Join(dir, managedFile), string(managed))
	enrolManaged(t, dir)
}

// enrolManaged records, in the director.store of the copy in dir, created when missing, that the copy is enrolled
// with steward "hq" and that a reload authorized the managed.toml beside its config, as enrol and a rollout's
// `config reload` would: the copy is then admitted as managed.
func enrolManaged(t *testing.T, dir string) {
	t.Helper()
	managed, err := os.ReadFile(filepath.Join(dir, managedFile))
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(dir, directorStoreFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	d := string(digest.Of(managed))
	if _, _, err := s.RecordEnrolment(store.Enrolment{Event: store.Enrolled, Steward: "hq", Digest: d}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RecordManagedRevision(store.ManagedRevision{Source: store.SourceReload, Digest: d}); err != nil {
		t.Fatal(err)
	}
}

// setLocal sets values under [local] in the config.toml of the copy in dir.
func setLocal(t *testing.T, dir string, values map[string]string) {
	t.Helper()
	path := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if b, err = config.SetLocal(b, values); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(b))
}

// appendFile appends text to the file at path.
func appendFile(t *testing.T, path, text string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(b)+text)
}

// normalizedConfigDigest is the digest a task binds for cfg with managed and local settings folded into their
// effective values, so a standalone and a managed copy with the same effective settings compare equal: the bound
// digest itself serializes the two sections apart and differs between the modes.
func normalizedConfigDigest(t *testing.T, cfg *config.Config) string {
	t.Helper()
	q := *cfg
	q.Local = map[string]string{}
	for k := range cfg.Managed {
		q.Local[k], _ = cfg.Setting(k)
	}
	for k := range cfg.Local {
		q.Local[k], _ = cfg.Setting(k)
	}
	q.Managed = nil
	b, err := json.Marshal(&q)
	if err != nil {
		t.Fatal(err)
	}
	return string(digest.Of(b))
}

// TestDualModeHelperGivesTheSameEffectiveConfig: the managed copy manageCopy makes is admitted as managed, with the
// effective settings and normalized config digest of its standalone twin, and a task created in each binds the
// same workflow and, normalized, the same configuration.
func TestDualModeHelperGivesTheSameEffectiveConfig(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	standalone, managed := newCopy(t), newCopy(t)
	for _, dir := range []string{standalone, managed} {
		setLocal(t, dir, map[string]string{config.LockWaitKey: "45s", "max_running_attempts": "3"})
	}
	manageCopy(t, managed)

	scfg, sadm, err := admitDirectorMode(filepath.Join(standalone, "config.toml"))
	if err != nil || sadm.Mode != modeStandalone {
		t.Fatalf("standalone admission = %+v, %v", sadm, err)
	}
	mcfg, madm, err := admitDirectorMode(filepath.Join(managed, "config.toml"))
	if err != nil || madm.Mode != modeManaged || !madm.Authorized || madm.Steward != "hq" {
		t.Fatalf("managed admission = %+v, %v", madm, err)
	}
	if len(scfg.Local) == 0 || len(mcfg.Managed) != len(scfg.Local) || len(mcfg.Local) != 0 {
		t.Fatalf("fixture moved nothing: standalone local %v, managed %v local %v", scfg.Local, mcfg.Managed, mcfg.Local)
	}
	if a, b := normalizedConfigDigest(t, scfg), normalizedConfigDigest(t, mcfg); a != b {
		t.Fatalf("normalized digests differ: %s, %s", a, b)
	}

	var tasks []store.Task
	for _, dir := range []string{standalone, managed} {
		if code, _, stderr := runTask(t, "new", "--copy", dir, "--repo", "app", "--title", "Sort", "t-1"); code != 0 {
			t.Fatalf("task new in %s: exit %d, %s", dir, code, stderr)
		}
		s, err := store.Open(filepath.Join(dir, directorStoreFile), store.Options{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		task, err := s.Task("app", "t-1")
		_ = s.Close()
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	if tasks[0].Workflow != tasks[1].Workflow || tasks[0].Title != tasks[1].Title {
		t.Fatalf("tasks differ: %+v and %+v", tasks[0], tasks[1])
	}
	if tasks[0].ConfigDigest == tasks[1].ConfigDigest {
		t.Fatal("the bound config digests are equal; the helper's normalization is no longer needed")
	}
}

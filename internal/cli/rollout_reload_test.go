package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/store"
)

// diskFleet is a fleet whose machine files live under root, at their remote paths, and whose remote commands are
// this binary's own commands run on the copy found there, so a rollout's config reload reaches a real Director copy.
// With home set, the remote guildhall uses it as its Guildhall home and reports report from any binary it probes.
type diskFleet struct{ root, home, report string }

func (f diskFleet) local(path string) string { return filepath.Join(f.root, filepath.FromSlash(path)) }

func (f diskFleet) Fetch(ctx context.Context, src steward.Remote, dst string) error {
	data, err := os.ReadFile(f.local(src.Path))
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

func (f diskFleet) Put(ctx context.Context, src string, dst steward.Remote) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(f.local(dst.Path), data, 0o600)
}

func (f diskFleet) Run(ctx context.Context, alias string, argv []string) ([]byte, error) {
	if err := steward.CheckRun(alias, argv); err != nil {
		return nil, err
	}
	if argv[0] != steward.DefaultGuildhall {
		return nil, fmt.Errorf("no %s on this fleet", argv[0])
	}
	args := append(append([]string{}, argv[1:len(argv)-1]...), f.local(argv[len(argv)-1]))
	if f.home != "" {
		var ran []string
		app, stdout, stderr := releaseTestApp(f.home, f.report, &ran)
		if code := app.Run(args); code != 0 {
			return nil, fmt.Errorf("exit status %d: %s", code, strings.TrimSpace(stderr.String()))
		}
		return []byte(stdout.String()), nil
	}
	app, stdout, stderr := newTestApp(nil, nil)
	if code := app.Run(args); code != 0 {
		return nil, fmt.Errorf("exit status %d: %s", code, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// TestRolloutReloadAuthorizesTheEnrolledCopy: on an enrolled copy whose first managed.toml enrolment left
// unauthorized, an approved rollout's apply runs config reload on the copy, so director mode shows it managed with
// the rollout digest authorized by reload, and the receipt shows the reload step.
func TestRolloutReloadAuthorizesTheEnrolledCopy(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	fleet := diskFleet{root: t.TempDir()}
	copyDir := fleet.local("/home/guild/tools")
	if err := os.MkdirAll(copyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(copyDir, "config.toml"), cliCopy)
	first, err := config.EncodeManaged(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(copyDir, config.ManagedFile), string(first))
	app, _, stderr := newTestApp(nil, nil)
	if code := app.Run([]string{"store", "init", filepath.Join(copyDir, directorStoreFile)}); code != 0 {
		t.Fatalf("store init: exit %d, %s", code, stderr)
	}
	enrolOnly(t, copyDir)
	if got := copyMode(t, copyDir); got.Mode != modeManaged || got.Authorized {
		t.Fatalf("mode after enrol = %+v", got)
	}

	cfg, private := writeStewardConfig(t, "schema_version = 1\n[managed.review_rule]\nvalue = \"two reviewers\"\npolicy = true\n")
	app, stdout, stderr := newTestApp(privateWorkTree(private))
	app.Copier, app.Runner = fleet, fleet
	plan := filepath.Join(t.TempDir(), "plan")
	args := []string{"steward", "rollout", "--config", cfg, "--machine", "workstation", "--plan", plan}
	if code := app.Run(append(args, "--dry-run")); code != 0 {
		t.Fatalf("dry run: exit %d, %s", code, stderr)
	}
	saved, err := steward.ReadPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if code := app.Run(append(args, "--apply", "--approve", saved.Digest)); code != 0 {
		t.Fatalf("apply: exit %d, %s\n%s", code, stderr, stdout)
	}

	got := copyMode(t, copyDir)
	want := "sha256:" + saved.Copies[0].AfterSHA256
	if got.Mode != modeManaged || !got.Authorized || got.ManagedDigest != want || got.Refusal != "" ||
		got.LastRevision == nil || got.LastRevision.Source != "reload" || got.LastRevision.Digest != want {
		t.Fatalf("mode after the rollout = %+v, want managed with %s authorized by reload", got, want)
	}
	files, err := filepath.Glob(filepath.Join(plan, "receipt-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("receipts %q, %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var receipt steward.Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	steps := receipt.Copies[0].Steps
	last := steps[len(steps)-1]
	if strings.Join(last.Command, " ") != "ssh -o BatchMode=yes -- guild-ws guildhall config reload --copy /home/guild/tools" ||
		!strings.Contains(last.Output, want+" authorized by reload (event 1)") {
		t.Fatalf("reload step %+v", last)
	}
	if code, stderr := newTaskInRepo(t, copyDir, "cli"); code != 0 {
		t.Fatalf("task new after the rollout: exit %d, %s", code, stderr)
	}
}

// TestRolloutReRunPinsAfterAMissingRelease (H41): a rollout naming a release whose binary is not yet in the copy's
// releases/.incoming/ is reload_failed and incomplete; once the binary is placed there, a re-run finds managed.toml
// unchanged but runs config reload again, which pins the release, and only then reports the copy done. A further
// re-run reloads again, recording no new event and leaving the pin alone.
func TestRolloutReRunPinsAfterAMissingRelease(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	fleet := diskFleet{root: t.TempDir(), home: filepath.Join(t.TempDir(), "home"),
		report: `{"version":"v1.1.0","modified":false,"store_schema":1,"release_contract":2}`}
	copyDir := fleet.local("/home/guild/tools")
	if err := os.MkdirAll(copyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(copyDir, "config.toml"), cliCopy)
	first, err := config.EncodeManaged(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(copyDir, config.ManagedFile), string(first))
	app, _, stderr := newTestApp(nil, nil)
	if code := app.Run([]string{"store", "init", filepath.Join(copyDir, directorStoreFile)}); code != 0 {
		t.Fatalf("store init: exit %d, %s", code, stderr)
	}
	enrolOnly(t, copyDir)

	from, sum := releaseFile(t, "release v1.1.0")
	cfg, private := writeStewardConfig(t, "schema_version = 1\n[managed.release]\nvalue = \"v1.1.0\"\n[managed.release_sha256]\nvalue = \""+sum+"\"\n")
	run := func(approve bool) (int, steward.Receipt) {
		t.Helper()
		app, stdout, stderr := newTestApp(privateWorkTree(private))
		app.Copier, app.Runner = fleet, fleet
		plan := filepath.Join(t.TempDir(), "plan")
		args := []string{"steward", "rollout", "--config", cfg, "--machine", "workstation", "--plan", plan}
		if code := app.Run(append(args, "--dry-run")); code != 0 {
			t.Fatalf("dry run: exit %d, %s", code, stderr)
		}
		args = append(args, "--apply")
		if approve {
			saved, err := steward.ReadPlan(plan)
			if err != nil {
				t.Fatal(err)
			}
			args = append(args, "--approve", saved.Digest)
		}
		code := app.Run(args)
		t.Logf("apply: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
		files, err := filepath.Glob(filepath.Join(plan, "receipt-*.json"))
		if err != nil || len(files) != 1 {
			t.Fatalf("receipts %q, %v", files, err)
		}
		var receipt steward.Receipt
		if err := json.Unmarshal([]byte(readText(t, files[0])), &receipt); err != nil {
			t.Fatal(err)
		}
		return code, receipt
	}

	code, receipt := run(true)
	if c := receipt.Copies[0]; code == 0 || c.Status != steward.StatusReloadFailed || !strings.Contains(c.Error, "the rollout left no copy of it") {
		t.Fatalf("first apply: exit %d, copy %+v, want reload_failed for the missing release", code, c)
	}
	if pin, err := release.ReadPin(copyDir); err == nil {
		t.Fatalf("the copy pins %q before its release is in place", pin)
	}

	h := release.Home{Dir: fleet.home, GOOS: runtime.GOOS}
	incoming := managedIncoming(h, "v1.1.0")
	if err := os.MkdirAll(filepath.Dir(incoming), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, incoming, readText(t, from))

	code, receipt = run(false)
	c := receipt.Copies[0]
	if code != 0 || c.Status != steward.StatusUnchanged || c.Error != "" || len(c.Steps) != 1 ||
		strings.Join(c.Steps[0].Command, " ") != "ssh -o BatchMode=yes -- guild-ws guildhall config reload --copy /home/guild/tools" ||
		!strings.Contains(c.Steps[0].Output, "pins v1.1.0") {
		t.Fatalf("re-run: exit %d, copy %+v, want unchanged with one reload that pins v1.1.0", code, c)
	}
	if pin, err := release.ReadPin(copyDir); err != nil || pin != "v1.1.0" {
		t.Fatalf("release.pin after the re-run = %q, %v", pin, err)
	}
	mode := copyMode(t, copyDir)
	want := "sha256:" + c.AfterSHA256
	if !mode.Authorized || mode.ManagedDigest != want || mode.LastRevision == nil {
		t.Fatalf("mode after the re-run = %+v, want %s authorized", mode, want)
	}
	pins := func() []store.ReleasePin {
		t.Helper()
		pins, err := withStore(filepath.Join(copyDir, directorStoreFile), true, func(s *store.Store) ([]store.ReleasePin, error) { return s.ReleasePins() })
		if err != nil {
			t.Fatal(err)
		}
		return pins
	}
	before := pins()

	code, receipt = run(false)
	if c := receipt.Copies[0]; code != 0 || c.Status != steward.StatusUnchanged || len(c.Steps) != 1 || !strings.Contains(c.Steps[0].Output, "already pins v1.1.0") {
		t.Fatalf("third run: exit %d, copy %+v", code, c)
	}
	if after := copyMode(t, copyDir); after.LastRevision == nil || after.LastRevision.Sequence != mode.LastRevision.Sequence {
		t.Errorf("the third run recorded a managed revision: %+v, was %+v", after.LastRevision, mode.LastRevision)
	}
	if after := pins(); len(after) != len(before) {
		t.Errorf("the third run recorded a pin: %+v, was %+v", after, before)
	}
	if pin, err := release.ReadPin(copyDir); err != nil || pin != "v1.1.0" {
		t.Fatalf("release.pin after the third run = %q, %v", pin, err)
	}
}

// newTaskInRepo runs `task new` in the copy in dir for repository repo.
func newTaskInRepo(t *testing.T, dir, repo string) (int, string) {
	t.Helper()
	code, _, stderr := runTask(t, "new", "--copy", dir, "--repo", repo, "--title", "Sort")
	return code, stderr
}

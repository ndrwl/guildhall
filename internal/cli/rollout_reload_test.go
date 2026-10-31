package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/steward"
)

// diskFleet is a fleet whose machine files live under root, at their remote paths, and whose remote commands are
// this binary's own commands run on the copy found there, so a rollout's config reload reaches a real Director copy.
type diskFleet struct{ root string }

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
	writeFile(t, filepath.Join(copyDir, managedFile), string(first))
	app, _, stderr := newTestApp(nil, nil)
	if code := app.Run([]string{"store", "init", filepath.Join(copyDir, directorStoreFile)}); code != 0 {
		t.Fatalf("store init: exit %d, %s", code, stderr)
	}
	enrolOnly(t, copyDir)
	if got := copyMode(t, copyDir); got.Mode != modeManaged || got.Authorized {
		t.Fatalf("mode after enrol = %+v", got)
	}

	cfg := writeStewardConfig(t, "schema_version = 1\n[managed.review_rule]\nvalue = \"two reviewers\"\npolicy = true\n")
	app, stdout, stderr := newTestApp(nil, nil)
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
	if code, stderr := newTaskInRepo(t, copyDir, "cli", "t-1"); code != 0 {
		t.Fatalf("task new after the rollout: exit %d, %s", code, stderr)
	}
}

// newTaskInRepo runs `task new` in the copy in dir for repository repo.
func newTaskInRepo(t *testing.T, dir, repo, id string) (int, string) {
	t.Helper()
	code, _, stderr := runTask(t, "new", "--copy", dir, "--repo", repo, "--title", "Sort", id)
	return code, stderr
}

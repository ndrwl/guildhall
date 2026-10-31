package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
)

// configCmd runs `guildhall config <args>`.
func configCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	code := app.Run(append([]string{"config"}, args...))
	return code, stdout.String(), stderr.String()
}

// managedView reads the managed facts of the copy in dir.
func managedView(t *testing.T, dir string) store.ManagedView {
	t.Helper()
	v, err := withStore(filepath.Join(dir, directorStoreFile), true, func(s *store.Store) (store.ManagedView, error) { return s.ManagedView() })
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestConfigReload: reload authorizes the enrolled copy's managed.toml digest with a reload event naming the
// digest authorized before, replays its event for a digest it already authorized, and changes no task, so a new
// revision reaches new tasks only.
func TestConfigReload(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	dir := newCopy(t)
	first := writeManaged(t, dir, pinned)
	if code, out, stderr := modeCmd(t, "enrol", "--steward", "hq", "--copy", dir); code != 0 {
		t.Fatalf("enrol: exit %d\n%s%s", code, out, stderr)
	}
	if code, _ := newTaskIn(t, dir, "t-1"); code != 1 {
		t.Fatalf("task new before reload: exit %d, want the unauthorized refusal", code)
	}
	// The refusal recorded the digest observed-untrusted (event 1) and opened its alert decision.
	if got := copyMode(t, dir); got.Untrusted == nil || got.Untrusted.State != "waiting" {
		t.Fatalf("mode before reload = %+v", got)
	}

	for range 2 {
		code, out, stderr := configCmd(t, "reload", "--copy", dir)
		if code != 0 || out != "managed.toml "+first+" authorized by reload (event 2); new tasks get its settings, and active tasks keep their bound config\n" {
			t.Fatalf("reload: exit %d\n%s%s", code, out, stderr)
		}
	}
	// The reload answers the alert: its decision is void, so nothing waits for it.
	if ds := alertDecisionsIn(t, dir); len(ds) != 1 || !ds[0].Void || !strings.Contains(ds[0].VoidReason, "config reload authorized "+first) {
		t.Fatalf("alert decisions after reload = %+v", ds)
	}
	receipts := receiptCount(t, dir)
	if code, _, _ := configCmd(t, "reload", "--copy", dir); code != 0 || receiptCount(t, dir) != receipts {
		t.Fatalf("a repeated reload recorded again: exit %d, receipts %d, want %d", code, receiptCount(t, dir), receipts)
	}
	got := copyMode(t, dir)
	if got.Mode != modeManaged || !got.Authorized || got.Refusal != "" || got.LastRevision == nil ||
		got.LastRevision.Source != store.SourceReload || got.LastRevision.Digest != first || got.LastRevision.Previous != "" {
		t.Fatalf("mode after reload = %+v", got)
	}
	if code, stderr := newTaskIn(t, dir, "t-1"); code != 0 {
		t.Fatalf("task new after reload: exit %d, %s", code, stderr)
	}
	task := readTask(t, filepath.Join(dir, directorStoreFile), "app", "t-1")

	second := writeManaged(t, dir, map[string]config.ManagedSetting{"max_running_attempts": {Value: "1"}, config.LockWaitKey: {Value: "40s", Overridable: true}})
	code, out, stderr := configCmd(t, "reload", "--copy", dir)
	if code != 0 || !strings.Contains(out, "managed.toml "+second+" authorized by reload (event 3, previous "+first+")") {
		t.Fatalf("reload of a new revision: exit %d\n%s%s", code, out, stderr)
	}
	v := managedView(t, dir)
	if len(v.Revisions) != 3 || v.Revisions[2].Source != store.SourceReload || v.Revisions[2].Digest != second || v.Revisions[2].Previous != first {
		t.Fatalf("revisions = %+v", v.Revisions)
	}
	receipt, err := withStore(filepath.Join(dir, directorStoreFile), true, func(s *store.Store) (store.Receipt, error) {
		return s.Receipt("managed-revision:1:reload:" + strings.TrimPrefix(second, "sha256:"))
	})
	if err != nil || receipt.Operation != "record-managed-revision" {
		t.Fatalf("reload receipt = %+v, %v", receipt, err)
	}
	if got := readTask(t, filepath.Join(dir, directorStoreFile), "app", "t-1"); got.ConfigDigest != task.ConfigDigest {
		t.Fatalf("reload changed an active task's config digest: %s, was %s", got.ConfigDigest, task.ConfigDigest)
	}
	if code, stderr := newTaskIn(t, dir, "t-2"); code != 0 {
		t.Fatalf("task new on the new revision: exit %d, %s", code, stderr)
	}
	if got := readTask(t, filepath.Join(dir, directorStoreFile), "app", "t-2"); got.ConfigDigest == task.ConfigDigest {
		t.Fatal("a new task did not bind the new revision")
	}
}

// TestConfigReloadRefusals: reload refuses, recording nothing, a copy in the wrong mode, an inline [managed] and a
// managed.toml that does not load with the config.
func TestConfigReloadRefusals(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  string
	}{
		{"standalone", func(*testing.T, string) {}, "this copy is standalone, so it has no managed revision to authorize"},
		{"not enrolled", func(t *testing.T, dir string) { writeManaged(t, dir, pinned) },
			"this copy is refused: managed.toml is present but this copy is not enrolled"},
		{"managed.toml missing", func(t *testing.T, dir string) {
			writeManaged(t, dir, pinned)
			enrolOnly(t, dir)
			if err := os.Remove(filepath.Join(dir, managedFile)); err != nil {
				t.Fatal(err)
			}
		}, "the managed settings are missing"},
		{"left", func(t *testing.T, dir string) {
			writeManaged(t, dir, pinned)
			enrolManaged(t, dir)
			if code, _, stderr := modeCmd(t, "leave", "--copy", dir); code != 0 {
				t.Fatalf("leave: exit %d, %s", code, stderr)
			}
		}, "this copy is standalone"},
		{"inline [managed]", func(t *testing.T, dir string) {
			writeManaged(t, dir, pinned)
			enrolOnly(t, dir)
			appendFile(t, filepath.Join(dir, "config.toml"), "\n[managed.stall_interval]\nvalue = \"5m\"\n")
		}, "inline [managed] section, which a Director copy refuses in every mode"},
		{"corrupt managed.toml", func(t *testing.T, dir string) {
			writeManaged(t, dir, pinned)
			enrolOnly(t, dir)
			appendFile(t, filepath.Join(dir, managedFile), "# edited in transit\n")
		}, "does not load with"},
		{"local value for a managed key", func(t *testing.T, dir string) {
			writeManaged(t, dir, pinned)
			enrolOnly(t, dir)
			setLocal(t, dir, map[string]string{"max_running_attempts": "5"})
		}, "does not load with"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := newCopy(t)
			c.setup(t, dir)
			before := receiptCount(t, dir)
			code, out, stderr := configCmd(t, "reload", "--copy", dir)
			if code != 1 || out != "" || !strings.Contains(stderr, "guildhall: config reload: ") || !strings.Contains(stderr, c.want) {
				t.Fatalf("reload: exit %d\n%s%s; want a refusal naming %q", code, out, stderr, c.want)
			}
			if after := receiptCount(t, dir); after != before {
				t.Fatalf("a refused reload recorded something: receipts %d, want %d", after, before)
			}
		})
	}

	dir := newCopy(t)
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{nil, 2, "config: the command is reload"},
		{[]string{"show"}, 2, "config: the command is reload"},
		{[]string{"reload", "--copy", dir, "extra"}, 2, `config reload: unexpected argument "extra"`},
		{[]string{"reload", "--copy", dir, "--force"}, 2, "config reload: "},
		{[]string{"reload", "--copy", t.TempDir()}, 1, "guildhall store init"},
	} {
		if code, _, stderr := configCmd(t, c.args...); code != c.code || !strings.Contains(stderr, c.want) {
			t.Errorf("config %q: exit %d, %s; want %d naming %q", c.args, code, stderr, c.code, c.want)
		}
	}
}

// enrolOnly records the copy's enrolment with steward hq and the digest of its managed.toml, authorizing nothing.
func enrolOnly(t *testing.T, dir string) {
	t.Helper()
	if code, _, stderr := modeCmd(t, "enrol", "--steward", "hq", "--copy", dir); code != 0 {
		t.Fatalf("enrol: exit %d, %s", code, stderr)
	}
}

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
)

// modeCmd runs `guildhall director <args>`.
func modeCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	code := app.Run(append([]string{"director"}, args...))
	return code, stdout.String(), stderr.String()
}

// copyMode is `director mode --json` on the copy in dir.
func copyMode(t *testing.T, dir string) admission {
	t.Helper()
	code, out, stderr := modeCmd(t, "mode", "--json", "--copy", dir)
	if code != 0 {
		t.Fatalf("director mode: exit %d, stderr %s", code, stderr)
	}
	var a admission
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatalf("director mode: %v\n%s", err, out)
	}
	return a
}

// writeManaged writes a managed.toml with settings into the copy in dir and returns its digest.
func writeManaged(t *testing.T, dir string, settings map[string]config.ManagedSetting) string {
	t.Helper()
	b, err := config.EncodeManaged(settings)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, managedFile), string(b))
	return string(digest.Of(b))
}

// recordEvent records enrolment event e in the copy's store.
func recordEvent(t *testing.T, dir string, e store.Enrolment) {
	t.Helper()
	if _, err := withStore(filepath.Join(dir, directorStoreFile), false, func(s *store.Store) (store.Enrolment, error) {
		got, _, err := s.RecordEnrolment(e)
		return got, err
	}); err != nil {
		t.Fatal(err)
	}
}

// receiptCount is the number of receipts in the copy's store, which a refusal leaves unchanged.
func receiptCount(t *testing.T, dir string) int {
	t.Helper()
	rep, err := withStore(filepath.Join(dir, directorStoreFile), true, func(s *store.Store) (store.Report, error) { return s.Doctor() })
	if err != nil {
		t.Fatal(err)
	}
	return rep.Receipts
}

// newTaskIn runs `task new` for app in the copy in dir.
func newTaskIn(t *testing.T, dir, id string) (int, string) {
	t.Helper()
	code, _, stderr := runTask(t, "new", "--copy", dir, "--repo", "app", "--title", "Sort", id)
	return code, stderr
}

var pinned = map[string]config.ManagedSetting{"max_running_attempts": {Value: "2"}, config.LockWaitKey: {Value: "40s", Overridable: true}}

// TestModeTable: one case per row of the mode table (docs/director-modes.md#mode); a refused row refuses `task new`
// with the state and the command that fixes it, and director mode reports it, exiting 0.
func TestModeTable(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	for _, c := range []struct {
		name    string
		setup   func(t *testing.T, dir string)
		mode    string
		refusal string
	}{
		{"no enrolment, no managed.toml", func(*testing.T, string) {}, modeStandalone, ""},
		{"no enrolment, managed.toml", func(t *testing.T, dir string) { writeManaged(t, dir, pinned) },
			modeRefused, "managed.toml is present but this copy is not enrolled: run guildhall director enrol --steward <label>"},
		{"enrolled, managed.toml", func(t *testing.T, dir string) { writeManaged(t, dir, pinned); enrolManaged(t, dir) }, modeManaged, ""},
		{"enrolled, no managed.toml", func(t *testing.T, dir string) {
			writeManaged(t, dir, pinned)
			enrolManaged(t, dir)
			if err := os.Remove(filepath.Join(dir, managedFile)); err != nil {
				t.Fatal(err)
			}
		}, modeRefused, `enrolled with steward "hq" but managed.toml is missing: the managed settings are missing`},
		{"left, no managed.toml", func(t *testing.T, dir string) {
			writeManaged(t, dir, pinned)
			enrolManaged(t, dir)
			if code, _, stderr := modeCmd(t, "leave", "--copy", dir); code != 0 {
				t.Fatalf("leave: exit %d, %s", code, stderr)
			}
		}, modeStandalone, ""},
		{"left, managed.toml", func(t *testing.T, dir string) {
			d := writeManaged(t, dir, pinned)
			enrolManaged(t, dir)
			recordEvent(t, dir, store.Enrolment{Event: store.Left, Digest: d, Revision: store.RevisionName(digest.Digest(d))})
			writeManaged(t, dir, map[string]config.ManagedSetting{"max_running_attempts": {Value: "1"}})
		}, modeRefused, "left managed mode (keeping managed.toml.rev-"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := newCopy(t)
			c.setup(t, dir)
			got := copyMode(t, dir)
			if got.Mode != c.mode || !strings.Contains(got.Refusal, c.refusal) || (c.refusal == "") != (got.Refusal == "") {
				t.Fatalf("mode = %+v; want %s refusing %q", got, c.mode, c.refusal)
			}
			code, stderr := newTaskIn(t, dir, "t-1")
			if c.refusal == "" && code != 0 || c.refusal != "" && (code != 1 || !strings.Contains(stderr, c.refusal)) {
				t.Fatalf("task new: exit %d, %s; want refusal %q", code, stderr, c.refusal)
			}
			if code, out, _ := modeCmd(t, "mode", "--copy", dir); code != 0 || !strings.Contains(out, "mode: "+c.mode+"\n") {
				t.Fatalf("director mode: exit %d\n%s", code, out)
			}
		})
	}
}

// TestInlineManagedRefusedInEveryMode: an inline [managed] section is refused standalone, managed and left alike,
// with the guidance to move it to managed.toml.
func TestInlineManagedRefusedInEveryMode(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	const inline = "\n[managed.stall_interval]\nvalue = \"5m\"\n"
	const want = "inline [managed] section, which a Director copy refuses in every mode: move those settings to managed.toml in the checksum-bearing format"
	for name, setup := range map[string]func(t *testing.T, dir string){
		"standalone": func(*testing.T, string) {},
		"managed":    func(t *testing.T, dir string) { writeManaged(t, dir, pinned); enrolManaged(t, dir) },
		"left": func(t *testing.T, dir string) {
			writeManaged(t, dir, pinned)
			enrolManaged(t, dir)
			if code, _, stderr := modeCmd(t, "leave", "--copy", dir); code != 0 {
				t.Fatalf("leave: exit %d, %s", code, stderr)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := newCopy(t)
			setup(t, dir)
			appendFile(t, filepath.Join(dir, "config.toml"), inline)
			if code, stderr := newTaskIn(t, dir, "t-1"); code != 1 || !strings.Contains(stderr, want) {
				t.Fatalf("task new: exit %d, %s", code, stderr)
			}
			if got := copyMode(t, dir); !strings.Contains(got.Refusal, want) {
				t.Fatalf("mode = %+v", got)
			}
		})
	}
}

// TestUnauthorizedDigestRefusesWithoutRecording: an enrolled copy whose managed.toml no reload or approval
// authorized refuses task new and leave, records nothing, and still reports through director mode and status.
func TestUnauthorizedDigestRefusesWithoutRecording(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	dir := newCopy(t)
	d := writeManaged(t, dir, pinned)
	if code, out, stderr := modeCmd(t, "enrol", "--steward", "hq", "--copy", dir); code != 0 || !strings.Contains(out, "is not authorized by enrolment") {
		t.Fatalf("enrol: exit %d\n%s%s", code, out, stderr)
	}
	before := receiptCount(t, dir)
	want := "managed.toml " + d + " is not authorized: this copy's store records no reload or Guildmaster approval of that digest, so its settings are not applied"
	if code, stderr := newTaskIn(t, dir, "t-1"); code != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("task new: exit %d, %s", code, stderr)
	}
	cfgBefore, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if code, _, stderr := modeCmd(t, "leave", "--copy", dir); code != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("leave: exit %d, %s", code, stderr)
	}
	if after := receiptCount(t, dir); after != before {
		t.Fatalf("receipts %d after the refusals, want %d", after, before)
	}
	if cfgAfter, _ := os.ReadFile(filepath.Join(dir, "config.toml")); string(cfgAfter) != string(cfgBefore) {
		t.Fatal("a refused leave changed config.toml")
	}
	if _, err := os.Stat(filepath.Join(dir, managedFile)); err != nil {
		t.Fatalf("a refused leave moved managed.toml: %v", err)
	}
	got := copyMode(t, dir)
	if got.Mode != modeManaged || got.Authorized || got.ManagedDigest != d || got.Steward != "hq" || got.Refusal != want {
		t.Fatalf("mode = %+v", got)
	}
	code, out, stderr := directorCmd(t, nil, "status", "--json", "--copy", dir)
	if code != 0 || !strings.Contains(out, `"mode":{"name":"managed"}`) {
		t.Fatalf("status: exit %d, %s\n%s", code, stderr, out)
	}

	// A hand edit after an authorized revision is just as unauthorized: the new digest is refused by name.
	authorized := newCopy(t)
	writeManaged(t, authorized, pinned)
	enrolManaged(t, authorized)
	if code, stderr := newTaskIn(t, authorized, "t-1"); code != 0 {
		t.Fatalf("task new on the authorized revision: exit %d, %s", code, stderr)
	}
	edited := writeManaged(t, authorized, map[string]config.ManagedSetting{"max_running_attempts": {Value: "1"}, config.LockWaitKey: {Value: "40s", Overridable: true}})
	before = receiptCount(t, authorized)
	want = "managed.toml " + edited + " is not authorized"
	if code, stderr := newTaskIn(t, authorized, "t-2"); code != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("task new after a hand edit: exit %d, %s", code, stderr)
	}
	if after := receiptCount(t, authorized); after != before {
		t.Fatalf("receipts %d after the hand-edit refusal, want %d", after, before)
	}
}

// TestEnrolAndLeave: enrol and leave are idempotent through their mutation ids and change no task; leave writes the
// effective managed values into [local], keeping every other byte of config.toml and every effective setting, and
// keeps managed.toml as managed.toml.rev-<16 hex>.
func TestEnrolAndLeave(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	dir := newCopy(t)
	cfgPath := filepath.Join(dir, "config.toml")
	// A comment and an overriding local value, both kept.
	local := strings.Replace(directorTOML, "[local]\n", "[local]\n# my lock wait\nlock_wait = \"50s\" # mine\n", 1)
	writeFile(t, cfgPath, local)
	if code, stderr := newTaskIn(t, dir, "t-1"); code != 0 {
		t.Fatalf("task new: exit %d, %s", code, stderr)
	}
	task := readTask(t, filepath.Join(dir, directorStoreFile), "app", "t-1")

	if code, _, stderr := modeCmd(t, "enrol", "--copy", dir, "--steward", "hq"); code != 1 || !strings.Contains(stderr, "needs a valid managed.toml") {
		t.Fatalf("enrol without managed.toml: exit %d, %s", code, stderr)
	}
	d := writeManaged(t, dir, pinned)
	for range 2 {
		if code, out, stderr := modeCmd(t, "enrol", "--copy", dir, "--steward", "hq"); code != 0 || !strings.Contains(out, "enrolled with steward hq (event 1)") {
			t.Fatalf("enrol: exit %d\n%s%s", code, out, stderr)
		}
	}
	if code, _, stderr := modeCmd(t, "enrol", "--copy", dir, "--steward", "other"); code != 1 || !strings.Contains(stderr, "already enrolled") {
		t.Fatalf("enrol with another steward: exit %d, %s", code, stderr)
	}
	if _, err := withStore(filepath.Join(dir, directorStoreFile), false, func(s *store.Store) (store.ManagedRevision, error) {
		r, _, err := s.RecordManagedRevision(store.ManagedRevision{Source: store.SourceReload, Digest: d})
		return r, err
	}); err != nil {
		t.Fatal(err)
	}
	managedCfg, err := admitDirector(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := withStore(filepath.Join(dir, directorStoreFile), true, func(s *store.Store) (store.Receipt, error) {
		return s.Receipt("director-enrol:1:hq:" + digest.Digest(d).Hex()[:16])
	})
	if err != nil || receipt.Operation != "record-enrolment" {
		t.Fatalf("enrol receipt = %+v, %v", receipt, err)
	}

	rev := "managed.toml.rev-" + digest.Digest(d).Hex()[:16]
	for range 2 {
		if code, out, stderr := modeCmd(t, "leave", "--copy", dir); code != 0 || !strings.Contains(out, "left managed mode (event 2); managed.toml kept as "+rev) {
			t.Fatalf("leave: exit %d\n%s%s", code, out, stderr)
		}
	}
	want := strings.Replace(local, "lock_wait = \"50s\" # mine\n", "lock_wait = \"50s\" # mine\nmax_running_attempts = \"2\"\n", 1)
	if got, _ := os.ReadFile(cfgPath); string(got) != want {
		t.Fatalf("config.toml after leave:\n%s\nwant:\n%s", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, managedFile)); !os.IsNotExist(err) {
		t.Fatalf("managed.toml is still there: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, rev)); err != nil || string(digest.Of(b)) != d {
		t.Fatalf("kept revision: %v", err)
	}
	after, adm, err := admitDirectorMode(cfgPath)
	if err != nil || adm.Mode != modeStandalone {
		t.Fatalf("admission after leave = %+v, %v", adm, err)
	}
	for _, k := range []string{"max_running_attempts", config.LockWaitKey} {
		w, _ := managedCfg.Setting(k)
		if g, _ := after.Setting(k); g != w {
			t.Errorf("%s = %q after leave, want %q", k, g, w)
		}
	}
	if got := readTask(t, filepath.Join(dir, directorStoreFile), "app", "t-1"); got.ConfigDigest != task.ConfigDigest || got.Workflow != task.Workflow {
		t.Fatalf("task changed: %+v, was %+v", got, task)
	}
	if code, stderr := newTaskIn(t, dir, "t-2"); code != 0 {
		t.Fatalf("task new after leave: exit %d, %s", code, stderr)
	}
}

// TestLeaveRefusesUneditableLocal: a [local] leave cannot edit safely refuses before anything is recorded or written.
func TestLeaveRefusesUneditableLocal(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	for name, local := range map[string]string{
		"dotted": strings.Replace(directorTOML, "[local]\n", "", 1) + "",
		"quoted": strings.Replace(directorTOML, "[local]\n", "[local]\n\"lock_wait\" = \"50s\"\n", 1),
	} {
		t.Run(name, func(t *testing.T) {
			dir := newCopy(t)
			cfgPath := filepath.Join(dir, "config.toml")
			if name == "dotted" {
				local = strings.Replace(local, "name = \"alpha\"\n", "name = \"alpha\"\nlocal.lock_wait = \"50s\"\n", 1)
			}
			writeFile(t, cfgPath, local)
			writeManaged(t, dir, pinned)
			enrolManaged(t, dir)
			before := receiptCount(t, dir)
			code, _, stderr := modeCmd(t, "leave", "--copy", dir)
			if code != 1 || !strings.Contains(stderr, "left unchanged") {
				t.Fatalf("leave: exit %d, %s", code, stderr)
			}
			if got, _ := os.ReadFile(cfgPath); string(got) != local {
				t.Fatal("a refused leave changed config.toml")
			}
			if receiptCount(t, dir) != before || copyMode(t, dir).Mode != modeManaged {
				t.Fatal("a refused leave recorded something")
			}
		})
	}
}

// TestLeaveFinishesAfterAnInterruption: a left event recorded with the files not yet changed, as an interrupted
// leave leaves it, is finished by running leave again.
func TestLeaveFinishesAfterAnInterruption(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	dir := newCopy(t)
	d := writeManaged(t, dir, pinned)
	enrolManaged(t, dir)
	recordEvent(t, dir, store.Enrolment{Event: store.Left, Digest: d, Revision: store.RevisionName(digest.Digest(d))})
	if got := copyMode(t, dir); got.Mode != modeRefused {
		t.Fatalf("mode mid-leave = %+v", got)
	}
	if code, out, stderr := modeCmd(t, "leave", "--copy", dir); code != 0 || !strings.Contains(out, "left managed mode (event 2)") {
		t.Fatalf("leave: exit %d\n%s%s", code, out, stderr)
	}
	if got := copyMode(t, dir); got.Mode != modeStandalone || got.Refusal != "" {
		t.Fatalf("mode after leave = %+v", got)
	}
}

func TestModeCommandRefusals(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	dir := newCopy(t)
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"enrol", "--copy", dir}, 2, "--steward <label> is required"},
		{[]string{"mode", "--copy", dir, "extra"}, 2, `unexpected argument "extra"`},
		{[]string{"leave", "--copy", dir}, 1, "not enrolled, so it has nothing to leave"},
		{[]string{"mode", "--copy", t.TempDir()}, 1, "guildhall store init"},
	} {
		if code, _, stderr := modeCmd(t, c.args...); code != c.code || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: exit %d, %s; want %d naming %q", c.args, code, stderr, c.code, c.want)
		}
	}
	writeManaged(t, dir, pinned)
	if code, _, stderr := modeCmd(t, "enrol", "--copy", dir, "--steward", "Not A Label"); code != 1 || !strings.Contains(stderr, "steward label") {
		t.Errorf("bad label: exit %d, %s", code, stderr)
	}
}

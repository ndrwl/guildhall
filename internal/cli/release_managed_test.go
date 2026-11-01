package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
)

// rollout writes the fixture copy's managed.toml naming release version with sha256 sum, as a Steward rollout does.
func (f *pinFixture) rollout(version, sum string) string {
	f.t.Helper()
	return writeManaged(f.t, f.copy, map[string]config.ManagedSetting{config.ReleaseKey: {Value: version}, config.ReleaseSHA256Key: {Value: sum}})
}

// incoming places a release binary holding body where a rollout copies it, releases/.incoming/<version>/, and
// returns its sha256.
func (f *pinFixture) incoming(version, body string) string {
	f.t.Helper()
	h := release.Home{Dir: f.home, GOOS: runtime.GOOS}
	path := managedIncoming(h, version)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	from, sum := releaseFile(f.t, body)
	writeFile(f.t, path, readText(f.t, from))
	f.report = `{"version":"` + version + `","modified":false,"store_schema":1,"release_contract":2}`
	return sum
}

// TestConfigReloadPinsTheManagedRelease: config reload of a managed revision naming a release authorizes the
// revision, then installs the release from the rollout's .incoming copy against release_sha256, records a
// release-pin event with source reload and writes release.pin, leaving another copy's pin alone; a second reload
// changes nothing; a rollout to an installed release moves the pin, and the rollout of the previous revision moves
// it back, also from a pin whose release was damaged.
func TestConfigReloadPinsTheManagedRelease(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	f := newPinFixture(t)
	f.tickingClock()
	h := release.Home{Dir: f.home, GOOS: runtime.GOOS}
	other := f.secondCopy()
	if code := f.run("release", "adopt", "--copy", other); code != 0 {
		t.Fatalf("adopt of the other copy = %d", code)
	}

	sum := f.incoming("v1.1.0", "release v1.1.0")
	first := f.rollout("v1.1.0", sum)
	if code := f.run("director", "enrol", "--steward", "hq", "--copy", f.copy); code != 0 {
		t.Fatalf("enrol = %d", code)
	}
	if code := f.run("config", "reload", "--copy", f.copy); code != 0 {
		t.Fatalf("config reload = %d, %s", code, f.stderr)
	}
	out := f.stdout.String()
	for _, want := range []string{"managed.toml " + first + " authorized by reload", "installed release v1.1.0", "pins v1.1.0", "its first pin"} {
		if !strings.Contains(out, want) {
			t.Fatalf("config reload output %q does not name %q", out, want)
		}
	}
	if r, err := h.Runnable("v1.1.0"); err != nil || r.SHA256 != sum {
		t.Fatalf("v1.1.0 not installed from .incoming: %+v, %v", r, err)
	}
	pins := f.pins()
	if len(pins) != 1 || pins[0].Version != "v1.1.0" || pins[0].SHA256 != sum || pins[0].Source != store.PinReload {
		t.Fatalf("pins %+v, want one reload pin of v1.1.0", pins)
	}
	if pin, err := release.ReadPin(f.copy); err != nil || pin != "v1.1.0" {
		t.Fatalf("release.pin = %q, %v", pin, err)
	}
	if pin, _ := release.ReadPin(other); pin != "v1.0.0" {
		t.Fatalf("the other copy's pin moved to %q", pin)
	}

	ran := len(f.ran)
	if code := f.run("config", "reload", "--copy", f.copy); code != 0 || !strings.Contains(f.stdout.String(), "already pins v1.1.0") {
		t.Fatalf("second config reload = %d, %q", code, f.stdout)
	}
	if len(f.pins()) != 1 || len(f.ran) != ran {
		t.Fatal("a second reload recorded a pin or ran a binary")
	}

	// A rollout to an installed release moves the pin; it is then damaged, and the rollout of the previous
	// revision moves the pin back from it.
	adopted, err := h.Runnable("v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	sum2 := f.install("v1.2.0", "release v1.2.0", 1)
	f.rollout("v1.2.0", sum2)
	if code := f.run("config", "reload", "--copy", f.copy); code != 0 || !strings.Contains(f.stdout.String(), "pins v1.2.0") || !strings.Contains(f.stdout.String(), "was v1.1.0") {
		t.Fatalf("reload of a rollout to v1.2.0 = %d, %q", code, f.stdout)
	}
	if err := os.WriteFile(h.Binary("v1.2.0"), []byte("damaged"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.rollout("v1.1.0", sum)
	if code := f.run("config", "reload", "--copy", f.copy); code != 0 || !strings.Contains(f.stdout.String(), "pins v1.1.0") || !strings.Contains(f.stdout.String(), "was v1.2.0") {
		t.Fatalf("reload of the rolled-back revision = %d, %q", code, f.stdout)
	}
	pins = f.pins()
	if len(pins) != 3 || pins[2].Version != "v1.1.0" || pins[2].PreviousVersion != "v1.2.0" || pins[2].Source != store.PinReload {
		t.Fatalf("pins %+v, want the rollback by rollout to v1.1.0", pins)
	}
	if pin, _ := release.ReadPin(other); pin != adopted.Version {
		t.Fatalf("the other copy's pin moved to %q", pin)
	}

	// A managed copy's pin is a managed key: release use refuses it, naming the rollout.
	if code := f.run("release", "use", "v1.0.0", "--copy", f.copy); code != 1 || !strings.Contains(f.stderr.String(), "is managed") || !strings.Contains(f.stderr.String(), "rollout") {
		t.Fatalf("release use on a managed copy = %d, %q", code, f.stderr)
	}
}

// TestConfigReloadRefusesAnUnverifiedRelease: a revision whose release cannot be verified, installed with another
// sha256, neither installed nor in .incoming, or an .incoming copy with another sha256, leaves the pin unchanged,
// naming the reason and that the revision stands authorized; a release key without its sha256 fails authorization
// and records nothing; and a target whose store schema differs from an active task's recorded release is refused.
func TestConfigReloadRefusesAnUnverifiedRelease(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	f := newPinFixture(t)
	f.tickingClock()
	if code := f.run("release", "adopt", "--copy", f.copy); code != 0 {
		t.Fatalf("release adopt = %d", code)
	}
	wrong := strings.Repeat("0", 64)
	sum := f.install("v1.1.0", "release v1.1.0", 1)
	f.rollout("v1.1.0", wrong)
	if code := f.run("director", "enrol", "--steward", "hq", "--copy", f.copy); code != 0 {
		t.Fatalf("enrol = %d", code)
	}
	check := func(name, want string) {
		t.Helper()
		before := len(f.pins())
		code := f.run("config", "reload", "--copy", f.copy)
		if code != 1 || !strings.Contains(f.stderr.String(), want) || !strings.Contains(f.stderr.String(), "is authorized, but its release is not pinned") {
			t.Fatalf("%s: config reload = %d, %q; want a refusal naming %q", name, code, f.stderr, want)
		}
		if pin, _ := release.ReadPin(f.copy); pin != "v1.0.0" || len(f.pins()) != before {
			t.Fatalf("%s: a refused reload moved the pin to %q", name, pin)
		}
	}
	check("installed with another sha256", "with sha256 "+sum)
	f.rollout("v1.3.0", wrong)
	check("neither installed nor in .incoming", "the rollout left no copy of it")
	f.incoming("v1.3.0", "release v1.3.0")
	check(".incoming copy with another sha256", "not the expected "+wrong)

	// Authorization fails before any pin is considered: nothing is recorded.
	writeManaged(t, f.copy, map[string]config.ManagedSetting{config.ReleaseKey: {Value: "v1.1.0"}})
	receipts := receiptCount(t, f.copy)
	if code := f.run("config", "reload", "--copy", f.copy); code != 1 || !strings.Contains(f.stderr.String(), "go together") || receiptCount(t, f.copy) != receipts {
		t.Fatalf("reload of a release without its sha256 = %d, %q", code, f.stderr)
	}

	// The store-schema rule: an active task records v1.0.0 (store schema 1), so a schema-2 release is refused.
	adopted, err := release.Home{Dir: f.home, GOOS: runtime.GOOS}.Runnable("v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	f.rollout("v1.0.0", adopted.SHA256)
	if code := f.run("config", "reload", "--copy", f.copy); code != 0 || !strings.Contains(f.stdout.String(), "already pins v1.0.0") {
		t.Fatalf("reload of the pinned release = %d, %q", code, f.stdout)
	}
	if code := f.newTask("t-1"); code != 0 {
		t.Fatalf("task new = %d", code)
	}
	sum2 := f.install("v2.0.0", "release v2.0.0", 2)
	f.rollout("v2.0.0", sum2)
	check("another store schema", "t-1")
}

//go:build unix

package secrets_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/secrets"
)

// installFixture is a machine with its key, and ciphertext encrypted to it, by the round-trip sops.
type installFixture struct {
	p      secrets.Paths
	ct     []byte
	sum    string
	copied string
}

func newInstallFixture(t *testing.T, recipient string) installFixture {
	t.Helper()
	roundTripTools(t)
	p := paths(t)
	write(t, p.Key, "# public key: "+machinePub+"\nAGE-SECRET-KEY-1X\n")
	ct, err := secrets.Encrypt(context.Background(), secrets.Values{}.With("API_KEY", testSecret), []string{recipient, recoveryPub})
	if err != nil {
		t.Fatal(err)
	}
	return installFixture{p: p, ct: ct, sum: fmt.Sprintf("%x", sha256.Sum256(ct)), copied: t.TempDir()}
}

func (f installFixture) drop(t *testing.T) string {
	t.Helper()
	dropped := secrets.NewFile(f.p.File, f.sum)
	write(t, dropped, string(f.ct))
	return dropped
}

func (f installFixture) install() secrets.InstallReport {
	return secrets.Install(context.Background(), f.p, f.copied, f.sum, nil)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestInstallFirstPush: with no file yet, install renames the dropped file into place, keeps no .prev and reports
// the names, never a value.
func TestInstallFirstPush(t *testing.T) {
	f := newInstallFixture(t, machinePub)
	dropped := f.drop(t)
	r := f.install()
	if !r.Installed || r.Step != "" || r.BeforeSHA256 != "" || r.Previous != "" || !slices.Equal(r.Names, []string{"API_KEY"}) {
		t.Fatalf("Install = %+v", r)
	}
	if got, _ := os.ReadFile(f.p.File); string(got) != string(f.ct) {
		t.Fatal("the secrets file is not the dropped ciphertext")
	}
	if exists(dropped) || exists(f.p.File+secrets.PrevSuffix) {
		t.Fatal("a dropped file or a .prev was left")
	}
	if strings.Contains(fmt.Sprintf("%+v", r), testSecret) {
		t.Fatal("the report holds a value")
	}
}

// TestInstallKeepsThePreviousAndRemovesStaleDrops: over an existing file, install keeps it as .prev, overwriting an
// older .prev, and removes every other dropped file beside it.
func TestInstallKeepsThePreviousAndRemovesStaleDrops(t *testing.T) {
	f := newInstallFixture(t, machinePub)
	write(t, f.p.File, "old ciphertext")
	write(t, f.p.File+secrets.PrevSuffix, "older ciphertext")
	stale := secrets.NewFile(f.p.File, strings.Repeat("a", 64))
	write(t, stale, "half a put")
	f.drop(t)
	r := f.install()
	if !r.Installed || r.BeforeSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte("old ciphertext"))) || r.Previous != f.p.File+secrets.PrevSuffix || !slices.Equal(r.Removed, []string{stale}) {
		t.Fatalf("Install = %+v", r)
	}
	if prev, _ := os.ReadFile(f.p.File + secrets.PrevSuffix); string(prev) != "old ciphertext" {
		t.Fatalf(".prev = %q", prev)
	}
	if got, _ := os.ReadFile(f.p.File); string(got) != string(f.ct) {
		t.Fatal("the secrets file is not the dropped ciphertext")
	}
	if left, _ := filepath.Glob(f.p.File + secrets.NewInfix + "*"); len(left) != 0 {
		t.Fatalf("dropped files left: %q", left)
	}
}

// TestInstallRefusals: a missing dropped file, one whose digest is not the expected one, one the machine's key does
// not decrypt, a missing key, and a key or file inside the copy are each refused at their step, leaving the live
// file byte-identical, writing no .prev and removing the dropped file.
func TestInstallRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		step  string
		setup func(t *testing.T, f *installFixture)
	}{
		"missing": {"digest", func(t *testing.T, f *installFixture) {}},
		"digest mismatch": {"digest", func(t *testing.T, f *installFixture) {
			f.drop(t)
			f.sum = strings.Repeat("b", 64)
			write(t, secrets.NewFile(f.p.File, f.sum), string(f.ct))
		}},
		"another key": {"decrypt", func(t *testing.T, f *installFixture) {
			*f = newInstallFixture(t, "age1another000000000000000000000000000000000000000000000000000")
			write(t, f.p.File, "old ciphertext")
			f.drop(t)
		}},
		"no key": {"decrypt", func(t *testing.T, f *installFixture) {
			if err := os.Remove(f.p.Key); err != nil {
				t.Fatal(err)
			}
			f.drop(t)
		}},
		"inside the copy": {"paths", func(t *testing.T, f *installFixture) {
			f.copied = filepath.Dir(filepath.Dir(f.p.File))
			f.drop(t)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newInstallFixture(t, machinePub)
			write(t, f.p.File, "old ciphertext")
			tc.setup(t, &f)
			r := f.install()
			if r.Installed || r.Step != tc.step || r.Problem == "" || strings.Contains(r.Problem, testSecret) {
				t.Fatalf("Install = %+v, want refused at %s", r, tc.step)
			}
			if got, _ := os.ReadFile(f.p.File); string(got) != "old ciphertext" {
				t.Fatalf("the secrets file changed: %q", got)
			}
			if exists(f.p.File+secrets.PrevSuffix) || exists(secrets.NewFile(f.p.File, f.sum)) {
				t.Fatal("a .prev was written or the dropped file was kept")
			}
		})
	}
}

// TestInstallRefusesAnExpectThatIsNotADigest: no path is built from an --expect that is not a sha256.
func TestInstallRefusesAnExpectThatIsNotADigest(t *testing.T) {
	f := newInstallFixture(t, machinePub)
	if r := secrets.Install(context.Background(), f.p, f.copied, "../x", nil); r.Installed || r.Step != "digest" || r.New != "" {
		t.Fatalf("Install = %+v", r)
	}
}

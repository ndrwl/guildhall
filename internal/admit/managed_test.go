package admit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestManaged: Managed gives the mode table's verdict (docs/director-modes.md#mode) for a copy with no config.toml,
// managed exactly when the mode is not standalone: a refused row is managed with Judge's refusal as a RefusalError,
// an empty managed.toml counts as present, and a pin refusal never changes the verdict.
func TestManaged(t *testing.T) {
	managed := []byte("schema_version = 1\n")
	d := string(digest.Of(managed))
	enrol := func(s *store.Store) error {
		_, _, err := s.RecordEnrolment(store.Enrolment{Event: store.Enrolled, Steward: "hq", Digest: d})
		return err
	}
	leave := func(s *store.Store) error {
		if err := enrol(s); err != nil {
			return err
		}
		_, _, err := s.RecordEnrolment(store.Enrolment{Event: store.Left, Digest: d, Revision: store.RevisionName(digest.Digest(d))})
		return err
	}
	empty := func(*store.Store) error { return nil }
	for _, tc := range []struct {
		name    string
		file    []byte // managed.toml, none when nil
		store   func(*store.Store) error
		pin     bool // a release.pin the store records no event of, which Judge refuses
		want    bool
		refusal string
	}{
		{name: "no store, no managed.toml"},
		{name: "store with no enrolment, no managed.toml", store: empty},
		{name: "no store, empty managed.toml", file: []byte{}, want: true, refusal: "is present but this copy is not enrolled"},
		{name: "no enrolment, managed.toml", file: managed, store: empty, want: true, refusal: "is present but this copy is not enrolled"},
		{name: "enrolled, managed.toml", file: managed, store: enrol, want: true},
		{name: "enrolled, empty managed.toml", file: []byte{}, store: enrol, want: true},
		{name: "enrolled, managed.toml missing", store: enrol, want: true, refusal: `enrolled with steward "hq" but managed.toml is missing`},
		{name: "left, managed.toml", file: managed, store: leave, want: true, refusal: "left managed mode"},
		{name: "left, no managed.toml", store: leave},
		{name: "standalone with a pin refusal", store: empty, pin: true},
		{name: "enrolled, managed.toml, with a pin refusal", file: managed, store: enrol, pin: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.file != nil {
				if err := os.WriteFile(filepath.Join(dir, ManagedFile), tc.file, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.store != nil {
				s, err := store.Open(filepath.Join(dir, StoreFile), store.Options{})
				if err != nil {
					t.Fatal(err)
				}
				err = tc.store(s)
				if err := errors.Join(err, s.Close()); err != nil {
					t.Fatal(err)
				}
			}
			if tc.pin {
				if err := release.WritePin(dir, "v1.0.0"); err != nil {
					t.Fatal(err)
				}
				v, err := ManagedView(filepath.Join(dir, "config.toml"))
				if err != nil {
					t.Fatal(err)
				}
				if PinRefusal(dir, v.ReleasePin) == "" {
					t.Fatal("the fixture's pin is not refused")
				}
			}
			got, err := Managed(dir)
			var refused RefusalError
			switch {
			case got != tc.want:
				t.Fatalf("Managed = %v, %v; want %v", got, err, tc.want)
			case tc.refusal == "" && err != nil:
				t.Fatalf("Managed = %v, %v; want no error", got, err)
			case tc.refusal != "" && (!errors.As(err, &refused) || !strings.Contains(refused.Text, tc.refusal)):
				t.Fatalf("Managed = %v, %v; want a RefusalError naming %q", got, err, tc.refusal)
			}
			if _, err := os.Stat(filepath.Join(dir, "config.toml")); err == nil {
				t.Fatal("Managed wrote a config.toml")
			}
			if _, err := os.Stat(filepath.Join(dir, StoreFile)); (err == nil) != (tc.store != nil) {
				t.Fatalf("Managed changed whether the store exists: %v", err)
			}
		})
	}
}

// TestManagedReadError: a managed.toml that exists but cannot be read, or one beside a store that cannot be read,
// is Managed's error, and the copy stays managed because the file is present.
func TestManagedReadError(t *testing.T) {
	t.Run("managed.toml cannot be read", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ManagedFile), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := Managed(dir)
		if err == nil {
			t.Fatal("a managed.toml that is a directory was read")
		}
		if !got {
			t.Fatalf("Managed = false, %v; a managed.toml that exists stays managed", err)
		}
	})
	for _, tc := range []struct {
		name string
		file []byte // managed.toml, none when nil
		want bool
	}{
		{name: "managed.toml, store cannot be read", file: []byte("schema_version = 1\n"), want: true},
		{name: "empty managed.toml, store cannot be read", file: []byte{}, want: true},
		{name: "no managed.toml, store cannot be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.file != nil {
				if err := os.WriteFile(filepath.Join(dir, ManagedFile), tc.file, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(dir, StoreFile), 0o755); err != nil {
				t.Fatal(err)
			}
			got, err := Managed(dir)
			if err == nil {
				t.Fatal("a store that is a directory was read")
			}
			if got != tc.want {
				t.Fatalf("Managed = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

// TestInline: Admission.Inline reports Judge's inline [managed] verdict, including an empty table, and the parse
// error that leaves it undecided.
func TestInline(t *testing.T) {
	for _, tc := range []struct {
		local      string
		want, fail bool
	}{
		{"schema_version = 1\n", false, false},
		{"schema_version = 1\n[managed]\n", true, false},
		{"schema_version = [\n", false, true},
	} {
		a := Judge(Files{CfgPath: "config.toml", Local: []byte(tc.local)}, store.ManagedView{})
		got, err := a.Inline()
		if got != tc.want || (err != nil) != tc.fail {
			t.Fatalf("Inline of %q = %v, %v; want %v, error %v", tc.local, got, err, tc.want, tc.fail)
		}
	}
}

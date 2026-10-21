package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

func writeStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "guildhall.store")
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	digest := "sha256:" + strings.Repeat("a", 64)
	task := store.Task{Repository: "app", ID: "t-1", Title: "Task", Workflow: workflow.Revision{Name: "default", Digest: digest}, ConfigDigest: digest}
	if _, err := s.CreateTask("m-1", task); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenDecision("m-2", store.Decision{Repository: "app", TaskID: "t-1", ID: "accept-plan", Kind: store.DecisionQuestion, Question: "Accept?"}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStoreDoctorReportsStore(t *testing.T) {
	path := writeStore(t)
	app, stdout, stderr := newTestApp(nil, nil)

	if code := app.Run([]string{"store", "doctor", path}); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	want := "store: " + path + "\n" +
		"schema_version: 1 (supported: 1)\n" +
		"integrity: ok\n" +
		"receipts: 2\n" +
		"workflow approvals: 0\n" +
		"repository app: tasks 1, attempts 0, documents 0, pane bindings 0, decisions 1, rulings 0, owners 0 (0 starting without containment), messages 0 (0 unread)\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	// doctor opens read-only and leaves the file unlocked.
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatalf("store after doctor: %v", err)
	}
	_ = s.Close()
}

func TestStoreDoctorRefusals(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.store")
	text := filepath.Join(dir, "notes.txt")
	// Reach metadata validation rather than bbolt's minimum two-page size check.
	if err := os.WriteFile(text, []byte(strings.Repeat("not a store\n", 2*os.Getpagesize())), 0o600); err != nil {
		t.Fatal(err)
	}
	locked := writeStore(t)
	holder, err := store.Open(locked, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()

	for _, tc := range []struct{ path, want string }{
		{missing, "no store at"},
		{text, "not a Guildhall store"},
		{locked, "locked"},
	} {
		app, stdout, stderr := newTestApp(nil, nil)
		if code := app.Run([]string{"store", "doctor", tc.path}); code != 1 {
			t.Errorf("%s: exit code = %d, want 1", tc.path, code)
		}
		if stdout.Len() != 0 || !strings.HasPrefix(stderr.String(), "guildhall: store doctor: ") || !strings.Contains(strings.ToLower(stderr.String()), strings.ToLower(tc.want)) {
			t.Errorf("%s: stdout %q, stderr %q; want an error mentioning %q", tc.path, stdout, stderr, tc.want)
		}
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("doctor created %s: %v", missing, err)
	}
}

func TestStoreDoctorMissingRequiredBuckets(t *testing.T) {
	for _, bucket := range []string{"meta", "receipts", "repositories"} {
		t.Run(bucket, func(t *testing.T) {
			path := writeStore(t)
			db, err := bolt.Open(path, 0o600, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket([]byte(bucket)) }); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			app, stdout, stderr := newTestApp(nil, nil)
			if code := app.Run([]string{"store", "doctor", path}); code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "not a Guildhall store") {
				t.Errorf("stdout %q, stderr %q; want store refusal", stdout, stderr)
			}
		})
	}
}

func TestStoreUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"store"},
		{"store", "compact", "x"},
		{"store", "doctor"},
		{"store", "doctor", "a", "b"},
	} {
		app, _, stderr := newTestApp(nil, nil)
		if code := app.Run(args); code != 2 {
			t.Errorf("%q: exit code = %d, want 2", args, code)
		}
		if !strings.Contains(stderr.String(), "Usage:") {
			t.Errorf("%q: stderr %q lacks usage", args, stderr)
		}
	}
}

package artisan_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
)

func TestProcessRecordRoundTrip(t *testing.T) {
	worker := artisan.ProcessRecord{Identity: artisan.Identity{PID: 41, Token: "t41"}, Role: "worker"}
	grandchild := artisan.ProcessRecord{Identity: artisan.Identity{PID: 42, Token: "t42"}, Role: "grandchild"}
	for _, tc := range []struct {
		name  string
		write []artisan.ProcessRecord
		files []string
		want  []artisan.ProcessRecord
	}{
		{"none", nil, nil, nil},
		{"one", []artisan.ProcessRecord{worker}, []string{"worker-41.json"}, []artisan.ProcessRecord{worker}},
		{"file-name order", []artisan.ProcessRecord{worker, grandchild},
			[]string{"grandchild-42.json", "worker-41.json"},
			[]artisan.ProcessRecord{grandchild, worker}},
		{"a rewrite replaces", []artisan.ProcessRecord{{Identity: artisan.Identity{PID: 41, Token: "old"}, Role: "worker"}, worker},
			[]string{"worker-41.json"},
			[]artisan.ProcessRecord{worker}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if len(tc.write) != 0 {
				dir = filepath.Join(dir, "created", "inventory") // RecordProcess creates it.
			}
			for _, r := range tc.write {
				if err := artisan.RecordProcess(dir, r); err != nil {
					t.Fatalf("RecordProcess(%+v) = %v", r, err)
				}
			}
			var files []string
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				files = append(files, e.Name())
			}
			if !reflect.DeepEqual(files, tc.files) {
				t.Errorf("files %v, want %v", files, tc.files)
			}
			got, err := artisan.ReadProcessRecords(dir)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ReadProcessRecords = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

// TestRecordProcessRefuses: a record without a positive PID, a token or a
// role, or whose role is not a file-name component, is refused and nothing
// is written.
func TestRecordProcessRefuses(t *testing.T) {
	id := artisan.Identity{PID: 7, Token: "t7"}
	for _, tc := range []struct {
		name string
		rec  artisan.ProcessRecord
	}{
		{"zero PID", artisan.ProcessRecord{Identity: artisan.Identity{Token: "t"}, Role: "worker"}},
		{"negative PID", artisan.ProcessRecord{Identity: artisan.Identity{PID: -1, Token: "t"}, Role: "worker"}},
		{"no token", artisan.ProcessRecord{Identity: artisan.Identity{PID: 7}, Role: "worker"}},
		{"no role", artisan.ProcessRecord{Identity: id}},
		{"role with a slash", artisan.ProcessRecord{Identity: id, Role: "a/b"}},
		{"role with a backslash", artisan.ProcessRecord{Identity: id, Role: `a\b`}},
		{"role dot-dot", artisan.ProcessRecord{Identity: id, Role: ".."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := artisan.RecordProcess(dir, tc.rec); !errors.Is(err, artisan.ErrInvalidProcessRecord) {
				t.Errorf("RecordProcess(%+v) = %v, want ErrInvalidProcessRecord", tc.rec, err)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("refused record wrote %v", entries)
			}
		})
	}
}

// TestReadProcessRecordsRefuses: a file that is not a valid record, or is not
// stored under its own name, is refused naming the file; files not named
// *.json are ignored; a missing directory is fs.ErrNotExist.
func TestReadProcessRecordsRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, file, body string
	}{
		{"not JSON", "worker-7.json", "{"},
		{"no PID", "worker-0.json", `{"token":"t","role":"worker"}`},
		{"zero PID", "worker-0.json", `{"pid":0,"token":"t","role":"worker"}`},
		{"negative PID", "worker--1.json", `{"pid":-1,"token":"t","role":"worker"}`},
		{"no token", "worker-7.json", `{"pid":7,"role":"worker"}`},
		{"no role", "-7.json", `{"pid":7,"token":"t"}`},
		{"another role's name", "grandchild-7.json", `{"pid":7,"token":"t","role":"worker"}`},
		{"another PID's name", "worker-8.json", `{"pid":7,"token":"t","role":"worker"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := artisan.ReadProcessRecords(dir)
			if !errors.Is(err, artisan.ErrInvalidProcessRecord) || !strings.Contains(err.Error(), tc.file) {
				t.Errorf("ReadProcessRecords = %v, want ErrInvalidProcessRecord naming %s", err, tc.file)
			}
		})
	}
	t.Run("other files are ignored", func(t *testing.T) {
		dir := t.TempDir()
		for _, n := range []string{"release", ".worker-7.json.123.tmp", "notes.txt"} {
			if err := os.WriteFile(filepath.Join(dir, n), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := artisan.ReadProcessRecords(dir); err != nil || len(got) != 0 {
			t.Errorf("ReadProcessRecords = %+v, %v; want none", got, err)
		}
	})
	t.Run("a missing directory", func(t *testing.T) {
		if _, err := artisan.ReadProcessRecords(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ReadProcessRecords = %v, want fs.ErrNotExist", err)
		}
	})
}

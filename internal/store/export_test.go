package store

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

var updateExport = flag.Bool("update-export", false, "rewrite testdata/schema-v1.export")

const goldenExport = "testdata/schema-v1.export"

// readTree reads every file under dir, keyed by its slash-separated relative path.
func readTree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)], err = os.ReadFile(p)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(t *testing.T, what string, got, want map[string][]byte) {
	t.Helper()
	for name, w := range want {
		if g, ok := got[name]; !ok {
			t.Errorf("%s: %s missing", what, name)
		} else if !bytes.Equal(g, w) {
			t.Errorf("%s: %s differs:\n%s\nwant:\n%s", what, name, g, w)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("%s: unexpected %s", what, name)
		}
	}
}

func export(t *testing.T, s *Store, opts ExportOptions) (string, ExportResult) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "export")
	res, err := s.Export(dir, opts)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	return dir, res
}

// richStore is a store holding every record kind the fixture has, Director sessions and notes whose sequences run
// above their record counts, and non-JSON and non-UTF-8 meta values.
func richStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rich.store")
	writeFixture(t, path)
	s := openTest(t, path)
	mustStartSession(t, s, sampleSession(1))
	for _, text := range []string{"first note", "second note", "third note"} {
		if _, _, err := s.AddNote(1, NotePlan, "app/t-1", text); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.RemoveNote(1, "n3", "done with it"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RemoveNote(1, "n2", "done with it too"); err != nil {
		t.Fatal(err)
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket(bucketMeta)
		if err := meta.Put([]byte("x-binary"), []byte{0xff, 0x00, '\t', '\n'}); err != nil {
			return err
		}
		return meta.Put([]byte("x-marker"), []byte(base64Marker+"not really"))
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func TestExportImportRoundTrip(t *testing.T) {
	s, _ := richStore(t)
	first, res := export(t, s, ExportOptions{})
	if res.ReceiptSequence < 10 || len(res.Documents) != 1 || res.Documents[0].Path != "tasks/t-1/plan.md" {
		t.Fatalf("export result = %+v", res)
	}
	files := readTree(t, first)
	for _, name := range []string{"meta.txt", "receipts.txt", "director.txt", "repositories.txt", "repositories/app.txt"} {
		if _, ok := files[name]; !ok {
			t.Fatalf("export has no %s: %v", name, res.Files)
		}
	}
	for name, data := range files {
		if bytes.Contains(data, []byte("\r")) || !bytes.HasPrefix(data, []byte(exportFormat)) {
			t.Errorf("%s is not an LF-only export file", name)
		}
	}
	if !bytes.Contains(files["meta.txt"], []byte("record\t1\tx-binary\tbase64:")) || !bytes.Contains(files["meta.txt"], []byte("record\t1\tx-marker\tbase64:")) {
		t.Errorf("meta.txt does not base64 its non-text values:\n%s", files["meta.txt"])
	}
	if !bytes.Contains(files["meta.txt"], []byte("record\t1\tformat\tguildhall-store\n")) {
		t.Errorf("meta.txt does not keep text values as they are:\n%s", files["meta.txt"])
	}
	if !bytes.Contains(files["director.txt"], []byte("bucket\t2\tnotes\t3\n")) {
		t.Errorf("director.txt does not carry the notes sequence:\n%s", files["director.txt"])
	}

	restored := filepath.Join(t.TempDir(), "restored.store")
	rep, err := Import(restored, first)
	if err != nil || len(rep.Problems) != 0 {
		t.Fatalf("Import = %+v, %v", rep, err)
	}
	r := openTest(t, restored)
	second, _ := export(t, r, ExportOptions{})
	sameTree(t, "second export", readTree(t, second), files)

	// Ids and sequences continue where the exported store left off.
	n, nr, err := r.AddNote(1, NotePlan, "", "after the restore")
	if err != nil || n.ID != "n4" || nr.Sequence != res.ReceiptSequence+1 {
		t.Fatalf("next note = %+v, %+v, %v; want n4 at sequence %d", n, nr, err, res.ReceiptSequence+1)
	}
}

func TestImportRefusals(t *testing.T) {
	s, _ := richStore(t)
	dir, _ := export(t, s, ExportOptions{})

	existing := filepath.Join(t.TempDir(), "existing.store")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(existing, dir); !errors.Is(err, ErrExists) {
		t.Fatalf("import over a file: %v; want ErrExists", err)
	}
	if data, _ := os.ReadFile(existing); string(data) != "keep" {
		t.Fatalf("import changed the existing file: %q", data)
	}

	meta := filepath.Join(dir, "meta.txt")
	data, err := os.ReadFile(meta)
	if err != nil {
		t.Fatal(err)
	}
	newer := bytes.Replace(data, []byte("record\t1\tschema_version\t1\n"), []byte(fmt.Sprintf("record\t1\tschema_version\t%d\n", SchemaVersion+1)), 1)
	if bytes.Equal(newer, data) {
		t.Fatalf("meta.txt has no schema_version line:\n%s", data)
	}
	if err := os.WriteFile(meta, newer, 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "new.store")
	if _, err := Import(target, dir); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("import of a newer schema: %v; want ErrNewerSchema", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused import left %s: %v", target, err)
	}
}

func TestExportParts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.store")
	s := openTest(t, path)
	for i := range 200 {
		mustCreate(t, s, fmt.Sprintf("create-%03d", i), sampleTask("app", fmt.Sprintf("t-%03d", i)))
	}
	const limit = 4096
	dir, res := export(t, s, ExportOptions{PartSize: limit})
	files := readTree(t, dir)
	var parts []string
	for name, data := range files {
		if strings.HasPrefix(name, "receipts.txt.part-") {
			parts = append(parts, name)
		}
		if int64(len(data)) > limit {
			t.Errorf("%s is %d bytes, over the %d limit", name, len(data), limit)
		}
		if len(data) == 0 || data[len(data)-1] != '\n' {
			t.Errorf("%s does not end at a line boundary", name)
		}
	}
	if len(parts) < 3 || files["receipts.txt.part-0001"] == nil {
		t.Fatalf("receipts were not split into parts: %v", res.Files)
	}
	if _, whole := files["receipts.txt"]; whole {
		t.Fatalf("a split file is also written whole")
	}
	if bytes.HasPrefix(files["receipts.txt.part-0002"], []byte(exportFormat)) {
		t.Errorf("a later part repeats the header")
	}
	restored := filepath.Join(t.TempDir(), "restored.store")
	if _, err := Import(restored, dir); err != nil {
		t.Fatal(err)
	}
	again, _ := export(t, openTest(t, restored), ExportOptions{PartSize: limit})
	sameTree(t, "re-export", readTree(t, again), files)
	whole, _ := export(t, s, ExportOptions{})
	if _, ok := readTree(t, whole)["receipts.txt"]; !ok {
		t.Fatalf("receipts under the default limit were split")
	}
}

// TestExportGolden pins the export of the committed schema 1 store, so every platform exports the same bytes.
// Run it with -update-export to rewrite the golden export.
func TestExportGolden(t *testing.T) {
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "copy.store")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ro := testOptions()
	ro.ReadOnly = true
	s, err := Open(path, ro)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	dir, _ := export(t, s, ExportOptions{})
	if *updateExport {
		if err := os.RemoveAll(goldenExport); err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(goldenExport, os.DirFS(dir)); err != nil {
			t.Fatal(err)
		}
	}
	sameTree(t, "golden export", readTree(t, dir), readTree(t, goldenExport))
}

// TestExportDuringWrites exports a store another open store keeps writing: each export is one consistent snapshot,
// and the writer, waiting the lock wait, is never refused.
func TestExportDuringWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.store")
	first := openTest(t, path)
	mustCreate(t, first, "create-0", sampleTask("app", "t-0"))
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	writes := make(chan error, 1)
	wg.Go(func() {
		for i := 1; ; i++ {
			select {
			case <-stop:
				writes <- nil
				return
			default:
			}
			s, err := Open(path, Options{LockTimeout: 30 * time.Second, RequireStore: true})
			if err == nil {
				_, err = s.CreateTask(fmt.Sprintf("create-%d", i), sampleTask("app", fmt.Sprintf("t-%d", i)))
				err = errors.Join(err, s.Close())
			}
			if err != nil {
				writes <- err
				return
			}
		}
	})
	defer wg.Wait()
	for i := range 5 {
		s, err := Open(path, Options{ReadOnly: true, LockTimeout: 30 * time.Second})
		if err != nil {
			close(stop)
			t.Fatal(err)
		}
		dir := filepath.Join(t.TempDir(), "export")
		res, err := s.Export(dir, ExportOptions{})
		_ = s.Close()
		if err != nil {
			close(stop)
			t.Fatal(err)
		}
		restored := filepath.Join(t.TempDir(), fmt.Sprintf("restored-%d.store", i))
		rep, err := Import(restored, dir)
		if err != nil {
			close(stop)
			t.Fatalf("export %d does not import: %v", i, err)
		}
		if rep.Receipts != int(res.ReceiptSequence) || rep.Repositories[0].Tasks != rep.Receipts {
			close(stop)
			t.Fatalf("export %d is not one snapshot: %d receipts at sequence %d, %d tasks", i, rep.Receipts, res.ReceiptSequence, rep.Repositories[0].Tasks)
		}
	}
	close(stop)
	if err := <-writes; err != nil {
		t.Fatalf("the writer was refused: %v", err)
	}
}

func TestExportRefusesANonEmptyDirectory(t *testing.T) {
	s, _ := newStore(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stale.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Export(dir, ExportOptions{}); err == nil {
		t.Fatal("export into a non-empty directory succeeded")
	}
}

func TestExportFileNames(t *testing.T) {
	for name, want := range map[string]string{
		"receipts": "receipts.txt", "workflow-approvals": "workflow-approvals.txt", "App": "x-417070.txt",
		"x-41": "x-782d3431.txt", "a/b": "x-612f62.txt", "a..b": "x-612e2e62.txt",
	} {
		if got := exportFileName([]byte(name)); got != want {
			t.Errorf("exportFileName(%q) = %q, want %q", name, got, want)
		}
	}
}

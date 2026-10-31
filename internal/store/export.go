package store

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

// The store export (docs/store.md#store-export-and-import) is a deterministic, LF-only text form of every bucket,
// key, value and bucket sequence in a store: one file per top-level bucket, with the repositories bucket's own
// header and records in one file and each repository's bucket in its own file under repositories/. A file over
// ExportOptions.PartSize is written as numbered parts, split at line boundaries.

// DefaultExportPartSize is the largest export file, and the largest part of a split one: 32 MiB.
const DefaultExportPartSize int64 = 32 << 20

// exportFormat is the first line of every export file, or of its first part.
const exportFormat = "guildhall-store-export\t1\n"

// base64Marker starts a key or value that is not plain text; its bytes follow, base64 encoded.
const base64Marker = "base64:"

// ExportOptions controls Store.Export.
type ExportOptions struct {
	// PartSize is the largest file written; a larger one is split into parts. Zero means DefaultExportPartSize.
	PartSize int64
}

// ExportResult is what Export wrote and read.
type ExportResult struct {
	// Files are the slash-separated paths written, relative to the export directory, in the order written.
	Files []string
	// Documents are every document reference in the store, as the export holds them.
	Documents []DocumentRef
	// ReceiptSequence is the receipts bucket's sequence: the sequence of the newest committed mutation.
	ReceiptSequence uint64
}

// Export writes the store's text export into dir, which it creates and which must not already hold anything,
// reading the whole store in one read transaction, so the export is one consistent snapshot. Raw bytes are copied,
// never decoded and re-encoded, so the same store always exports to the same bytes.
func (s *Store) Export(dir string, opts ExportOptions) (ExportResult, error) {
	part := opts.PartSize
	if part <= 0 {
		part = DefaultExportPartSize
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ExportResult{}, fmt.Errorf("store: export: %w", err)
	}
	if entries, err := os.ReadDir(dir); err != nil {
		return ExportResult{}, fmt.Errorf("store: export: %w", err)
	} else if len(entries) > 0 {
		return ExportResult{}, fmt.Errorf("store: export: %s is not empty", dir)
	}
	var out ExportResult
	err := s.view(func(tx *bolt.Tx) error {
		if _, err := schemaVersion(tx); err != nil {
			return err
		}
		out.ReceiptSequence = tx.Bucket(bucketReceipts).Sequence()
		return tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			file := exportFileName(name)
			w, err := newPartWriter(dir, file, part)
			if err != nil {
				return err
			}
			w.line("root")
			// The repositories bucket's nested repository buckets each get their own file.
			top := bytes.Equal(name, bucketRepos)
			if err := exportBucket(w, 1, name, b, top); err != nil {
				return errors.Join(err, w.abort())
			}
			files, err := w.close()
			if err != nil {
				return err
			}
			out.Files = append(out.Files, files...)
			if !top {
				return nil
			}
			return b.ForEachBucket(func(repo []byte) error {
				rb := b.Bucket(repo)
				w, err := newPartWriter(dir, "repositories/"+exportFileName(repo), part)
				if err != nil {
					return err
				}
				w.line("root", encodeField(bucketRepos))
				if err := exportBucket(w, 1, repo, rb, false); err != nil {
					return errors.Join(err, w.abort())
				}
				files, err := w.close()
				if err != nil {
					return err
				}
				out.Files = append(out.Files, files...)
				if docs := rb.Bucket(bucketDocuments); docs != nil {
					return docs.ForEach(func(k, v []byte) error {
						if v == nil {
							return nil
						}
						var d DocumentRef
						if err := json.Unmarshal(v, &d); err != nil {
							return fmt.Errorf("document %q of repository %q: %w", k, repo, err)
						}
						out.Documents = append(out.Documents, d)
						return nil
					})
				}
				return nil
			})
		})
	})
	if err != nil {
		return ExportResult{}, fmt.Errorf("store: export: %w", err)
	}
	return out, nil
}

// exportBucket writes bucket b, named name at depth, and everything in it depth first in key order; with
// skipNested its nested buckets are left out.
func exportBucket(w *partWriter, depth int, name []byte, b *bolt.Bucket, skipNested bool) error {
	w.line("bucket", strconv.Itoa(depth), encodeField(name), strconv.FormatUint(b.Sequence(), 10))
	c := b.Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		if nested := b.Bucket(k); nested != nil {
			if skipNested {
				continue
			}
			if err := exportBucket(w, depth+1, k, nested, false); err != nil {
				return err
			}
			continue
		}
		w.line("record", strconv.Itoa(depth), encodeField(k), encodeField(v))
	}
	return w.err
}

// encodeField is a key, value or bucket name as an export field: its bytes as they are when they are valid UTF-8
// with no control characters and do not start with the base64 marker, else the marker and their base64.
func encodeField(b []byte) string {
	plain := utf8.Valid(b) && !bytes.HasPrefix(b, []byte(base64Marker))
	for _, c := range b {
		if c < 0x20 || c == 0x7f {
			plain = false
			break
		}
	}
	if plain {
		return string(b)
	}
	return base64Marker + base64.StdEncoding.EncodeToString(b)
}

func decodeField(s string) ([]byte, error) {
	if rest, ok := strings.CutPrefix(s, base64Marker); ok {
		return base64.StdEncoding.DecodeString(rest)
	}
	return []byte(s), nil
}

// safeExportName is a bucket name used as its file name unchanged: lowercase, so case-insensitive file systems
// keep distinct names distinct, and never starting "x-", which starts the hex form of every other name.
var safeExportName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)

// exportFileName is the export file of bucket name: "<name>.txt", or "x-<hex of name>.txt" for a name that is not
// a safe file name.
func exportFileName(name []byte) string {
	s := string(name)
	if !safeExportName.MatchString(s) || strings.HasPrefix(s, "x-") || strings.Contains(s, "..") {
		s = "x-" + hex.EncodeToString(name)
	}
	return s + ".txt"
}

// partWriter writes one export file, starting a new part whenever the next line would take the current one past
// the limit; a file that needed only one part is renamed to its own name at close.
type partWriter struct {
	dir, file string
	limit     int64
	parts     []string
	f         *os.File
	buf       *bufio.Writer
	size      int64
	err       error
}

func newPartWriter(dir, file string, limit int64) (*partWriter, error) {
	w := &partWriter{dir: dir, file: file, limit: limit}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, filepath.FromSlash(file))), 0o755); err != nil {
		return nil, err
	}
	if err := w.next(); err != nil {
		return nil, err
	}
	if _, err := w.buf.WriteString(exportFormat); err != nil {
		return nil, err
	}
	w.size = int64(len(exportFormat))
	return w, nil
}

func partName(file string, n int) string { return fmt.Sprintf("%s.part-%04d", file, n) }

func (w *partWriter) next() error {
	if w.f != nil {
		if err := w.finish(); err != nil {
			return err
		}
	}
	name := partName(w.file, len(w.parts)+1)
	f, err := os.OpenFile(filepath.Join(w.dir, filepath.FromSlash(name)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	w.parts = append(w.parts, name)
	w.f, w.buf, w.size = f, bufio.NewWriterSize(f, 1<<16), 0
	return nil
}

func (w *partWriter) finish() error {
	err := w.buf.Flush()
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	w.f = nil
	return err
}

// line writes one line of tab-separated fields; the first error is kept and ends all later writes.
func (w *partWriter) line(fields ...string) {
	if w.err != nil {
		return
	}
	l := strings.Join(fields, "\t") + "\n"
	if w.size > 0 && w.size+int64(len(l)) > w.limit {
		if w.err = w.next(); w.err != nil {
			return
		}
	}
	_, w.err = w.buf.WriteString(l)
	w.size += int64(len(l))
}

// close finishes the file and returns the paths it wrote.
func (w *partWriter) close() ([]string, error) {
	if w.err != nil {
		return nil, errors.Join(w.err, w.abort())
	}
	if err := w.finish(); err != nil {
		return nil, errors.Join(err, w.abort())
	}
	if len(w.parts) == 1 {
		if err := os.Rename(filepath.Join(w.dir, filepath.FromSlash(w.parts[0])), filepath.Join(w.dir, filepath.FromSlash(w.file))); err != nil {
			return nil, err
		}
		return []string{w.file}, nil
	}
	return w.parts, nil
}

// abort closes and removes every part written.
func (w *partWriter) abort() error {
	if w.f != nil {
		_ = w.finish()
	}
	var errs []error
	for _, p := range w.parts {
		if err := os.Remove(filepath.Join(w.dir, filepath.FromSlash(p))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

var partSuffix = regexp.MustCompile(`^(.+)\.part-(\d{4,})$`)

// exportFile is one logical export file and its parts in order.
type exportFile struct {
	name  string
	parts []string
}

// readExportFiles lists the export files in dir: the top-level ones in name order, then repositories/ in name order.
func readExportFiles(dir string) ([]exportFile, error) {
	var out []exportFile
	for _, sub := range []string{"", "repositories"} {
		entries, err := os.ReadDir(filepath.Join(dir, sub))
		if errors.Is(err, fs.ErrNotExist) && sub != "" {
			continue
		} else if err != nil {
			return nil, err
		}
		byName := map[string]*exportFile{}
		parts := map[string]map[int]string{}
		for _, e := range entries {
			if e.IsDir() {
				if sub == "" && e.Name() == "repositories" {
					continue
				}
				return nil, fmt.Errorf("unexpected directory %s in the export", joinExport(sub, e.Name()))
			}
			name, n := e.Name(), 0
			if m := partSuffix.FindStringSubmatch(name); m != nil {
				name = m[1]
				n, _ = strconv.Atoi(m[2])
				if n < 1 {
					return nil, fmt.Errorf("export part %s is not numbered from 1", joinExport(sub, e.Name()))
				}
			}
			if !strings.HasSuffix(name, ".txt") {
				return nil, fmt.Errorf("unexpected file %s in the export", joinExport(sub, e.Name()))
			}
			f := byName[name]
			if f == nil {
				f = &exportFile{name: joinExport(sub, name)}
				byName[name] = f
				parts[name] = map[int]string{}
			}
			// Part 0 stands for the whole file, so a file both whole and in parts has 0 and another number.
			if _, whole := parts[name][0]; whole || n == 0 && len(parts[name]) > 0 {
				return nil, fmt.Errorf("export file %s is both whole and in parts", joinExport(sub, name))
			}
			parts[name][n] = joinExport(sub, e.Name())
		}
		for _, name := range slices.Sorted(maps.Keys(byName)) {
			f := byName[name]
			ps := parts[name]
			if whole, ok := ps[0]; ok {
				f.parts = []string{whole}
			} else {
				for i := 1; i <= len(ps); i++ {
					p, ok := ps[i]
					if !ok {
						return nil, fmt.Errorf("export file %s is missing part %d", f.name, i)
					}
					f.parts = append(f.parts, p)
				}
			}
			out = append(out, *f)
		}
	}
	return out, nil
}

func joinExport(sub, name string) string {
	if sub == "" {
		return name
	}
	return sub + "/" + name
}

// Import creates a new store at path from the export in dir. It refuses anything already at path, and an export
// whose schema_version is newer than this Guildhall supports; it restores every bucket's sequence, so ids and
// receipt sequences continue where the exported store left off, and it ends with Doctor, failing on any problem.
// A failed import removes the file it created.
func Import(path, dir string) (Report, error) {
	fail := func(err error) (Report, error) { return Report{}, fmt.Errorf("store: import: %w", err) }
	files, err := readExportFiles(dir)
	if err != nil {
		return fail(err)
	}
	if _, err := os.Lstat(path); err == nil {
		return fail(fmt.Errorf("%s already exists, and import only creates a new store: %w", path, ErrExists))
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return fail(fmt.Errorf("%s already exists, and import only creates a new store: %w", path, ErrExists))
	} else if err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fail(err)
	}
	rep, err := importInto(path, dir, files)
	if err != nil {
		_ = os.Remove(path)
		return fail(err)
	}
	return rep, nil
}

func importInto(path, dir string, files []exportFile) (Report, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: DefaultLockTimeout, NoSync: syncSkipped.Load()})
	if err != nil {
		return Report{}, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, f := range files {
			if err := importFile(tx, dir, f); err != nil {
				return fmt.Errorf("%s: %w", f.name, err)
			}
		}
		// A newer schema_version is refused here, rolling back everything written.
		return checkMeta(tx)
	})
	if cerr := db.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Report{}, err
	}
	s, err := Open(path, Options{ReadOnly: true})
	if err != nil {
		return Report{}, err
	}
	rep, err := s.Doctor()
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Report{}, err
	}
	if len(rep.Problems) > 0 {
		return rep, fmt.Errorf("the imported store has %d problems: %s", len(rep.Problems), strings.Join(rep.Problems, "; "))
	}
	return rep, nil
}

// importFile writes one export file's buckets and records into tx.
func importFile(tx *bolt.Tx, dir string, f exportFile) error {
	// stack[d] is the bucket at depth d below the file's root; stack[0] is nil for the root of the transaction.
	var stack []*bolt.Bucket
	header, root := false, false
	lineNo := 0
	for _, p := range f.parts {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			return err
		}
		if len(data) > 0 && data[len(data)-1] != '\n' {
			return fmt.Errorf("%s does not end with a newline", p)
		}
		for line := range strings.Lines(string(data)) {
			lineNo++
			line = strings.TrimSuffix(line, "\n")
			if !header {
				if line+"\n" != exportFormat {
					return fmt.Errorf("line %d: not a Guildhall store export", lineNo)
				}
				header = true
				continue
			}
			fields := strings.Split(line, "\t")
			bad := func(format string, args ...any) error {
				return fmt.Errorf("line %d: %s", lineNo, fmt.Sprintf(format, args...))
			}
			switch {
			case fields[0] == "root" && !root:
				root = true
				var parent *bolt.Bucket
				for i, field := range fields[1:] {
					name, err := decodeField(field)
					if err != nil {
						return bad("root: %v", err)
					}
					var b *bolt.Bucket
					if i == 0 {
						b = tx.Bucket(name)
					} else {
						b = parent.Bucket(name)
					}
					if b == nil {
						return bad("root bucket %q is not in the store yet", name)
					}
					parent = b
				}
				stack = []*bolt.Bucket{parent}
			case !root:
				return bad("want the root line")
			case fields[0] == "bucket" && len(fields) == 4:
				depth, err := strconv.Atoi(fields[1])
				if err != nil || depth < 1 || depth > len(stack) {
					return bad("bucket depth %q", fields[1])
				}
				name, err := decodeField(fields[2])
				if err != nil {
					return bad("bucket name: %v", err)
				}
				seq, err := strconv.ParseUint(fields[3], 10, 64)
				if err != nil {
					return bad("bucket sequence %q", fields[3])
				}
				var b *bolt.Bucket
				if parent := stack[depth-1]; parent == nil {
					b, err = tx.CreateBucket(name)
				} else {
					b, err = parent.CreateBucket(name)
				}
				if err != nil {
					return bad("bucket %q: %v", name, err)
				}
				if err := b.SetSequence(seq); err != nil {
					return bad("bucket %q: %v", name, err)
				}
				stack = append(stack[:depth], b)
			case fields[0] == "record" && len(fields) == 4:
				depth, err := strconv.Atoi(fields[1])
				if err != nil || depth < 1 || depth >= len(stack) {
					return bad("record depth %q", fields[1])
				}
				k, err := decodeField(fields[2])
				if err != nil {
					return bad("key: %v", err)
				}
				v, err := decodeField(fields[3])
				if err != nil {
					return bad("value: %v", err)
				}
				b := stack[depth]
				if b.Get(k) != nil || b.Bucket(k) != nil {
					return bad("key %q appears twice", k)
				}
				if err := b.Put(k, v); err != nil {
					return bad("key %q: %v", k, err)
				}
			default:
				return bad("unexpected line %q", line)
			}
		}
	}
	if !header || !root {
		return errors.New("the file is empty")
	}
	return nil
}

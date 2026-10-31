package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
)

// storeCmd runs `guildhall store init|doctor|export|import`, on the path given or the selected copy's store.
func (a *App) storeCmd(args []string) int {
	const usage = "usage: guildhall store init|doctor <path>, store export <path> --out <dir>, or store import <path> --from <dir>; --copy <dir> in place of <path>"
	if len(args) == 0 || (args[0] != "init" && args[0] != "doctor" && args[0] != "export" && args[0] != "import") {
		return a.usageError(usage)
	}
	fl := flag.NewFlagSet("store "+args[0], flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	var dir string
	switch args[0] {
	case "export":
		fl.StringVar(&dir, "out", "", "the new directory the export is written to")
	case "import":
		fl.StringVar(&dir, "from", "", "the directory holding the export")
	}
	if err := parseInterspersed(fl, args[1:]); err != nil {
		return a.usageError("store " + args[0] + ": " + err.Error())
	}
	if (args[0] == "export" || args[0] == "import") && dir == "" {
		return a.usageError(usage)
	}
	var path string
	if fl.NArg() == 1 {
		path = fl.Arg(0)
	}
	if err := cp.apply(a, &path, nil, nil); err != nil {
		return a.usageError("store " + args[0] + ": " + err.Error())
	}
	if cp.searched && fl.NArg() == 0 {
		return a.usageError("store " + args[0] + ": " + cp.missing(a).Error())
	}
	if fl.NArg() > 1 || path == "" {
		return a.usageError(usage)
	}
	switch args[0] {
	case "init":
		return a.storeInit(path)
	case "export":
		return a.storeExport(path, dir, cp.dir)
	case "import":
		return a.storeImport(path, dir)
	}
	return a.storeDoctor(path)
}

// storeExport writes the text export of the store at path into the new directory dir, in one read transaction that
// waits at most the copy's lock_wait, or the default lock_wait with no copy, for a writer.
func (a *App) storeExport(path, dir, copyDir string) int {
	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: store export: %v\n", err)
		return 1
	}
	wait := config.DefaultLockWait
	if copyDir != "" {
		cfg, err := config.Load(filepath.Join(copyDir, "config.toml"))
		if err != nil {
			return fail(err)
		}
		if wait, err = cfg.LockWait(); err != nil {
			return fail(err)
		}
	}
	s, err := store.Open(path, store.Options{ReadOnly: true, LockTimeout: wait})
	if errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(path))
	} else if err != nil {
		return fail(err)
	}
	res, err := s.Export(dir, store.ExportOptions{})
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(err)
	}
	return a.printJSON(struct {
		Store           string   `json:"store"`
		Out             string   `json:"out"`
		Files           []string `json:"files"`
		ReceiptSequence uint64   `json:"receipt_sequence"`
	}{path, dir, res.Files, res.ReceiptSequence})
}

// storeImport creates a new store at path from the export in dir; it refuses anything already at path.
func (a *App) storeImport(path, dir string) int {
	rep, err := store.Import(path, dir)
	if err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: store import: %v\n", err)
		return 1
	}
	return a.printJSON(struct {
		Store         string `json:"store"`
		SchemaVersion int    `json:"schema_version"`
		Receipts      int    `json:"receipts"`
	}{path, rep.SchemaVersion, rep.Receipts})
}

// storeInit creates and initializes an empty store at path. It refuses anything already there, of any kind,
// and never overwrites or repairs it; it exits 0 with the new store's path and schema version, 1 otherwise.
func (a *App) storeInit(path string) int {
	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: store init: %v\n", err)
		return 1
	}
	exists := func() int {
		return fail(fmt.Errorf("%s already exists, and store init never replaces or repairs it: %w", path, store.ErrExists))
	}
	if _, err := os.Lstat(path); err == nil {
		return exists()
	}
	// Exclusive creation keeps the refusal when something appears after the check.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return exists()
	} else if err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fail(err)
	}
	// Open initializes the empty file this command just created.
	s, err := store.Open(path, store.Options{})
	if err == nil {
		err = s.Close()
	}
	if err != nil {
		_ = os.Remove(path)
		return fail(err)
	}
	return a.printJSON(struct {
		Store         string `json:"store"`
		SchemaVersion int    `json:"schema_version"`
	}{path, store.SchemaVersion})
}

// storeDoctor opens the store at path read-only, checks it and reports what it found.
// It exits 0 only when the store opened and showed no problems.
func (a *App) storeDoctor(path string) int {
	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: store doctor: %v\n", err)
		return 1
	}
	s, err := store.Open(path, store.Options{ReadOnly: true})
	if errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(path))
	} else if err != nil {
		return fail(err)
	}
	rep, err := s.Doctor()
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "store: %s\nschema_version: %d (supported: %d)\n", path, rep.SchemaVersion, store.SchemaVersion)
	if len(rep.Problems) == 0 {
		b.WriteString("integrity: ok\n")
	} else {
		fmt.Fprintf(&b, "integrity: %d problems\n", len(rep.Problems))
	}
	fmt.Fprintf(&b, "receipts: %d\nworkflow approvals: %d\n", rep.Receipts, rep.WorkflowApprovals)
	fmt.Fprintf(&b, "director: sessions %d, wakes %d\n", rep.DirectorSessions, rep.Wakes)
	for _, r := range rep.Repositories {
		fmt.Fprintf(&b, "repository %s: tasks %d, attempts %d, documents %d, pane bindings %d, decisions %d, rulings %d, task contexts %d, task links %d, task overrides %d, task dependencies %d, owners %d (%d starting without containment), messages %d (%d unread), pruned tasks %d\n",
			r.Repository, r.Tasks, r.Attempts, r.Documents, r.PaneBindings, r.Decisions, r.Rulings, r.TaskContexts, r.TaskLinks, r.TaskOverrides, r.TaskDependencies, r.Owners, r.UncontainedStarting, r.Messages, r.UnreadMessages, r.Pruned)
	}
	for _, p := range rep.Problems {
		fmt.Fprintf(&b, "problem: %s\n", p)
	}
	if _, err := io.WriteString(a.Stdout, b.String()); err != nil {
		return 1
	}
	if len(rep.Problems) > 0 {
		return 1
	}
	return 0
}

// mutationID derives a command's store mutation id "<kind>:<id>:<id>...": ids never contain ':', so distinct
// records get distinct mutation ids. When that text exceeds the store's 128-byte limit it becomes
// "<kind>:sha256:<hex digest of the text>", so a retry of the same command still derives the same id.
func mutationID(kind string, ids ...string) string {
	id := kind + ":" + strings.Join(ids, ":")
	if len(id) <= 128 {
		return id
	}
	return kind + ":" + string(digest.Of([]byte(id)))
}

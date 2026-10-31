// Package backup backs a Director copy up to its own branch of a private git repository, and restores a copy from
// one (docs/running.md#backups).
//
// A backup exports the copy's store as text (store.Store.Export), checks every referenced document against its
// sha256, stages every regular file of the copy but a fixed exclusion list into a bare repository at
// <copy>/backup.git, commits the tree when it differs from the last commit's, and pushes the commit to
// refs/heads/<branch> of the backup repository, fast-forward only. A failed push keeps the commit; the next run
// pushes it with whatever came after. Guildhall never force-pushes and never deletes a branch.
package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/store"
)

const (
	// RepoDir is the bare backup repository's directory in the copy, outside state/ and director/.
	RepoDir = "backup.git"
	// StampFile is the run stamp in the copy's state directory; it is never itself backed up.
	StampFile = "backup-last.json"
	// DefaultSizeLimit is the largest copy file a backup holds, and the size store export files are split at: 32 MiB.
	DefaultSizeLimit = store.DefaultExportPartSize
	// SupervisorPushTimeout bounds the session supervisor's push; `guildhall backup now` has no bound.
	SupervisorPushTimeout = 10 * time.Minute
	// attributes keeps git from rewriting any line ending on any platform.
	attributes = "* -text\n"

	exportDir  = "guildhall-export"
	copyIndex  = "guildhall-index"
	storeIndex = "guildhall-store-index"
	// pushedRef records the last commit the backup repository accepted, under the branch's name.
	pushedRef = "refs/guildhall/pushed/"
)

// unbounded is the push timeout of a run with no push bound.
const unbounded = 100 * 365 * 24 * time.Hour

// Options is one backup run of a copy.
type Options struct {
	// Copy is the copy directory, and Store its director.store.
	Copy, Store string
	Config      config.Backup
	// LockWait bounds the wait for the store's lock: the copy's lock_wait.
	LockWait time.Duration
	// PushTimeout bounds the push; zero means no bound.
	PushTimeout time.Duration
	// SizeLimit is the largest copy file backed up, and the export's part size; zero means DefaultSizeLimit.
	SizeLimit int64
	// Git runs git; its Timeout bounds every git run but the push.
	Git git.Program
	// Now stamps the commit and the run; nil means time.Now.
	Now func() time.Time
}

// LeftOut is a copy file the size guard left out of a backup.
type LeftOut struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Stamp is the presentation-only record of the copy's backups, in <state>/backup-last.json. It is never read as
// task state.
type Stamp struct {
	Branch string `json:"branch"`
	// RanAt is the last run's start, and OK whether it succeeded.
	RanAt time.Time `json:"ran_at"`
	OK    bool      `json:"ok"`
	// Error is the last run's error; empty after a run that succeeded.
	Error string `json:"error,omitempty"`
	// Commit is the newest backup commit, made at CommittedAt with the store's receipt sequence ReceiptSequence.
	Commit          string    `json:"commit,omitempty"`
	CommittedAt     time.Time `json:"committed_at,omitzero"`
	ReceiptSequence uint64    `json:"receipt_sequence,omitempty"`
	// Pushed is the newest commit the backup repository accepted, at PushedAt.
	Pushed   string    `json:"pushed,omitempty"`
	PushedAt time.Time `json:"pushed_at,omitzero"`
	// LeftOut lists the copy files the last run's size guard left out.
	LeftOut []LeftOut `json:"left_out,omitempty"`
}

// ReadStamp reads the copy's stamp from its state directory; a copy that never backed up has the zero stamp.
func ReadStamp(state string) (Stamp, error) {
	var s Stamp
	data, err := os.ReadFile(filepath.Join(state, StampFile))
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	} else if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return Stamp{}, fmt.Errorf("%s: %w", filepath.Join(state, StampFile), err)
	}
	return s, nil
}

func writeStamp(state string, s Stamp) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(state, 0o755); err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(state, StampFile), append(data, '\n'))
}

// Result is what one run did.
type Result struct {
	// Commit is the commit the run made, empty when the tree was unchanged.
	Commit string
	// Tip is the newest backup commit, and Pushed the newest the backup repository accepted.
	Tip, Pushed     string
	ReceiptSequence uint64
	LeftOut         []LeftOut
}

// ErrBusy reports a backup of the same copy already running.
var ErrBusy = errors.New("another backup of this copy is running")

// Run runs one backup of the copy and records it in the copy's stamp, whether it succeeded or not.
func Run(ctx context.Context, o Options) (Result, error) {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	state := filepath.Join(o.Copy, "state")
	started := now().UTC()
	prior, _ := ReadStamp(state)
	if prior.Branch != o.Config.Branch {
		prior = Stamp{}
	}
	res, err := run(ctx, o, started)
	stamp := prior
	stamp.Branch, stamp.RanAt, stamp.OK, stamp.Error = o.Config.Branch, started, err == nil, ""
	if err != nil {
		stamp.Error = err.Error()
	}
	if res.Tip != "" && res.Tip != stamp.Commit {
		stamp.Commit, stamp.CommittedAt, stamp.ReceiptSequence = res.Tip, started, res.ReceiptSequence
	}
	if res.Pushed != "" && res.Pushed != stamp.Pushed {
		stamp.Pushed, stamp.PushedAt = res.Pushed, now().UTC()
	}
	if err == nil || res.Tip != "" {
		stamp.LeftOut = res.LeftOut
	}
	if errors.Is(err, ErrBusy) {
		return res, err
	}
	if serr := writeStamp(state, stamp); serr != nil {
		err = errors.Join(err, fmt.Errorf("stamp: %w", serr))
	}
	return res, err
}

func run(ctx context.Context, o Options, started time.Time) (Result, error) {
	var res Result
	limit := o.SizeLimit
	if limit <= 0 {
		limit = DefaultSizeLimit
	}
	repoDir := filepath.Join(o.Copy, RepoDir)
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return res, err
	}
	lockCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	release, err := artisan.LockAttempt(lockCtx, repoDir)
	cancel()
	if errors.Is(err, context.DeadlineExceeded) {
		return res, ErrBusy
	} else if err != nil {
		return res, fmt.Errorf("lock %s: %w", repoDir, err)
	}
	defer release()
	b, err := o.Git.InitBare(ctx, repoDir)
	if err != nil {
		return res, err
	}

	// 1. The store export, one read transaction.
	exported := filepath.Join(repoDir, exportDir)
	if err := os.RemoveAll(exported); err != nil {
		return res, err
	}
	ex, err := exportStore(o, exported, limit)
	if err != nil {
		return res, err
	}
	res.ReceiptSequence = ex.ReceiptSequence

	// 2. Every referenced document against its sha256, so a damaged document never replaces a good backup.
	if err := checkDocuments(filepath.Join(o.Copy, "state", "documents"), ex.Documents); err != nil {
		return res, err
	}

	// 3. The copy's files, with the exclusion list and the size guard.
	paths, left, err := CopyFiles(o.Copy, limit)
	if err != nil {
		return res, err
	}
	res.LeftOut = left
	if err := b.Stage(ctx, filepath.Join(repoDir, copyIndex), o.Copy, paths); err != nil {
		return res, err
	}
	copyTree, err := b.WriteTree(ctx, filepath.Join(repoDir, copyIndex))
	if err != nil {
		return res, err
	}
	if err := b.Stage(ctx, filepath.Join(repoDir, storeIndex), exported, ex.Files); err != nil {
		return res, err
	}
	storeTree, err := b.WriteTree(ctx, filepath.Join(repoDir, storeIndex))
	if err != nil {
		return res, err
	}
	// 4. The .gitattributes that keeps every byte as it is.
	attrs, err := b.HashBlob(ctx, []byte(attributes))
	if err != nil {
		return res, err
	}
	root, err := b.MakeTree(ctx, []git.TreeEntry{{ID: attrs, Name: ".gitattributes"}, {Tree: true, ID: copyTree, Name: "copy"}, {Tree: true, ID: storeTree, Name: "store"}})
	if err != nil {
		return res, err
	}

	// 5. A commit only when the tree changed.
	local := "refs/heads/" + o.Config.Branch
	tip, ok, err := b.Resolve(ctx, local)
	if err != nil {
		return res, err
	}
	if !ok {
		// A new backup repository continues the branch's history when the backup repository already has it.
		if tip, err = adoptRemote(ctx, b, o); err != nil {
			return res, err
		}
	}
	pushed, _, err := b.Resolve(ctx, pushedRef+o.Config.Branch)
	if err != nil {
		return res, err
	}
	res.Tip, res.Pushed = tip, pushed
	same := false
	if tip != "" {
		tree, err := b.TreeOf(ctx, tip)
		if err != nil {
			return res, err
		}
		same = tree == root
	}
	if !same {
		msg := fmt.Sprintf("Back up %s at receipt sequence %d\n\nTaken %s.\n", o.Config.Branch, ex.ReceiptSequence, started.Format(time.RFC3339))
		commit, err := b.CommitTree(ctx, root, tip, msg, git.Signature{Name: "Guildhall backup", Email: "backup@guildhall.invalid", When: started})
		if err != nil {
			return res, err
		}
		if err := b.SetRef(ctx, local, commit); err != nil {
			return res, err
		}
		res.Commit, res.Tip = commit, commit
		_ = b.GC(ctx)
	}

	// 6. The push of every pending commit, fast-forward only.
	if res.Tip == res.Pushed {
		return res, nil
	}
	timeout := o.PushTimeout
	if timeout <= 0 {
		timeout = unbounded
	}
	pusher := b
	pusher.Timeout = timeout
	if err := pusher.Push(ctx, o.Config.Repo, res.Tip, o.Config.Branch); err != nil {
		return res, fmt.Errorf("push to %s %s: %w", o.Config.Repo, o.Config.Branch, err)
	}
	if err := b.SetRef(ctx, pushedRef+o.Config.Branch, res.Tip); err != nil {
		return res, err
	}
	res.Pushed = res.Tip
	return res, nil
}

// adoptRemote fetches the backup repository's branch into a backup repository that has none, so the next commit
// continues its history; it returns "" when the branch is new.
func adoptRemote(ctx context.Context, b git.Bare, o Options) (string, error) {
	remote, ok, err := b.RemoteBranch(ctx, o.Config.Repo, o.Config.Branch)
	if err != nil {
		return "", fmt.Errorf("read branch %s of %s: %w", o.Config.Branch, o.Config.Repo, err)
	}
	if !ok {
		return "", nil
	}
	tip, err := b.Fetch(ctx, o.Config.Repo, remote, "refs/heads/"+o.Config.Branch)
	if err != nil {
		return "", err
	}
	return tip, b.SetRef(ctx, pushedRef+o.Config.Branch, tip)
}

// exportStore exports the copy's store into dir, waiting at most the lock wait for a writer.
func exportStore(o Options, dir string, limit int64) (store.ExportResult, error) {
	s, err := store.Open(o.Store, store.Options{ReadOnly: true, RequireStore: true, LockTimeout: o.LockWait})
	if err != nil {
		return store.ExportResult{}, err
	}
	res, err := s.Export(dir, store.ExportOptions{PartSize: limit})
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	return res, err
}

// checkDocuments checks each document reference's file in dir against its sha256.
func checkDocuments(dir string, docs []store.DocumentRef) error {
	var bad []string
	for _, d := range docs {
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(d.Path)))
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s (%s/%s %s): %v", d.Path, d.Repository, d.TaskID, d.Name, err))
			continue
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		_ = f.Close()
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", d.Path, err))
		} else if got := hex.EncodeToString(h.Sum(nil)); got != d.SHA256 {
			bad = append(bad, fmt.Sprintf("%s (%s/%s %s): sha256 %s, recorded %s", d.Path, d.Repository, d.TaskID, d.Name, got, d.SHA256))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%d documents do not match their records, so nothing was committed: %s", len(bad), strings.Join(bad, "; "))
	}
	return nil
}

// CopyFiles lists the slash-separated paths of the copy files a backup holds: every regular file under the copy
// but its store, its backup repository, the stamp, worktrees and CI log caches under state/<repo>/<task>/, lock
// files and anything named .git. Files over limit are left out and returned apart.
func CopyFiles(copyDir string, limit int64) (paths []string, left []LeftOut, err error) {
	err = filepath.WalkDir(copyDir, func(p string, d fs.DirEntry, err error) error {
		// A file or directory removed while the walk runs, as a prune or a finishing attempt removes one, is skipped.
		if errors.Is(err, fs.ErrNotExist) && p != copyDir {
			return nil
		} else if err != nil {
			return err
		}
		rel, err := filepath.Rel(copyDir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if excluded(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		if info.Size() > limit {
			left = append(left, LeftOut{Path: rel, Size: info.Size()})
			return nil
		}
		paths = append(paths, rel)
		return nil
	})
	slices.Sort(paths)
	return paths, left, err
}

// excluded reports whether the copy path rel, a directory when dir is set, is on the exclusion list.
func excluded(rel string, dir bool) bool {
	parts := strings.Split(rel, "/")
	if slices.Contains(parts, ".git") {
		return true
	}
	switch rel {
	case "director.store", RepoDir, "state/" + StampFile:
		return true
	}
	if !dir && strings.HasSuffix(rel, ".lock") {
		return true
	}
	// A task's worktree and CI log cache, state/<repo>/<task>/work and state/<repo>/<task>/ci-logs.
	return dir && len(parts) == 4 && parts[0] == "state" && parts[1] != "documents" && (parts[3] == "work" || parts[3] == "ci-logs")
}

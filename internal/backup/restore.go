package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/store"
)

// RestoreOptions is one restore of a copy from a backup.
type RestoreOptions struct {
	// Target is the copy directory restored into; its director.store must not exist.
	Target string
	// Repo is the backup repository, and Rev the branch or full commit id restored.
	Repo, Rev string
	Git       git.Program
}

// Restored is what a restore wrote.
type Restored struct {
	Commit string
	// Files counts the copy files written, and Store is the import's report on the rebuilt store.
	Files int
	Store store.Report
}

const restoreRef = "refs/guildhall/restore"

// Restore fetches Rev from the backup repository into the target's backup repository, writes its copy files into
// the target, all but the copy marker, which the caller writes for the target, and rebuilds the target's
// director.store from its store export with store.Import, last, so a target with a store is a finished restore.
// A branch restored becomes the target's own backup history, so its next backup to that branch continues it.
func Restore(ctx context.Context, o RestoreOptions) (Restored, error) {
	var out Restored
	storePath := filepath.Join(o.Target, "director.store")
	if _, err := os.Lstat(storePath); err == nil {
		return out, fmt.Errorf("%s already exists, and a restore never replaces a store: %w", storePath, store.ErrExists)
	}
	if err := os.MkdirAll(o.Target, 0o755); err != nil {
		return out, err
	}
	b, err := o.Git.InitBare(ctx, filepath.Join(o.Target, RepoDir))
	if err != nil {
		return out, err
	}
	commit, err := b.Fetch(ctx, o.Repo, o.Rev, restoreRef)
	if err != nil && digest.ValidHead(o.Rev) {
		// A server that refuses a want by id still serves its branches, one of which holds the commit.
		if ferr := b.FetchAll(ctx, o.Repo, restoreRef+"/all/"); ferr != nil {
			return out, errors.Join(err, ferr)
		}
		var ok bool
		if commit, ok, err = b.Resolve(ctx, o.Rev); err == nil && !ok {
			err = fmt.Errorf("no branch of %s holds commit %s", o.Repo, o.Rev)
		}
	}
	if err != nil {
		return out, fmt.Errorf("fetch %s from %s: %w", o.Rev, o.Repo, err)
	}
	out.Commit = commit
	archive, err := b.Archive(ctx, commit)
	if err != nil {
		return out, err
	}
	exported := filepath.Join(o.Target, RepoDir, "guildhall-restore")
	if err := os.RemoveAll(exported); err != nil {
		return out, err
	}
	sawStore := false
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return out, fmt.Errorf("read the backup's tree: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		var dest string
		switch {
		case strings.HasPrefix(h.Name, "copy/"):
			rel := strings.TrimPrefix(h.Name, "copy/")
			if rel == ".guildhall-copy" {
				continue
			}
			if !filepath.IsLocal(filepath.FromSlash(rel)) || excluded(rel, false) {
				return out, fmt.Errorf("the backup holds %s, which a restore does not write", h.Name)
			}
			dest = filepath.Join(o.Target, filepath.FromSlash(rel))
			out.Files++
		case strings.HasPrefix(h.Name, "store/"):
			rel := strings.TrimPrefix(h.Name, "store/")
			if !filepath.IsLocal(filepath.FromSlash(rel)) {
				return out, fmt.Errorf("the backup holds %s, which a restore does not write", h.Name)
			}
			dest = filepath.Join(exported, filepath.FromSlash(rel))
			sawStore = true
		default:
			continue
		}
		if err := writeFile(dest, tr); err != nil {
			return out, err
		}
	}
	if !sawStore {
		return out, fmt.Errorf("commit %s holds no store export", commit)
	}
	if out.Store, err = store.Import(storePath, exported); err != nil {
		return out, err
	}
	_ = os.RemoveAll(exported)
	if !digest.ValidHead(o.Rev) {
		for _, ref := range []string{"refs/heads/" + o.Rev, pushedRef + o.Rev} {
			if err := b.SetRef(ctx, ref, commit); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

func writeFile(dest string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if info, err := os.Lstat(dest); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s exists and is not a regular file", dest)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

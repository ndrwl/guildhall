package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/ndrwl/guildhall/internal/adapter"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/store"
)

// workspacePlacement is a presentation journal, not task or endpoint authority.
// Only exact creation-response workspaces may enter it. Old or corrupt journals
// never authorize adoption, retries, cleanup, renaming or movement of siblings.
type workspacePlacement struct {
	Session   string
	Parent    string
	Workspace herdr.Workspace
}

type workspaceOrderer interface {
	Session() string
	PlaceWorkspace(context.Context, herdr.Workspace, string, []herdr.Workspace) error
}

// artisanWorkspaces holds the session lock across creation and ordering so the
// journal and child block preserve creation order even across concurrent tasks.
// An unavailable lock skips only ordering; it never fails workspace creation.
type artisanWorkspaces struct {
	adapter.TabHost
	client           *herdr.Client
	storePath, state string
	warnings         io.Writer
}

func (h *artisanWorkspaces) CreateWorkspace(ctx context.Context, label, cwd string) (herdr.Workspace, error) {
	release, lockErr := lockWorkspaceOrder(h.client.Session())
	if lockErr == nil {
		defer release()
	}
	child, err := h.TabHost.CreateWorkspace(ctx, label, cwd)
	if err != nil {
		return child, err
	}
	if lockErr == nil {
		lockErr = placeArtisan(ctx, h.client, h.storePath, h.state, child)
	}
	if lockErr != nil {
		_, _ = fmt.Fprintf(h.warnings, "guildhall: warning: Artisan workspace ordering skipped: %v\n", lockErr)
	}
	return child, nil
}

// lockWorkspaceOrder never reclaims crash residue or another process's lock.
func lockWorkspaceOrder(session string) (func(), error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cache, "guildhall", "herdr-order")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return nil, errors.New("presentation lock directory is not private")
	}
	lock := filepath.Join(dir, session+".lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		return nil, fmt.Errorf("presentation lock unavailable: %w", err)
	}
	return func() { _ = os.Remove(lock) }, nil
}

func placeArtisan(ctx context.Context, c *herdr.Client, storePath, state string, child herdr.Workspace) error {
	s, err := store.Open(storePath, store.Options{ReadOnly: true, RequireStore: true})
	if err != nil {
		return err
	}
	director, err := s.LatestDirectorSession()
	closeErr := s.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if director.Pane.Session != c.Session() || director.Pane.Workspace == "" {
		return errors.New("director has no exact workspace in this session")
	}
	parentTab, err := c.RecordedTab(ctx, director.Pane.Workspace, director.Pane.Tab, director.Pane.Pane)
	if err != nil {
		return fmt.Errorf("director workspace identity: %w", err)
	}
	if err := c.CheckOwnedPane(ctx, parentTab); err != nil {
		return err
	}
	return journalPlacement(ctx, c, filepath.Join(state, "artisan-workspaces.json"), director.Pane.Workspace, child)
}

func journalPlacement(ctx context.Context, c workspaceOrderer, path, parent string, child herdr.Workspace) error {
	var journal []workspacePlacement
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &journal); err != nil {
			return fmt.Errorf("presentation journal: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	siblings := []herdr.Workspace{}
	for _, record := range journal {
		if record.Session == "" || record.Parent == "" || record.Workspace.ID == "" || record.Workspace.Label == "" {
			return errors.New("incomplete presentation journal")
		}
		if record.Session == c.Session() && record.Workspace.ID == child.ID {
			return errors.New("workspace already journalled; refusing retry")
		}
		if record.Session == c.Session() && record.Parent == parent {
			siblings = append(siblings, record.Workspace)
		}
	}
	journal = append(journal, workspacePlacement{Session: c.Session(), Parent: parent, Workspace: herdr.Workspace{ID: child.ID, Label: child.Label}})
	b, err = json.Marshal(journal)
	if err != nil {
		return err
	}
	// Publish before moving. A lost response can never trigger a second move.
	// A partial publication leaves a corrupt journal, which refuses future moves.
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return c.PlaceWorkspace(ctx, child, parent, siblings)
}

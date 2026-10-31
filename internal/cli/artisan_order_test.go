package cli

import (
	"bytes"
	"context"

	"errors"
	"fmt"
	"github.com/ndrwl/guildhall/internal/adapter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/store"
)

type creationProbe struct {
	adapter.TabHost
	calls int
	label string
}

func (p *creationProbe) CreateWorkspace(_ context.Context, label, cwd string) (herdr.Workspace, error) {
	p.calls++
	p.label = label
	return herdr.Workspace{ID: "new", Label: label}, nil
}

func TestOrderingSkipNeverFailsCreation(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	// UserCacheDir uses LOCALAPPDATA on Windows and HOME/Library/Caches on macOS.
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprint(busy), func(t *testing.T) {
			c, err := herdr.New(herdr.Options{Session: "lab", Command: []string{"herdr"}})
			if err != nil {
				t.Fatal(err)
			}
			if busy {
				release, err := lockWorkspaceOrder("lab")
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			probe := &creationProbe{}
			var warnings bytes.Buffer
			h := &artisanWorkspaces{TabHost: probe, client: c, storePath: filepath.Join(t.TempDir(), "absent.store"), state: t.TempDir(), warnings: &warnings}
			w, err := h.CreateWorkspace(context.Background(), "└ task · build #1", "")
			if err != nil || w.ID != "new" || probe.calls != 1 || probe.label != "└ task · build #1" || !strings.Contains(warnings.String(), "ordering skipped") {
				t.Fatalf("creation=%+v err=%v calls=%d warning=%s", w, err, probe.calls, &warnings)
			}
		})
	}
}

func TestOrderingPreservesStartupBudget(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		lookup  bool
	}{
		{name: "stalled lookup", timeout: 30 * time.Second, lookup: true},
		// Exactly the reserved startup budget leaves no time for optional ordering.
		{name: "insufficient budget", timeout: 10 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			path := filepath.Join(state, "director.store")
			s, err := store.Open(path, store.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			startSession(t, path, store.DirectorPane{Session: "lab", Workspace: "director", Tab: "director:t1", Pane: "director:p1"})
			marker := filepath.Join(state, "lookup-started")
			t.Setenv("GUILDHALL_TEST_ORDERING_LOOKUP", marker)
			client, err := herdr.New(herdr.Options{Session: "lab", Command: []string{executable, "-test.run=^TestArtisanOrderingStalledTransport$", "--"}})
			if err != nil {
				t.Fatal(err)
			}
			probe := &creationProbe{}
			var warnings bytes.Buffer
			host := &artisanWorkspaces{TabHost: probe, client: client, storePath: path, state: state, warnings: &warnings}
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			child, err := host.CreateWorkspace(ctx, "└ task · build #1", "")
			if err != nil || child.ID != "new" || probe.calls != 1 {
				t.Fatalf("creation=%+v err=%v calls=%d", child, err, probe.calls)
			}
			if ctx.Err() != nil {
				t.Fatalf("ordering exhausted startup context: %v", ctx.Err())
			}
			if !strings.Contains(warnings.String(), "ordering skipped") {
				t.Fatalf("missing ordering warning: %s", &warnings)
			}
			if tc.lookup {
				// The optional lookup must expire without exhausting startup.
				// Its short product budget need not allow the child to start.
				if !strings.Contains(warnings.String(), context.DeadlineExceeded.Error()) {
					t.Fatalf("ordering did not time out: %s", &warnings)
				}
			} else {
				if !strings.Contains(warnings.String(), "insufficient startup budget") {
					t.Fatalf("ordering was not skipped for its budget: %s", &warnings)
				}
				if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("lookup ran without startup budget: %v", err)
				}
			}
		})
	}
}

func TestArtisanOrderingStalledTransport(t *testing.T) {
	marker := os.Getenv("GUILDHALL_TEST_ORDERING_LOOKUP")
	if marker == "" {
		t.Skip("ordering transport helper")
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Hour) // timing:allow intentionally hung lookup fixture; parent cancels and reaps it.
}

type orderRecorder struct {
	calls    int
	siblings []herdr.Workspace
	err      error
}

func (r *orderRecorder) Session() string { return "lab" }
func (r *orderRecorder) PlaceWorkspace(_ context.Context, _ herdr.Workspace, parent string, siblings []herdr.Workspace) error {
	if parent != "director" {
		return errors.New("wrong parent")
	}
	r.calls++
	r.siblings = append([]herdr.Workspace(nil), siblings...)
	return r.err
}
func TestPresentationJournalOnlySuppliesExactCreatedSiblings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	r := &orderRecorder{}
	first := herdr.Workspace{ID: "first", Label: "└ first · plan #1"}
	second := herdr.Workspace{ID: "second", Label: "└ second · plan #1"}
	if err := journalPlacement(context.Background(), r, path, "director", first); err != nil {
		t.Fatal(err)
	}
	r.err = errors.New("lost move response")
	if err := journalPlacement(context.Background(), r, path, "director", second); err == nil {
		t.Fatal("missing ordering warning")
	}
	if !reflect.DeepEqual(r.siblings, []herdr.Workspace{first}) {
		t.Fatalf("siblings %+v", r.siblings)
	}
	// A failed move is still journalled: it cannot be retried or adopted.
	before, _ := os.ReadFile(path)
	if err := journalPlacement(context.Background(), r, path, "director", second); err == nil || r.calls != 2 {
		t.Fatalf("retried ambiguous move: %v calls=%d", err, r.calls)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("retry changed journal")
	}
	for _, bad := range []string{"broken", "[{}]"} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := journalPlacement(context.Background(), r, path, "director", first); err == nil || r.calls != 2 {
			t.Fatalf("corrupt journal authorized move: %v", err)
		}
	}
}

// closingProbe records the abandoned workspace close it is asked for.
type closingProbe struct {
	adapter.TabHost
	workspace string
}

func (p *closingProbe) CloseAbandonedWorkspace(_ context.Context, workspaceID string, _ []adapter.TabRef, _ []string, _ string) error {
	p.workspace = workspaceID
	return nil
}

// TestArtisanWorkspacesForwardsAbandonedWorkspaceClose: the production
// presentation host reaches the wrapped host's close of a workspace a failed
// launch abandoned, so a recovered launch can close it (d91).
func TestArtisanWorkspacesForwardsAbandonedWorkspaceClose(t *testing.T) {
	probe := &closingProbe{}
	h := &artisanWorkspaces{TabHost: probe}
	if err := h.CloseAbandonedWorkspace(context.Background(), "w2", nil, []string{"└ t · build #1"}, "artisan t"); err != nil || probe.workspace != "w2" {
		t.Fatalf("close = %v, forwarded %q", err, probe.workspace)
	}
	h = &artisanWorkspaces{TabHost: &creationProbe{}}
	if err := h.CloseAbandonedWorkspace(context.Background(), "w2", nil, []string{"└ t · build #1"}, "artisan t"); err == nil {
		t.Fatal("a host that cannot close accepted the close")
	}
}

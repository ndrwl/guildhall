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
		{name: "stalled lookup", timeout: 15 * time.Second, lookup: true},
		{name: "insufficient budget", timeout: time.Second},
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
			_, markerErr := os.Stat(marker)
			if tc.lookup && markerErr != nil {
				t.Fatalf("lookup did not reach stalled transport: %v", markerErr)
			}
			if !tc.lookup && !errors.Is(markerErr, os.ErrNotExist) {
				t.Fatalf("lookup ran without startup budget: %v", markerErr)
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
	time.Sleep(time.Hour)
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

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

	"github.com/ndrwl/guildhall/internal/herdr"
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

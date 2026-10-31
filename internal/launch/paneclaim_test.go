package launch_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/launch"
)

func TestReadPaneClaim(t *testing.T) {
	const tab = `{"Session":"lab","WorkspaceID":"w2","TabID":"w2:t1","PaneID":"w2:p1"}`
	ref := adapter.TabRef{Session: "lab", WorkspaceID: "w2", TabID: "w2:t1", PaneID: "w2:p1"}
	for _, tc := range []struct {
		name, record string
		want         launch.PaneClaim
		malformed    bool
	}{
		{name: "legacy", record: `{"state":"launching"}` + "\n", want: launch.PaneClaim{Launching: &launch.Launching{}}},
		{name: "workspace only", record: `{"state":"launching","workspace":"w2"}` + "\n", want: launch.PaneClaim{Launching: &launch.Launching{Workspace: "w2"}}},
		{name: "workspace with tabs", record: `{"state":"launching","workspace":"w2","tabs":[` + tab + `]}` + "\n",
			want: launch.PaneClaim{Launching: &launch.Launching{Workspace: "w2", Tabs: []adapter.TabRef{ref}}}},
		{name: "bound without a trailing newline", record: tab, want: launch.PaneClaim{Bound: &ref}},
		{name: "mixed", record: `{"state":"launching","PaneID":"p1"}`, malformed: true},
		{name: "unknown field", record: `{"state":"launching","owner":"x"}`, malformed: true},
		{name: "trailing data", record: tab + `{}`, malformed: true},
		{name: "partial ref", record: `{"Session":"lab","WorkspaceID":"w2","TabID":"w2:t1"}`, malformed: true},
		{name: "tabs without a workspace", record: `{"state":"launching","tabs":[` + tab + `]}`, malformed: true},
		{name: "a tab in another workspace", record: `{"state":"launching","workspace":"w3","tabs":[` + tab + `]}`, malformed: true},
		{name: "a bound claim with tabs", record: `{"Session":"lab","WorkspaceID":"w2","TabID":"w2:t1","PaneID":"w2:p1","tabs":[]}`, malformed: true},
		{name: "empty", record: ``, malformed: true},
		{name: "unparsable", record: `{`, malformed: true},
		{name: "another state", record: `{"state":"binding"}`, malformed: true},
		{name: "a null state", record: `{"state":null}`, malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), launch.PaneClaimFile)
			if err := os.WriteFile(path, []byte(tc.record), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := launch.ReadPaneClaim(path)
			if tc.malformed {
				if !errors.Is(err, launch.ErrMalformedPaneClaim) || !strings.Contains(err.Error(), path) {
					t.Fatalf("ReadPaneClaim = %+v, %v; want an ErrMalformedPaneClaim naming %s", got, err, path)
				}
				if got != (launch.PaneClaim{}) {
					t.Errorf("ReadPaneClaim returned %+v with its error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (got.Launching == nil) == (got.Bound == nil) {
				t.Fatalf("ReadPaneClaim = %+v, want exactly one variant", got)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ReadPaneClaim = %+v / %+v, want %+v / %+v", got.Launching, got.Bound, tc.want.Launching, tc.want.Bound)
			}
		})
	}
	t.Run("a missing file", func(t *testing.T) {
		_, err := launch.ReadPaneClaim(filepath.Join(t.TempDir(), launch.PaneClaimFile))
		if !errors.Is(err, fs.ErrNotExist) || errors.Is(err, launch.ErrMalformedPaneClaim) {
			t.Fatalf("ReadPaneClaim of a missing file: %v, want fs.ErrNotExist", err)
		}
	})
}

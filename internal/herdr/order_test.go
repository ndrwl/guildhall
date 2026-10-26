package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlaceWorkspace(t *testing.T) {
	parent := Workspace{ID: "director", Label: "Director"}
	sibling := Workspace{ID: "older", Label: "└ d2 · build #1"}
	foreign := Workspace{ID: "foreign", Label: "Personal"}
	child := Workspace{ID: "new", Label: "└ d3 · review #1", InitialTab: OwnedTab{session: "guildhall-lab", workspaceID: "new", tabID: "new:t1", paneID: "new:p1"}}
	for _, tc := range []struct {
		name     string
		list     []Workspace
		siblings []Workspace
		status   string
		wire     string
		failMove bool
		wantMove bool
		wantOK   bool
	}{
		{name: "first child", list: []Workspace{parent, foreign, child}, wantMove: true, wantOK: true},
		{name: "second child", list: []Workspace{parent, sibling, foreign, child}, siblings: []Workspace{sibling}, wantMove: true, wantOK: true},
		{name: "already adjacent", list: []Workspace{parent, child, foreign}, wantOK: true},
		{name: "closed sibling", list: []Workspace{parent, foreign, child}, siblings: []Workspace{sibling}, wantMove: true, wantOK: true},
		{name: "old protocol", list: []Workspace{parent, foreign, child}, status: "old"},
		{name: "missing socket", list: []Workspace{parent, foreign, child}, status: "socket"},
		{name: "wrong session", list: []Workspace{parent, foreign, child}, status: "session"},
		{name: "status fails", list: []Workspace{parent, foreign, child}, status: "failure"},
		{name: "missing parent", list: []Workspace{foreign, child}},
		{name: "duplicate id", list: []Workspace{parent, parent, child}},
		{name: "detached sibling", list: []Workspace{parent, foreign, sibling, child}, siblings: []Workspace{sibling}},
		{name: "manual interleave", list: []Workspace{parent, sibling, foreign, {ID: "later", Label: "└ later"}, child}, siblings: []Workspace{sibling, {ID: "later", Label: "└ later"}}},
		{name: "renamed sibling", list: []Workspace{parent, {ID: "older", Label: "renamed"}, foreign, child}, siblings: []Workspace{sibling}},
		{name: "foreign child", list: []Workspace{parent, {ID: "stranger", Label: "└ stranger"}, foreign, child}},
		{name: "method unavailable", list: []Workspace{parent, foreign, child}, failMove: true, wantMove: true},
		{name: "wrong full order", list: []Workspace{parent, foreign, child}, wire: `{"id":"guildhall-workspace-move","result":{"type":"workspace_list","workspaces":[{"workspace_id":"new"}]}}`, wantMove: true},
		{name: "mismatched response", list: []Workspace{parent, foreign, child}, wire: `{"id":"other","result":{"type":"workspace_list","workspaces":[]}}`, wantMove: true},
		{name: "malformed response", list: []Workspace{parent, foreign, child}, wire: `broken`, wantMove: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socketPath := filepath.Join(t.TempDir(), "herdr.sock")
			var statusRecord map[string]any
			if err := json.Unmarshal([]byte(fixture(t, "status.json")), &statusRecord); err != nil {
				t.Fatal(err)
			}
			statusRecord["server"].(map[string]any)["socket"] = socketPath
			if tc.status == "socket" {
				statusRecord["server"].(map[string]any)["socket"] = ""
			}
			statusBytes, err := json.Marshal(statusRecord)
			if err != nil {
				t.Fatal(err)
			}
			status := string(statusBytes)
			switch tc.status {
			case "old":
				status = strings.ReplaceAll(status, `"protocol":22`, `"protocol":15`)
			case "session":
				status = strings.ReplaceAll(status, `guildhall-lab`, `default`)
			}
			b, _ := json.Marshal(map[string]any{"result": map[string]any{"workspaces": tc.list}})
			c, _ := newTestClient(t, map[string]response{"status --json": {stdout: status, startErr: tc.status == "failure"}, "workspace list": {stdout: string(b)}})
			moved := false
			c.moveWorkspace = func(_ context.Context, socket, id string, index int) ([]byte, error) {
				moved = true
				if socket != socketPath || id != "new" {
					t.Fatalf("unsafe target %q %q", socket, id)
				}
				if tc.failMove {
					return nil, errors.New("method unavailable")
				}
				if tc.wire != "" {
					return []byte(tc.wire), nil
				}
				expected := []Workspace{parent}
				for _, s := range tc.siblings {
					for _, w := range tc.list {
						if s.ID == w.ID {
							expected = append(expected, w)
						}
					}
				}
				if index != len(expected) {
					t.Fatalf("insert index %d want %d", index, len(expected))
				}
				expected = append(expected, child, foreign)
				out, _ := json.Marshal(map[string]any{"id": "guildhall-workspace-move", "result": map[string]any{"type": "workspace_list", "workspaces": expected}})
				return out, nil
			}
			err = c.PlaceWorkspace(context.Background(), child, "director", tc.siblings)
			if (err == nil) != tc.wantOK || moved != tc.wantMove {
				t.Fatalf("err=%v moved=%v; want OK=%v moved=%v", err, moved, tc.wantOK, tc.wantMove)
			}
		})
	}
	c, _ := newTestClient(t, nil)
	for _, w := range []Workspace{{ID: "new"}, child} {
		if w.InitialTab.session != "" {
			w.InitialTab.session = "default"
		}
		if err := c.PlaceWorkspace(context.Background(), w, "director", nil); err == nil {
			t.Fatal("accepted unowned workspace")
		}
	}
}

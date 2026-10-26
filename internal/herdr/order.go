package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// PlaceWorkspace moves only child, whose creation response supplies ownership,
// immediately after parent and its adjacent journal-bound siblings, in their
// launch order. It verifies the full returned order. Any ambiguity is an error;
// callers must warn and continue launch without retrying or rolling back.
// Callers serialize presentation mutations across the named session.
func (c *Client) PlaceWorkspace(ctx context.Context, child Workspace, parent string, siblings []Workspace) error {
	if child.InitialTab.session != c.session || child.InitialTab.workspaceID != child.ID || child.InitialTab.tabID == "" || child.InitialTab.paneID == "" || child.ID == parent {
		return errors.New("workspace ordering: no creation ownership")
	}
	out, err := c.call(ctx, "status", "--json")
	if err != nil {
		return err
	}
	var status struct {
		Client struct {
			Session  string
			Protocol int
		}
		Server struct {
			Session  string
			Protocol int
			Socket   string
			Running  bool
		}
	}
	if err := json.Unmarshal(out, &status); err != nil {
		return err
	}
	if status.Client.Session != c.session || status.Server.Session != c.session || !status.Server.Running || status.Client.Protocol < 16 || status.Server.Protocol < 16 || !filepath.IsAbs(status.Server.Socket) {
		return errors.New("workspace ordering: unavailable named-session socket or protocol")
	}
	list, err := c.WorkspaceList(ctx)
	if err != nil {
		return err
	}
	byID := map[string]Workspace{}
	parentIndex := -1
	for i, w := range list {
		if checkID("workspace", w.ID) != nil {
			return errors.New("workspace ordering: malformed identity")
		}
		if _, ok := byID[w.ID]; ok {
			return errors.New("workspace ordering: duplicate identity")
		}
		byID[w.ID] = w
		if w.ID == parent {
			parentIndex = i
		}
	}
	if parentIndex < 0 || byID[child.ID].Label != child.Label {
		return errors.New("workspace ordering: missing parent or changed child")
	}
	// Remove only the newly created workspace from the planning snapshot.
	base := make([]Workspace, 0, len(list)-1)
	for _, w := range list {
		if w.ID != child.ID {
			base = append(base, w)
		}
	}
	parentIndex = -1
	for i, w := range base {
		if w.ID == parent {
			parentIndex = i
		}
	}
	index := parentIndex + 1
	seen := map[string]bool{parent: true, child.ID: true}
	for _, s := range siblings {
		if seen[s.ID] {
			return errors.New("workspace ordering: ambiguous sibling journal")
		}
		seen[s.ID] = true
		live, ok := byID[s.ID]
		if !ok {
			continue
		} // Closed spaces have no presentation left.
		if live.Label != s.Label || index >= len(base) || base[index].ID != s.ID {
			return errors.New("workspace ordering: renamed, detached or interleaved sibling")
		}
		index++
	}
	if index < len(base) && strings.HasPrefix(base[index].Label, "└") {
		return errors.New("workspace ordering: foreign child block")
	}
	expected := append([]Workspace(nil), base[:index]...)
	expected = append(expected, Workspace{ID: child.ID, Label: child.Label})
	expected = append(expected, base[index:]...)
	equal := func(a, b []Workspace) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i].ID != b[i].ID || a[i].Label != b[i].Label {
				return false
			}
		}
		return true
	}
	if equal(list, expected) {
		return nil
	}
	move := c.moveWorkspace
	if move == nil {
		move = socketWorkspaceMove
	}
	wire, err := move(ctx, status.Server.Socket, child.ID, index)
	if err != nil {
		return err
	}
	var response struct {
		ID     string          `json:"id"`
		Error  json.RawMessage `json:"error"`
		Result struct {
			Type       string      `json:"type"`
			Workspaces []Workspace `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(wire, &response); err != nil {
		return err
	}
	if response.ID != "guildhall-workspace-move" || (len(response.Error) > 0 && !reflect.DeepEqual(response.Error, json.RawMessage("null"))) || response.Result.Type != "workspace_list" || !equal(response.Result.Workspaces, expected) {
		return errors.New("workspace ordering: full returned order verification failed")
	}
	return nil
}

// socketWorkspaceMove sends only the whitelisted protocol-16 method. The
// endpoint is read from explicit named-session status, never the environment.
func socketWorkspaceMove(ctx context.Context, path, id string, index int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	request := struct {
		ID     string         `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}{"guildhall-workspace-move", "workspace.move", map[string]any{"workspace_id": id, "insert_index": index}}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(io.LimitReader(conn, 4*1024*1024+1)).ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if len(line) > 4*1024*1024 {
		return nil, fmt.Errorf("workspace ordering: oversized response")
	}
	return line, nil
}

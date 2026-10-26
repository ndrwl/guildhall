//go:build unix

package herdr

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSocketWorkspaceMoveWire(t *testing.T) {
	dir, err := os.MkdirTemp("", "gh-move-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "socket")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(time.Minute))
		var req struct {
			ID     string `json:"id"`
			Method string `json:"method"`
			Params struct {
				WorkspaceID string `json:"workspace_id"`
				InsertIndex int    `json:"insert_index"`
			} `json:"params"`
		}
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			done <- err
			return
		}
		if req.ID != "guildhall-workspace-move" || req.Method != "workspace.move" || req.Params.WorkspaceID != "child" || req.Params.InsertIndex != 2 {
			done <- &net.AddrError{Err: "unsafe wire request", Addr: req.Method}
			return
		}
		_, err = conn.Write([]byte("{\"id\":\"guildhall-workspace-move\",\"result\":{\"type\":\"workspace_list\",\"workspaces\":[]}}\n"))
		done <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := socketWorkspaceMove(ctx, path, "child", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out) {
		t.Fatalf("invalid response %q", out)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Cancelled and unavailable transports fail without a second request.
	cancel()
	if _, err := socketWorkspaceMove(ctx, path, "child", 2); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err := socketWorkspaceMove(context.Background(), path+"missing", "child", 2); err == nil {
		t.Fatal("accepted missing socket")
	}
}

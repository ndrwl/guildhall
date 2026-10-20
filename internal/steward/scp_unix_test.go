//go:build unix

package steward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTransportOpenSSHTimeout(t *testing.T) {
	scp, err := exec.LookPath("scp")
	if err != nil {
		t.Skip("OpenSSH scp not installed")
	}
	for _, operation := range []string{"fetch", "put"} {
		t.Run(operation, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			accepted := make(chan struct{})
			closed := make(chan struct{})
			go func() {
				defer close(closed)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				close(accepted)
				// A silent SSH peer, with a safety bound for the broken version.
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				_, _ = io.Copy(io.Discard, conn)
			}()
			defer func() { _ = listener.Close(); <-closed }()
			root := t.TempDir()
			wrapper := filepath.Join(root, "scp")
			// Isolate SSH configuration; no authentication is ever reached.
			script := fmt.Sprintf("#!/bin/sh\nexec '%s' -F /dev/null -P %d \"$@\"\n",
				strings.ReplaceAll(scp, "'", "'\"'\"'"), listener.Addr().(*net.TCPAddr).Port)
			if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			local := filepath.Join(root, "file")
			if err := os.WriteFile(local, []byte("unchanged"), 0o600); err != nil {
				t.Fatal(err)
			}
			transport := Transport{Path: wrapper, Timeout: 250 * time.Millisecond}
			remote := Remote{Alias: "127.0.0.1", Path: "/file"}
			start := time.Now()
			if operation == "fetch" {
				err = transport.Fetch(context.Background(), remote, local)
			} else {
				err = transport.Put(context.Background(), local, remote)
			}
			elapsed := time.Since(start)
			select {
			case <-accepted:
			default:
				t.Fatalf("scp did not connect to the silent peer: %v", err)
			}
			if !errors.Is(err, context.DeadlineExceeded) || elapsed > time.Second {
				t.Fatalf("250ms timeout returned after %s: %v", elapsed, err)
			}
			select {
			case <-closed:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("SSH child still holds its connection after cancellation")
			}
		})
	}
}

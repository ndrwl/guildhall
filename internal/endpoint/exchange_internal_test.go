package endpoint

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type silentServer struct{}

func (silentServer) Owns(struct{}) bool                  { return true }
func (silentServer) Serve(struct{}) map[string]string    { return nil }
func (silentServer) Refuse(msg string) map[string]string { return map[string]string{"error": msg} }

// TestASilentPeerIsDisconnected: a peer that sends no request line is refused and disconnected once the exchange
// bound passes, so it holds no connection open.
func TestASilentPeerIsDisconnected(t *testing.T) {
	old := exchangeTimeout
	exchangeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { exchangeTimeout = old })
	dir, err := os.MkdirTemp("", "ge")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	e, err := Listen[struct{}, map[string]string](filepath.Join(dir, "s.sock"), Protocol{Schema: "guildhall.test-silent/", Version: 1, MaxRequest: 64, MaxReply: 64}, silentServer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	c, err := net.Dial("unix", e.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	start := time.Now()
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || !strings.Contains(line, "malformed request") {
		t.Fatalf("reply to a silent peer %q, %v; want a refusal", line, err)
	}
	if took := time.Since(start); took < exchangeTimeout {
		t.Errorf("refused after %v, before the %v bound", took, exchangeTimeout)
	}
}

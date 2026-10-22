package endpoint

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCallRequestWriteBound(t *testing.T) {
	old := exchangeTimeout
	exchangeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { exchangeTimeout = old })
	dir, err := os.MkdirTemp("", "ge")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, err = Call[string, struct{}](ctx, path, Protocol{}, strings.Repeat("x", 8<<20), func(struct{}) bool { return true })
	if c := <-accepted; c != nil {
		defer c.Close()
	}
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() || ctx.Err() != nil {
		t.Fatalf("Call: %v, context: %v; want transport timeout", err, ctx.Err())
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("request write took %v", elapsed)
	}
}

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

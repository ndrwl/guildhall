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

// delayedRequest lets the write deadline expire before the encoder writes, without
// relying on a payload exceeding the platform's socket buffer capacity.
type delayedRequest struct{}

func (delayedRequest) MarshalJSON() ([]byte, error) {
	time.Sleep(2 * exchangeTimeout) // timing:allow intentionally expire an already established socket write deadline before encoding.
	return []byte(`{}`), nil
}

func TestCallRequestWriteBound(t *testing.T) {
	old := exchangeTimeout
	exchangeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { exchangeTimeout = old })
	dir, err := os.MkdirTemp("", "ge")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }() // Best-effort temporary socket directory cleanup.
	path := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }() // Cleanup only; listener-close errors are not under test.
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err = Call[delayedRequest, struct{}](ctx, path, Protocol{}, delayedRequest{}, func(struct{}) bool { return true })
	if c := <-accepted; c != nil {
		defer func() { _ = c.Close() }() // Cleanup only; connection-close errors are not under test.
	}
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() || ctx.Err() != nil {
		t.Fatalf("Call: %v, context: %v; want transport timeout", err, ctx.Err())
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
	start := time.Now()
	c, err := net.Dial("unix", e.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || !strings.Contains(line, "malformed request") {
		t.Fatalf("reply to a silent peer %q, %v; want a refusal", line, err)
	}
	if took := time.Since(start); took < exchangeTimeout {
		t.Errorf("refused after %v, before the %v bound", took, exchangeTimeout)
	}
}

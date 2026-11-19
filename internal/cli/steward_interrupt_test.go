package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// TestReadTypedValueInterrupted (H117): an interrupt while steward secrets set waits for a typed value ends the wait
// with an interrupted error, and echo is restored before it returns.
func TestReadTypedValueInterrupted(t *testing.T) {
	app, _, stderr := newTestApp(nil, nil)
	in, typist := io.Pipe()
	defer func() { _ = typist.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	restored := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := app.readTypedValue(ctx, in, func() { close(restored) }, "workstation", "API_KEY")
		done <- err
	}()
	// Nothing is ever typed on the pipe, so the read stays blocked and only the interrupt can end the wait.
	cancel()
	select {
	case err := <-done:
		if !strings.Contains(stderr.String(), "value of API_KEY for workstation (not shown): ") {
			t.Errorf("prompt %q", stderr)
		}
		if !errors.Is(err, steward.ErrInterrupted) || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "interrupted") {
			t.Errorf("error %v, want it interrupted", err)
		}
	case <-time.After(testenv.Ceiling(10 * time.Second)):
		t.Fatal("the interrupted prompt did not return")
	}
	select {
	case <-restored:
	default:
		t.Error("echo was not restored")
	}
}

// TestReadTypedValue: a typed line is the value, without its line ending, and echo is restored.
func TestReadTypedValue(t *testing.T) {
	app, _, _ := newTestApp(nil, nil)
	restored := false
	value, err := app.readTypedValue(context.Background(), strings.NewReader("s3cret\r\n"), func() { restored = true }, "workstation", "API_KEY")
	if err != nil || value != "s3cret" || !restored {
		t.Errorf("value %q, err %v, restored %v", value, err, restored)
	}
}

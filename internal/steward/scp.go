package steward

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Transport is the Copier that copies files with scp.
// It adds no credentials or trust options, so SSH's own config, agent, keys and host-key checks decide access,
// and batch mode makes scp fail rather than prompt.
type Transport struct {
	// Path is the resolved scp executable.
	Path string
	// Output runs a command and returns its combined output.
	Output func(ctx context.Context, path string, args ...string) ([]byte, error)
	// Timeout bounds each scp run.
	Timeout time.Duration
}

var _ Copier = Transport{}

func scpCommand(src, dst string) []string {
	return []string{"scp", "-q", "-B", "-o", "BatchMode=yes", "--", src, dst}
}

// Fetch runs scp from src to dst.
func (t Transport) Fetch(ctx context.Context, src Remote, dst string) error {
	return t.run(ctx, scpCommand(src.String(), dst))
}

// Put runs scp from src to dst.
func (t Transport) Put(ctx context.Context, src string, dst Remote) error {
	return t.run(ctx, scpCommand(src, dst.String()))
}

func (t Transport) run(ctx context.Context, argv []string) error {
	ctx, cancel := context.WithTimeout(ctx, t.Timeout)
	defer cancel()
	out, err := t.Output(ctx, t.Path, argv[1:]...)
	if err != nil {
		if ctx.Err() != nil {
			// scp stopped by the context reports only the signal that killed it.
			err = ctx.Err()
		}
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("scp: %w: %s", err, msg)
		}
		return fmt.Errorf("scp: %w", err)
	}
	return nil
}

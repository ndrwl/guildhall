package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/session"
)

// contextFromTurn consumes only the hook input's transcript_path. Failures are
// advisory: the caller must still signal the supervisor, including StopFailure.
// Empty stdin is valid for runtimes without Claude's hook payload.
func (a *App) contextFromTurn(dir string) error {
	if a.Stdin == nil {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(a.Stdin, maxStatusLineBytes+1))
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	if len(b) > maxStatusLineBytes {
		return fmt.Errorf("hook input exceeds 1 MiB")
	}
	var in struct {
		Transcript string `json:"transcript_path"`
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	if in.Transcript == "" {
		return nil
	}
	if !filepath.IsAbs(in.Transcript) {
		return fmt.Errorf("transcript_path must be absolute")
	}
	f, err := os.Open(in.Transcript)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("transcript is not a regular file")
	}
	percent, known, err := claude.TranscriptContext(f)
	if err != nil {
		return err
	}
	if !known {
		err := os.Remove(filepath.Join(dir, session.ContextFile))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	return session.WriteContext(dir, session.Context{UsedPercent: percent, ReportedAt: now().UTC()})
}

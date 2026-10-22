package session

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/strictjson"
)

// ContextFile is the context report's file name in the session directory.
const ContextFile = "context.json"

// NumberEnv names the session's number in the runtime's environment, set by the supervisor, so a command the
// runtime runs finds its session directory without reading the store.
const NumberEnv = "GUILDHALL_SESSION"

// contextSchema is the context report's schema; contextMaxBytes bounds it.
const (
	contextSchemaPrefix = "guildhall.session-context/"
	contextSchema       = "guildhall.session-context/1"
	contextMaxBytes     = 1 << 10
)

// Context is the newest context use a runtime reported for its session (docs/director.md#automatic-handoff).
type Context struct {
	// UsedPercent is the share of the context window in use, 0 to 100.
	UsedPercent int `json:"used_percent"`
	// ReportedAt is when the report was written, in UTC.
	ReportedAt time.Time `json:"reported_at"`
}

// contextFile is the context report's form on disk.
type contextFile struct {
	Schema      string    `json:"schema"`
	UsedPercent int       `json:"used_percent"`
	ReportedAt  time.Time `json:"reported_at"`
}

// WriteContext replaces the context report in the absolute session directory dir with c, atomically, so a reader
// sees the whole earlier or the whole new report. A percent outside 0 to 100 is refused, writing nothing.
func WriteContext(dir string, c Context) error {
	switch {
	case !filepath.IsAbs(dir):
		return fmt.Errorf("session: context report: directory %q is not absolute", dir)
	case c.UsedPercent < 0 || c.UsedPercent > 100:
		return fmt.Errorf("session: context report: used percent %d is not between 0 and 100", c.UsedPercent)
	}
	b, err := json.Marshal(contextFile{Schema: contextSchema, UsedPercent: c.UsedPercent, ReportedAt: c.ReportedAt})
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, ContextFile), append(b, '\n'))
}

// ReadContext reads the context report at path, strictly decoded; a missing file wraps fs.ErrNotExist, and a
// report that is not the schema, or a percent outside 0 to 100, is an error.
func ReadContext(path string) (Context, error) {
	b, err := atomicfile.Read(path)
	if err != nil {
		return Context{}, fmt.Errorf("session: context report: %w", err)
	}
	var f contextFile
	if err := strictjson.Decode(b, &f, strictjson.Options{Schema: contextSchemaPrefix, Version: 1, MaxBytes: contextMaxBytes}); err != nil {
		return Context{}, fmt.Errorf("session: context report %s: %w", path, err)
	}
	if f.UsedPercent < 0 || f.UsedPercent > 100 {
		return Context{}, fmt.Errorf("session: context report %s: used percent %d is not between 0 and 100", path, f.UsedPercent)
	}
	return Context{UsedPercent: f.UsedPercent, ReportedAt: f.ReportedAt}, nil
}

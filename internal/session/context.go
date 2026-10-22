package session

import (
	"errors"
	"time"
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

// WriteContext replaces the context report in the absolute session directory dir with c, atomically, so a reader
// sees the whole earlier or the whole new report. A percent outside 0 to 100 is refused, writing nothing.
func WriteContext(dir string, c Context) error {
	return errors.New("session: WriteContext: not implemented")
}

// ReadContext reads the context report at path, strictly decoded; a missing file wraps fs.ErrNotExist, and a
// report that is not the schema, or a percent outside 0 to 100, is an error.
func ReadContext(path string) (Context, error) {
	return Context{}, errors.New("session: ReadContext: not implemented")
}

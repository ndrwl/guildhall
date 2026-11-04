package session

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/strictjson"
)

// TurnFile is the last turn's file name in the session directory, which `director turn-started` and
// `director turn-ended` write and `director status` reads (docs/director.md#status).
const TurnFile = "turn.json"

// turnSchema is the last turn record's schema; turnMaxBytes bounds it.
const (
	turnSchemaPrefix = "guildhall.session-turn/"
	turnSchema       = "guildhall.session-turn/1"
	turnMaxBytes     = 1 << 10
)

// LastTurn is the newest turn signal a runtime's hooks gave its session: which it was and when, in UTC.
type LastTurn struct {
	Turn Turn      `json:"turn"`
	At   time.Time `json:"at"`
}

type turnFile struct {
	Schema string    `json:"schema"`
	Turn   Turn      `json:"turn"`
	At     time.Time `json:"at"`
}

// WriteLastTurn replaces the last turn record in the absolute session directory dir with t, atomically. A turn that
// is not TurnStarted or TurnEnded is refused, writing nothing.
func WriteLastTurn(dir string, t LastTurn) error {
	switch {
	case !filepath.IsAbs(dir):
		return fmt.Errorf("session: last turn: directory %q is not absolute", dir)
	case t.Turn != TurnStarted && t.Turn != TurnEnded:
		return fmt.Errorf("session: last turn: %q is not a turn signal", t.Turn)
	}
	b, err := json.Marshal(turnFile{Schema: turnSchema, Turn: t.Turn, At: t.At.UTC()})
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, TurnFile), append(b, '\n'))
}

// ReadLastTurn reads the last turn record at path, strictly decoded; a missing file wraps fs.ErrNotExist.
func ReadLastTurn(path string) (LastTurn, error) {
	b, err := atomicfile.Read(path)
	if err != nil {
		return LastTurn{}, fmt.Errorf("session: last turn: %w", err)
	}
	var f turnFile
	if err := strictjson.Decode(b, &f, strictjson.Options{Schema: turnSchemaPrefix, Version: 1, MaxBytes: turnMaxBytes}); err != nil {
		return LastTurn{}, fmt.Errorf("session: last turn %s: %w", path, err)
	}
	if f.Turn != TurnStarted && f.Turn != TurnEnded {
		return LastTurn{}, fmt.Errorf("session: last turn %s: %q is not a turn signal", path, f.Turn)
	}
	return LastTurn{Turn: f.Turn, At: f.At}, nil
}

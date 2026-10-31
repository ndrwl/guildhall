package workbench

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func env(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }

// TestDetectNonTerminal: a writer that is not a terminal, a buffer or a regular file, gets no colour, and its
// width is COLUMNS, else 80, and never under 40.
func TestDetectNonTerminal(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "pane"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	for _, out := range []any{&bytes.Buffer{}, f} {
		w := out.(interface{ Write([]byte) (int, error) })
		for columns, want := range map[string]int{"": 80, "120": 120, "20": 40, "wide": 80, "-5": 80} {
			got := Detect(w, env(map[string]string{"TERM": "xterm-256color", "COLUMNS": columns}))
			if got.Color || got.Width != want {
				t.Errorf("Detect(%T, COLUMNS=%q) = %+v, want no colour at width %d", out, columns, got, want)
			}
		}
	}
}

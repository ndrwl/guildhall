package session

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestContextReportRoundTrips: a written report is read back whole, and a newer one replaces it.
func TestContextReportRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ContextFile)
	if _, err := ReadContext(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no report = %v, want fs.ErrNotExist", err)
	}
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	for _, c := range []Context{{UsedPercent: 42, ReportedAt: at}, {UsedPercent: 100, ReportedAt: at.Add(time.Minute)}, {UsedPercent: 0, ReportedAt: at.Add(2 * time.Minute)}} {
		if err := WriteContext(dir, c); err != nil {
			t.Fatal(err)
		}
		if got, err := ReadContext(path); err != nil || got != c {
			t.Fatalf("read back %+v, %v; want %+v", got, err, c)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), `"schema":"guildhall.session-context/1"`) {
		t.Fatalf("report = %s, %v; want the schema named", b, err)
	}
}

func TestContextReportRefusals(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []int{-1, 101} {
		if err := WriteContext(dir, Context{UsedPercent: p, ReportedAt: time.Now().UTC()}); err == nil {
			t.Errorf("WriteContext(%d%%) wrote", p)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ContextFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused report left a file: %v", err)
	}
	if err := WriteContext("relative", Context{UsedPercent: 1}); err == nil {
		t.Error("WriteContext to a relative directory wrote")
	}
	bad := map[string]string{
		"not json":      `percent: 4`,
		"no schema":     `{"used_percent":4,"reported_at":"2026-10-22T12:00:00Z"}`,
		"newer schema":  `{"schema":"guildhall.session-context/2","used_percent":4,"reported_at":"2026-10-22T12:00:00Z"}`,
		"unknown field": `{"schema":"guildhall.session-context/1","used_percent":4,"reported_at":"2026-10-22T12:00:00Z","cost":1}`,
		"over 100":      `{"schema":"guildhall.session-context/1","used_percent":140,"reported_at":"2026-10-22T12:00:00Z"}`,
	}
	for name, body := range bad {
		path := filepath.Join(t.TempDir(), ContextFile)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := ReadContext(path); err == nil {
			t.Errorf("%s: read %+v, want an error", name, got)
		}
	}
}

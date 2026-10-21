package artisan_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/strictjson"
)

func beat() artisan.Heartbeat {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return artisan.Heartbeat{Repository: "app", TaskID: "t7", Step: "build", Attempt: 1, Sequence: 1, Time: at, WorkerState: "running", LastOutputAt: at.Add(-time.Minute)}
}

func TestHeartbeatFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, err := artisan.ReadHeartbeat(dir); !errors.Is(err, artisan.ErrNoHeartbeat) {
		t.Fatalf("missing file: %v, want ErrNoHeartbeat", err)
	}
	h := beat()
	if err := artisan.WriteHeartbeat(dir, h); err != nil {
		t.Fatal(err)
	}
	h.Sequence++
	h.Time = h.Time.Add(time.Second)
	if err := artisan.WriteHeartbeat(dir, h); err != nil {
		t.Fatal(err)
	}
	h.Schema = artisan.HeartbeatSchema
	got, err := artisan.ReadHeartbeat(dir)
	if err != nil || got != h {
		t.Fatalf("read %+v, %v; want %+v", got, err, h)
	}
	b, err := os.ReadFile(filepath.Join(dir, artisan.HeartbeatFile))
	if err != nil || !strings.Contains(string(b), `"schema": "guildhall.attempt-heartbeat/1"`) {
		t.Fatalf("file %s, %v; want schema guildhall.attempt-heartbeat/1", b, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestWriteHeartbeatRefusesInvalid(t *testing.T) {
	for name, change := range map[string]func(*artisan.Heartbeat){
		"zero sequence":        func(h *artisan.Heartbeat) { h.Sequence = 0 },
		"zero attempt":         func(h *artisan.Heartbeat) { h.Attempt = 0 },
		"blank step":           func(h *artisan.Heartbeat) { h.Step = "" },
		"zero time":            func(h *artisan.Heartbeat) { h.Time = time.Time{} },
		"zero last output":     func(h *artisan.Heartbeat) { h.LastOutputAt = time.Time{} },
		"output after time":    func(h *artisan.Heartbeat) { h.LastOutputAt = h.Time.Add(time.Second) },
		"blank worker state":   func(h *artisan.Heartbeat) { h.WorkerState = " " },
		"long worker state":    func(h *artisan.Heartbeat) { h.WorkerState = strings.Repeat("x", 257) },
		"invalid worker state": func(h *artisan.Heartbeat) { h.WorkerState = "\xff" },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			h := beat()
			change(&h)
			if err := artisan.WriteHeartbeat(dir, h); !errors.Is(err, artisan.ErrInvalidHeartbeat) {
				t.Fatalf("write = %v, want ErrInvalidHeartbeat", err)
			}
			if _, err := artisan.ReadHeartbeat(dir); !errors.Is(err, artisan.ErrNoHeartbeat) {
				t.Fatalf("a refused write left a heartbeat: %v", err)
			}
		})
	}
}

func TestReadHeartbeatRefuses(t *testing.T) {
	const fields = `"repository":"app","task_id":"t7","step":"build","attempt":1,"sequence":1,"time":"2026-01-01T00:00:00Z","worker_state":"running","last_output_at":"2025-12-31T23:59:00Z"`
	write := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, artisan.HeartbeatFile), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	t.Run("newer schema", func(t *testing.T) {
		_, err := artisan.ReadHeartbeat(write(t, `{"schema":"guildhall.attempt-heartbeat/2",`+fields+`,"extra":true}`))
		if !errors.Is(err, strictjson.ErrNewerSchema) || errors.Is(err, artisan.ErrInvalidHeartbeat) {
			t.Fatalf("read = %v, want ErrNewerSchema only", err)
		}
	})
	for name, body := range map[string]string{
		"unversioned":       `{` + fields + `}`,
		"other schema":      `{"schema":"guildhall.attempt-control/1",` + fields + `}`,
		"unknown field":     `{"schema":"guildhall.attempt-heartbeat/1",` + fields + `,"pid":7}`,
		"duplicate field":   `{"schema":"guildhall.attempt-heartbeat/1",` + fields + `,"sequence":2}`,
		"zero sequence":     `{"schema":"guildhall.attempt-heartbeat/1",` + strings.Replace(fields, `"sequence":1`, `"sequence":0`, 1) + `}`,
		"blank state":       `{"schema":"guildhall.attempt-heartbeat/1",` + strings.Replace(fields, `"running"`, `""`, 1) + `}`,
		"output after time": `{"schema":"guildhall.attempt-heartbeat/1",` + strings.Replace(fields, "2025-12-31T23:59:00Z", "2026-01-01T00:00:01Z", 1) + `}`,
		"trailing data":     `{"schema":"guildhall.attempt-heartbeat/1",` + fields + `} {}`,
		"torn":              `{"schema":"guildhall.attempt-heartbeat/1",`,
		"empty":             ``,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := artisan.ReadHeartbeat(write(t, body))
			if !errors.Is(err, artisan.ErrInvalidHeartbeat) || errors.Is(err, strictjson.ErrNewerSchema) || errors.Is(err, artisan.ErrNoHeartbeat) {
				t.Fatalf("read = %v, want ErrInvalidHeartbeat", err)
			}
		})
	}
}

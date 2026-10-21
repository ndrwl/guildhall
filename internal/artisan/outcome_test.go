package artisan_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/strictjson"
)

func TestReadOutcome(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, artisan.OutcomeFile), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	t.Run("accepted", func(t *testing.T) {
		got, err := artisan.ReadOutcome(write(t, `{"schema":"guildhall.attempt-outcome/1","state":"cancelled","detail":"stopped","late_result_rejected":true,"rung":4,"survivors":[{"pid":7,"token":"t"}]}`))
		if err != nil || got.State != artisan.Cancelled || got.Detail != "stopped" || !got.LateResultRejected || got.Rung != 4 || len(got.Survivors) != 1 || got.Survivors[0] != (artisan.Identity{PID: 7, Token: "t"}) {
			t.Fatalf("read = %+v, %v", got, err)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		_, err := artisan.ReadOutcome(t.TempDir())
		if !errors.Is(err, fs.ErrNotExist) || errors.Is(err, artisan.ErrInvalidOutcome) {
			t.Fatalf("read = %v, want fs.ErrNotExist only", err)
		}
	})
	t.Run("newer schema", func(t *testing.T) {
		_, err := artisan.ReadOutcome(write(t, `{"schema":"guildhall.attempt-outcome/2","state":"finished","detail":"","extra":true}`))
		if !errors.Is(err, strictjson.ErrNewerSchema) || errors.Is(err, artisan.ErrInvalidOutcome) {
			t.Fatalf("read = %v, want ErrNewerSchema only", err)
		}
	})
	const fields = `"state":"failed","detail":"worker exited without a result"`
	for name, body := range map[string]string{
		"unversioned":     `{` + fields + `}`,
		"other schema":    `{"schema":"guildhall.attempt-heartbeat/1",` + fields + `}`,
		"unknown field":   `{"schema":"guildhall.attempt-outcome/1",` + fields + `,"pid":7}`,
		"nested unknown":  `{"schema":"guildhall.attempt-outcome/1",` + fields + `,"survivors":[{"pid":7,"token":"t","name":"x"}]}`,
		"duplicate field": `{"schema":"guildhall.attempt-outcome/1",` + fields + `,"state":"finished"}`,
		"trailing data":   `{"schema":"guildhall.attempt-outcome/1",` + fields + `} {}`,
		"unknown state":   `{"schema":"guildhall.attempt-outcome/1","state":"running","detail":""}`,
		"torn":            `{"schema":"guildhall.attempt-outcome/1",`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := artisan.ReadOutcome(write(t, body))
			if !errors.Is(err, artisan.ErrInvalidOutcome) || errors.Is(err, strictjson.ErrNewerSchema) || errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("read = %v, want ErrInvalidOutcome", err)
			}
		})
	}
}

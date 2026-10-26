package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ndrwl/guildhall/internal/testenv"
)

func TestSpecValidate(t *testing.T) {
	dir := t.TempDir()
	good := Spec{Identity: testID, WorkDir: dir, PromptFile: filepath.Join(dir, "p.md"), ResultFile: filepath.Join(dir, "r.json")}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid spec refused: %v", err)
	}
	if granted := (Spec{Identity: testID, WorkDir: dir, PromptFile: good.PromptFile, ResultFile: good.ResultFile, Copy: dir, Profile: "builder"}); granted.Validate() != nil {
		t.Errorf("spec with an allowed tool refused: %v", granted.Validate())
	}
	bad := []Spec{
		{Identity: Identity{}, WorkDir: dir, PromptFile: good.PromptFile, ResultFile: good.ResultFile},
		{Identity: testID, WorkDir: "rel", PromptFile: good.PromptFile, ResultFile: good.ResultFile},
		{Identity: testID, WorkDir: dir, PromptFile: "p.md", ResultFile: good.ResultFile},
		{Identity: testID, WorkDir: dir, PromptFile: good.PromptFile, ResultFile: "r.json"},
		{Identity: testID, WorkDir: dir, PromptFile: good.PromptFile, ResultFile: good.PromptFile},
		{Identity: testID, WorkDir: dir, PromptFile: good.PromptFile, ResultFile: good.ResultFile, Env: []string{"NOVALUE"}},
	}
	for _, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("spec %+v accepted", s)
		}
	}
}

func TestCapabilitiesDefaultToNone(t *testing.T) {
	if (Capabilities{}).Any() {
		t.Error("zero Capabilities reports a capability")
	}
	if !(Capabilities{Hooks: true}).Any() {
		t.Error("Capabilities with hooks reports none")
	}
}

func TestWaitResultReturnsOnlyFromTheFile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "r.json")
		done := make(chan struct{})
		go func() {
			defer close(done)
			r, err := WaitResult(context.Background(), path, testID, WaitOptions{Poll: 5 * time.Millisecond})
			if err != nil || r.Status != StatusFailed {
				t.Errorf("WaitResult = %+v, %v", r, err)
			}
		}()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("returned without a result")
		default:
		}
		if err := WriteResult(path, Result{Identity: testID, Status: StatusFailed, Summary: "gave up"}); err != nil {
			t.Fatal(err)
		}
		<-done
	})
}

func TestWaitResultTimesOutWithoutAResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		_, err := WaitResult(ctx, filepath.Join(t.TempDir(), "r.json"), testID, WaitOptions{Poll: 5 * time.Millisecond})
		if !errors.Is(err, ErrNoResultYet) {
			t.Errorf("WaitResult = %v, want ErrNoResultYet", err)
		}
	})
}

func TestWaitResultRejectsMalformedAtOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	if err := os.WriteFile(path, []byte(`{"schema":`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(30*time.Second))
	defer cancel()
	if _, err := WaitResult(ctx, path, testID, WaitOptions{Poll: 5 * time.Millisecond}); !errors.Is(err, ErrMalformedResult) {
		t.Errorf("WaitResult = %v, want ErrMalformedResult", err)
	}
}

func TestWaitResultReportsExitWithoutResult(t *testing.T) {
	exited := make(chan struct{})
	close(exited)
	ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(30*time.Second))
	defer cancel()
	_, err := WaitResult(ctx, filepath.Join(t.TempDir(), "r.json"), testID, WaitOptions{Poll: 5 * time.Millisecond, Exited: exited})
	if !errors.Is(err, ErrExitedWithoutResult) {
		t.Errorf("WaitResult = %v, want ErrExitedWithoutResult", err)
	}
}

func TestWaitResultPrefersResultOverExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	if err := WriteResult(path, Result{Identity: testID, Status: StatusSucceeded, Summary: "ok"}); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	close(exited)
	r, err := WaitResult(context.Background(), path, testID, WaitOptions{Poll: 5 * time.Millisecond, Exited: exited})
	if err != nil || r.Status != StatusSucceeded {
		t.Errorf("WaitResult = %+v, %v", r, err)
	}
}

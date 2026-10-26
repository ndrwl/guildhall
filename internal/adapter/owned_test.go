package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/testenv"
)

func TestOwnedWorkerHelper(t *testing.T) {
	if os.Getenv("GUILDHALL_OWNED_HELPER") != "1" {
		return
	}
	id := Identity{TaskID: "task", AttemptID: "attempt"}
	ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(60*time.Second))
	defer cancel()
	if err := JoinOwnedWorker(ctx, os.Getenv("GUILDHALL_CONTROL"), os.Getenv("GUILDHALL_TOKEN"), id, os.Getenv("GUILDHALL_RESULT")); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(os.Getenv("GUILDHALL_WORK"), []byte("unfinished work"), 0600); err != nil {
		os.Exit(3)
	}
	for {
		if _, err := os.Stat(os.Getenv("GUILDHALL_WORK") + ".release"); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			os.Exit(5)
		case <-time.After(10 * time.Millisecond): // timing:allow release-file polling bounded by ctx.
		}
	}
	err := WriteResult(os.Getenv("GUILDHALL_RESULT"), Result{Identity: id, Status: StatusSucceeded, Summary: "done", Outputs: []Output{}})
	if err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}

func TestOwnedWorkerStopContract(t *testing.T) {
	if runtime.GOOS != "windows" {
		testenv.SkipUnlessRequired(t, "windows", "native handle contract requires Windows")
	}
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	start := func(name string) (*OwnedWorker, *exec.Cmd, string, string) {
		t.Helper()
		result, work := filepath.Join(dir, name+".json"), filepath.Join(dir, name+".work")
		spec := Spec{Identity: Identity{TaskID: "task", AttemptID: "attempt"}, ResultFile: result, PromptFile: filepath.Join(dir, "prompt"), WorkDir: dir}
		owner, err := NewOwnedWorker(spec, exe)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = owner.Close() })
		cmd := exec.Command(exe, "-test.run=^TestOwnedWorkerHelper$")
		cmd.Env = append(os.Environ(), "GUILDHALL_OWNED_HELPER=1", "GUILDHALL_CONTROL="+owner.Address(), "GUILDHALL_TOKEN="+owner.Token(), "GUILDHALL_RESULT="+result, "GUILDHALL_WORK="+work)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(15*time.Second))
		defer cancel()
		if err := owner.Accept(ctx); err != nil {
			t.Fatal(err)
		}
		for {
			if _, err := os.Stat(work); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("worker did not begin")
			case <-time.After(10 * time.Millisecond): // timing:allow work-file polling bounded by ctx.
			}
		}
		return owner, cmd, result, work
	}
	owner, target, result, work := start("target")
	_, unrelated, otherResult, otherWork := start("unrelated")
	ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(10*time.Second))
	defer cancel()
	if err := owner.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := target.Wait(); err == nil {
		t.Fatal("terminated worker exited successfully")
	}
	if err := owner.Stop(ctx); err != nil {
		t.Fatalf("repeat stop: %v", err)
	}
	for _, path := range []string{work + ".release", otherWork + ".release"} {
		if err := os.WriteFile(path, []byte("finish"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := unrelated.Wait(); err != nil {
		t.Fatalf("unrelated worker affected: %v", err)
	}
	if _, err := os.Stat(otherResult); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stopped worker published result: %v", err)
	}
	if data, err := os.ReadFile(work); err != nil || string(data) != "unfinished work" {
		t.Fatalf("unfinished work lost: %q, %v", data, err)
	}
	if err := WriteResult(result, Result{Identity: Identity{TaskID: "task", AttemptID: "attempt"}, Status: StatusSucceeded, Summary: "late", Outputs: []Output{}}); err == nil {
		t.Fatal("late publication allowed")
	}
}

func TestOwnedWorkerRejectsUnverifiedIdentity(t *testing.T) {
	if runtime.GOOS != "windows" {
		testenv.SkipUnlessRequired(t, "windows", "native handle identity requires Windows")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	created, err := nativeCreationTime()
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"token", "attempt", "result", "creation", "image"} {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			spec := Spec{Identity: Identity{TaskID: "task", AttemptID: "attempt"}, PromptFile: filepath.Join(dir, "prompt"), ResultFile: filepath.Join(dir, "result"), WorkDir: dir}
			image := exe
			if fault == "image" {
				image = filepath.Join(dir, "unrelated.exe")
			}
			o, err := NewOwnedWorker(spec, image)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := o.Close(); err != nil {
					t.Errorf("close owned worker: %v", err)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(10*time.Second))
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- o.Accept(ctx) }()
			conn, err := net.Dial("tcp4", o.Address())
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := conn.Close(); err != nil {
					t.Errorf("close connection: %v", err)
				}
			}()
			hello := workerHello{Identity: spec.Identity, Token: o.Token(), Result: spec.ResultFile, PID: uint32(os.Getpid()), Created: created}
			switch fault {
			case "token":
				hello.Token = "wrong"
			case "attempt":
				hello.AttemptID = "other"
			case "result":
				hello.Result += ".other"
			case "creation":
				hello.Created++
			}
			if err := json.NewEncoder(conn).Encode(hello); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Fatal("accepted unverified worker identity")
			}
			if o.worker != nil {
				t.Fatal("retained unverified native process")
			}
		})
	}
}

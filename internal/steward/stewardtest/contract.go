// Package stewardtest holds the behavioural contract every steward.Copier must
// pass and an in-memory Fleet that passes it, for testing consumers of rollouts
// without scp, ssh or a network.
package stewardtest

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// Harness gives the contract suite a fresh Copier and the enrolled machines it reaches.
// Each call to the suite's factory returns a new one with no files and every machine reachable.
type Harness interface {
	Copier() steward.Copier
	// Write sets a file on a machine, creating its directories.
	Write(t *testing.T, file steward.Remote, data []byte)
	// Read returns a file on a machine and whether it exists.
	Read(t *testing.T, file steward.Remote) ([]byte, bool)
	// Refuse makes every copy to or from the machine with alias fail, as an unreachable or refusing machine does.
	Refuse(t *testing.T, alias string)
	// Hang makes every copy to or from the machine with alias wait, transferring nothing, until its context ends.
	Hang(t *testing.T, alias string)
}

// The machines the suite uses.
const (
	Alpha = "alpha"
	Bravo = "bravo"
)

// content has CRLF and non-UTF-8 bytes, which a copy must carry unchanged.
var content = []byte("schema_version = 1\r\n# \xff\xfe\x00 bytes\n")

// RunCopierContract runs the Copier contract against harnesses from newHarness.
func RunCopierContract(t *testing.T, newHarness func(t *testing.T) Harness) {
	managed := func(alias string) steward.Remote {
		return steward.Remote{Alias: alias, Path: "/home/guild/tools/managed.toml"}
	}
	for _, c := range []struct {
		name string
		run  func(t *testing.T, h Harness, dir string)
	}{
		{"fetch copies the remote file exactly and replaces the local file", func(t *testing.T, h Harness, dir string) {
			h.Write(t, managed(Alpha), content)
			local := filepath.Join(dir, "fetched.toml")
			writeLocal(t, local, append(bytes.Repeat([]byte("old "), 64), '\n'))
			if err := h.Copier().Fetch(context.Background(), managed(Alpha), local); err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if got := readLocal(t, local); !bytes.Equal(got, content) {
				t.Errorf("fetched %q, want %q", got, content)
			}
		}},
		{"put creates or replaces the remote file exactly", func(t *testing.T, h Harness, dir string) {
			h.Write(t, managed(Alpha), append(bytes.Repeat([]byte("old "), 64), '\n'))
			local := filepath.Join(dir, "managed.toml")
			writeLocal(t, local, content)
			revision := steward.Remote{Alias: Alpha, Path: managed(Alpha).Path + ".rev-0123456789abcdef"}
			for _, dst := range []steward.Remote{managed(Alpha), revision} {
				if err := h.Copier().Put(context.Background(), local, dst); err != nil {
					t.Fatalf("Put %s: %v", dst, err)
				}
				if got, ok := h.Read(t, dst); !ok || !bytes.Equal(got, content) {
					t.Errorf("%s holds %q (exists %v), want %q", dst, got, ok, content)
				}
			}
		}},
		{"machines are separate", func(t *testing.T, h Harness, dir string) {
			h.Write(t, managed(Alpha), []byte("alpha\n"))
			h.Write(t, managed(Bravo), []byte("bravo\n"))
			local := filepath.Join(dir, "managed.toml")
			writeLocal(t, local, content)
			if err := h.Copier().Put(context.Background(), local, managed(Bravo)); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if got, _ := h.Read(t, managed(Alpha)); string(got) != "alpha\n" {
				t.Errorf("put to %s changed %s to %q", Bravo, Alpha, got)
			}
		}},
		{"missing remote file fails and leaves the local file", func(t *testing.T, h Harness, dir string) {
			local := filepath.Join(dir, "fetched.toml")
			writeLocal(t, local, []byte("kept\n"))
			if err := h.Copier().Fetch(context.Background(), managed(Alpha), local); err == nil {
				t.Fatal("Fetch of a missing file succeeded")
			}
			if got := readLocal(t, local); string(got) != "kept\n" {
				t.Errorf("failed fetch left %q, want the previous file", got)
			}
		}},
		{"missing local file fails and leaves the remote file", func(t *testing.T, h Harness, dir string) {
			h.Write(t, managed(Alpha), []byte("kept\n"))
			if err := h.Copier().Put(context.Background(), filepath.Join(dir, "absent.toml"), managed(Alpha)); err == nil {
				t.Fatal("Put of a missing file succeeded")
			}
			if got, _ := h.Read(t, managed(Alpha)); string(got) != "kept\n" {
				t.Errorf("failed put left %q, want the previous file", got)
			}
		}},
		{"refused machine fails with a diagnostic and changes nothing", func(t *testing.T, h Harness, dir string) {
			h.Write(t, managed(Alpha), []byte("remote\n"))
			h.Write(t, managed(Bravo), []byte("bravo\n"))
			local := filepath.Join(dir, "local.toml")
			writeLocal(t, local, []byte("local\n"))
			h.Refuse(t, Alpha)
			err := h.Copier().Fetch(context.Background(), managed(Alpha), local)
			if err == nil || err.Error() == "" {
				t.Errorf("Fetch from a refused machine: error %v, want a diagnostic", err)
			}
			err = h.Copier().Put(context.Background(), local, managed(Alpha))
			if err == nil || err.Error() == "" {
				t.Errorf("Put to a refused machine: error %v, want a diagnostic", err)
			}
			if got := readLocal(t, local); string(got) != "local\n" {
				t.Errorf("refused fetch left %q", got)
			}
			if got, _ := h.Read(t, managed(Alpha)); string(got) != "remote\n" {
				t.Errorf("refused put left %q", got)
			}
			if err := h.Copier().Fetch(context.Background(), managed(Bravo), local); err != nil {
				t.Errorf("Fetch from a reachable machine: %v", err)
			}
		}},
		{"deadline stops a hung copy and changes nothing", func(t *testing.T, h Harness, dir string) {
			h.Write(t, managed(Alpha), []byte("remote\n"))
			local := filepath.Join(dir, "local.toml")
			writeLocal(t, local, []byte("local\n"))
			h.Hang(t, Alpha)
			for name, transfer := range map[string]func(context.Context) error{
				"Fetch": func(ctx context.Context) error { return h.Copier().Fetch(ctx, managed(Alpha), local) },
				"Put":   func(ctx context.Context) error { return h.Copier().Put(ctx, local, managed(Alpha)) },
			} {
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond) // timing:allow intentionally hung transfer; no startup or success must fit this deadline.
				done := make(chan error, 1)
				go func() { done <- transfer(ctx) }()
				select {
				case err := <-done:
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Errorf("hung %s: error %v, want context.DeadlineExceeded", name, err)
					}
				case <-time.After(testenv.Ceiling(30 * time.Second)):
					t.Fatalf("hung %s did not return after its deadline", name)
				}
				cancel()
			}
			if got := readLocal(t, local); string(got) != "local\n" {
				t.Errorf("hung fetch left %q", got)
			}
			if got, _ := h.Read(t, managed(Alpha)); string(got) != "remote\n" {
				t.Errorf("hung put left %q", got)
			}
		}},
		{"cancelled context copies nothing", func(t *testing.T, h Harness, dir string) {
			h.Write(t, managed(Alpha), []byte("remote\n"))
			local := filepath.Join(dir, "local.toml")
			writeLocal(t, local, []byte("local\n"))
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := h.Copier().Fetch(ctx, managed(Alpha), local); !errors.Is(err, context.Canceled) {
				t.Errorf("Fetch: error %v, want context.Canceled", err)
			}
			if err := h.Copier().Put(ctx, local, managed(Alpha)); !errors.Is(err, context.Canceled) {
				t.Errorf("Put: error %v, want context.Canceled", err)
			}
			if got := readLocal(t, local); string(got) != "local\n" {
				t.Errorf("cancelled fetch left %q", got)
			}
			if got, _ := h.Read(t, managed(Alpha)); string(got) != "remote\n" {
				t.Errorf("cancelled put left %q", got)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) { c.run(t, newHarness(t), t.TempDir()) })
	}
}

func writeLocal(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readLocal(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

//go:build unix

package fake

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestAckDirectiveRunsGuildhallMessagesAck: the ack directive runs guildhall messages ack, the guildhall on PATH, for
// each message it names, in order, before the result is written; a failed ack is reported and the result still
// written, for intake to refuse.
func TestAckDirectiveRunsGuildhallMessagesAck(t *testing.T) {
	bin := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	script := "#!/bin/sh\necho \"$*\" >> '" + calls + "'\n[ \"$3\" = bad ] && exit 1\necho receipt\n"
	if err := os.WriteFile(filepath.Join(bin, "guildhall"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	args := setup(t, "guildhall-fake: ack=steer-1,bad,steer-2\n")
	out, err := run(t, args)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	got, err := os.ReadFile(calls)
	if err != nil || string(got) != "messages ack steer-1\nmessages ack bad\nmessages ack steer-2\n" {
		t.Fatalf("guildhall ran %q, %v", got, err)
	}
	if !strings.Contains(out, "acknowledged steering message steer-1") || !strings.Contains(out, "guildhall messages ack bad: exit status 1") {
		t.Errorf("output %q does not report the acks", out)
	}
	if _, err := os.Stat(args.ResultFile); err != nil {
		t.Errorf("no result after the acks: %v", err)
	}
}

func TestIgnoreInterrupt(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		inv := t.TempDir()
		prompt := "guildhall-fake: result=never\nguildhall-fake: lifetime=1h\nguildhall-fake: inventory=" + inv + "\n"
		if ignore {
			prompt += "guildhall-fake: ignore-interrupt=true\n"
		}
		args := setup(t, prompt)
		p := startFake(t, args)
		recorded(t, inv, RoleWorker, 1)
		if err := p.cmd.Process.Signal(syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
		bound := deadline
		if ignore {
			bound = time.Second
		} // timing:allow negative observation only; ignored signal must leave the acknowledged worker alive.
		select {
		case <-p.done:
			if ignore {
				t.Error("a fake ignoring interrupts exited on SIGINT")
			}
		case <-time.After(bound):
			if !ignore {
				t.Error("a fake did not exit on SIGINT")
			}
		}
	}
}

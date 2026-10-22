package claude

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

const emitEnv = "GUILDHALL_CLAUDE_TEST_EMIT"

// TestLimitedBufferCapsAChildsOutput: a child process writing more than the limit to a limitedBuffer leaves only
// the first limit bytes kept and the buffer marked truncated.
func TestLimitedBufferCapsAChildsOutput(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestEmitOutput$")
	cmd.Env = append(os.Environ(), emitEnv+"=100000")
	out := &limitedBuffer{limit: 10}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if got := out.buf.String(); got != strings.Repeat("x", 10) || !out.truncated {
		t.Errorf("kept %d bytes, truncated %v; want 10 bytes, truncated", len(got), out.truncated)
	}
}

// TestEmitOutput is the child of TestLimitedBufferCapsAChildsOutput: it writes the asked number of bytes and exits.
func TestEmitOutput(t *testing.T) {
	n, err := strconv.Atoi(os.Getenv(emitEnv))
	if err != nil {
		t.Skip("run only as a child")
	}
	_, _ = os.Stdout.WriteString(strings.Repeat("x", n))
	os.Exit(0)
}

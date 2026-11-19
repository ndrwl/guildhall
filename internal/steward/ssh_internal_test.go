package steward

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

// certutilMissing is what certutil -hashfile prints on standard output for a path with no file.
const certutilMissing = "CertUtil: -hashfile command FAILED: 0x80070002 (WIN32: 2 ERROR_FILE_NOT_FOUND)\r\nCertUtil: The system cannot find the file specified.\r\n"

// sshWarning is a line OpenSSH itself prints on standard error, before the remote command's own output.
const sshWarning = "** WARNING: connection is not using a post-quantum key exchange algorithm."

// TestBareMachineSeesCertutilBehindSSHWarning: certutil's "file not found" on stdout still marks a Windows machine as
// bare when ssh printed a warning on stderr (H120).
func TestBareMachineSeesCertutilBehindSSHWarning(t *testing.T) {
	st := newStub(t)
	st.answer("laptop", certutilMissing, sshWarning)
	s := &Setup{Runner: st.runner()}
	bare, err := s.bareMachine(context.Background(), Machine{
		Label: "laptop", SSHAlias: "laptop", OS: OSWindows, GuildhallPath: "C:/Users/guild/.local/bin/guildhall.exe",
	})
	if err != nil || !bare {
		t.Fatalf("bareMachine = %v, %v; want true, nil", bare, err)
	}
}

// TestSSHRunErrorKeepsBothStreams: a failed command's error carries stderr, then stdout.
func TestSSHRunErrorKeepsBothStreams(t *testing.T) {
	st := newStub(t)
	st.answer("laptop", certutilMissing, sshWarning)
	_, err := st.runner().Run(context.Background(), "laptop", HashCommand(OSWindows, "C:/Users/guild/.local/bin/guildhall.exe"))
	if err == nil {
		t.Fatal("Run succeeded; want the command's failure")
	}
	msg := err.Error()
	w, c := strings.Index(msg, sshWarning), strings.Index(msg, "ERROR_FILE_NOT_FOUND")
	if !strings.HasPrefix(msg, "ssh: ") || w < 0 || c < 0 || w > c {
		t.Errorf("error %q: want the ssh: prefix, then the warning, then certutil's ERROR_FILE_NOT_FOUND", msg)
	}
}

// TestSSHRunErrorCapsEachStream: each stream is capped on its own, so a long stderr cannot push out stdout's head.
func TestSSHRunErrorCapsEachStream(t *testing.T) {
	st := newStub(t)
	st.answer("laptop", "ERROR_FILE_NOT_FOUND"+strings.Repeat("y", 64<<10), strings.Repeat("x", 64<<10))
	_, err := st.runner().Run(context.Background(), "laptop", HashCommand(OSWindows, "C:/Users/guild/.local/bin/guildhall.exe"))
	if err == nil {
		t.Fatal("Run succeeded; want the command's failure")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ERROR_FILE_NOT_FOUND") {
		t.Errorf("error lost stdout's head: %.200q", msg)
	}
	if len(msg) > 2*diagnosticCap+256 {
		t.Errorf("error is %d bytes; want at most two capped streams", len(msg))
	}
	if !strings.Contains(msg, "…") {
		t.Errorf("error does not mark the cut: %.200q", msg)
	}
}

// TestCapDiagnostic: text under the cap is unchanged; longer text keeps its head, cut on a rune boundary and marked.
func TestCapDiagnostic(t *testing.T) {
	head := strings.Repeat("a", diagnosticCap-1)
	for _, tc := range []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"under", "line one\nline two", "line one\nline two"},
		{"at the cap", strings.Repeat("a", diagnosticCap), strings.Repeat("a", diagnosticCap)},
		{"over", strings.Repeat("a", diagnosticCap) + "bbb", strings.Repeat("a", diagnosticCap) + "…"},
		{"rune straddles the cap", head + "é" + "tail", head + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := capDiagnostic(tc.in)
			if got != tc.want {
				t.Errorf("capDiagnostic = %.40q… (%d bytes); want %d bytes", got, len(got), len(tc.want))
			}
			if !utf8.ValidString(got) {
				t.Errorf("capDiagnostic returned invalid UTF-8")
			}
		})
	}
}

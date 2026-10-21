package herdr

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/ndrwl/guildhall/internal/testenv"
)

// ShellCommand's contract is that the line it renders, typed into the named
// shell, runs argv[0] with exactly argv[1:]. These tests execute the rendered
// line in each supported shell found on the host and compare the arguments an
// echo child receives. A shell that is not installed is skipped by name, so the
// matrix of native CI runners decides which shells are proven where; a runner
// that names the shell in testenv.RequireEnv fails instead of skipping.

// roundTripArgs are arguments every supported shell must pass unchanged.
var roundTripArgs = []string{
	"plain", "two words", "it's", "$HOME", "`x`", `back\slash`, "*", "?", "[a]",
	";", "&", "|", "a>b", "<in", "héllo", `C:\Program Files\x`, "--flag=value",
	"%PATH%", "$(echo no)", "#not-a-comment", "@(1)", "{a,b}", "~", "!x",
	`trailing\`,
}

// posixOnlyArgs are passed unchanged by POSIX shells; PowerShell refuses them.
var posixOnlyArgs = []string{"", `say "hi"`, "it\u2019s", `with space\`, `C:\dir with space\`}

// shellRunner runs one rendered line in a real shell found at path.
type shellRunner struct {
	name string // the process name Herdr would report
	run  func(path, line string) *exec.Cmd
}

// find returns the shell's path, or skips the test naming why it cannot run;
// a missing shell the runner requires fails it.
func (s shellRunner) find(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath(s.name)
	if err != nil {
		testenv.SkipUnlessRequired(t, s.name, "%s is not installed on this host", s.name)
	}
	if runtime.GOOS == "windows" && strings.Contains(strings.ToLower(path), `\system32\bash`) {
		t.Skipf("%s resolves to %s, the WSL launcher, not a native shell", s.name, path)
	}
	return path
}

func posixShell(name string) shellRunner {
	return shellRunner{name: name, run: func(path, line string) *exec.Cmd {
		return exec.Command(path, "-c", line)
	}}
}

func powerShell(name string) shellRunner {
	return shellRunner{name: name, run: func(path, line string) *exec.Cmd {
		// -EncodedCommand hands PowerShell the exact line, as typing it would,
		// with no further quoting by the process that starts it.
		u := utf16.Encode([]rune(line))
		b := make([]byte, 2*len(u))
		for i, c := range u {
			b[2*i], b[2*i+1] = byte(c), byte(c>>8)
		}
		return exec.Command(path, "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(b))
	}}
}

func TestShellCommandRoundTrips(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shells := []struct {
		shellRunner
		args []string
	}{
		{posixShell("sh"), append(slices.Clone(roundTripArgs), posixOnlyArgs...)},
		{posixShell("bash"), append(slices.Clone(roundTripArgs), posixOnlyArgs...)},
		{posixShell("dash"), append(slices.Clone(roundTripArgs), posixOnlyArgs...)},
		{posixShell("ksh"), append(slices.Clone(roundTripArgs), posixOnlyArgs...)},
		{posixShell("zsh"), append(slices.Clone(roundTripArgs), posixOnlyArgs...)},
		{powerShell("pwsh"), roundTripArgs},
		{powerShell("powershell"), roundTripArgs},
	}
	for _, sh := range shells {
		t.Run(sh.name, func(t *testing.T) {
			path := sh.find(t)
			for _, arg := range sh.args {
				t.Run(fmt.Sprintf("%q", arg), func(t *testing.T) {
					out := filepath.Join(t.TempDir(), "argv.json")
					argv := []string{self, "-test.run=^TestArgvEchoHelper$", "--", "argv-echo", out, "first", arg, "last"}
					line, err := ShellCommand(sh.name, argv)
					if err != nil {
						t.Fatalf("ShellCommand(%q) refused %q: %v", sh.name, arg, err)
					}
					cmd := sh.run(path, line)
					if b, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("%s ran %s: %v\n%s", sh.name, line, err, b)
					}
					data, err := os.ReadFile(out)
					if err != nil {
						t.Fatalf("echo child wrote nothing for line %s: %v", line, err)
					}
					var got []string
					if err := json.Unmarshal(data, &got); err != nil {
						t.Fatal(err)
					}
					if want := []string{"first", arg, "last"}; !slices.Equal(got, want) {
						t.Errorf("%s passed %q, want %q (line %s)", sh.name, got, want, line)
					}
				})
			}
		})
	}
}

// TestArgvEchoHelper is not a test: it is the child TestShellCommandRoundTrips
// starts through a shell; it records the arguments it received.
func TestArgvEchoHelper(t *testing.T) {
	at := slices.Index(os.Args, "--")
	if at < 0 || len(os.Args) < at+3 || os.Args[at+1] != "argv-echo" {
		t.Skip("child for TestShellCommandRoundTrips")
	}
	b, _ := json.Marshal(os.Args[at+3:])
	if err := os.WriteFile(os.Args[at+2], b, 0o600); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

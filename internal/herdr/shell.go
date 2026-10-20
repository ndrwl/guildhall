package herdr

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// driveProgram matches a program named by a Windows drive-letter path.
var driveProgram = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// ShellCommand renders argv as one command line for the interactive shell a
// Herdr pane runs, identified by the process name Herdr reports for it, such
// that the shell starts argv[0] with exactly argv[1:].
// POSIX shells (sh, bash, dash, ksh, zsh) and PowerShell are supported; any
// other shell is refused rather than guessed, and an argument a shell cannot
// pass unchanged is refused rather than altered.
func ShellCommand(shell string, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("shell command needs a program")
	}
	for _, a := range argv {
		for _, r := range a {
			if r < 0x20 || r == 0x7f {
				return "", fmt.Errorf("shell argument %q contains a control character", a)
			}
		}
	}
	name := strings.ToLower(path.Base(strings.ReplaceAll(shell, `\`, "/")))
	name = strings.TrimSuffix(name, ".exe")
	switch name {
	case "sh", "bash", "dash", "ksh", "zsh":
		quoted := make([]string, len(argv))
		for i, a := range argv {
			if i == 0 && driveProgram.MatchString(a) {
				// dash under Git for Windows cannot start a program named by a
				// backslashed drive path; every POSIX shell accepts '/'.
				a = strings.ReplaceAll(a, `\`, "/")
			}
			quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		return strings.Join(quoted, " "), nil
	case "powershell", "pwsh":
		quoted := make([]string, len(argv))
		for i, a := range argv {
			// Windows PowerShell 5.1 drops empty arguments and mangles
			// embedded double quotes when it starts a native program, and it
			// treats typographic single quotes as quote characters.
			// It also quotes an argument containing whitespace without doubling
			// a trailing backslash, which then escapes the closing quote.
			if a == "" || strings.ContainsAny(a, "\"‘’‚‛") ||
				(strings.ContainsAny(a, " \t") && strings.HasSuffix(a, `\`)) {
				return "", fmt.Errorf("argument %q cannot be passed through PowerShell unchanged", a)
			}
			quoted[i] = "'" + strings.ReplaceAll(a, "'", "''") + "'"
		}
		return "& " + strings.Join(quoted, " "), nil
	}
	return "", fmt.Errorf("pane shell %q is not supported; supported shells are sh, bash, dash, ksh, zsh and PowerShell", shell)
}

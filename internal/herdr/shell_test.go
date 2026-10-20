package herdr

import "testing"

func TestShellCommandQuotesPerShell(t *testing.T) {
	argv := []string{"/opt/guild hall/guildhall", "fake-artisan", "--prompt", "it's $HOME `x` \"q\""}
	cases := []struct {
		shell string
		want  string
	}{
		{"zsh", `'/opt/guild hall/guildhall' 'fake-artisan' '--prompt' 'it'\''s $HOME ` + "`x`" + ` "q"'`},
		{"bash.exe", `'/opt/guild hall/guildhall' 'fake-artisan' '--prompt' 'it'\''s $HOME ` + "`x`" + ` "q"'`},
		{"/bin/sh", `'/opt/guild hall/guildhall' 'fake-artisan' '--prompt' 'it'\''s $HOME ` + "`x`" + ` "q"'`},
	}
	for _, tc := range cases {
		got, err := ShellCommand(tc.shell, argv)
		if err != nil {
			t.Errorf("ShellCommand(%q) = %v", tc.shell, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ShellCommand(%q) =\n %s\nwant\n %s", tc.shell, got, tc.want)
		}
	}
}

func TestShellCommandQuotesForPowerShell(t *testing.T) {
	argv := []string{"C:/Program Files/guildhall.exe", "fake-artisan", "--prompt", "it's $HOME `x`"}
	want := `& 'C:/Program Files/guildhall.exe' 'fake-artisan' '--prompt' 'it''s $HOME ` + "`x`" + `'`
	for _, shell := range []string{"powershell.exe", "pwsh", "pwsh.exe", "powershell"} {
		got, err := ShellCommand(shell, argv)
		if err != nil || got != want {
			t.Errorf("ShellCommand(%q) = %s, %v; want %s", shell, got, err, want)
		}
	}
	// Windows PowerShell 5.1 drops empty arguments and mangles embedded double
	// quotes when calling native programs, and treats typographic single quotes
	// as quote characters, so those arguments are refused rather than guessed.
	for _, arg := range []string{"", `say "hi"`, "it’s", "‘x"} {
		if _, err := ShellCommand("powershell.exe", []string{"x", arg}); err == nil {
			t.Errorf("PowerShell accepted %q", arg)
		}
	}
}

func TestShellCommandRefusesUnknownShellsAndUnsafeText(t *testing.T) {
	for _, shell := range []string{"fish", "cmd.exe", "nu", ""} {
		if _, err := ShellCommand(shell, []string{"x"}); err == nil {
			t.Errorf("ShellCommand(%q) succeeded, want refusal", shell)
		}
	}
	for _, arg := range []string{"a\nb", "a\rb", "a\x00b", "a\x1bb"} {
		if _, err := ShellCommand("bash", []string{"x", arg}); err == nil {
			t.Errorf("ShellCommand accepted control character in %q", arg)
		}
	}
	if _, err := ShellCommand("bash", nil); err == nil {
		t.Error("ShellCommand accepted an empty argv")
	}
}

func TestShellCommandPOSIXUsesForwardSlashesForADriveProgram(t *testing.T) {
	// dash under Git for Windows cannot start a program named by a
	// backslashed drive path; every POSIX shell there accepts forward slashes.
	got, err := ShellCommand("dash.exe", []string{`C:\Program Files\guildhall.exe`, `C:\keep\as is`})
	if err != nil {
		t.Fatal(err)
	}
	if want := `'C:/Program Files/guildhall.exe' 'C:\keep\as is'`; got != want {
		t.Errorf("ShellCommand = %s, want %s", got, want)
	}
	got, err = ShellCommand("bash", []string{`/opt/a\b`, "x"})
	if err != nil || got != `'/opt/a\b' 'x'` {
		t.Errorf("non-drive program changed: %s, %v", got, err)
	}
}

func TestShellCommandPowerShellRefusesSpacedTrailingBackslash(t *testing.T) {
	// Windows PowerShell 5.1 quotes an argument containing whitespace for the
	// native program without doubling a trailing backslash, which then escapes
	// the closing quote and merges the next argument.
	for _, arg := range []string{`with space\`, `C:\dir with space\`, `two  spaces\\`} {
		if _, err := ShellCommand("powershell.exe", []string{"x", arg}); err == nil {
			t.Errorf("PowerShell accepted %q", arg)
		}
	}
	if _, err := ShellCommand("powershell.exe", []string{"x", `trailing\`, `C:\no space\x`}); err != nil {
		t.Errorf("PowerShell refused a safe backslash argument: %v", err)
	}
}

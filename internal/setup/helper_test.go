package setup_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/setup"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// stubDir holds copies of the test binary named setuptool, git, claude and herdr; TestMain runs a copy as the stub
// its name says, so the tests run the same on every OS without a shell.
var stubDir string

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func TestMain(m *testing.M) {
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	switch name {
	case "setuptool":
		os.Exit(setuptool(os.Args[1:]))
	case "git", "claude", "herdr":
		os.Exit(stubProgram(name, os.Args[1:]))
	}
	dir, err := os.MkdirTemp("", "setup-stubs-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	stubDir = dir
	for _, n := range []string{"setuptool", "git", "claude", "herdr"} {
		if err := copyFile(os.Args[0], filepath.Join(dir, exe(n))); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	code := testenv.RunWithGuards(m)
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// setuptool is the test's argv tool: exists, cat, write, envfile and fail.
func setuptool(args []string) int {
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "exists":
		if _, err := os.Stat(args[1]); err != nil {
			fmt.Println("missing", args[1])
			return 1
		}
		return 0
	case "cat":
		data, err := os.ReadFile(args[1])
		if err != nil {
			fmt.Println("missing", args[1])
			return 1
		}
		fmt.Print(string(data))
		return 0
	case "write":
		if err := os.WriteFile(args[1], []byte(args[2]), 0o600); err != nil {
			fmt.Println(err)
			return 1
		}
		return 0
	case "envfile":
		if err := os.WriteFile(args[2], []byte(os.Getenv(args[1])), 0o600); err != nil {
			fmt.Println(err)
			return 1
		}
		return 0
	case "printenv":
		fmt.Println(os.Getenv(args[1]))
		return 1
	case "fail":
		fmt.Println(strings.Join(args[1:], " "))
		return 1
	}
	return 2
}

// stubProgram is a stub git, claude or herdr: --version prints "<name> version 1.0.0".
func stubProgram(name string, args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Printf("%s version 1.0.0\n", name)
		return 0
	}
	return 1
}

// tool is the path of the setuptool stub, the ${tool} param of the tests' step files.
func tool() string { return filepath.Join(stubDir, exe("setuptool")) }

// sandbox is a temporary HOME and setup directory, with Facts for them and the tool param set.
type sandbox struct {
	home, setupDir string
	facts          setup.Facts
}

func newSandbox(t *testing.T) sandbox {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	sd := filepath.Join(home, "setup")
	return sandbox{home: home, setupDir: sd, facts: setup.Facts{Home: home, SetupDir: sd, Vars: map[string]string{"tool": tool()}}}
}

// parse parses a test step file, failing the test on error.
func parse(t *testing.T, src string) setup.Steps {
	t.Helper()
	s, err := setup.Parse([]byte(src), "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s.Ref = "test.toml"
	return s
}

// scripted is a Prompter that answers from lists and keeps everything shown.
type scripted struct {
	confirms []bool
	answers  []string
	secrets  []string
	shown    strings.Builder
}

func (p *scripted) Show(text string) error { p.shown.WriteString(text + "\n"); return nil }

func (p *scripted) Confirm(q string) (bool, error) {
	p.shown.WriteString("? " + q + "\n")
	if len(p.confirms) == 0 {
		return false, io.EOF
	}
	v := p.confirms[0]
	p.confirms = p.confirms[1:]
	return v, nil
}

func (p *scripted) Ask(q string) (string, error) {
	if len(p.answers) == 0 {
		return "", io.EOF
	}
	v := p.answers[0]
	p.answers = p.answers[1:]
	return v, nil
}

func (p *scripted) AskSecret(q string) (string, error) {
	if len(p.secrets) == 0 {
		return "", io.EOF
	}
	v := p.secrets[0]
	p.secrets = p.secrets[1:]
	return v, nil
}

// tree lists every file under dir with its contents, for comparing before and after.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		out[p] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

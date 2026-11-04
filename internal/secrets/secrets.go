// Package secrets is a machine's injected secrets (docs/secrets.md): its age key and its sops-encrypted secrets
// file, both kept outside every copy, decrypted in memory by the session supervisor at start, never written in
// clear, and withheld from model-driven processes except the names a profile lists in inherit_env.
//
// Encryption and decryption run the sops and age-keygen programs, so the package adds no module dependency; values
// exist only in this process's memory and in the environment of the processes it chooses to give them to.
package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	// KeyFile and File are the machine's age key and its secrets file, in Dir.
	KeyFile = "age.key"
	File    = "secrets.sops.yaml"
	// NamesEnv lists, comma-separated, the names the session supervisor injected into its own environment. It
	// carries names only, never a value, and every environment built for a model-driven process drops the names it
	// lists (Strip).
	NamesEnv = "GUILDHALL_SECRET_NAMES"
	// RedactEnv lists, comma-separated, the names an Artisan inherited through its profile's inherit_env, whose
	// values the runtime wrapper redacts from launcher.log, the Workbench event file and the result (FromEnv).
	RedactEnv = "GUILDHALL_REDACT_NAMES"
	// SopsProgram and KeygenProgram are the programs run, looked up on PATH.
	SopsProgram   = "sops"
	KeygenProgram = "age-keygen"
	// MinRedacted is the shortest value redacted: a shorter one would match ordinary text.
	MinRedacted = 4
	// Redacted replaces each known value.
	Redacted = "[redacted]"
)

// namePattern is an injected name: an environment variable name in the portable shell form.
var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidName reports whether name can be an injected or inherited name.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// Paths are a machine's key and secrets file.
type Paths struct {
	Key, File string
}

// Dir is the directory under home holding the key and the secrets file: home/.config/guildhall.
func Dir(home string) string { return filepath.Join(home, ".config", "guildhall") }

// DirEnv, when set, names the directory DefaultPaths uses instead of Dir of the home directory. The test guards set
// it to an empty directory (testenv.RunWithGuards), so no test reads a machine's real key or secrets file.
const DirEnv = "GUILDHALL_SECRETS_DIR"

// DefaultPaths are the key and secrets file in DirEnv's directory, or else in Dir of the user's home directory.
func DefaultPaths() (Paths, error) {
	d := os.Getenv(DirEnv)
	if d == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("secrets: no home directory: %w", err)
		}
		d = Dir(home)
	}
	return Paths{Key: filepath.Join(d, KeyFile), File: filepath.Join(d, File)}, nil
}

// Outside refuses paths when the key or the secrets file is inside dir, such as a copy, which is backed up file by
// file and never pruned.
func (p Paths) Outside(dir string) error {
	for _, f := range []struct{ what, path string }{{"machine key", p.Key}, {"secrets file", p.File}} {
		if inside(f.path, dir) {
			return fmt.Errorf("the %s %s is inside %s; keep it outside every copy", f.what, f.path, dir)
		}
	}
	return nil
}

// inside reports whether path is dir or below it, comparing absolute cleaned paths with symbolic links resolved
// in their nearest existing ancestor, so a path not yet created compares the same way as one that exists (macOS
// temporary directories sit behind /var -> /private/var, and Windows ones behind short 8.3 names).
func inside(path, dir string) bool {
	rel, err := filepath.Rel(resolve(dir), resolve(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolve is p made absolute and cleaned, with symbolic links resolved in its longest existing prefix and the
// missing remainder joined back on.
func resolve(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	var rest []string
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			slices.Reverse(rest)
			return filepath.Join(append([]string{r}, rest...)...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

// Runner runs the program at path with args, its environment this process's with env added, and returns its
// standard output and standard error apart, so a decryption's output is never mixed into an error.
type Runner func(ctx context.Context, env []string, path string, args ...string) (stdout, stderr []byte, err error)

// Exec is the real Runner.
func Exec(ctx context.Context, env []string, path string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

// Values are decrypted secrets, by name. The zero value holds none.
type Values struct {
	m map[string]string
}

// Len is the number of names.
func (v Values) Len() int { return len(v.m) }

// Names are the names, sorted.
func (v Values) Names() []string {
	names := make([]string, 0, len(v.m))
	for n := range v.m {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Get is the value of name.
func (v Values) Get(name string) (string, bool) {
	s, ok := v.m[name]
	return s, ok
}

// Pick is the values of those of names v holds.
func (v Values) Pick(names []string) Values {
	out := Values{m: map[string]string{}}
	for _, n := range names {
		if s, ok := v.m[n]; ok {
			out.m[n] = s
		}
	}
	return out
}

// Env is NAME=VALUE for each name, sorted by name.
func (v Values) Env() []string {
	env := make([]string, 0, len(v.m))
	for _, n := range v.Names() {
		env = append(env, n+"="+v.m[n])
	}
	return env
}

// String names the values without any value, so a Values printed by mistake shows none.
func (v Values) String() string { return fmt.Sprintf("secrets(%s)", strings.Join(v.Names(), ", ")) }

// GoString is String, for %#v.
func (v Values) GoString() string { return v.String() }

// ErrDecrypt is a secrets file that exists and fails to decrypt or to read as names and values.
var ErrDecrypt = errors.New("the secrets file does not decrypt")

// Load decrypts p.File with p.Key by running sops once, in memory: no key or no secrets file is no values and no
// error, the machine then having no injected secrets. A file that fails to decrypt, or whose plaintext is not a flat
// map of valid names to strings, is an error wrapping ErrDecrypt that names no value: sops's own error output is
// reported, never its standard output.
func Load(ctx context.Context, p Paths, run Runner) (Values, error) {
	for _, f := range []string{p.Key, p.File} {
		if _, err := os.Stat(f); errors.Is(err, os.ErrNotExist) {
			return Values{}, nil
		} else if err != nil {
			return Values{}, fmt.Errorf("secrets: %w", err)
		}
	}
	path, err := exec.LookPath(SopsProgram)
	if err != nil {
		return Values{}, fmt.Errorf("secrets: %s exists but %s is not on PATH: %w", p.File, SopsProgram, err)
	}
	if run == nil {
		run = Exec
	}
	out, errOut, err := run(ctx, []string{"SOPS_AGE_KEY_FILE=" + p.Key}, path, "--decrypt", "--output-type", "json", p.File)
	if err != nil {
		msg := strings.TrimSpace(string(errOut))
		if len(msg) > 500 {
			msg = msg[:500] + "..."
		}
		if msg == "" {
			msg = "no error output"
		}
		return Values{}, fmt.Errorf("secrets: %w: %s with key %s: %v: %s", ErrDecrypt, p.File, p.Key, err, msg)
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		return Values{}, fmt.Errorf("secrets: %w: %s: the plaintext is not a map of names to values", ErrDecrypt, p.File)
	}
	v := Values{m: map[string]string{}}
	for name, val := range raw {
		s, ok := val.(string)
		switch {
		case name == "sops":
			continue
		case !ValidName(name):
			return Values{}, fmt.Errorf("secrets: %w: %s: %q is not an environment variable name", ErrDecrypt, p.File, name)
		case !ok:
			return Values{}, fmt.Errorf("secrets: %w: %s: %s is not a string", ErrDecrypt, p.File, name)
		case strings.ContainsRune(s, 0):
			return Values{}, fmt.Errorf("secrets: %w: %s: %s holds a NUL byte", ErrDecrypt, p.File, name)
		}
		v.m[name] = s
	}
	return v, nil
}

// SplitNames is the names a comma-separated list such as NamesEnv's value holds, without empty or invalid ones.
func SplitNames(list string) []string {
	var names []string
	for _, n := range strings.Split(list, ",") {
		if n = strings.TrimSpace(n); ValidName(n) && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return names
}

// JoinNames is names, sorted, without duplicates, joined with commas.
func JoinNames(names ...[]string) string {
	var all []string
	for _, ns := range names {
		all = append(all, ns...)
	}
	slices.Sort(all)
	return strings.Join(slices.Compact(all), ",")
}

// Strip is environ without each variable NamesEnv, as environ sets it, lists: the environment of a model-driven
// process, which gets no injected secret. NamesEnv itself stays, so the processes it starts strip the same names.
func Strip(environ []string) []string {
	var names []string
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, NamesEnv+"="); ok {
			names = append(names, SplitNames(v)...)
		}
	}
	if len(names) == 0 {
		return slices.Clone(environ)
	}
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(names, name) {
			out = append(out, kv)
		}
	}
	return out
}

// Redactor replaces known values with Redacted. A nil Redactor redacts nothing.
type Redactor struct {
	values [][]byte
}

// NewRedactor redacts each of values at least MinRedacted bytes long, longest first so a value that holds another
// is replaced whole; it is nil when there is none.
func NewRedactor(values []string) *Redactor {
	var r Redactor
	for _, v := range values {
		if len(v) >= MinRedacted {
			r.values = append(r.values, []byte(v))
		}
	}
	if len(r.values) == 0 {
		return nil
	}
	slices.SortFunc(r.values, func(a, b []byte) int { return len(b) - len(a) })
	return &r
}

// FromEnv is the Redactor of the values getenv gives the names RedactEnv lists, or nil when there is none.
func FromEnv(getenv func(string) string) *Redactor {
	var values []string
	for _, n := range SplitNames(getenv(RedactEnv)) {
		if v := getenv(n); v != "" {
			values = append(values, v)
		}
	}
	return NewRedactor(values)
}

// Bytes is b with each known value replaced; b itself is returned when it holds none.
func (r *Redactor) Bytes(b []byte) []byte {
	if r == nil {
		return b
	}
	for _, v := range r.values {
		if bytes.Contains(b, v) {
			b = bytes.ReplaceAll(b, v, []byte(Redacted))
		}
	}
	return b
}

// String is s with each known value replaced.
func (r *Redactor) String(s string) string {
	if r == nil {
		return s
	}
	return string(r.Bytes([]byte(s)))
}

// Writer is w with each write's known values replaced, or w itself for a nil Redactor. Each write is redacted on
// its own, so a value split across two writes is not found: the wrappers write whole lines and events.
func (r *Redactor) Writer(w io.Writer) io.Writer {
	if r == nil || w == nil {
		return w
	}
	return redactingWriter{w: w, r: r}
}

type redactingWriter struct {
	w io.Writer
	r *Redactor
}

// Write reports all of p written when its redacted form is.
func (w redactingWriter) Write(p []byte) (int, error) {
	if _, err := w.w.Write(w.r.Bytes(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

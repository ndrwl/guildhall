// Package setup is Guildhall's setup engine: declarative step files (guildhall.steps/1), the doctor that reports a
// machine's gaps against them, the fixer that closes the fixable ones, and the wizard that walks a person through the
// human ones (docs/setup.md).
//
// It is a port, not a copy, of the shell scripts Firstmate uses: wizard-template.sh and wizard-template.ps1 from
// ndrwl/dev-setup (the wizard), and Firstmate's fm-remote-doctor.sh (the doctor) and fm-install-herdr.sh (the
// built-in herdr step file), used under the MIT licence (see NOTICE and
// docs/decisions/setup-engine-from-firstmate.md, which records the source commits).
package setup

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Schema is the format a step file names in its schema key.
const Schema = "guildhall.steps/1"

// Errors the engine returns; each is wrapped with the detail that caused it.
var (
	// ErrDigestMismatch is a step file whose sha256 is not the digest its caller approved.
	ErrDigestMismatch = errors.New("step file digest mismatch")
	// ErrUnknownStep is a step id the step file does not hold.
	ErrUnknownStep = errors.New("unknown step")
	// ErrNotOwned is a write to an existing file the ownership manifest does not list.
	ErrNotOwned = errors.New("file not owned by the setup engine")
	// ErrHumanStep is a human step named where only fixable steps may run, as in doctor --fix --only.
	ErrHumanStep = errors.New("human step")
	// ErrUnsupportedOS is a step named on an operating system its os list leaves out.
	ErrUnsupportedOS = errors.New("step not for this operating system")
	// ErrChecksum is a download whose sha256 is not the one its step names.
	ErrChecksum = errors.New("checksum mismatch")
	// ErrInvalid is a step file that does not follow guildhall.steps/1.
	ErrInvalid = errors.New("invalid step file")
)

// Tag says who closes a step's gap: the engine (fixable) or a person through the wizard (human).
type Tag string

// The two tags.
const (
	Fixable Tag = "fixable"
	Human   Tag = "human"
)

// Command is a check, action, verify or run: an argv run with no shell, or a script run by a named shell.
// Exactly one of Argv and Script is set.
type Command struct {
	Argv   []string `json:"argv,omitempty"`
	Shell  string   `json:"shell,omitempty"`
	Script string   `json:"script,omitempty"`
}

// IsZero reports whether c names nothing to run.
func (c Command) IsZero() bool { return len(c.Argv) == 0 && c.Script == "" }

// String is c as one display line: the argv joined by spaces, or the shell and the script's first line.
func (c Command) String() string {
	if c.Script != "" {
		first, _, more := strings.Cut(strings.TrimSpace(c.Script), "\n")
		if more {
			first += " …"
		}
		return c.Shell + " script: " + first
	}
	return strings.Join(c.Argv, " ")
}

// Prompt is one question a human step asks: confirm (yes or no), ask (a recorded answer) or ask_secret (a masked
// answer that is never recorded).
type Prompt struct {
	Kind string `toml:"kind" json:"kind"`
	Name string `toml:"name" json:"name,omitempty"`
	Text string `toml:"text" json:"text"`
}

// Fetch is a fixable step's download: the file at URL, refused unless its sha256 is SHA256, written to Dest, or
// unpacked there when Unpack is "tar.gz" or "zip" (dropping Strip leading path elements of each entry).
type Fetch struct {
	URL    string `toml:"url" json:"url"`
	SHA256 string `toml:"sha256" json:"sha256"`
	Dest   string `toml:"dest" json:"dest"`
	Unpack string `toml:"unpack" json:"unpack,omitempty"`
	Strip  int    `toml:"strip" json:"strip,omitempty"`
}

// Step is one numbered step: check, act, verify, with an undo note shown when it fails.
type Step struct {
	ID    string
	Title string
	Tag   Tag
	OS    []string
	// Check passes when it exits 0 and, if Expect is set, its output contains Expect.
	Check  Command
	Expect string
	// Action or Fetch is a fixable step's act.
	Action Command
	Fetch  *Fetch
	// Verify runs after the act; when zero it is a fresh run of Check.
	Verify Command
	// Instructions, Prompts and Run are a human step's act, carried out by the wizard with a person.
	Instructions string
	Prompts      []Prompt
	Run          Command
	// Creates lists the files the act writes, which must not exist unless the ownership manifest lists them.
	Creates []string
	Undo    string
}

// ForOS reports whether the step runs on goos.
func (s Step) ForOS(goos string) bool { return slices.Contains(s.OS, goos) }

// Steps is a loaded step file.
type Steps struct {
	Name  string
	Title string
	// Digest is the lowercase hex sha256 of the file's bytes.
	Digest string
	// Ref is how the command line names the file: a built-in's name, or the file's path.
	Ref     string
	Builtin bool
	// Params are the values the file's steps may substitute, with their defaults.
	Params map[string]string
	Steps  []Step
}

// Step returns the step with the given id: its form for goos when it has one, else its first form.
func (s Steps) Step(id, goos string) (Step, bool) {
	found := -1
	for i, st := range s.Steps {
		switch {
		case st.ID != id:
		case st.ForOS(goos):
			return st, true
		case found < 0:
			found = i
		}
	}
	if found < 0 {
		return Step{}, false
	}
	return s.Steps[found], true
}

// Digest returns the lowercase hex sha256 of data, a step file's digest.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// LoadSteps reads and parses the step file at path, refusing it with ErrDigestMismatch unless its digest is
// wantDigest; an empty wantDigest checks nothing, which only a caller that trusts the file may pass.
func LoadSteps(path, wantDigest string) (Steps, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Steps{}, err
	}
	s, err := Parse(data, wantDigest)
	if err != nil {
		return Steps{}, fmt.Errorf("%s: %w", path, err)
	}
	s.Ref = path
	return s, nil
}

//go:embed builtin/*.toml
var builtinFS embed.FS

// Builtins returns the names of the step files built into the binary, sorted.
func Builtins() []string {
	entries, _ := builtinFS.ReadDir("builtin")
	var names []string
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".toml"))
	}
	sort.Strings(names)
	return names
}

// BuiltinBytes returns the bytes of the named built-in step file.
func BuiltinBytes(name string) ([]byte, error) {
	if !idPattern.MatchString(name) {
		return nil, fmt.Errorf("%w: no built-in step file %q", ErrUnknownStep, name)
	}
	data, err := builtinFS.ReadFile("builtin/" + name + ".toml")
	if err != nil {
		return nil, fmt.Errorf("%w: no built-in step file %q (have %s)", ErrUnknownStep, name, strings.Join(Builtins(), ", "))
	}
	return data, nil
}

// LoadBuiltin parses the named built-in step file, refusing it with ErrDigestMismatch unless its digest is
// wantDigest; an empty wantDigest checks nothing, as the binary vouches for its own files.
func LoadBuiltin(name, wantDigest string) (Steps, error) {
	data, err := BuiltinBytes(name)
	if err != nil {
		return Steps{}, err
	}
	s, err := Parse(data, wantDigest)
	if err != nil {
		return Steps{}, fmt.Errorf("built-in %s: %w", name, err)
	}
	s.Ref, s.Builtin = name, true
	return s, nil
}

// idPattern is a step file name, step id, prompt name or param name.
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// digestPattern is a lowercase hex sha256.
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// rawFile and rawStep are the TOML shapes; check, action, verify and run are an argv array or a {shell, script}
// table, so they decode as any and are checked by command.
type rawFile struct {
	Schema string            `toml:"schema"`
	Name   string            `toml:"name"`
	Title  string            `toml:"title"`
	Params map[string]string `toml:"params"`
	Step   []rawStep         `toml:"step"`
}

type rawStep struct {
	ID           string   `toml:"id"`
	Title        string   `toml:"title"`
	Tag          string   `toml:"tag"`
	OS           []string `toml:"os"`
	Check        any      `toml:"check"`
	Expect       string   `toml:"expect"`
	Action       any      `toml:"action"`
	Fetch        *Fetch   `toml:"fetch"`
	Verify       any      `toml:"verify"`
	Instructions string   `toml:"instructions"`
	Prompt       []Prompt `toml:"prompt"`
	Run          any      `toml:"run"`
	Creates      []string `toml:"creates"`
	Undo         string   `toml:"undo"`
}

// Parse parses step file bytes, refusing them with ErrDigestMismatch unless their digest is wantDigest (when set),
// and with ErrInvalid unless they follow guildhall.steps/1 strictly: no unknown key, every step complete.
func Parse(data []byte, wantDigest string) (Steps, error) {
	got := Digest(data)
	if wantDigest != "" && wantDigest != got {
		return Steps{}, fmt.Errorf("%w: approved %s, file is %s", ErrDigestMismatch, wantDigest, got)
	}
	var raw rawFile
	md, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&raw)
	if err != nil {
		return Steps{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	for _, k := range md.Undecoded() {
		// A {shell, script} table decodes into an any, which command checks key by key.
		if len(k) == 3 && k[0] == "step" && slices.Contains([]string{"check", "action", "verify", "run"}, k[1]) {
			continue
		}
		return Steps{}, fmt.Errorf("%w: unknown key %s", ErrInvalid, k)
	}
	var problems []string
	fault := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if raw.Schema != Schema {
		fault("schema is %q, want %q", raw.Schema, Schema)
	}
	if !idPattern.MatchString(raw.Name) {
		fault("name %q is not a lowercase id", raw.Name)
	}
	s := Steps{Name: raw.Name, Title: raw.Title, Digest: got, Params: map[string]string{}}
	if s.Title == "" {
		s.Title = s.Name
	}
	for k, v := range raw.Params {
		if !idPattern.MatchString(k) || strings.Contains(k, "-") {
			fault("param %q is not a lowercase identifier", k)
		}
		if _, ok := defaultVars[k]; ok {
			fault("param %q is set by the engine", k)
		}
		s.Params[k] = v
	}
	if len(raw.Step) == 0 {
		fault("no steps")
	}
	// An id names one step per operating system: a step's Windows form shares its Linux form's id, so a gap id is
	// the same on every machine, but no two steps of one id run on the same operating system.
	seen := map[string]bool{}
	for i, rs := range raw.Step {
		where := fmt.Sprintf("step %d", i+1)
		if rs.ID != "" {
			where += " (" + rs.ID + ")"
		}
		st, errs := convertStep(rs)
		for _, e := range errs {
			fault("%s: %s", where, e)
		}
		for _, o := range st.OS {
			if seen[st.ID+"/"+o] {
				fault("%s: duplicate id for %s", where, o)
			}
			seen[st.ID+"/"+o] = true
		}
		for _, name := range st.referencedVars() {
			if _, ok := s.Params[name]; !ok {
				if _, ok := defaultVars[name]; !ok {
					fault("%s: ${%s} is neither a param nor an engine value", where, name)
				}
			}
		}
		s.Steps = append(s.Steps, st)
	}
	if len(problems) > 0 {
		return Steps{}, fmt.Errorf("%w: %s", ErrInvalid, strings.Join(problems, "; "))
	}
	return s, nil
}

// knownOS are the operating systems a step's os list may name.
var knownOS = []string{"darwin", "linux", "windows"}

func convertStep(rs rawStep) (Step, []string) {
	var errs []string
	fault := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	st := Step{ID: rs.ID, Title: rs.Title, Tag: Tag(rs.Tag), OS: rs.OS, Expect: rs.Expect, Fetch: rs.Fetch,
		Instructions: rs.Instructions, Prompts: rs.Prompt, Creates: rs.Creates, Undo: rs.Undo}
	if !idPattern.MatchString(st.ID) {
		fault("id %q is not a lowercase id", st.ID)
	}
	if st.Title == "" {
		fault("no title")
	}
	if st.Undo == "" {
		fault("no undo note")
	}
	if len(st.OS) == 0 {
		fault("no os list")
	}
	for _, o := range st.OS {
		if !slices.Contains(knownOS, o) {
			fault("os %q is not one of %s", o, strings.Join(knownOS, ", "))
		}
	}
	var err error
	if st.Check, err = command(rs.Check); err != nil || st.Check.IsZero() {
		fault("check: %s", orMissing(err))
	}
	if st.Action, err = command(rs.Action); err != nil {
		fault("action: %v", err)
	}
	if st.Verify, err = command(rs.Verify); err != nil {
		fault("verify: %v", err)
	}
	if st.Run, err = command(rs.Run); err != nil {
		fault("run: %v", err)
	}
	switch st.Tag {
	case Fixable:
		if st.Action.IsZero() == (st.Fetch == nil) {
			fault("a fixable step needs exactly one of action and fetch")
		}
		if st.Instructions != "" || len(st.Prompts) > 0 || !st.Run.IsZero() {
			fault("instructions, prompts and run belong to human steps")
		}
		if f := st.Fetch; f != nil {
			if f.Dest == "" || f.URL == "" || f.SHA256 == "" {
				fault("fetch needs url, sha256 and dest")
			}
			if f.Unpack != "" && f.Unpack != "tar.gz" && f.Unpack != "zip" {
				fault("fetch unpack %q is not tar.gz or zip", f.Unpack)
			}
			if f.Strip < 0 || (f.Strip > 0 && f.Unpack == "") {
				fault("fetch strip needs unpack and is not negative")
			}
			if !slices.Contains(st.Creates, f.Dest) {
				st.Creates = append(st.Creates, f.Dest)
			}
		}
	case Human:
		if !st.Action.IsZero() || st.Fetch != nil {
			fault("a human step has no action or fetch; its act is instructions, prompts and run")
		}
		if st.Instructions == "" && len(st.Prompts) == 0 {
			fault("a human step needs instructions or prompts")
		}
		names := map[string]bool{}
		for j, p := range st.Prompts {
			switch p.Kind {
			case "confirm", "ask", "ask_secret":
			default:
				fault("prompt %d: kind %q is not confirm, ask or ask_secret", j+1, p.Kind)
			}
			if p.Text == "" {
				fault("prompt %d: no text", j+1)
			}
			if p.Kind != "confirm" || p.Name != "" {
				if !idPattern.MatchString(p.Name) || strings.Contains(p.Name, "-") {
					fault("prompt %d: name %q is not a lowercase identifier", j+1, p.Name)
				} else if names[p.Name] {
					fault("prompt %d: duplicate name %q", j+1, p.Name)
				}
				names[p.Name] = true
			}
		}
	default:
		fault("tag %q is not fixable or human", st.Tag)
	}
	return st, errs
}

func orMissing(err error) string {
	if err != nil {
		return err.Error()
	}
	return "missing"
}

// shells are the script shells a command may name.
var shells = []string{"sh", "pwsh"}

// command converts a decoded check, action, verify or run: nil, an array of strings, or a {shell, script} table.
func command(v any) (Command, error) {
	switch v := v.(type) {
	case nil:
		return Command{}, nil
	case []any:
		if len(v) == 0 {
			return Command{}, errors.New("empty argv")
		}
		argv := make([]string, len(v))
		for i, e := range v {
			s, ok := e.(string)
			if !ok || (i == 0 && s == "") {
				return Command{}, fmt.Errorf("argv element %d is not a non-empty string", i+1)
			}
			argv[i] = s
		}
		return Command{Argv: argv}, nil
	case map[string]any:
		var c Command
		for k, e := range v {
			s, ok := e.(string)
			switch {
			case !ok:
				return Command{}, fmt.Errorf("%s is not a string", k)
			case k == "shell":
				c.Shell = s
			case k == "script":
				c.Script = s
			default:
				return Command{}, fmt.Errorf("unknown key %s", k)
			}
		}
		if !slices.Contains(shells, c.Shell) {
			return Command{}, fmt.Errorf("shell %q is not sh or pwsh", c.Shell)
		}
		if strings.TrimSpace(c.Script) == "" {
			return Command{}, errors.New("empty script")
		}
		return c, nil
	default:
		return Command{}, fmt.Errorf("is %T, not an argv array or a {shell, script} table", v)
	}
}

// referencedVars lists the ${name} values the step substitutes: in argvs, expect, fetch and creates, but not in
// script bodies, which read values from the environment instead.
func (s Step) referencedVars() []string {
	var texts []string
	for _, c := range []Command{s.Check, s.Action, s.Verify, s.Run} {
		texts = append(texts, c.Argv...)
	}
	texts = append(texts, s.Expect)
	texts = append(texts, s.Creates...)
	if f := s.Fetch; f != nil {
		texts = append(texts, f.URL, f.SHA256, f.Dest)
	}
	var names []string
	for _, t := range texts {
		for _, m := range varPattern.FindAllStringSubmatch(t, -1) {
			names = append(names, m[1])
		}
	}
	return names
}

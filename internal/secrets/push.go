package secrets

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// RecoveryKeyFile is the Guildmaster's recovery key on the Steward machine, in the same directory as its own machine
// key: the age key every machine's secrets file is also encrypted to, with which steward secrets set decrypts a
// machine's file to add a value (docs/secrets.md#the-steward-pushes-them).
const RecoveryKeyFile = "recovery.key"

// RecoveryKey is the recovery key's path beside p.Key.
func (p Paths) RecoveryKey() string { return filepath.Join(filepath.Dir(p.Key), RecoveryKeyFile) }

// With is v with name set to value; v itself is unchanged.
func (v Values) With(name, value string) Values {
	out := Values{m: maps.Clone(v.m)}
	if out.m == nil {
		out.m = map[string]string{}
	}
	out.m[name] = value
	return out
}

// CheckValue refuses a value a secrets file cannot hold, naming no part of it: an empty one, or one holding a NUL
// byte.
func CheckValue(value string) error {
	switch {
	case value == "":
		return errors.New("the value is empty")
	case strings.ContainsRune(value, 0):
		return errors.New("the value holds a NUL byte")
	}
	return nil
}

// Encrypt is v as a sops YAML secrets file encrypted to recipients, by running sops once with the plaintext on its
// standard input, never in a file or on its command line. sops's error output is reported on failure, never its
// standard output. The result is checked to be ciphertext holding exactly v's names, encrypted to recipients.
func Encrypt(ctx context.Context, v Values, recipients []string) ([]byte, error) {
	if v.Len() == 0 {
		return nil, errors.New("secrets: nothing to encrypt")
	}
	for _, r := range recipients {
		if !ValidRecipient(r) {
			return nil, fmt.Errorf("secrets: %q is not an age recipient", r)
		}
	}
	path, err := exec.LookPath(SopsProgram)
	if err != nil {
		return nil, fmt.Errorf("secrets: %s is not on PATH: %w", SopsProgram, err)
	}
	plain, err := json.Marshal(v.m)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path, "--encrypt", "--age", strings.Join(recipients, ","), "--input-type", "json", "--output-type", "yaml", "/dev/stdin")
	cmd.Stdin = bytes.NewReader(plain)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	clear(plain)
	if err != nil {
		msg := strings.TrimSpace(errOut.String())
		if len(msg) > 500 {
			msg = msg[:500] + "..."
		}
		return nil, fmt.Errorf("secrets: %s --encrypt: %v: %s", SopsProgram, err, msg)
	}
	names, got, err := Ciphertext(out.Bytes())
	switch {
	case err != nil:
		return nil, fmt.Errorf("secrets: %s --encrypt: %w", SopsProgram, err)
	case !slices.Equal(names, v.Names()):
		return nil, fmt.Errorf("secrets: %s --encrypt wrote the names %s, not %s", SopsProgram, strings.Join(names, ","), strings.Join(v.Names(), ","))
	case !slices.Equal(got, sorted(recipients)):
		return nil, fmt.Errorf("secrets: %s --encrypt encrypted to %s, not %s", SopsProgram, strings.Join(got, ","), strings.Join(sorted(recipients), ","))
	}
	return out.Bytes(), nil
}

func sorted(s []string) []string { return slices.Compact(slices.Sorted(slices.Values(s))) }

// Ciphertext reads a sops YAML secrets file without decrypting it: the names it holds, which sops leaves in clear,
// and the age recipients its metadata lists, both sorted. A top-level value that is not sops ciphertext (ENC[...]),
// a name that is not an environment variable name, or a file with no sops metadata is an error naming no value.
func Ciphertext(data []byte) (names, recipients []string, err error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	inSops, meta := false, false
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' && line[0] != '-' {
			name, rest, ok := strings.Cut(line, ":")
			switch {
			case !ok:
				return nil, nil, fmt.Errorf("line %d is not a name and its value", n)
			case name == "sops":
				inSops, meta = true, true
				continue
			case !ValidName(name):
				return nil, nil, fmt.Errorf("line %d: %q is not an environment variable name", n, name)
			case !strings.HasPrefix(strings.TrimSpace(rest), "ENC["):
				return nil, nil, fmt.Errorf("%s is not sops ciphertext", name)
			}
			inSops = false
			names = append(names, name)
			continue
		}
		if !inSops {
			return nil, nil, fmt.Errorf("line %d is not a top-level name: the file is not a flat map", n)
		}
		field := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if r, ok := strings.CutPrefix(field, "recipient:"); ok {
			r = strings.Trim(strings.TrimSpace(r), `"'`)
			if !ValidRecipient(r) {
				return nil, nil, fmt.Errorf("line %d names an age recipient %q that is not one", n, r)
			}
			recipients = append(recipients, r)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	if !meta {
		return nil, nil, errors.New("the file has no sops metadata: it is not a sops secrets file")
	}
	slices.Sort(names)
	if len(slices.Compact(slices.Clone(names))) != len(names) {
		return nil, nil, errors.New("a name is given twice")
	}
	return names, sorted(recipients), nil
}

// Report is what `guildhall secrets check` finds on a machine (docs/secrets.md#secrets-check): its key and that key's
// public key, its secrets file with the file's digest, whether it decrypts and the names it holds. It holds no
// value, and Problem holds sops's error output, never its standard output.
type Report struct {
	Key        string   `json:"key"`
	KeyExists  bool     `json:"key_exists"`
	Recipient  string   `json:"recipient,omitempty"`
	File       string   `json:"file"`
	FileExists bool     `json:"file_exists"`
	FileSHA256 string   `json:"file_sha256,omitempty"`
	Decrypts   bool     `json:"decrypts"`
	Names      []string `json:"names"`
	Problem    string   `json:"problem,omitempty"`
}

// Check reports on p: the key's public key, read with age-keygen -y, the file's sha256 and, with both present, whether
// the file decrypts with the key and its names, by Load. It never fails: what it cannot read is its Problem.
func Check(ctx context.Context, p Paths, run Runner) Report {
	r := Report{Key: p.Key, File: p.File, Names: []string{}}
	var problems []string
	if _, err := os.Stat(p.Key); err == nil {
		r.KeyExists = true
		if r.Recipient, err = PublicKey(ctx, p.Key, run); err != nil {
			problems = append(problems, err.Error())
		}
	} else {
		problems = append(problems, "no machine key at "+p.Key+"; run guildhall secrets init")
	}
	if data, err := os.ReadFile(p.File); err == nil {
		r.FileExists, r.FileSHA256 = true, fmt.Sprintf("%x", sha256.Sum256(data))
	} else if !errors.Is(err, os.ErrNotExist) {
		problems = append(problems, err.Error())
	}
	if r.KeyExists && r.FileExists {
		v, err := Load(ctx, p, run)
		if err != nil {
			problems = append(problems, err.Error())
		} else {
			r.Decrypts, r.Names = true, v.Names()
		}
	}
	r.Problem = strings.Join(problems, "; ")
	return r
}

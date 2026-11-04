package secrets

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrWindows refuses secrets init on Windows, where an owner-only key is not yet supported or tested (R1.27).
var ErrWindows = errors.New("the machine key is not supported on Windows yet")

// Init creates the machine's age key at p.Key, owner-only (0600 in a 0700 directory), by running age-keygen and
// writing its output itself, and returns the key's public key, which is a recipient and not a secret, and whether
// it created the key. An existing key is kept: its public key is read with age-keygen -y. The secret key is never
// returned or printed.
func Init(ctx context.Context, p Paths, run Runner) (string, bool, error) {
	if runtime.GOOS == "windows" {
		return "", false, ErrWindows
	}
	if run == nil {
		run = Exec
	}
	path, err := exec.LookPath(KeygenProgram)
	if err != nil {
		return "", false, fmt.Errorf("secrets init: %s is not on PATH: %w", KeygenProgram, err)
	}
	if _, err := os.Lstat(p.Key); err == nil {
		pub, err := publicKeyOf(ctx, path, p.Key, run)
		if err != nil {
			return "", false, fmt.Errorf("secrets init: %w", err)
		}
		return pub, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("secrets init: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(p.Key), 0o700); err != nil {
		return "", false, fmt.Errorf("secrets init: %w", err)
	}
	out, _, err := run(ctx, nil, path)
	if err != nil {
		return "", false, fmt.Errorf("secrets init: %s failed: %v", KeygenProgram, err)
	}
	pub, ok := keyPublic(out)
	if !ok || !bytes.Contains(out, []byte("AGE-SECRET-KEY-")) {
		return "", false, fmt.Errorf("secrets init: %s printed no age key", KeygenProgram)
	}
	f, err := os.OpenFile(p.Key, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", false, fmt.Errorf("secrets init: %w", err)
	}
	_, err = f.Write(out)
	if err == nil {
		err = f.Chmod(0o600)
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		_ = os.Remove(p.Key)
		return "", false, fmt.Errorf("secrets init: write %s: %w", p.Key, err)
	}
	return pub, true, nil
}

// keyPublic is the public key age-keygen's "# public key: age1..." comment names in out.
func keyPublic(out []byte) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if pub, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "# public key: "); ok && ValidRecipient(pub) {
			return pub, true
		}
	}
	return "", false
}

// PublicKey is the public key of the age key file key, the recipient a secrets file for it is encrypted to, read
// with age-keygen -y. The secret key is never returned.
func PublicKey(ctx context.Context, key string, run Runner) (string, error) {
	if run == nil {
		run = Exec
	}
	path, err := exec.LookPath(KeygenProgram)
	if err != nil {
		return "", fmt.Errorf("%s is not on PATH: %w", KeygenProgram, err)
	}
	return publicKeyOf(ctx, path, key, run)
}

func publicKeyOf(ctx context.Context, keygen, key string, run Runner) (string, error) {
	out, _, err := run(ctx, nil, keygen, "-y", key)
	if err != nil {
		return "", fmt.Errorf("%s exists but %s -y cannot read its public key: %v", key, KeygenProgram, err)
	}
	pub := strings.TrimSpace(string(out))
	if !ValidRecipient(pub) {
		return "", fmt.Errorf("%s -y %s printed no age public key", KeygenProgram, key)
	}
	return pub, nil
}

// ValidRecipient reports whether s has the shape of one age X25519 recipient, an age1... public key.
func ValidRecipient(s string) bool {
	return strings.HasPrefix(s, "age1") && !strings.ContainsAny(s, " \t\r\n") && len(s) < 200
}

package secrets

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/atomicfile"
)

// NewInfix and PrevSuffix name the files beside a machine's secrets file that a Steward push uses
// (docs/secrets.md#secrets-install): the Steward puts the ciphertext to File+NewInfix+<its sha256 hex>, and
// secrets install keeps the file it replaces as File+PrevSuffix.
const (
	NewInfix   = ".new-"
	PrevSuffix = ".prev"
)

// sha256Hex is a lowercase hex sha256, the only --expect secrets install takes.
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidSHA256 reports whether s is a lowercase hex sha256.
func ValidSHA256(s string) bool { return sha256Hex.MatchString(s) }

// NewFile is where a push of ciphertext with sha256 hex sum is dropped beside file.
func NewFile(file, sum string) string { return file + NewInfix + sum }

// The steps secrets install refuses at, which its report names.
const (
	InstallStepPaths    = "paths"
	InstallStepDigest   = "digest"
	InstallStepDecrypt  = "decrypt"
	InstallStepPrevious = "previous"
	InstallStepRename   = "rename"
)

// InstallReport is what `guildhall secrets install` did (docs/secrets.md#secrets-install): the file it installed
// from, its digest, whether it was installed and, when not, the step that refused and why. It holds names and
// digests, never a value.
type InstallReport struct {
	File         string   `json:"file"`
	New          string   `json:"new"`
	ExpectSHA256 string   `json:"expect_sha256"`
	Installed    bool     `json:"installed"`
	Step         string   `json:"step,omitempty"`
	Problem      string   `json:"problem,omitempty"`
	BeforeSHA256 string   `json:"before_sha256,omitempty"`
	Previous     string   `json:"previous,omitempty"`
	Names        []string `json:"names"`
	Removed      []string `json:"removed,omitempty"`
}

// Install swaps the ciphertext a Steward push dropped at NewFile(p.File, expect) into p.File only once it is known
// good: it refuses a key or file inside copyDir, a dropped file that is missing or whose sha256 is not expect, a
// missing key, or a file that does not decrypt with it (by Check, as secrets check does). It then copies the
// current file, if there is one, to p.File+PrevSuffix and renames the dropped file over p.File in one rename, so the
// live name always holds the old or the new ciphertext, and removes any other dropped file beside it. On a refusal
// the live file is untouched and the dropped file is removed. It never fails: what refused is the report's Step
// and Problem.
func Install(ctx context.Context, p Paths, copyDir, expect string, run Runner) InstallReport {
	r := InstallReport{File: p.File, New: NewFile(p.File, expect), ExpectSHA256: expect, Names: []string{}}
	refuse := func(step string, err error) InstallReport {
		r.Step, r.Problem = step, err.Error()
		if rmErr := os.Remove(r.New); rmErr == nil {
			r.Removed = append(r.Removed, r.New)
		}
		return r
	}
	if !ValidSHA256(expect) {
		// No path is built from an expect that is not a digest, so nothing is removed.
		r.New, r.Step, r.Problem = "", InstallStepDigest, fmt.Sprintf("%q is not a lowercase hex sha256", expect)
		return r
	}
	if copyDir != "" {
		if err := p.Outside(copyDir); err != nil {
			return refuse(InstallStepPaths, err)
		}
	}
	data, err := os.ReadFile(r.New)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return refuse(InstallStepDigest, fmt.Errorf("no file at %s to install; the push did not put it", r.New))
	case err != nil:
		return refuse(InstallStepDigest, err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != expect {
		return refuse(InstallStepDigest, fmt.Errorf("%s has sha256 %s, not the expected %s", r.New, got, expect))
	}
	c := Check(ctx, Paths{Key: p.Key, File: r.New}, run)
	switch {
	case !c.KeyExists:
		return refuse(InstallStepDecrypt, fmt.Errorf("no machine key at %s; run guildhall secrets init", p.Key))
	case !c.Decrypts:
		return refuse(InstallStepDecrypt, fmt.Errorf("%s does not decrypt with the machine's key: %s", r.New, c.Problem))
	}
	r.Names = c.Names
	old, err := os.ReadFile(p.File)
	switch {
	case err == nil:
		r.BeforeSHA256 = fmt.Sprintf("%x", sha256.Sum256(old))
		prev := p.File + PrevSuffix
		if err := atomicfile.Write(prev, old); err != nil {
			return refuse(InstallStepPrevious, fmt.Errorf("keeping the current file as %s: %w", prev, err))
		}
		r.Previous = prev
	case !errors.Is(err, os.ErrNotExist):
		return refuse(InstallStepPrevious, err)
	}
	if err := atomicfile.Rename(r.New, p.File); err != nil {
		return refuse(InstallStepRename, err)
	}
	r.Installed = true
	dir, prefix := filepath.Dir(p.File), filepath.Base(p.File)+NewInfix
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if f := filepath.Join(dir, e.Name()); !e.IsDir() && strings.HasPrefix(e.Name(), prefix) && os.Remove(f) == nil {
			r.Removed = append(r.Removed, f)
		}
	}
	slices.Sort(r.Removed)
	return r
}

package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// ResultSchema and ResultVersion identify the result file format in
// docs/adapters.md.
const (
	ResultSchema  = "guildhall.artisan-result"
	ResultVersion = 1
)

// MaxResultBytes bounds the size of a result file.
const MaxResultBytes = 1 << 20

// Result-file errors. A missing file is reported as fs.ErrNotExist.
var (
	// ErrMalformedResult reports a result file that breaks the contract.
	ErrMalformedResult = errors.New("malformed artisan result")
	// ErrIdentityMismatch reports a well-formed result for another task attempt.
	ErrIdentityMismatch = errors.New("artisan result is for another task attempt")
	// ErrUnsupportedResultVersion reports a result written by a newer contract.
	ErrUnsupportedResultVersion = errors.New("artisan result version is newer than this guildhall supports")
	// ErrResultExists reports an attempt to write a result that already exists.
	ErrResultExists = errors.New("artisan result already exists")
)

// identityShape bounds task and attempt ids to portable file-name-safe text.
var identityShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Identity is the immutable identity of one attempt at one task. A result
// counts only for the identity it names.
type Identity struct {
	TaskID    string `json:"task_id"`
	AttemptID string `json:"attempt_id"`
}

// Validate checks that both ids are present and portable.
func (id Identity) Validate() error {
	if !identityShape.MatchString(id.TaskID) {
		return fmt.Errorf("task id %q: need 1-128 letters, digits, '.', '_' or '-', starting with a letter or digit", id.TaskID)
	}
	if !identityShape.MatchString(id.AttemptID) {
		return fmt.Errorf("attempt id %q: need 1-128 letters, digits, '.', '_' or '-', starting with a letter or digit", id.AttemptID)
	}
	return nil
}

// Status is the Artisan's own verdict on its attempt. Both values complete the
// attempt.
type Status string

// Result statuses.
const (
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// Output names one file the Artisan produced, relative to its working directory.
type Output struct {
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

// Result is the content of a version 1 result file.
type Result struct {
	Identity
	Status    Status   `json:"status"`
	Summary   string   `json:"summary"`
	Outputs   []Output `json:"outputs"`
	LandedRef string   `json:"landed_ref,omitempty"`
}

// resultFile is the on-disk shape, with every field required by pointer so a
// missing field is distinguishable from an empty one.
type resultFile struct {
	Schema    *string   `json:"schema"`
	Version   *int      `json:"version"`
	TaskID    *string   `json:"task_id"`
	AttemptID *string   `json:"attempt_id"`
	Status    *Status   `json:"status"`
	Summary   *string   `json:"summary"`
	Outputs   *[]Output `json:"outputs"`
	LandedRef string    `json:"landed_ref,omitempty"`
}

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformedResult, fmt.Sprintf(format, args...))
}

// ParseResult decodes and validates a result file's bytes for the expected
// identity.
func ParseResult(data []byte, want Identity) (Result, error) {
	if len(data) > MaxResultBytes {
		return Result{}, malformed("larger than %d bytes", MaxResultBytes)
	}
	if err := checkResultKeys(data); err != nil {
		return Result{}, malformed("%v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f resultFile
	if err := dec.Decode(&f); err != nil {
		return Result{}, malformed("%v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Result{}, malformed("data after the result object")
	}
	switch {
	case f.Schema == nil || *f.Schema != ResultSchema:
		return Result{}, malformed("schema must be %q", ResultSchema)
	case f.Version == nil || *f.Version < 1:
		return Result{}, malformed("version must be a positive integer")
	case *f.Version > ResultVersion:
		return Result{}, fmt.Errorf("%w: version %d", ErrUnsupportedResultVersion, *f.Version)
	case f.TaskID == nil, f.AttemptID == nil, f.Status == nil, f.Summary == nil, f.Outputs == nil:
		return Result{}, malformed("task_id, attempt_id, status, summary and outputs are required")
	}
	r := Result{
		Identity:  Identity{TaskID: *f.TaskID, AttemptID: *f.AttemptID},
		Status:    *f.Status,
		Summary:   *f.Summary,
		Outputs:   *f.Outputs,
		LandedRef: f.LandedRef,
	}
	if err := r.validate(); err != nil {
		return Result{}, err
	}
	if r.Identity != want {
		return Result{}, fmt.Errorf("%w: result names task %q attempt %q, want task %q attempt %q",
			ErrIdentityMismatch, r.TaskID, r.AttemptID, want.TaskID, want.AttemptID)
	}
	return r, nil
}

// validate checks the content rules shared by reading and writing.
func (r Result) validate() error {
	if err := Identity.Validate(r.Identity); err != nil {
		return malformed("%v", err)
	}
	if r.Status != StatusSucceeded && r.Status != StatusFailed {
		return malformed("status %q must be %q or %q", r.Status, StatusSucceeded, StatusFailed)
	}
	if strings.TrimSpace(r.Summary) == "" {
		return malformed("summary is empty")
	}
	for _, o := range r.Outputs {
		if o.Path == "" || strings.Contains(o.Path, `\`) || path.IsAbs(o.Path) || filepath.IsAbs(o.Path) ||
			filepath.VolumeName(o.Path) != "" || path.Clean(o.Path) != o.Path || o.Path == ".." || strings.HasPrefix(o.Path, "../") {
			return malformed("output path %q must be a clean relative path with '/' separators inside the working directory", o.Path)
		}
	}
	if r.LandedRef != "" && strings.ContainsFunc(r.LandedRef, func(c rune) bool { return c <= ' ' || c == 0x7f }) {
		return malformed("landed_ref %q contains whitespace or control characters", r.LandedRef)
	}
	return nil
}

func checkResultKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'):
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				k, _ := key.(string)
				switch k {
				case "schema", "version", "task_id", "attempt_id", "status", "summary", "outputs", "landed_ref", "path", "description":
				default:
					return fmt.Errorf("unknown key %q", k)
				}
				if seen[k] {
					return fmt.Errorf("duplicate key %q", k)
				}
				seen[k] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case json.Delim('['):
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		return nil
	}
	return walk()
}

// ReadResult reads and validates the result file at path. A file that does
// not exist yet yields an error matching fs.ErrNotExist.
func ReadResult(path string, want Identity) (Result, error) {
	if err := checkCancellation(path, want); err != nil {
		return Result{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxResultBytes+1))
	// The file was opened read-only, so a close error loses nothing.
	_ = f.Close()
	if err != nil {
		return Result{}, err
	}
	r, err := ParseResult(data, want)
	if err != nil {
		return Result{}, err
	}
	if err := checkCancellation(path, want); err != nil {
		return Result{}, err
	}
	return r, nil
}

// WriteResult validates r and publishes it at path exactly once: the content
// is written to a temporary file beside path and then hard-linked into place,
// so a reader sees either no file or the whole result, and an existing result
// is never replaced.
func WriteResult(path string, r Result) error {
	if err := checkCancellation(path, r.Identity); err != nil {
		return err
	}
	if r.Outputs == nil {
		r.Outputs = []Output{}
	}
	if err := r.validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(struct {
		Schema  string `json:"schema"`
		Version int    `json:"version"`
		Result
	}{ResultSchema, ResultVersion, r}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	// The temporary name is removed whatever happens; once linked, the result
	// lives on under path.
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(append(data, '\n'))
	if err == nil {
		err = tmp.Sync()
	}
	if err = errors.Join(err, tmp.Close()); err != nil {
		return err
	}
	if err := checkCancellation(path, r.Identity); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrResultExists, path)
		}
		return err
	}
	return nil
}

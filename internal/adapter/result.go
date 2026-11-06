package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/strictjson"
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

// OutcomeKind is what a failed attempt's structured outcome reports instead of a plain failure.
type OutcomeKind string

// Outcome kinds.
const (
	// OutcomeAlreadyDelivered reports that another merged pull request, or an earlier slice, already shipped the
	// change the attempt was to make.
	OutcomeAlreadyDelivered OutcomeKind = "already-delivered"
	// OutcomeNothingToDo reports that the task needs no change at all.
	OutcomeNothingToDo OutcomeKind = "nothing-to-do"
)

// Outcome is a result's optional structured outcome (docs/adapters.md#result-file): an already-delivered outcome
// names exactly one of ByPR, the merged pull request's URL, and BySlice, an earlier slice of a multi-PR task; a
// nothing-to-do outcome gives Reason. It pairs only with status failed and no outputs. Which steps may report one,
// and whether by_slice is allowed, is the engine's to check, not the file's.
type Outcome struct {
	Kind    OutcomeKind `json:"kind"`
	ByPR    string      `json:"by_pr,omitempty"`
	BySlice int         `json:"by_slice,omitempty"`
	Reason  string      `json:"reason,omitempty"`
}

// Label is the outcome as reports name it: "already delivered by <url>", "already delivered by slice <j>" or
// "nothing to do: <reason>".
func (o Outcome) Label() string {
	switch {
	case o.Kind == OutcomeNothingToDo:
		return "nothing to do: " + o.Reason
	case o.BySlice > 0:
		return fmt.Sprintf("already delivered by slice %d", o.BySlice)
	default:
		return "already delivered by " + o.ByPR
	}
}

// outcomeFile is an outcome's on-disk shape: kind and by_slice by pointer, so a missing kind or a given 0 is refused
// rather than read as absent.
type outcomeFile struct {
	Kind    *OutcomeKind `json:"kind"`
	ByPR    string       `json:"by_pr,omitempty"`
	BySlice *int         `json:"by_slice,omitempty"`
	Reason  string       `json:"reason,omitempty"`
}

// Result is the content of a version 1 result file.
type Result struct {
	Identity
	Status    Status   `json:"status"`
	Summary   string   `json:"summary"`
	Outputs   []Output `json:"outputs"`
	LandedRef string   `json:"landed_ref,omitempty"`
	Outcome   *Outcome `json:"outcome,omitempty"`
}

// resultFile is the on-disk shape, with every field required by pointer so a
// missing field is distinguishable from an empty one.
type resultFile struct {
	Schema    *string      `json:"schema"`
	Version   *int         `json:"version"`
	TaskID    *string      `json:"task_id"`
	AttemptID *string      `json:"attempt_id"`
	Status    *Status      `json:"status"`
	Summary   *string      `json:"summary"`
	Outputs   *[]Output    `json:"outputs"`
	LandedRef string       `json:"landed_ref,omitempty"`
	Outcome   *outcomeFile `json:"outcome,omitempty"`
}

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformedResult, fmt.Sprintf(format, args...))
}

// ParseResult decodes and validates a result file's bytes for the expected
// identity.
func ParseResult(data []byte, want Identity) (Result, error) {
	// The format versions with separate "schema" and "version" fields, so strictjson checks only shape.
	var f resultFile
	if err := strictjson.Decode(data, &f, strictjson.Options{MaxBytes: MaxResultBytes}); err != nil {
		return Result{}, malformed("%v", err)
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
	if o := f.Outcome; o != nil {
		switch {
		case o.Kind == nil:
			return Result{}, malformed("outcome.kind is required")
		case o.BySlice != nil && *o.BySlice < 1:
			return Result{}, malformed("outcome.by_slice %d must be a slice number, 1 or more", *o.BySlice)
		}
		r.Outcome = &Outcome{Kind: *o.Kind, ByPR: o.ByPR, Reason: o.Reason}
		if o.BySlice != nil {
			r.Outcome.BySlice = *o.BySlice
		}
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

// Validate checks r against the content rules ParseResult and WriteResult apply, such as an outcome's shape, so a
// wrapper can refuse a result before it writes anything; a refusal wraps ErrMalformedResult.
func (r Result) Validate() error { return r.validate() }

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
	if r.Outcome != nil {
		if err := r.Outcome.validate(); err != nil {
			return err
		}
		if r.Status != StatusFailed {
			return malformed("an outcome pairs with status %q, not %q", StatusFailed, r.Status)
		}
		if len(r.Outputs) > 0 {
			return malformed("a result with an outcome lists no outputs")
		}
	}
	return nil
}

// validate checks an outcome's shape: a known kind with exactly the fields that kind takes.
func (o Outcome) validate() error {
	switch o.Kind {
	case OutcomeAlreadyDelivered:
		switch {
		case o.Reason != "":
			return malformed("an %s outcome takes by_pr or by_slice, not reason", o.Kind)
		case o.ByPR != "" && o.BySlice != 0:
			return malformed("an %s outcome names by_pr or by_slice, not both", o.Kind)
		case o.ByPR == "" && o.BySlice == 0:
			return malformed("an %s outcome names the merged pull request in by_pr, or the earlier slice in by_slice", o.Kind)
		case o.BySlice < 0:
			return malformed("outcome.by_slice %d must be a slice number, 1 or more", o.BySlice)
		case o.ByPR != "":
			u, err := url.Parse(o.ByPR)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
				strings.ContainsFunc(o.ByPR, func(c rune) bool { return c <= ' ' || c == 0x7f }) {
				return malformed("outcome.by_pr %q must be the pull request's http or https URL", o.ByPR)
			}
		}
	case OutcomeNothingToDo:
		switch {
		case o.ByPR != "" || o.BySlice != 0:
			return malformed("a %s outcome takes reason, not by_pr or by_slice", o.Kind)
		case strings.TrimSpace(o.Reason) == "":
			return malformed("a %s outcome says why in reason", o.Kind)
		}
	default:
		return malformed("outcome.kind %q must be %q or %q", o.Kind, OutcomeAlreadyDelivered, OutcomeNothingToDo)
	}
	return nil
}

// ReadResult reads and validates the result file at path. A file that does
// not exist yet yields an error matching fs.ErrNotExist.
func ReadResult(path string, want Identity) (Result, error) {
	if err := checkCancellation(path, want); err != nil {
		return Result{}, err
	}
	f, err := atomicfile.Open(path)
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

// ReadyDraft returns the draft result at path when it is a valid result for id: no larger than MaxResultBytes and
// accepted by ParseResult. It runs none of intake's checks. A model-run Artisan's wrapper publishes such a draft, and
// `guildhall result hook` counts one as the attempt's result, so it sends no nudge; a missing, unreadable, oversized,
// malformed or misidentified draft is not ready.
func ReadyDraft(path string, id Identity) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxResultBytes+1))
	_ = f.Close()
	if err != nil || len(data) > MaxResultBytes {
		return nil, false
	}
	if _, err := ParseResult(data, id); err != nil {
		return nil, false
	}
	return data, true
}

// Cancelled reports whether the cancel marker of the attempt id is present:
// the `.cancelled` sidecar beside the result file resultFile. A missing
// sidecar is false; one naming another attempt is an error matching
// ErrIdentityMismatch, and an unreadable one is an error.
func Cancelled(resultFile string, id Identity) (bool, error) {
	err := checkCancellation(resultFile, id)
	if errors.Is(err, ErrAttemptCancelled) {
		return true, nil
	}
	return false, err
}

// MarkCancelled writes the cancel marker of the attempt id beside the result
// file resultFile, durably; marking a marked attempt changes nothing. Once it
// is present ReadResult and WriteResult refuse with ErrAttemptCancelled.
func MarkCancelled(resultFile string, id Identity) error {
	return cancelAttempt(resultFile, id)
}

// WriteResult validates r and publishes it at path exactly once: the content
// is written to a temporary file beside path and then hard-linked into place,
// so a reader sees either no file or the whole result, and an existing result
// is never replaced. The temporary file is synced before it is linked, except
// in a test binary that called SkipSyncForTests.
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
	if err == nil && !resultSyncSkipped.Load() {
		err = syncFile(tmp)
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

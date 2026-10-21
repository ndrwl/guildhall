package messaging

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/strictjson"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Decode reads one envelope strictly: unknown, duplicate or case-variant fields,
// trailing data, more than MaxEnvelopeBytes and unknown or newer schemas are refused.
// A leading UTF-8 byte order mark, as some Windows tools write, is ignored, and UTF-16 is refused.
func Decode(r io.Reader) (Envelope, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxEnvelopeBytes+1))
	if err != nil {
		return Envelope{}, fmt.Errorf("messaging: read envelope: %w", err)
	}
	var e Envelope
	err = strictjson.Decode(data, &e, strictjson.Options{Schema: SchemaPrefix, Version: SchemaVersion, MaxBytes: MaxEnvelopeBytes})
	if errors.Is(err, ErrNewerSchema) {
		return Envelope{}, fmt.Errorf("messaging: %w", err)
	}
	if err != nil {
		return Envelope{}, invalid("envelope: %v", err)
	}
	return e, nil
}

// ParseRecipient reads "director" or "artisan:<repository>/<task>/<step>/<attempt>".
func ParseRecipient(s string) (store.Recipient, error) {
	if s == string(store.RecipientDirector) {
		return store.Recipient{Role: store.RecipientDirector}, nil
	}
	rest, ok := strings.CutPrefix(s, string(store.RecipientArtisan)+":")
	parts := strings.Split(rest, "/")
	if !ok || len(parts) != 4 {
		return store.Recipient{}, invalid(`recipient %q must be "director" or "artisan:<repository>/<task>/<step>/<attempt>"`, s)
	}
	for _, id := range parts[:3] {
		if !workflow.ValidID(id) {
			return store.Recipient{}, invalid("recipient %q: %q is not a valid id", s, id)
		}
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n < 1 || strconv.Itoa(n) != parts[3] || n > 1<<31-1 {
		return store.Recipient{}, invalid("recipient %q: attempt %q must be a positive number", s, parts[3])
	}
	return store.Recipient{Role: store.RecipientArtisan, Repository: parts[0], TaskID: parts[1], Step: parts[2], Attempt: n}, nil
}

// FormatRecipient is the inverse of ParseRecipient.
func FormatRecipient(r store.Recipient) string {
	if r.Role == store.RecipientArtisan {
		return fmt.Sprintf("%s:%s/%s/%s/%d", r.Role, r.Repository, r.TaskID, r.Step, r.Attempt)
	}
	return string(r.Role)
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("messaging: %w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

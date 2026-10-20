package messaging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/store"
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
	if len(data) > MaxEnvelopeBytes {
		return Envelope{}, invalid("envelope is larger than %d bytes", MaxEnvelopeBytes)
	}
	// Windows PowerShell 5.1 writes UTF-16 for ">" and Out-File; say so rather than report a JSON syntax error.
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		return Envelope{}, invalid("envelope is UTF-16; write it as UTF-8, for example with Set-Content -Encoding utf8")
	}
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	if err := checkKeys(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return Envelope{}, invalid("envelope: %v", err)
	}
	var head struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return Envelope{}, invalid("envelope: %v", err)
	}
	num, ok := strings.CutPrefix(head.Schema, SchemaPrefix)
	version, err := strconv.Atoi(num)
	if !ok || err != nil || version < 1 {
		return Envelope{}, invalid("schema %q is not %s<version>", head.Schema, SchemaPrefix)
	}
	if version > SchemaVersion {
		return Envelope{}, fmt.Errorf("messaging: %w: %q (supported: %s%d)", ErrNewerSchema, head.Schema, SchemaPrefix, SchemaVersion)
	}
	var e Envelope
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return Envelope{}, invalid("envelope: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Envelope{}, invalid("unexpected data after the envelope")
	}
	return e, nil
}

// checkKeys refuses an object holding the same key twice, compared without case
// because encoding/json matches field names that way; the value would otherwise
// silently be the last one given.
func checkKeys(dec *json.Decoder) error {
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
			name := strings.ToLower(key.(string))
			if seen[name] {
				return fmt.Errorf("duplicate key %q", key)
			}
			seen[name] = true
			if err := checkKeys(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
	case json.Delim('['):
		for dec.More() {
			if err := checkKeys(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
	}
	return err
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

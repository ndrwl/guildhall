package messaging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

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
	if !utf8.Valid(data) {
		return Envelope{}, invalid("envelope is not valid UTF-8")
	}
	if err := checkUnicode(data); err != nil {
		return Envelope{}, invalid("envelope: %v", err)
	}
	if err := checkKeys(json.NewDecoder(bytes.NewReader(data)), reflect.TypeFor[Envelope]()); err != nil {
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

func checkUnicode(data []byte) error {
	if !json.Valid(data) {
		return fmt.Errorf("invalid JSON")
	}
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if data[i] != 'u' {
			continue
		}
		n, _ := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		i += 4
		if n < 0xd800 || n > 0xdfff {
			continue
		}
		if n > 0xdbff || i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
			return fmt.Errorf("unpaired Unicode surrogate")
		}
		low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return fmt.Errorf("unpaired Unicode surrogate")
		}
		i += 6
	}
	return nil
}

func checkKeys(dec *json.Decoder, typ reflect.Type) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('{'):
		if typ.Kind() != reflect.Struct {
			return fmt.Errorf("unexpected object for %s", typ)
		}
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name := key.(string)
			var fieldType reflect.Type
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
				if name == tag {
					fieldType = field.Type
					break
				}
			}
			if fieldType == nil {
				return fmt.Errorf("unknown field %q", name)
			}
			if seen[name] {
				return fmt.Errorf("duplicate key %q", key)
			}
			seen[name] = true
			if err := checkKeys(dec, fieldType); err != nil {
				return err
			}
		}
		_, err = dec.Token()
	case json.Delim('['):
		if typ.Kind() != reflect.Slice {
			return fmt.Errorf("unexpected array for %s", typ)
		}
		for dec.More() {
			if err := checkKeys(dec, typ.Elem()); err != nil {
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

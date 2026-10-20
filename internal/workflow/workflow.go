// Package workflow parses and validates Guildhall workflow definitions.
//
// A workflow is a JSON file naming the built-in step kinds of one task in order,
// with typed input wiring and bounded loop rules.
// Parsing and validation are pure: they read no files, run nothing and grant no authority.
// Deciding who may author, approve or activate a workflow edit is deliberately outside this package.
package workflow

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// SchemaPrefix and SchemaVersion form the workflow "schema" value, for example "guildhall.workflow/1".
const (
	SchemaPrefix  = "guildhall.workflow/"
	SchemaVersion = 1
)

// MaxLoopRounds caps the "max" of any loop rule so every loop is bounded.
const MaxLoopRounds = 10

// ErrNewerSchema reports a document written for a newer Guildhall; it is refused rather than guessed at.
var ErrNewerSchema = errors.New("schema is newer than this Guildhall supports")

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// ValidID reports whether s is a valid workflow or step identifier.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// Definition is one parsed workflow.
type Definition struct {
	Schema      string `json:"schema"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Steps       []Step `json:"steps"`
}

// Step is one use of a built-in kind.
type Step struct {
	ID       string                     `json:"id"`
	Type     string                     `json:"type"`
	Version  int                        `json:"version"`
	Settings map[string]json.RawMessage `json:"settings,omitempty"`
	// Inputs maps each input port to "step.output" references.
	Inputs map[string]Refs `json:"inputs,omitempty"`
	// On maps an outcome to a loop rule.
	On map[string]Rule `json:"on,omitempty"`
}

// Profile returns the step's "profile" setting: the name of a Director config profile
// choosing the adapter, model, effort and role prompt of the Artisan that runs it, or "" when unset.
// Call it on a validated definition; an invalid value reads as "".
func (s Step) Profile() string { return s.stringSetting("profile") }

// Skills returns the step's "skills" setting in order, or nil when unset.
// Each name is a skill directory resolved by package render.
// Call it on a validated definition; an invalid value reads as nil.
func (s Step) Skills() []string {
	var names []string
	if raw, ok := s.Settings["skills"]; !ok || json.Unmarshal(raw, &names) != nil {
		return nil
	}
	return names
}

// Instructions returns the step's "instructions" setting, or "" when unset.
func (s Step) Instructions() string { return s.stringSetting("instructions") }

func (s Step) stringSetting(name string) string {
	var v string
	if raw, ok := s.Settings[name]; ok && json.Unmarshal(raw, &v) == nil {
		return v
	}
	return ""
}

// Rule sends the task back to the same or an earlier step on an outcome, at most Max times.
type Rule struct {
	Goto string `json:"goto"`
	Max  int    `json:"max"`
}

type Refs []string

// Revision identifies the exact workflow content a task binds when accepted.
type Revision struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

//go:embed default.json
var defaultJSON []byte

// Default returns Guildhall's shipped default workflow.
func Default() (*Definition, error) {
	def, err := Parse(defaultJSON)
	if err != nil {
		return nil, err
	}
	return def, Validate(def)
}

// Parse decodes a workflow strictly: unknown fields, duplicate keys, trailing data
// and unknown or newer schemas are refused. It does not validate wiring; call Validate.
func Parse(data []byte) (*Definition, error) {
	if err := checkSchema(data, SchemaPrefix, SchemaVersion); err != nil {
		return nil, err
	}
	var def Definition
	if err := decodeStrict(data, &def); err != nil {
		return nil, fmt.Errorf("workflow: %w", err)
	}
	return &def, nil
}

// checkSchema reads only the "schema" field so a newer document is refused before strict decoding.
func checkSchema(data []byte, prefix string, supported int) error {
	var head map[string]json.RawMessage
	if err := json.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("malformed JSON: %w", err)
	}
	var schema string
	if err := json.Unmarshal(head["schema"], &schema); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	num, ok := strings.CutPrefix(schema, prefix)
	version, err := strconv.Atoi(num)
	if !ok || err != nil || version < 1 {
		return fmt.Errorf("schema %q is not %s<version>", schema, prefix)
	}
	if version > supported {
		return fmt.Errorf("%w: %q (supported: %s%d)", ErrNewerSchema, schema, prefix, supported)
	}
	return nil
}

func decodeStrict(data []byte, v any) error {
	if err := checkJSONKeys(json.NewDecoder(bytes.NewReader(data)), reflect.TypeOf(v).Elem()); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("unexpected data after the JSON document")
	}
	return nil
}

func checkJSONKeys(dec *json.Decoder, typ reflect.Type) error {
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
			name := key.(string)
			if seen[name] {
				return fmt.Errorf("duplicate key %q", name)
			}
			seen[name] = true
			var child reflect.Type
			if typ != nil {
				switch typ.Kind() {
				case reflect.Struct:
					for i := 0; i < typ.NumField(); i++ {
						field := typ.Field(i)
						tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
						if tag == name {
							child = field.Type
							break
						}
					}
					if child == nil {
						return fmt.Errorf("unknown field %q", name)
					}
				case reflect.Map:
					child = typ.Elem()
				}
			}
			if err := checkJSONKeys(dec, child); err != nil {
				return err
			}
		}
		_, err = dec.Token()
	case json.Delim('['):
		var child reflect.Type
		if typ != nil && typ.Kind() == reflect.Slice {
			child = typ.Elem()
		}
		for dec.More() {
			if err := checkJSONKeys(dec, child); err != nil {
				return err
			}
		}
		_, err = dec.Token()
	}
	return err
}

// Digest is the sha256 of the definition's canonical JSON encoding, so formatting does not change it.
func (d *Definition) Digest() string {
	data, err := json.Marshal(d)
	if err != nil {
		panic(fmt.Sprintf("workflow: canonical encoding failed: %v", err))
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Revision returns the immutable revision a task binds.
func (d *Definition) Revision() Revision {
	return Revision{Name: d.Name, Digest: d.Digest()}
}

// Override chooses a step's actor for one repository, for example the Guildmaster instead of an Artisan for review.
type Override struct {
	Actor Actor `toml:"actor" json:"actor"`
}

// ApplyOverrides returns a copy of def with the given steps' "actor" settings replaced.
// Only a step whose kind allows more than one actor can be overridden, and only with an actor its kind allows,
// so the kind, wiring and every other setting stay the same. def is not modified.
// The result has its own digest; validate it before use.
func ApplyOverrides(def *Definition, overrides map[string]Override) (*Definition, error) {
	out := *def
	out.Steps = append([]Step(nil), def.Steps...)
	for id, ov := range overrides {
		i := out.stepIndex(id)
		if i < 0 {
			return nil, fmt.Errorf("override: unknown step %q", id)
		}
		step := &out.Steps[i]
		kind, err := lookup(step.Type, step.Version)
		if err != nil {
			return nil, fmt.Errorf("override %q: %w", id, err)
		}
		if len(kind.Actors) == 1 {
			return nil, fmt.Errorf("override %q: %s always runs as %s", id, kind.Type, kind.Actors[0])
		}
		if !slices.Contains(kind.Actors, ov.Actor) {
			return nil, fmt.Errorf("override %q: %s does not allow actor %q", id, kind.Type, ov.Actor)
		}
		actor, err := json.Marshal(ov.Actor)
		if err != nil {
			return nil, fmt.Errorf("override %q: %w", id, err)
		}
		step.Settings = maps.Clone(step.Settings)
		if step.Settings == nil {
			step.Settings = map[string]json.RawMessage{}
		}
		step.Settings[ActorSetting] = actor
	}
	return &out, nil
}

// Actor returns who fulfils the step: its "actor" setting, or the only actor its kind allows.
// It returns "" for an unknown kind or a missing or malformed setting, which Validate refuses.
func (s Step) Actor() Actor {
	kind, ok := LookupKind(s.Type, s.Version)
	if !ok {
		return ""
	}
	if len(kind.Actors) == 1 {
		return kind.Actors[0]
	}
	var actor Actor
	if raw, set := s.Settings[ActorSetting]; !set || json.Unmarshal(raw, &actor) != nil || !slices.Contains(kind.Actors, actor) {
		return ""
	}
	return actor
}

func lookup(typ string, version int) (Kind, error) {
	if k, ok := LookupKind(typ, version); ok {
		return k, nil
	}
	if knownType(typ) {
		return Kind{}, fmt.Errorf("step type %q does not support version %d", typ, version)
	}
	return Kind{}, fmt.Errorf("unknown step type %q", typ)
}

func (d *Definition) stepIndex(id string) int {
	for i, s := range d.Steps {
		if s.ID == id {
			return i
		}
	}
	return -1
}

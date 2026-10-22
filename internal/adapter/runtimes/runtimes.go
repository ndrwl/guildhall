// Package runtimes is the one owner of the runtime adapters Guildhall can launch, keyed by the name
// `artisan start --adapter` and a profile's adapter use (docs/adapters.md#running-an-attempt).
// A new runtime is added here and nowhere else: launch.Process passes the engine's name through to
// `artisan start`, which looks it up.
package runtimes

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
)

// ErrUnknown reports a name that is not one of Names.
var ErrUnknown = errors.New("unknown runtime adapter")

// Runtime builds the command that runs one attempt's Artisan.
type Runtime interface {
	// Command returns the attempt's command for a valid spec and refuses an invalid one with no command.
	Command(spec adapter.Spec) (adapter.Command, error)
}

// Session builds the command that runs one interactive Director session (docs/director.md#start).
type Session interface {
	// Program is the program the session needs on PATH, or "" when it is the guildhall executable itself.
	Program() string
	// SkillsDir is the slash path, relative to the session directory, the session's skills are rendered into.
	SkillsDir() string
	// SessionFiles are the runtime's own files for a session of the copy, by slash path relative to the session
	// directory, written before the session starts.
	SessionFiles(copyDir string) map[string][]byte
	// Session returns the session's command for a valid spec and refuses an invalid one with no command.
	Session(spec adapter.SessionSpec) (adapter.Command, error)
	// TurnEndHook reports whether the runtime signals the end of each turn by running `guildhall director
	// turn-ended`, as its SessionFiles register (docs/director.md#the-end-of-a-turn).
	TurnEndHook() bool
}

// all builds each runtime over the guildhall executable.
var all = map[string]func(executable string) both{
	adapter.FakeName:   func(exe string) both { return fake.Adapter{Executable: exe} },
	adapter.ClaudeName: func(exe string) both { return claude.Adapter{Executable: exe} },
}

// both is a runtime that runs attempts and Director sessions.
type both interface {
	Runtime
	Session
}

// Names returns the names of the runtimes Guildhall can launch, in sorted order.
func Names() []string {
	return slices.Sorted(maps.Keys(all))
}

// Lookup returns the runtime called name, whose commands run executable, the guildhall binary.
// A name that is not one of Names is refused with an error that wraps ErrUnknown and lists Names.
func Lookup(name, executable string) (Runtime, error) {
	build, ok := all[name]
	if !ok {
		return nil, fmt.Errorf("%w %q: the adapters are %s", ErrUnknown, name, strings.Join(Names(), ", "))
	}
	return build(executable), nil
}

// LookupSession returns the Director session runtime called name, as Lookup does for an attempt's runtime;
// every runtime Guildhall can launch runs a Director session.
func LookupSession(name, executable string) (Session, error) {
	build, ok := all[name]
	if !ok {
		return nil, fmt.Errorf("%w %q: the adapters are %s", ErrUnknown, name, strings.Join(Names(), ", "))
	}
	return build(executable), nil
}

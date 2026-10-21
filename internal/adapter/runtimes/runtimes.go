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

// The names of the runtimes Guildhall can launch.
const (
	// Fake is the fake Artisan, package adapter/fake.
	Fake = "fake"
	// Claude is the Claude Code adapter, package adapter/claude.
	Claude = "claude"
)

// ErrUnknown reports a name that is not one of Names.
var ErrUnknown = errors.New("unknown runtime adapter")

// Runtime builds the command that runs one attempt's Artisan.
type Runtime interface {
	// Command returns the attempt's command for a valid spec and refuses an invalid one with no command.
	Command(spec adapter.Spec) (adapter.Command, error)
}

// all builds each runtime over the guildhall executable.
var all = map[string]func(executable string) Runtime{
	Fake:   func(exe string) Runtime { return fake.Adapter{Executable: exe} },
	Claude: func(exe string) Runtime { return claude.Adapter{Executable: exe} },
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

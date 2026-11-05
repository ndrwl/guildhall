// Package runtimes is the one owner of the runtime adapters Guildhall can launch, keyed by the name
// `artisan start --adapter` and a profile's adapter use (docs/adapters.md#running-an-attempt).
// Most also run a Director session; an attempt-only runtime, codex, does not.
// A new runtime is added here and nowhere else: launch.Process passes the engine's name through to
// `artisan start`, which looks it up, and what the runtime can do is its Runtime's capabilities, never a compare of
// its name. Only its name and its adapter.Declaration, which packages that cannot import this one read, sit beside
// the other names in package adapter, and the registry test refuses a runtime whose declaration is missing or
// disagrees with its methods.
package runtimes

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/codex"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/config"
)

// ErrUnknown reports a name that is not one of Names.
var ErrUnknown = errors.New("unknown runtime adapter")

// ErrNoSession reports a runtime that runs attempts only: one of Names that is not one of SessionNames.
var ErrNoSession = errors.New("runtime adapter runs no Director session")

// Runtime builds the command that runs one attempt's Artisan.
type Runtime interface {
	// Command returns the attempt's command for a valid spec and refuses an invalid one with no command, including
	// a spec whose Settings CheckSettings refuses.
	Command(spec adapter.Spec) (adapter.Command, error)
	// CheckSettings refuses, with an error wrapping adapter.ErrUnsupportedSetting that names the setting and why,
	// a model or effort the runtime cannot honour; the zero Settings, the runtime's defaults, is always honoured.
	// It builds and starts nothing, so a preflight can run it before any attempt is recorded.
	CheckSettings(s adapter.Settings) error
	// Steerable reports whether the runtime can be steered: it tells its Artisan about steering and lets it run
	// `guildhall messages` and `guildhall messages ack` (docs/messaging.md). `messages send` refuses an attempt
	// on a runtime that cannot, and intake does not check that attempt's inbox.
	Steerable() bool
	// GrantsTools reports whether the runtime applies a profile's allowed_tools and repository grants
	// (grant.EffectiveGrant); it agrees with adapter.GrantsTools for the runtime's name.
	GrantsTools() bool
	// SkillPolicy reports whether the runtime applies a Claude skill policy (config.ClaudeSkillPolicy); it agrees
	// with adapter.SkillPolicy for the runtime's name.
	SkillPolicy() bool
	// Signin is the runtime's entry in `guildhall signin` (docs/signin.md), or the zero entry when the runtime
	// needs no sign-in.
	Signin() adapter.Signin
}

// Session builds the command that runs one interactive Director session (docs/director.md#start). A session
// runtime also runs attempts, so it is a Runtime too.
type Session interface {
	Runtime
	// SessionOptions returns the runtime with the session's resolved Director skill policy and task-page links
	// applied, each where the runtime takes it; a nil argument leaves that option as it was.
	SessionOptions(skills *config.ClaudeSkillPolicy, links *adapter.Links) Session
	// Program is the program the session needs on PATH, or "" when it is the guildhall executable itself.
	Program() string
	// SkillsDir is the slash path, relative to the session directory, the session's skills are rendered into.
	SkillsDir() string
	// SessionFiles are the runtime's own files for a session of the copy, by slash path relative to the session
	// directory, written before the session starts.
	SessionFiles(copyDir string) map[string][]byte
	// Session returns the session's command for a valid spec and refuses an invalid one with no command.
	Session(spec adapter.SessionSpec) (adapter.Command, error)
	// TurnEndHook reports whether the runtime signals the start and end of each turn by running `guildhall
	// director turn-started` and `guildhall director turn-ended`, as its SessionFiles register
	// (docs/director.md#the-end-of-a-turn).
	TurnEndHook() bool
	// ContextReport reports whether the runtime supplies context use for automatic handoff,
	// an optional capability (docs/director.md#automatic-handoff).
	ContextReport() bool
}

// PersistentSession is an interactive runtime whose folder identity must survive numbered sessions.
// PrepareSessionWorkspace publishes the rendered runtime files after the previous process has ended,
// returning the stable process directory; it must not modify the numbered record or runtime-owned trust state.
type PersistentSession interface {
	PrepareSessionWorkspace(copyDir, recordDir string) (string, error)
}

// sessions builds each runtime that runs both attempts and Director sessions over the guildhall executable.
var sessions = map[string]func(executable string) Session{
	adapter.FakeName:   func(exe string) Session { return fakeSession{fake.Adapter{Executable: exe}} },
	adapter.ClaudeName: func(exe string) Session { return claudeSession{claude.Adapter{Executable: exe}} },
}

// claudeSession and fakeSession are the session runtimes, whose SessionOptions return a Session: the adapter
// packages cannot name this package's types.
type (
	claudeSession struct{ claude.Adapter }
	fakeSession   struct{ fake.Adapter }
)

func (s claudeSession) SessionOptions(skills *config.ClaudeSkillPolicy, links *adapter.Links) Session {
	return claudeSession{s.Adapter.SessionOptions(skills, links)}
}

func (s fakeSession) SessionOptions(skills *config.ClaudeSkillPolicy, links *adapter.Links) Session {
	return fakeSession{s.Adapter.SessionOptions(skills, links)}
}

// attemptsOnly builds each runtime that runs attempts but no Director session over the guildhall executable.
var attemptsOnly = map[string]func(executable string) Runtime{
	adapter.CodexName: func(exe string) Runtime { return codex.Adapter{Executable: exe} },
}

// Names returns the names of the runtimes Guildhall can launch for an attempt, in sorted order.
func Names() []string {
	names := slices.AppendSeq(slices.Collect(maps.Keys(sessions)), maps.Keys(attemptsOnly))
	slices.Sort(names)
	return names
}

// SessionNames returns the names of the runtimes that also run a Director session, in sorted order: Names without
// the attempt-only runtimes.
func SessionNames() []string {
	return slices.Sorted(maps.Keys(sessions))
}

// Lookup returns the runtime called name, whose commands run executable, the guildhall binary.
// A name that is not one of Names is refused with an error that wraps ErrUnknown and lists Names.
func Lookup(name, executable string) (Runtime, error) {
	if build, ok := sessions[name]; ok {
		return build(executable), nil
	}
	if build, ok := attemptsOnly[name]; ok {
		return build(executable), nil
	}
	return nil, fmt.Errorf("%w %q: the adapters are %s", ErrUnknown, name, strings.Join(Names(), ", "))
}

// LookupSession returns the Director session runtime called name, as Lookup does for an attempt's runtime.
// An attempt-only runtime, one of Names that is not one of SessionNames, is refused with an error that wraps
// ErrNoSession and lists SessionNames; any other name that is not one of SessionNames is refused as Lookup refuses it.
func LookupSession(name, executable string) (Session, error) {
	if build, ok := sessions[name]; ok {
		return build(executable), nil
	}
	if _, ok := attemptsOnly[name]; ok {
		return nil, fmt.Errorf("%w: %q runs attempts only; the Director session adapters are %s", ErrNoSession, name, strings.Join(SessionNames(), ", "))
	}
	return nil, fmt.Errorf("%w %q: the adapters are %s", ErrUnknown, name, strings.Join(SessionNames(), ", "))
}

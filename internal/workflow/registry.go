package workflow

// ArtifactType names what flows between steps, so wiring can be type-checked.
type ArtifactType string

const (
	Document         ArtifactType = "document"
	AcceptedSpec     ArtifactType = "accepted-spec"
	CodeHead         ArtifactType = "code-head"
	ReviewResult     ArtifactType = "review-outcome"
	ValidationResult ArtifactType = "validation-result"
)

// Port is one named, typed input or output of a step kind.
type Port struct {
	Name     string
	Type     ArtifactType
	Optional bool // inputs only: may be left unwired
	Many     bool // inputs only: may be wired to several outputs
}

// SettingType is the JSON type a step setting must have.
type SettingType string

const (
	StringSetting SettingType = "string"
	IntSetting    SettingType = "integer"
	BoolSetting   SettingType = "boolean"
	// NameSetting is a string that is a valid identifier (ValidID), such as a profile name.
	NameSetting SettingType = "name"
	// NameListSetting is a JSON array of distinct identifiers, such as skill names.
	NameListSetting SettingType = "list of names"
)

// Setting describes one setting a step kind accepts.
type Setting struct {
	Type     SettingType
	Required bool
}

// Actor names who fulfils a step: who is asked, launched or run when the task reaches it.
type Actor string

const (
	// ArtisanActor launches a fresh Artisan session for the step.
	ArtisanActor Actor = "artisan"
	// GuildmasterActor is a durable pause that waits for the Guildmaster without model calls.
	GuildmasterActor Actor = "guildmaster"
	// DirectorActor runs inside the Director's own session and ends with a recorded document.
	DirectorActor Actor = "director"
	// EngineActor runs inline in Guildhall itself, with no model.
	EngineActor Actor = "engine"
)

// ActorSetting is the step setting that chooses the actor of a kind that allows more than one.
const ActorSetting = "actor"

// Kind is one built-in, versioned step type shipped with Guildhall.
// Workflow JSON can only name kinds from this registry; it cannot add new ones.
type Kind struct {
	Type    string
	Version int
	Inputs  []Port
	Outputs []Port
	// Settings lists the only settings a step of this kind may carry.
	Settings map[string]Setting
	// Loops lists the outcomes that may carry an "on" rule sending the task back to an earlier step.
	Loops []string
	// Actors lists who may fulfil the kind's contract.
	// With one actor the step always runs as it and takes no "actor" setting;
	// with more, the "actor" setting is required and must name one of them.
	Actors []Actor
}

var instructions = map[string]Setting{"instructions": {Type: StringSetting}}

// artisan lists the profile and skills rendered for an Artisan.
var artisan = map[string]Setting{"profile": {Type: NameSetting}, "skills": {Type: NameListSetting}}

// reviewSettings survives actor overrides; profile and skills are unused by the Guildmaster.
var reviewSettings = map[string]Setting{
	"instructions": {Type: StringSetting},
	"profile":      artisan["profile"],
	"skills":       artisan["skills"],
}

var reviewOutputs = []Port{{Name: "outcome", Type: ReviewResult}}

var reviewLoops = []string{string(VerdictChangesRequested)}

var (
	artisanOnly     = []Actor{ArtisanActor}
	guildmasterOnly = []Actor{GuildmasterActor}
	engineOnly      = []Actor{EngineActor}
	// reviewers may fulfil either review kind, so a repository swaps human and automated review with one setting.
	reviewers = []Actor{ArtisanActor, GuildmasterActor}
)

var kinds = []Kind{
	{Type: "grill", Version: 1, Outputs: []Port{{Name: "notes", Type: Document}}, Settings: map[string]Setting{"skills": artisan["skills"]}, Actors: []Actor{DirectorActor, ArtisanActor}},
	{Type: "research", Version: 1, Outputs: []Port{{Name: "notes", Type: Document}}, Settings: map[string]Setting{"profile": artisan["profile"]}, Actors: artisanOnly},
	{
		Type: "plan", Version: 1,
		Inputs:   []Port{{Name: "context", Type: Document, Optional: true, Many: true}},
		Outputs:  []Port{{Name: "spec", Type: Document}},
		Settings: artisan,
		Actors:   artisanOnly,
	},
	{
		// Refute attacks a proposed spec without granting acceptance or a review verdict.
		Type: "refute", Version: 1,
		Inputs:   []Port{{Name: "spec", Type: Document}, {Name: "context", Type: Document, Optional: true, Many: true}},
		Outputs:  []Port{{Name: "findings", Type: Document}},
		Settings: artisan,
		Actors:   artisanOnly,
	},
	{
		// Document-review reviews a document, such as a plan's spec, before it is accepted;
		// its outcome is bound to the document's digest (Subject.DocumentDigest).
		Type: "document-review", Version: 1,
		Inputs:   []Port{{Name: "subject", Type: Document}},
		Outputs:  reviewOutputs,
		Settings: reviewSettings,
		Loops:    reviewLoops,
		Actors:   reviewers,
	},
	{
		Type: "guildmaster-approval", Version: 1,
		Inputs: []Port{
			{Name: "spec", Type: Document},
			{Name: "reviews", Type: ReviewResult, Optional: true, Many: true},
			{Name: "findings", Type: Document, Optional: true, Many: true},
		},
		Outputs:  []Port{{Name: "spec", Type: AcceptedSpec}},
		Settings: instructions,
		Loops:    reviewLoops,
		Actors:   guildmasterOnly,
	},
	{
		Type: "build", Version: 1,
		Inputs:   []Port{{Name: "spec", Type: AcceptedSpec}},
		Outputs:  []Port{{Name: "head", Type: CodeHead}},
		Settings: artisan,
		Actors:   artisanOnly,
	},
	{
		// Review reviews a code head against the accepted spec; its outcome is bound to the head (Subject.CodeHead).
		Type: "review", Version: 1,
		Inputs:   []Port{{Name: "spec", Type: AcceptedSpec}, {Name: "head", Type: CodeHead}},
		Outputs:  reviewOutputs,
		Settings: reviewSettings,
		Loops:    reviewLoops,
		Actors:   reviewers,
	},
	{
		// Publish pushes the reviewed head to the repository's remote branch and opens or updates its pull request.
		// It never merges, and it takes no settings: the repository's configuration names the remote.
		// Its output is the same head, so validate and deliver read the head that was pushed.
		Type: "publish", Version: 1,
		Inputs: []Port{
			{Name: "head", Type: CodeHead},
			{Name: "review", Type: ReviewResult, Optional: true, Many: true},
		},
		Outputs: []Port{{Name: "head", Type: CodeHead}},
		Actors:  engineOnly,
	},
	{
		Type: "validate", Version: 1,
		Inputs:  []Port{{Name: "head", Type: CodeHead}},
		Outputs: []Port{{Name: "result", Type: ValidationResult}},
		Loops:   []string{"failed"},
		Actors:  engineOnly,
	},
	{
		// Deliver takes no settings: the repository's merge policy decides, never the workflow.
		Type: "deliver", Version: 1,
		Inputs: []Port{
			{Name: "head", Type: CodeHead},
			{Name: "review", Type: ReviewResult, Optional: true, Many: true},
			{Name: "validation", Type: ValidationResult, Optional: true},
		},
		Actors: engineOnly,
	},
	{
		Type: "handoff-wait", Version: 1,
		Outputs:  []Port{{Name: "handoff", Type: Document}},
		Settings: map[string]Setting{"from": {Type: StringSetting, Required: true}},
		Actors:   guildmasterOnly,
	},
}

// Kinds returns every built-in step kind.
func Kinds() []Kind {
	return append([]Kind(nil), kinds...)
}

// LookupKind returns the built-in kind with this type and version.
func LookupKind(typ string, version int) (Kind, bool) {
	for _, k := range kinds {
		if k.Type == typ && k.Version == version {
			return k, true
		}
	}
	return Kind{}, false
}

func knownType(typ string) bool {
	for _, k := range kinds {
		if k.Type == typ {
			return true
		}
	}
	return false
}

func (k Kind) input(name string) (Port, bool) { return findPort(k.Inputs, name) }

func (k Kind) output(name string) (Port, bool) { return findPort(k.Outputs, name) }

func findPort(ports []Port, name string) (Port, bool) {
	for _, p := range ports {
		if p.Name == name {
			return p, true
		}
	}
	return Port{}, false
}

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
	// Human marks a durable pause that waits for the Guildmaster without model calls.
	Human bool
	// Review marks a step that produces the shared ReviewOutcome; review kinds are interchangeable.
	Review bool
}

var instructions = map[string]Setting{"instructions": {Type: StringSetting}}

// artisan lists the settings of kinds an Artisan can run: the Director profile and the skills rendered for it.
var artisan = map[string]Setting{"profile": {Type: NameSetting}, "skills": {Type: NameListSetting}}

// reviewSettings is shared by both review kinds, so overriding one with the other keeps a step valid.
var reviewSettings = map[string]Setting{
	"instructions": {Type: StringSetting},
	"profile":      artisan["profile"],
	"skills":       artisan["skills"],
}

var reviewInputs = []Port{
	{Name: "spec", Type: AcceptedSpec},
	{Name: "head", Type: CodeHead},
}

var reviewOutputs = []Port{{Name: "outcome", Type: ReviewResult}}

var reviewLoops = []string{string(VerdictChangesRequested)}

var kinds = []Kind{
	{Type: "grill", Version: 1, Outputs: []Port{{Name: "notes", Type: Document}}},
	{Type: "research", Version: 1, Outputs: []Port{{Name: "notes", Type: Document}}},
	{
		Type: "plan", Version: 1,
		Inputs:   []Port{{Name: "context", Type: Document, Optional: true, Many: true}},
		Outputs:  []Port{{Name: "spec", Type: Document}},
		Settings: artisan,
	},
	{
		Type: "guildmaster-approval", Version: 1,
		Inputs:   []Port{{Name: "spec", Type: Document}},
		Outputs:  []Port{{Name: "spec", Type: AcceptedSpec}},
		Settings: instructions,
		Loops:    reviewLoops,
		Human:    true,
	},
	{
		Type: "build", Version: 1,
		Inputs:   []Port{{Name: "spec", Type: AcceptedSpec}},
		Outputs:  []Port{{Name: "head", Type: CodeHead}},
		Settings: artisan,
	},
	{
		Type: "automated-review", Version: 1,
		Inputs: reviewInputs, Outputs: reviewOutputs, Settings: reviewSettings, Loops: reviewLoops,
		Review: true,
	},
	{
		Type: "human-review", Version: 1,
		Inputs: reviewInputs, Outputs: reviewOutputs, Settings: reviewSettings, Loops: reviewLoops,
		Human: true, Review: true,
	},
	{
		Type: "validate", Version: 1,
		Inputs:  []Port{{Name: "head", Type: CodeHead}},
		Outputs: []Port{{Name: "result", Type: ValidationResult}},
		Loops:   []string{"failed"},
	},
	{
		// Deliver takes no settings: the repository's merge policy decides, never the workflow.
		Type: "deliver", Version: 1,
		Inputs: []Port{
			{Name: "head", Type: CodeHead},
			{Name: "review", Type: ReviewResult, Optional: true, Many: true},
			{Name: "validation", Type: ValidationResult, Optional: true},
		},
	},
	{
		Type: "handoff-wait", Version: 1,
		Outputs:  []Port{{Name: "handoff", Type: Document}},
		Settings: map[string]Setting{"from": {Type: StringSetting, Required: true}},
		Human:    true,
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

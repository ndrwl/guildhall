package web

// Tone is what an option does to its ask, shown as the colour of its tile's edge.
type Tone string

const (
	// ToneNeutral is an option the table does not describe, or one that neither ends nor carries on the work.
	ToneNeutral Tone = ""
	// TonePositive ends the ask well, such as approve or accept (green).
	TonePositive Tone = "pos"
	// ToneNegative stops the work, such as reject (red).
	ToneNegative Tone = "neg"
	// ToneCarryOn carries the work on another round, such as continue (blue).
	ToneCarryOn Tone = "carry"
)

// Meaning is the one line an answer tile shows under its option, and its tone.
type Meaning struct {
	Text string
	Tone Tone
}

// meanings maps each decision kind, then each option it offers, to its meaning. It covers every option set the
// engine (interp.DecisionOptions), the rendered Guildmaster questions (render.DecisionOptions), grants
// (store.DecisionOptions) and the Steward (cli.StewardDecisionOptions) offer; TestOptionMeaningsCoverEveryOption
// keeps it in step with them. The Steward's rollout approval is listed although a Director's page never reads the
// Steward's store, so a later Steward page needs no new table. The untyped kind "" holds the handoff wait's option,
// which render.Question offers with no kind of its own.
var meanings = map[string]map[string]Meaning{
	"loop-exit": {
		"continue": {"another round of the loop", ToneCarryOn},
		"accept":   {"take the work as it is and go on", TonePositive},
		"reject":   {"stop here; nothing more is delivered", ToneNegative},
	},
	"approval": {
		"approved":          {"approve it; the task goes on", TonePositive},
		"changes_requested": {"send it back with your words", ToneCarryOn},
		"approve":           {"roll the plan out to its machines", TonePositive},
		"decline":           {"do not roll the plan out", ToneNegative},
	},
	"review": {
		"approved":          {"the work passes review", TonePositive},
		"changes_requested": {"send it back for another round", ToneCarryOn},
		"rejected":          {"stop here; the work is not delivered", ToneNegative},
	},
	"": {
		"received": {"the handoff arrived; carry on", TonePositive},
	},
	"grant": {
		"approve": {"the grant takes effect for its attempt", TonePositive},
		"reject":  {"the attempt runs without it", ToneNegative},
	},
	"merge": {
		"merge": {"merge the pull request", TonePositive},
		"hold":  {"keep it open for now; nothing merges", ToneCarryOn},
	},
	"follow-ups": {
		"pick": {"make a task of each id on the lines below", TonePositive},
		"none": {"make no follow-up tasks", ToneNeutral},
	},
}

// OptionMeaning is what option means for a decision of kind, and whether the table describes it; an option it does
// not describe has no meaning and the neutral tone, and its tile shows its word alone.
func OptionMeaning(kind, option string) (Meaning, bool) {
	m, ok := meanings[kind][option]
	return m, ok
}

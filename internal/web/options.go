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
// (store.DecisionOptions), the Steward (cli.StewardDecisionOptions) and the managed-mode alerts
// (admit.AlertDecisionOptions) offer; TestOptionMeaningsCoverEveryOption
// keeps it in step with them. The Steward's rollout approval is listed although a Director's page never reads the
// Steward's store, so a later Steward page needs no new table. The untyped kind "" holds the handoff wait's option,
// which render.Question offers with no kind of its own. The answer tiles read it through OptionMeaning, and the
// stepper and the story through DecisionState, so the page has one tone source.
var meanings = map[string]map[string]Meaning{
	"loop-exit": {
		"continue": {"another round of the loop", ToneCarryOn},
		"accept":   {"take the work as it is and go on", TonePositive},
		"reject":   {"stop here; nothing more is delivered", ToneNegative},
	},
	"approval": {
		"approved":          {"approve it; the task goes on", TonePositive},
		"changes_requested": {"send it back with your words", ToneCarryOn},
		"approve":           {"approve exactly this; a rollout plan rolls out, a managed revision applies", TonePositive},
		"decline":           {"do not roll the plan out", ToneNegative},
		"reject":            {"the managed revision stays refused", ToneNegative},
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

// DecisionState is the stepper's and the story's state for a decision of kind answered with choice: a positive
// answer is done, a negative one failed, a carry-on or neutral one done with its word, and an option the table does
// not describe for kind is back. It tests ok before the tone, since an unknown option and follow-ups none are both
// neutral.
func DecisionState(kind, choice string) StepState {
	m, ok := OptionMeaning(kind, choice)
	switch {
	case !ok:
		return StateBack
	case m.Tone == ToneNegative:
		return StateFailed
	default:
		return StateDone
	}
}

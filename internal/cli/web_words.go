package cli

import (
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// The page's words: fixed templates that say each record in plain English, beside docKind's groups (d150). They are
// the task page's only; Clerk's stands and its goldens keep their own words.
//
// A template may name a place, a step in a slice, as {Place} ("Slice 2 review", "Review"), {The} ("The slice 2
// review", "The review") or {the}; a build's work as {Slice} ("Slice 2", "The work"); and a round as {n}. A story
// sentence that names no {n} has " (round n)" added.

// stepWords is how the page says a step of one type: its noun, as in "the review", and the story's sentence for one
// of its attempts that finished.
type stepWords struct {
	noun, done string
}

// webStepWords holds every built-in step type's words; TestWebWordsCoverEveryStepType keeps it complete.
var webStepWords = map[string]stepWords{
	"grill":                {"grill", "{Place} round {n} finished"},
	"research":             {"research", "{Place} round {n} finished"},
	"plan":                 {"plan", "{The} was written"},
	"refute":               {"refutation", "{The} finished"},
	"document-review":      {"document review", "{The} finished"},
	"guildmaster-approval": {"plan approval", "{The} finished"},
	"build":                {"build", "{Slice} was built"},
	"rebase":               {"rebase", "The branch was rebased"},
	"design":               {"design", "{The} was drawn"},
	"review":               {"review", "{The} passed"},
	"publish":              {"publish", "The pull request was published"},
	"validate":             {"validation", "{The} finished"},
	"deliver":              {"delivery", "{The} finished"},
	"hallmark":             {"hallmark", "The hallmark report was written"},
	"handoff-wait":         {"handoff", "The handoff arrived"},
}

// webStageGroup is the task list's stage group of each built-in step type, for a task whose next step of that type
// starts, runs or waits for a free slot; TestWebStageGroupsCoverEveryStepType keeps it complete. The list never
// groups by step name, since workflows name their steps freely.
var webStageGroup = map[string]web.Group{
	"grill": web.GroupPlan, "research": web.GroupPlan, "plan": web.GroupPlan, "refute": web.GroupPlan,
	"document-review": web.GroupPlan, "guildmaster-approval": web.GroupPlan, "design": web.GroupPlan,
	"build": web.GroupBuild, "rebase": web.GroupBuild, "review": web.GroupBuild, "publish": web.GroupBuild,
	"validate": web.GroupBuild, "deliver": web.GroupBuild, "hallmark": web.GroupBuild, "handoff-wait": web.GroupBuild,
}

// webUnattendedGrill is the stepper's name of a grill an Artisan runs, an unattended grill (docs/tasks.md#grill-mode),
// whose row says finished rather than counting rounds with the Guildmaster; the story and documents keep the grill's
// words.
const webUnattendedGrill = "Unattended grill (Artisan)"

// stepWordsOf is the words of the bound workflow's step id: its type's, or, with no workflow, the type named like the
// step, as the default workflow names them; a step of no known type is named by its id.
func (b webBuilder) stepWordsOf(id string) stepWords {
	if w, ok := webStepWords[b.stepType(id)]; ok {
		return w
	}
	if id == "designer" {
		return webStepWords["design"]
	}
	if w, ok := webStepWords[id]; ok {
		return w
	}
	return stepWords{noun: strings.ToLower(webStepName(id)), done: "{Place} round {n} finished"}
}

// placeName names a step in slice k: "Slice 2 review", or "Review" in slice 1 and a one-slice task.
func placeName(noun string, k int) string {
	if k > 1 {
		return "Slice " + strconv.Itoa(k) + " " + noun
	}
	return upperFirst(noun)
}

// thePlace is placeName as "the slice 2 review".
func thePlace(noun string, k int) string {
	return "the " + lowerFirst(placeName(noun, k))
}

// fillWords fills a template's place, slice and round.
func fillWords(tmpl, noun string, k, n int) string {
	work := "The work"
	if k > 1 {
		work = "Slice " + strconv.Itoa(k)
	}
	return strings.NewReplacer("{Place}", placeName(noun, k), "{The}", upperFirst(thePlace(noun, k)), "{the}", thePlace(noun, k),
		"{Slice}", work, "{n}", strconv.Itoa(n)).Replace(tmpl)
}

// sentence fills a story template, adding " (round n)" when it names no round.
func sentence(tmpl, noun string, k, n int) string {
	s := fillWords(tmpl, noun, k, n)
	if !strings.Contains(tmpl, "{n}") {
		s += " (round " + strconv.Itoa(n) + ")"
	}
	return s
}

// attemptName names attempt n of step: "Slice 2 review, attempt 2".
func (b webBuilder) attemptName(step string, n int) string {
	base, k := workflow.ParseSliceStep(step)
	return placeName(b.stepWordsOf(base).noun, k) + ", attempt " + strconv.Itoa(n)
}

// attemptSentence is the story's title of an attempt: what happened, as a sentence.
func (b webBuilder) attemptSentence(at timedAttempt, back string, findings bool) string {
	base, k := workflow.ParseSliceStep(at.Step)
	w := b.stepWordsOf(base)
	switch {
	case at.IntakeRejection != "":
		return fillWords("{Place} round {n} was refused at intake", w.noun, k, at.Number)
	case at.Outcome == "":
		return fillWords("{Place} round {n} is running", w.noun, k, at.Number)
	case at.Outcome != store.AttemptSucceeded:
		return fillWords("{Place} round {n} "+strings.ReplaceAll(string(at.Outcome), "_", " "), w.noun, k, at.Number)
	case back != "":
		return sentence("{The} sent the "+b.stepWordsOf(back).noun+" back", w.noun, k, at.Number)
	case findings:
		return sentence("{The} asked for changes", w.noun, k, at.Number)
	}
	return sentence(w.done, w.noun, k, at.Number)
}

// askWords is how the page says a decision of one kind, with {s} its subject, such as "the plan" or "the slice 2
// review", and {S} the same capitalised: the story's sentence while it is open, which is also the ask card's headline
// (askHeadline), the short phrase the where-it-stands line and the Needs you index name it by, the sentence once
// answered with an option of each tone or in free text, and once void. It is the page's one per-kind table of ask
// words, beside the answer tiles' meanings (web.OptionMeaning).
type askWords struct {
	open, phrase string
	tones        map[web.Tone]string
	answered     string
	void         string
}

// webAskWords holds every decision kind's words; TestWebWordsCoverEveryDecisionKind keeps it complete.
var webAskWords = map[store.DecisionKind]askWords{
	store.DecisionApproval: {
		open: "{S} is ready for your approval", phrase: "approval of {s}",
		tones:    map[web.Tone]string{web.TonePositive: "You approved {s}", web.ToneCarryOn: "You asked for changes to {s}", web.ToneNegative: "You declined {s}"},
		answered: "You answered on {s}", void: "An earlier approval of {s} was withdrawn",
	},
	store.DecisionReview: {
		open: "{S} waits for your verdict", phrase: "your verdict on {s}",
		tones:    map[web.Tone]string{web.TonePositive: "You passed {s}", web.ToneCarryOn: "You sent {s} back for another round", web.ToneNegative: "You rejected the work at {s}"},
		answered: "You gave your verdict on {s}", void: "An earlier verdict on {s} was withdrawn",
	},
	store.DecisionLoopExit: {
		open: "{S} asks whether to go on", phrase: "whether to go on after {s}",
		tones:    map[web.Tone]string{web.ToneCarryOn: "You chose another round of {s}", web.TonePositive: "You accepted the work at {s}", web.ToneNegative: "You stopped the task at {s}"},
		answered: "You answered whether to go on after {s}", void: "An earlier ask to go on after {s} was withdrawn",
	},
	store.DecisionMerge: {
		open: "The work is ready to merge", phrase: "whether to merge",
		tones:    map[web.Tone]string{web.TonePositive: "You chose to merge", web.ToneCarryOn: "You held the merge"},
		answered: "You answered the merge ask", void: "An earlier merge ask was withdrawn",
	},
	store.DecisionQuestion: {
		open: "The Director asked you a question", phrase: "a question from the Director",
		answered: "You answered the Director's question", void: "An earlier question was withdrawn",
	},
	store.DecisionAudit: {
		open: "An audit waits for your answer", phrase: "an audit",
		answered: "You answered the audit", void: "An earlier audit was withdrawn",
	},
	store.DecisionGrant: {
		open: "An attempt asked for a grant", phrase: "a grant for an attempt",
		tones:    map[web.Tone]string{web.TonePositive: "You granted it", web.ToneNegative: "You refused the grant"},
		answered: "You answered the grant", void: "An earlier grant ask was withdrawn",
	},
	store.DecisionFollowUps: {
		open: "The hallmark's follow-ups wait for your pick", phrase: "your pick of follow-ups",
		tones:    map[web.Tone]string{web.TonePositive: "You picked follow-ups", web.ToneNeutral: "You chose no follow-ups"},
		answered: "You answered the follow-ups", void: "An earlier follow-ups ask was withdrawn",
	},
	// The untyped kind is the handoff wait's (render.Question).
	"": {
		open: "The handoff waits for you", phrase: "the handoff",
		tones:    map[web.Tone]string{web.TonePositive: "You confirmed the handoff"},
		answered: "You answered the handoff", void: "An earlier handoff ask was withdrawn",
	},
}

// askWordsOf is kind's words, a question's for a kind the table does not hold.
func askWordsOf(kind store.DecisionKind) askWords {
	if w, ok := webAskWords[kind]; ok {
		return w
	}
	return webAskWords[store.DecisionQuestion]
}

// askSubject is what decision id of kind is about, as {s} says it: an approval's plan or design, a review's or loop
// exit's step, "" for a kind whose words name none.
func (b webBuilder) askSubject(kind store.DecisionKind, id string) string {
	p := recordPlace(id)
	switch kind {
	case store.DecisionApproval:
		typ := b.stepType(p.Step)
		switch {
		case typ == "design" || (typ == "" && p.Step == "design"):
			return "the design" + sliceSuffix(p.Slice)
		case typ == "guildmaster-approval" || (typ == "" && strings.HasPrefix(p.Step, "accept")):
			return "the plan" + sliceSuffix(p.Slice)
		case typ == "":
			return "the work" + sliceSuffix(p.Slice)
		}
		return thePlace(b.stepWordsOf(p.Step).noun, p.Slice)
	case store.DecisionReview, store.DecisionLoopExit:
		return thePlace(b.stepWordsOf(p.Step).noun, p.Slice)
	}
	return ""
}

// sliceSuffix is " for slice k" for a slice after the first.
func sliceSuffix(k int) string {
	if k > 1 {
		return " for slice " + strconv.Itoa(k)
	}
	return ""
}

// fillAsk fills a decision template's subject.
func fillAsk(tmpl, subject string) string {
	return strings.NewReplacer("{S}", upperFirst(subject), "{s}", subject).Replace(tmpl)
}

// askHeadline is an open ask's headline on its card, the story's open sentence as a sentence, such as "The plan for
// slice 2 is ready for your approval.", from its kind and subject.
func (b webBuilder) askHeadline(a web.Ask) string {
	k := store.DecisionKind(a.Kind)
	return fillAsk(askWordsOf(k).open, b.askSubject(k, a.ID)) + "."
}

// askPhrase is an open ask's short phrase for the where-it-stands line and the Needs you index, from its kind and
// subject.
func (b webBuilder) askPhrase(a web.Ask) string {
	k := store.DecisionKind(a.Kind)
	return fillAsk(askWordsOf(k).phrase, b.askSubject(k, a.ID))
}

// decisionSentence is the story's title of a decision: open, answered with an option of its tone or in free text,
// or void.
func (b webBuilder) decisionSentence(dec timedDecision) string {
	w := askWordsOf(dec.Kind)
	s := b.askSubject(dec.Kind, dec.ID)
	switch {
	case dec.Void:
		return fillAsk(w.void, s)
	case dec.Open():
		return fillAsk(w.open, s)
	case dec.Choice != "":
		m, _ := web.OptionMeaning(string(dec.Kind), dec.Choice)
		if t, ok := w.tones[m.Tone]; ok {
			return fillAsk(t, s)
		}
	}
	return fillAsk(w.answered, s)
}

// decisionNoun is what the stepper's note calls a decision: "plan" for a plan approval, so accept-2 approved is "plan
// approved".
func (b webBuilder) decisionNoun(dec store.Decision) string {
	s := b.askSubject(dec.Kind, dec.ID)
	if s != "" {
		return strings.TrimPrefix(s, "the ")
	}
	if dec.Kind == "" {
		return "handoff"
	}
	return string(dec.Kind)
}

// webPortNames are the documents' names by output port or record part; a name of none of them is the step's place
// and the port's own word, such as "Build head".
var webPortNames = map[string]string{
	"spec": "Spec", "design": "Design", "mockup": "Mock-up", "outcome": "{Place}", "notes": "{Place} notes",
	"report": "{Place} report", "pr": "Pull request", "merge": "Merge record", "result": "{Place} result",
	"handoff": "Handoff", "answer": "{Place} answer", "rebase": "{Place} rebase", "escalated": "{Place} escalation",
	"checks": "{Place} checks", "revisions": "{Place} revisions",
}

// docLabel is the name the page shows for a document of this task, from its record name: its kind, slice and, when
// the task holds another of its kind or it is past the first, round, such as "Grill notes, round 3"; a name of no
// record form is shown as it is. rounds counts the task's documents of each step and part.
func (b webBuilder) docLabel(name string, rounds map[string]int) string {
	p, ok := interp.RecordPlace(name)
	if !ok {
		return name
	}
	noun := b.stepWordsOf(p.Step).noun
	if p.Part == "send-back" {
		return "Feedback to " + thePlace(noun, p.Slice) + ", round " + strconv.Itoa(p.Round)
	}
	tmpl, known := webPortNames[p.Part]
	if !known {
		tmpl = "{Place} " + strings.ReplaceAll(p.Part, "-", " ")
	}
	if p.Part == "design" && p.Index == 2 {
		tmpl = "Mock-up"
	}
	label := strings.NewReplacer("{Place}", upperFirst(noun)).Replace(tmpl)
	if p.Slice > 1 {
		label = "Slice " + strconv.Itoa(p.Slice) + " " + lowerFirst(label)
	}
	// A port's later output counts from 2 and a sequence part from 1; a design's second output is its mock-up.
	if p.Index > 0 && (p.Part != "design" || p.Index != 2) {
		label += " " + strconv.Itoa(p.Index)
	}
	if p.Round > 0 && (p.Round > 1 || rounds[docRoundKey(p)] > 1) {
		label += ", round " + strconv.Itoa(p.Round)
	}
	return label
}

// docRoundKey groups documents that differ only by round.
func docRoundKey(p interp.Place) string {
	return p.Step + "/" + strconv.Itoa(p.Slice) + "/" + p.Part + "/" + strconv.Itoa(p.Index)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func lowerFirst(s string) string {
	if s == "" || strings.HasPrefix(s, "PR") {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// webJoin joins phrases as a sentence lists them: "a", "a, and b", "a, b, and c".
func webJoin(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
}

// where is the task's where-it-stands sentence: who it waits on, in bold, and what for, built from its open asks,
// the grill summary waiting on the Guildmaster (b.summary), a Director stage, its wait and its running attempt. Clerk's stands phrase is the fallback for a wait of no sentence.
func (b webBuilder) where(asks []web.Ask, stage string, waits []string) web.Where {
	v := b.ct.view
	switch v.State {
	case store.TaskCancelled:
		if r := oneLine(v.CancelReason); r != "" {
			return web.Where{Lead: "Cancelled:", Rest: r + "."}
		}
		return web.Where{Lead: "Cancelled."}
	case store.TaskDone:
		if b.ct.merging {
			return web.Where{Lead: "Done:", Rest: "the work is merged."}
		}
		return web.Where{Lead: "Done."}
	}
	var phrases []string
	deferred := 0
	for _, a := range asks {
		if a.Deferred {
			deferred++
			continue
		}
		phrases = append(phrases, b.askPhrase(a))
	}
	if b.summary != nil {
		// The grill summary waits on the Guildmaster as an ask does.
		if len(phrases) == 0 {
			return web.Where{Lead: "Waiting for you on the grill summary:", Rest: "confirm it or request changes on the task page.", You: true}
		}
		phrases = append(phrases, "the grill summary")
	}
	if n := len(phrases); n > 0 {
		return web.Where{Lead: "Waiting for you on " + webCount(n, "ask") + ":", Rest: webJoin(phrases) + ".", You: true}
	}
	if stage != "" {
		return web.Where{Lead: "Waiting for you in the Director's session:", Rest: thePlace(b.stepWordsOf(stage).noun, 1) + " runs there with you.", You: true}
	}
	if deferred > 0 {
		return web.Where{Lead: "Waiting for you, later:", Rest: webCount(deferred, "deferred ask") + ", held until other tasks merge.", You: true}
	}
	n, ok := b.next()
	if !ok {
		return web.Where{Lead: "Not started", Rest: "yet."}
	}
	base, k := workflow.ParseSliceStep(n.Step)
	place := thePlace(b.stepWordsOf(base).noun, k)
	stands := b.d.stands(b.ct)
	switch interp.Wait(n.Wait) {
	case interp.WaitAttempt:
		verb := "is starting"
		if strings.HasSuffix(stands, " running") {
			verb = "is running"
		}
		return web.Where{Lead: "Moving:", Rest: place + " " + verb + " (attempt " + strconv.Itoa(n.Attempt) + ")."}
	case interp.WaitDecision, interp.WaitDirector, interp.WaitDirectorStage:
		rest := "it has the next move."
		if len(waits) > 0 {
			rest = waits[0] + "."
		}
		return web.Where{Lead: "Waiting on the Director:", Rest: rest}
	case interp.WaitChecks:
		batch := ""
		if e := b.ct.view.MergeQueue; e != nil && e.Batch != nil {
			// The front leading a merge batch.
			batch = ", " + e.Batch.String()
		}
		if b.ct.pr != nil {
			return web.Where{Lead: "Moving:", Rest: "CI is running on PR #" + strconv.Itoa(b.ct.pr.Number) + batch + "."}
		}
		return web.Where{Lead: "Moving:", Rest: "CI is running" + batch + "."}
	case interp.WaitMergeQueue:
		return web.Where{Lead: "Moving:", Rest: "the work is " + stands + "."}
	case interp.WaitQueued:
		return web.Where{Lead: "Waiting for a free slot:", Rest: place + " is " + stands + "."}
	case interp.WaitPaused:
		return web.Where{Lead: "Paused:", Rest: "nothing runs until the Director resumes it."}
	case interp.WaitDependencies:
		if on, ok := strings.CutPrefix(stands, "waiting on "); ok && on != "its dependencies" {
			return web.Where{Lead: "Waiting on other tasks:", Rest: on + " must finish first."}
		}
		return web.Where{Lead: "Waiting on other tasks:", Rest: "its dependencies must finish first."}
	case interp.WaitDone:
		return web.Where{Lead: "Done."}
	}
	return web.Where{Lead: "Waiting:", Rest: stands + "."}
}

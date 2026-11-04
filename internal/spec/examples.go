package spec

// Example is one strict section as the prompts teach it: the rule, a valid section its parser accepts, and an invalid
// section it refuses with exactly Error. The parser tests run every example, and the rendered prompts, stage guides
// and skills quote them, so what an Artisan is shown is what Guildhall does.
type Example struct {
	// Heading is the section's heading, such as OpenQuestionsHeading.
	Heading string
	// Rule is the section's shape and the shapes it refuses, in one paragraph.
	Rule string
	// Valid is a section, heading included, that its parser accepts.
	Valid string
	// Invalid is a section, heading included, that its parser refuses.
	Invalid string
	// Error is the error Invalid gets, as a document whose first line is its heading; intake's rejection starts
	// with it.
	Error string
}

// The open-questions section of a spec or design (SpecQuestions).
const (
	// OpenQuestionsRule is the shape SpecQuestions accepts and the shapes it refuses most often.
	OpenQuestionsRule = "Write exactly \"None\", or questions each starting on a line `**Qn. <title>**`, numbered from Q1 with no gaps, " +
		"each with an `Options: (a) …; (b) ….` line and a `Recommended: (<letter>). <why>` line whose letter is one of its options. " +
		QuestionsRefused
	// QuestionsRefused names the shapes an open-questions section refuses before its first question.
	QuestionsRefused = "Nothing else may come before the first `**Q1.` line: no sentence, no `### Q1.` heading, no list marker such as `- **Q1.`, and no explanation after \"None\"."
	// OpenQuestionsValid is a well-formed open-questions section.
	OpenQuestionsValid = "## Open questions\n\n" +
		"**Q1. Where does the Artisan write its draft?**\n" +
		"A rejected draft must never be published.\n" +
		"Options: (a) a draft file that only submit copies; (b) the result file itself.\n" +
		"Recommended: (a). Only an accepted draft is then published."
	// OpenQuestionsInvalid is OpenQuestionsValid with a sentence before Q1, which SpecQuestions refuses.
	OpenQuestionsInvalid = "## Open questions\n\n" +
		"One question is still open.\n\n" +
		"**Q1. Where does the Artisan write its draft?**\n" +
		"Options: (a) a draft file that only submit copies; (b) the result file itself.\n" +
		"Recommended: (a). Only an accepted draft is then published."
	// OpenQuestionsError is the error OpenQuestionsInvalid gets.
	OpenQuestionsError = `open questions malformed: ## Open questions line 3 is neither "None" nor a **Qn. title** question`
)

// The slice list of a multi-PR spec (Slices).
const (
	// SlicesRule is the shape Slices accepts and the shapes it refuses.
	SlicesRule = "Write a numbered list `1.`, `2.`, … with no gaps, each entry starting with a bold title, then what that pull request delivers and its expected size, and no two titles alike. " +
		"No text may come before entry 1, and an entry without a `**<title>**` is refused."
	// SlicesValid is a well-formed slice list.
	SlicesValid = "## Slices\n\n" +
		"1. **Result check command**: adds the check and submit commands; about 700 lines in 12 files.\n" +
		"2. **Prompts submit**: the prompts tell the Artisan to submit; about 400 lines in 13 files."
	// SlicesInvalid is SlicesValid with its first entry's title not bold, which Slices refuses.
	SlicesInvalid = "## Slices\n\n" +
		"1. Result check command: adds the check and submit commands; about 700 lines in 12 files.\n" +
		"2. **Prompts submit**: the prompts tell the Artisan to submit; about 400 lines in 13 files."
	// SlicesError is the error SlicesInvalid gets.
	SlicesError = "spec slices refused: slice 1 (line 3) has no bold title: start it with **<title>**"
)

// The Summary block of a spec a Guildmaster approves (CheckSummary).
const (
	// SummaryRule is the shape CheckSummary accepts and the shape it refuses most often.
	SummaryRule = "Write its parts as list items, Goal, Size, Slices, Changed since the grill and Open questions, in at most 4 KiB. " +
		"Never write them as `##` headings: the next `##` heading ends the block, so a block that starts with one is empty and refused."
	// SummaryValid is a well-formed Summary block.
	SummaryValid = "## Summary\n\n" +
		"- **Goal:** An Artisan fixes a rejected result in the same attempt.\n" +
		"- **Size:** 2 slices, about 1,100 lines in 25 files.\n" +
		"- **Slices:** (1) Result check command; (2) Prompts submit.\n" +
		"- **Changed since the grill:** Nothing; the plan follows the grill's agreed change.\n" +
		"- **Open questions:** None."
	// SummaryInvalid is a Summary block written as headings, which CheckSummary refuses.
	SummaryInvalid = "## Summary\n\n" +
		"## Goal\n\n" +
		"An Artisan fixes a rejected result in the same attempt."
	// SummaryError is the error SummaryInvalid gets.
	SummaryError = "spec has no summary: the spec has no ## Summary block, or it is empty or over 4096 bytes"
)

// The questions of a hallmark report (ReportQuestions).
const (
	// ReportQuestionsRule is the shape ReportQuestions accepts and the shapes it refuses.
	ReportQuestionsRule = "Write exactly \"None\", or questions each starting on a line `**Qn. <title>**`, numbered from Q1 with no gaps, " +
		"each with a `Recommended:` line and an `Alternative:` line. " + QuestionsRefused
	// ReportQuestionsValid is a well-formed hallmark questions section.
	ReportQuestionsValid = "## Questions\n\n" +
		"**Q1. Split intake out of the engine?**\n" +
		"Recommended: split it into its own file, because every intake change touches the engine.\n" +
		"Alternative: leave it, and pay a larger diff in each intake change."
	// ReportQuestionsInvalid is ReportQuestionsValid with a list marker before Q1, which ReportQuestions refuses.
	ReportQuestionsInvalid = "## Questions\n\n" +
		"- **Q1. Split intake out of the engine?**\n" +
		"Recommended: split it into its own file, because every intake change touches the engine.\n" +
		"Alternative: leave it, and pay a larger diff in each intake change."
	// ReportQuestionsError is the error ReportQuestionsInvalid gets.
	ReportQuestionsError = `open questions malformed: ## Questions line 3 is neither "None" nor a **Qn. title** question`
)

// The strict sections' examples, one per section.
var (
	OpenQuestionsExample   = Example{OpenQuestionsHeading, OpenQuestionsRule, OpenQuestionsValid, OpenQuestionsInvalid, OpenQuestionsError}
	SlicesExample          = Example{SlicesHeading, SlicesRule, SlicesValid, SlicesInvalid, SlicesError}
	SummaryExample         = Example{SummaryHeading, SummaryRule, SummaryValid, SummaryInvalid, SummaryError}
	ReportQuestionsExample = Example{ReportQuestionsHeading, ReportQuestionsRule, ReportQuestionsValid, ReportQuestionsInvalid, ReportQuestionsError}
)

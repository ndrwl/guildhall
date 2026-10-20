package workflow

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// withSettings returns the small workflow with extra settings on one step.
func withSettings(step, settings string) string {
	def, err := Parse([]byte(valid))
	if err != nil {
		panic(err)
	}
	s := &def.Steps[def.stepIndex(step)]
	if err := json.Unmarshal([]byte(settings), &s.Settings); err != nil {
		panic(err)
	}
	data, err := json.Marshal(def)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func TestProfileAndSkillsAcceptedOnArtisanKinds(t *testing.T) {
	for _, step := range []string{"plan", "build", "review"} {
		def, err := load(t, withSettings(step, `{"profile": "reviewer-astra", "skills": ["go-tdd", "guildhall-contributing"]}`))
		if err != nil {
			t.Fatalf("%s: profile and skills refused: %v", step, err)
		}
		s := def.Steps[def.stepIndex(step)]
		if s.Profile() != "reviewer-astra" {
			t.Fatalf("%s: Profile() = %q", step, s.Profile())
		}
		if got := s.Skills(); !slices.Equal(got, []string{"go-tdd", "guildhall-contributing"}) {
			t.Fatalf("%s: Skills() = %q", step, got)
		}
	}
}

func TestProfileAndSkillsSurviveHumanReviewOverride(t *testing.T) {
	def, err := load(t, withSettings("review", `{"profile": "reviewer-opus", "skills": ["go-tdd"]}`))
	if err != nil {
		t.Fatal(err)
	}
	swapped, err := ApplyOverrides(def, map[string]Override{"review": {Actor: GuildmasterActor}})
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(swapped); err != nil {
		t.Fatalf("override of a review step with a profile refused: %v", err)
	}
}

func TestProfileRefusedOnKindsThatDoNotDeclareIt(t *testing.T) {
	for _, step := range []string{"accept", "deliver"} {
		for _, name := range []string{"profile", "skills"} {
			value := `"reviewer"`
			if name == "skills" {
				value = `["go-tdd"]`
			}
			_, err := load(t, withSettings(step, `{"`+name+`": `+value+`}`))
			if err == nil || !strings.Contains(err.Error(), `unknown setting "`+name+`"`) {
				t.Fatalf("%s %s: err = %v", step, name, err)
			}
		}
	}
}

func TestProfileAndSkillsMustBeNames(t *testing.T) {
	cases := map[string]string{
		`{"profile": 3}`:                    `setting "profile" must be a name`,
		`{"profile": "Reviewer"}`:           `setting "profile" must be a name`,
		`{"profile": ""}`:                   `setting "profile" must be a name`,
		`{"skills": "go-tdd"}`:              `setting "skills" must be a list of names`,
		`{"skills": ["go-tdd", 1]}`:         `setting "skills" must be a list of names`,
		`{"skills": ["../escape"]}`:         `setting "skills" must be a list of names`,
		`{"skills": ["go-tdd", "go-tdd"]}`:  `setting "skills" names "go-tdd" twice`,
		`{"skills": ["a/b"]}`:               `setting "skills" must be a list of names`,
		`{"skills": ["go-tdd"], "x": true}`: `unknown setting "x"`,
	}
	for settings, want := range cases {
		_, err := load(t, withSettings("build", settings))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", settings, err, want)
		}
	}
}

func TestStepWithoutSettingsHasNoProfileOrSkills(t *testing.T) {
	def, err := load(t, valid)
	if err != nil {
		t.Fatal(err)
	}
	s := def.Steps[def.stepIndex("build")]
	if s.Profile() != "" || s.Skills() != nil || s.Instructions() != "" {
		t.Fatalf("unset settings read as %q %q %q", s.Profile(), s.Skills(), s.Instructions())
	}
}

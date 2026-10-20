package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStepActor(t *testing.T) {
	def, err := load(t, valid)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Actor{"plan": ArtisanActor, "accept": GuildmasterActor, "build": ArtisanActor, "review": ArtisanActor, "deliver": EngineActor}
	for id, actor := range want {
		if got := def.Steps[def.stepIndex(id)].Actor(); got != actor {
			t.Errorf("%s: Actor() = %q, want %q", id, got, actor)
		}
	}
	for _, s := range []Step{
		{ID: "x", Type: "shell", Version: 1},
		{ID: "x", Type: "review", Version: 1},
		{ID: "x", Type: "review", Version: 1, Settings: map[string]json.RawMessage{"actor": json.RawMessage(`5`)}},
	} {
		if got := s.Actor(); got != "" {
			t.Errorf("%+v: Actor() = %q, want empty", s, got)
		}
	}
}

func TestActorSetting(t *testing.T) {
	cases := []struct {
		name, old, new, want string
	}{
		{"missing on a review", `"settings": {"actor": "artisan"}`, `"settings": {}`, `required setting "actor" is missing`},
		{"actor not allowed", `"actor": "artisan"`, `"actor": "engine"`, `review does not allow actor "engine"`},
		{"unknown actor", `"actor": "artisan"`, `"actor": "robot"`, `review does not allow actor "robot"`},
		{"not a string", `"actor": "artisan"`, `"actor": true`, `setting "actor" must be a string`},
		{"on a single-actor kind", `{"id": "build", "type": "build", "version": 1,`, `{"id": "build", "type": "build", "version": 1, "settings": {"actor": "artisan"},`, `build always runs as artisan`},
		{"old human-review kind", `"type": "review", "version": 1, "settings": {"actor": "artisan"}`, `"type": "human-review", "version": 1`, `unknown step type "human-review"`},
		{"old automated-review kind", `"type": "review", "version": 1, "settings": {"actor": "artisan"}`, `"type": "automated-review", "version": 1`, `unknown step type "automated-review"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(valid, c.old) {
				t.Fatalf("fixture does not contain %q", c.old)
			}
			_, err := load(t, strings.Replace(valid, c.old, c.new, 1))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v does not mention %q", err, c.want)
			}
		})
	}
	guildmaster := strings.Replace(valid, `"actor": "artisan"`, `"actor": "guildmaster"`, 1)
	def, err := load(t, guildmaster)
	if err != nil {
		t.Fatalf("Guildmaster review refused: %v", err)
	}
	if got := def.Steps[def.stepIndex("review")].Actor(); got != GuildmasterActor {
		t.Fatalf("Actor() = %q", got)
	}
}

// reviewed is the small workflow with a design review before acceptance and a publish before delivery.
const reviewed = `{
  "schema": "guildhall.workflow/1",
  "name": "reviewed",
  "steps": [
    {"id": "plan", "type": "plan", "version": 1},
    {"id": "design-review", "type": "document-review", "version": 1, "settings": {"actor": "artisan"},
     "inputs": {"subject": ["plan.spec"]},
     "on": {"changes_requested": {"goto": "plan", "max": 2}}},
    {"id": "accept", "type": "guildmaster-approval", "version": 1,
     "inputs": {"spec": ["plan.spec"], "reviews": ["design-review.outcome"]}},
    {"id": "build", "type": "build", "version": 1, "inputs": {"spec": ["accept.spec"]}},
    {"id": "review", "type": "review", "version": 1, "settings": {"actor": "guildmaster"},
     "inputs": {"spec": ["accept.spec"], "head": ["build.head"]}},
    {"id": "publish", "type": "publish", "version": 1,
     "inputs": {"head": ["build.head"], "review": ["review.outcome"]}},
    {"id": "validate", "type": "validate", "version": 1, "inputs": {"head": ["publish.head"]}},
    {"id": "deliver", "type": "deliver", "version": 1,
     "inputs": {"head": ["publish.head"], "review": ["review.outcome"], "validation": ["validate.result"]}}
  ]
}`

func TestDocumentReviewAndPublish(t *testing.T) {
	def, err := load(t, reviewed)
	if err != nil {
		t.Fatalf("workflow with document-review and publish refused: %v", err)
	}
	if got := def.Steps[def.stepIndex("publish")].Actor(); got != EngineActor {
		t.Fatalf("publish actor = %q", got)
	}
	cases := []struct {
		name, old, new, want string
	}{
		{"document review of a code head", `"subject": ["plan.spec"]`, `"subject": ["build.head"]`, `must come from an earlier step`},
		{"document review needs a subject", `"inputs": {"subject": ["plan.spec"]},`, ``, `required input "subject" is not wired`},
		{"document review actor", `"type": "document-review", "version": 1, "settings": {"actor": "artisan"}`, `"type": "document-review", "version": 1, "settings": {"actor": "director"}`, `document-review does not allow actor "director"`},
		{"publish takes no settings", `{"id": "publish", "type": "publish", "version": 1,`, `{"id": "publish", "type": "publish", "version": 1, "settings": {"remote": "origin"},`, `unknown setting "remote"`},
		{"publish has no loop", `"inputs": {"head": ["build.head"], "review": ["review.outcome"]}}`, `"inputs": {"head": ["build.head"], "review": ["review.outcome"]}, "on": {"failed": {"goto": "build", "max": 1}}}`, `outcome "failed" cannot carry a rule for publish`},
		{"publish needs a head", `"inputs": {"head": ["build.head"], "review": ["review.outcome"]}}`, `"inputs": {"review": ["review.outcome"]}}`, `required input "head" is not wired`},
		{"approval reviews are outcomes", `"reviews": ["design-review.outcome"]`, `"reviews": ["plan.spec"]`, `wants review-outcome`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(reviewed, c.old) {
				t.Fatalf("fixture does not contain %q", c.old)
			}
			_, err := load(t, strings.Replace(reviewed, c.old, c.new, 1))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v does not mention %q", err, c.want)
			}
		})
	}
}

func TestActorOverride(t *testing.T) {
	def, err := load(t, strings.Replace(valid, `"settings": {"actor": "artisan"}`, `"settings": {"actor": "artisan", "instructions": "Check the spec."}`, 1))
	if err != nil {
		t.Fatal(err)
	}
	review := def.stepIndex("review")
	swapped, err := ApplyOverrides(def, map[string]Override{"review": {Actor: GuildmasterActor}})
	if err != nil {
		t.Fatalf("actor swap refused: %v", err)
	}
	if err := Validate(swapped); err != nil {
		t.Fatalf("swapped workflow invalid: %v", err)
	}
	got := swapped.Steps[review]
	if got.Actor() != GuildmasterActor || got.Type != "review" || got.Version != 1 {
		t.Fatalf("swapped step = %s@%d as %q", got.Type, got.Version, got.Actor())
	}
	if string(got.Settings["instructions"]) != `"Check the spec."` {
		t.Fatalf("override dropped other settings: %s", got.Settings)
	}
	if def.Steps[review].Actor() != ArtisanActor {
		t.Fatal("override mutated the original definition")
	}
	if swapped.Digest() == def.Digest() {
		t.Fatal("override kept digest")
	}
	back, err := ApplyOverrides(swapped, map[string]Override{"review": {Actor: ArtisanActor}})
	if err != nil || back.Digest() != def.Digest() {
		t.Fatalf("swapping back did not restore the digest: %v", err)
	}
	same, err := ApplyOverrides(def, nil)
	if err != nil || same.Digest() != def.Digest() {
		t.Fatalf("empty override changed workflow: %v", err)
	}
	doc, err := load(t, reviewed)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := ApplyOverrides(doc, map[string]Override{"design-review": {Actor: GuildmasterActor}}); err != nil || out.Steps[doc.stepIndex("design-review")].Actor() != GuildmasterActor {
		t.Fatalf("document-review actor swap: %v", err)
	}

	refusals := []struct {
		name string
		ov   map[string]Override
		want string
	}{
		{"unknown step", map[string]Override{"nope": {Actor: GuildmasterActor}}, `unknown step "nope"`},
		{"single-actor step", map[string]Override{"build": {Actor: GuildmasterActor}}, `build always runs as artisan`},
		{"actor not allowed", map[string]Override{"review": {Actor: EngineActor}}, `review does not allow actor "engine"`},
		{"empty actor", map[string]Override{"review": {}}, `review does not allow actor ""`},
	}
	for _, c := range refusals {
		t.Run(c.name, func(t *testing.T) {
			_, err := ApplyOverrides(def, c.ov)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v does not mention %q", err, c.want)
			}
		})
	}
}

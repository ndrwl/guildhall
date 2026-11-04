package setup_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/setup"
)

// signin is a human step file whose run command writes the secret from its environment to a file the check reads,
// and a second whose run command fails printing the secret.
const signin = `schema = "guildhall.steps/1"
name = "signin-demo"
title = "Sign-in demo"

[params]
tool = ""

[[step]]
id = "token"
title = "Give the token"
tag = "human"
os = ["linux", "darwin", "windows"]
check = ["${tool}", "exists", "${home}/token"]
instructions = "Paste the token when asked."
run = ["${tool}", "envfile", "SETUP_TOKEN", "${home}/token"]
undo = "Remove ~/token."

[[step.prompt]]
kind = "confirm"
text = "Ready?"

[[step.prompt]]
kind = "ask"
name = "account"
text = "Which account?"

[[step.prompt]]
kind = "ask_secret"
name = "token"
text = "Token"

[[step]]
id = "leaky"
title = "A run that prints the secret"
tag = "human"
os = ["linux", "darwin", "windows"]
check = ["${tool}", "exists", "${home}/leaky"]
run = ["${tool}", "printenv", "SETUP_PASSWORD"]
undo = "Sign out of the leaky service."

[[step.prompt]]
kind = "ask_secret"
name = "password"
text = "Password"
`

func TestWizardKeepsNoSecret(t *testing.T) {
	sb := newSandbox(t)
	s := parse(t, signin)
	const secret, leaked = "s3cr3t-token-value", "hunter2-password"
	p := &scripted{confirms: []bool{true}, answers: []string{"guildmaster"}, secrets: []string{secret, leaked}}
	rec, err := setup.Wizard(context.Background(), s, sb.facts, setup.WizardOptions{}, p, &setup.FileJournal{Dir: sb.setupDir})
	var se *setup.StepError
	if !errors.As(err, &se) || se.Step != "leaky" {
		t.Fatalf("Wizard = %v, want the leaky step to fail", err)
	}
	if data, _ := os.ReadFile(filepath.Join(sb.home, "token")); string(data) != secret {
		t.Errorf("the run command did not get the secret: %q", data)
	}
	if rec.Status != "failed" || len(rec.Results) != 2 || rec.Results[0].Status != "done" || rec.Results[0].Answers["account"] != "guildmaster" {
		t.Errorf("record = %+v", rec)
	}
	if rec.Results[0].Secrets[0] != "token" {
		t.Errorf("record secrets = %v, want the name token", rec.Results[0].Secrets)
	}
	for path, data := range tree(t, sb.setupDir) {
		for _, v := range []string{secret, leaked} {
			if strings.Contains(data, v) {
				t.Errorf("%s holds a secret value", path)
			}
		}
	}
	if strings.Contains(err.Error(), leaked) {
		t.Errorf("error holds the secret: %v", err)
	}
	got, err := setup.ReadRecord(setup.RecordPath(sb.setupDir, s.Name, s.Digest))
	if err != nil || got.Digest != s.Digest || got.Status != "failed" || strings.Join(got.StepIDs, ",") != "token,leaky" {
		t.Errorf("ReadRecord = %+v, %v", got, err)
	}
}

func TestWizardShowsUndoOnFailure(t *testing.T) {
	sb := newSandbox(t)
	s := demoFor(t)
	p := &scripted{confirms: []bool{true}}
	rec, err := setup.Wizard(context.Background(), s, sb.facts, setup.WizardOptions{IDs: []string{"person"}}, p, nil)
	if err == nil {
		t.Fatal("a human step confirmed but not done passed")
	}
	shown := p.shown.String()
	if !strings.Contains(shown, "Step 1 of 1 · A person's step [human]") || !strings.Contains(shown, "Create ~/person by hand.") {
		t.Errorf("no numbered card:\n%s", shown)
	}
	if !strings.Contains(shown, "Step 1 failed: ") || !strings.Contains(shown, "Undo: Remove ~/person.") {
		t.Errorf("no undo note on failure:\n%s", shown)
	}
	if rec.Status != "failed" || rec.Results[0].Status != "failed" {
		t.Errorf("record = %+v", rec)
	}
	p = &scripted{confirms: []bool{false}}
	if _, err := setup.Wizard(context.Background(), s, sb.facts, setup.WizardOptions{IDs: []string{"person"}}, p, nil); !errors.Is(err, setup.ErrDeclined) {
		t.Errorf("declined confirm = %v, want ErrDeclined", err)
	}
}

func TestWizardRunsFixableAndSkipsDone(t *testing.T) {
	sb := newSandbox(t)
	s := demoFor(t)
	if err := os.WriteFile(filepath.Join(sb.home, "person"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := &scripted{}
	rec, err := setup.Wizard(context.Background(), s, sb.facts, setup.WizardOptions{}, p, &setup.FileJournal{Dir: sb.setupDir})
	if err != nil {
		t.Fatalf("Wizard: %v\n%s", err, p.shown.String())
	}
	var statuses []string
	for _, r := range rec.Results {
		statuses = append(statuses, r.ID+"="+r.Status)
	}
	if got := strings.Join(statuses, " "); got != "ready=already-done alpha=done beta=done person=already-done" {
		t.Errorf("results = %s", got)
	}
	journal, err := os.ReadFile(filepath.Join(sb.setupDir, "journal.jsonl"))
	if err != nil || !bytes.Contains(journal, []byte(`"step":"alpha","event":"done"`)) {
		t.Errorf("journal = %s, %v", journal, err)
	}
}

func TestWizardDryRunChangesNothing(t *testing.T) {
	sb := newSandbox(t)
	s := parse(t, signin)
	before := tree(t, sb.home)
	p := &scripted{}
	rec, err := setup.Wizard(context.Background(), s, sb.facts, setup.WizardOptions{DryRun: true}, p, &setup.FileJournal{Dir: sb.setupDir})
	if err != nil {
		t.Fatal(err)
	}
	if after := tree(t, sb.home); len(after) != len(before) {
		t.Errorf("dry run wrote files: %v", after)
	}
	if _, err := os.Stat(sb.setupDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dry run made the setup directory: %v", err)
	}
	shown := p.shown.String()
	for _, want := range []string{"dry run", "Step 1 of 2", "would ask (ask_secret): Token", "would run: ", "undo: Remove ~/token."} {
		if !strings.Contains(shown, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, shown)
		}
	}
	if len(rec.Results) != 0 {
		t.Errorf("dry run results = %+v", rec.Results)
	}
}

func TestTermPrompter(t *testing.T) {
	var out bytes.Buffer
	p := &setup.TermPrompter{In: strings.NewReader("yes\nalice\nsecret\nn\n"), Out: &out}
	if ok, err := p.Confirm("Go?"); !ok || err != nil {
		t.Errorf("Confirm = %v, %v", ok, err)
	}
	if v, err := p.Ask("Name"); v != "alice" || err != nil {
		t.Errorf("Ask = %q, %v", v, err)
	}
	if v, err := p.AskSecret("Token"); v != "secret" || err != nil {
		t.Errorf("AskSecret = %q, %v", v, err)
	}
	if ok, err := p.Confirm("Again?"); ok || err != nil {
		t.Errorf("Confirm(n) = %v, %v", ok, err)
	}
	if strings.Contains(out.String(), "secret\n") {
		t.Errorf("a secret was echoed: %q", out.String())
	}
	if _, err := p.Ask("More"); err == nil {
		t.Error("Ask at end of input succeeded")
	}
}

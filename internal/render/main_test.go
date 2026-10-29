package render

import (
	"os"
	"strings"
	"testing"
)

func mainPrompt(t *testing.T, retry *Retry, main *Main) string {
	t.Helper()
	stage := attempt(t, step(t, "build", "build", `{"profile": "builder"}`))
	stage.Retry, stage.Main = retry, main
	r, err := Prompt(stage, nil, baseline("build"), roots())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(r.PromptPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// mainSectionOf is the behind-main section of prompt, checked to sit after the retry section and before the role.
func mainSectionOf(t *testing.T, prompt string) string {
	t.Helper()
	at, role := strings.Index(prompt, "\n## Behind main\n"), strings.Index(prompt, "\n## Role\n")
	if at < 0 {
		t.Fatalf("no behind-main section:\n%s", prompt)
	}
	if retry := strings.Index(prompt, "\n## Why this is a retry\n"); retry < 0 || retry > at || role < at {
		t.Fatalf("behind-main section is not between the retry section and the role:\n%s", prompt)
	}
	return prompt[at:role]
}

func TestMainSectionAbsentWhenCurrent(t *testing.T) {
	for _, main := range []*Main{nil, {}} {
		if prompt := mainPrompt(t, nil, main); strings.Contains(prompt, "Behind main") {
			t.Errorf("a prompt with main %+v has a behind-main section:\n%s", main, prompt)
		}
	}
}

func TestMainSectionCountsAndAdvisesRebase(t *testing.T) {
	retry := &Retry{Attempt: 1, Outcome: "failed"}
	one := mainSectionOf(t, mainPrompt(t, retry, &Main{Behind: 1}))
	if !strings.Contains(one, "is 1 commit behind the default branch") {
		t.Errorf("section = %q, want one commit behind", one)
	}
	if !strings.Contains(one, "run `git fetch` and then `git rebase origin/main`; never use `git merge`") {
		t.Errorf("section = %q, want git fetch, git rebase and never git merge", one)
	}
	if strings.Contains(one, "not moved") {
		t.Errorf("section = %q says a branch with its own commits was kept", one)
	}
	kept := mainSectionOf(t, mainPrompt(t, retry, &Main{Behind: 3, Kept: "untracked `x.txt` would be overwritten"}))
	if !strings.Contains(kept, "3 commits behind") || !strings.Contains(kept, "was not moved to the current main, because:\n\n```text\nuntracked `x.txt` would be overwritten\n```\n") {
		t.Errorf("section = %q, want the count and the fenced reason", kept)
	}
}

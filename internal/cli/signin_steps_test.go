package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/setup"
)

// TestSigninStepsMatchRuntimes ties the sign-in built-in step file to signinRuntimes, so the setup doctor and
// guildhall signin check the same thing and tell the person the same commands (H85 of d209). It compares
// structured pieces, not whole text: the act lines and the instructions are written in different shapes.
func TestSigninStepsMatchRuntimes(t *testing.T) {
	steps, err := setup.LoadBuiltin("sign-in", "")
	if err != nil {
		t.Fatal(err)
	}
	var runtimeIDs, stepIDs []string
	for _, r := range signinRuntimes() {
		runtimeIDs = append(runtimeIDs, r.name+"-sign-in")
	}
	// The Linux forms are compared whole; a Windows form of its own (gh-sign-in's, as Windows has no env program)
	// must set the same variables and run the same status command.
	var linux []setup.Step
	for _, s := range steps.Steps {
		if s.ForOS("linux") {
			linux = append(linux, s)
			stepIDs = append(stepIDs, s.ID)
		}
	}
	for _, r := range signinRuntimes() {
		w, ok := steps.Step(r.name+"-sign-in", "windows")
		if !ok || !w.ForOS("windows") {
			t.Errorf("%s-sign-in has no Windows form", r.name)
			continue
		}
		if w.Check.Script == "" {
			continue
		}
		for _, kv := range r.Env {
			k, v, _ := strings.Cut(kv, "=")
			if !strings.Contains(w.Check.Script, "$env:"+k+" = '"+v+"'") {
				t.Errorf("%s-sign-in's Windows check does not set %s", r.name, kv)
			}
		}
		if status := strings.Join(r.Status, " "); !strings.Contains(w.Check.Script, " "+status+"\n") {
			t.Errorf("%s-sign-in's Windows check does not run %s %s", r.name, r.Program, status)
		}
	}
	if !slices.Equal(runtimeIDs, stepIDs) {
		t.Fatalf("sign-in step ids = %q, want one <runtime>-sign-in per signinRuntimes entry, in order: %q", stepIDs, runtimeIDs)
	}

	for i, r := range signinRuntimes() {
		s := linux[i]
		t.Run(r.name, func(t *testing.T) {
			var want []string
			if len(r.Env) > 0 {
				want = append(append([]string{"env"}, r.Env...), r.Program)
			} else {
				want = []string{r.Program}
			}
			want = append(want, r.Status...)
			if !slices.Equal(s.Check.Argv, want) {
				t.Errorf("check = %q, want %q", s.Check.Argv, want)
			}

			instrLines := strings.Split(s.Instructions, "\n")
			var helpLine string
			for _, l := range instrLines {
				if u, ok := strings.CutPrefix(l, "Help: "); ok {
					helpLine = strings.TrimSpace(u)
				}
			}
			sawHelp := false
			for _, act := range r.Act {
				switch {
				case strings.HasPrefix(act, "help: "):
					sawHelp = true
					if u := strings.TrimPrefix(act, "help: "); u != helpLine {
						t.Errorf("instructions Help: %q, want %q", helpLine, u)
					}
				case strings.HasPrefix(act, "run: "), strings.HasPrefix(act, "then: "):
					_, cmd, _ := strings.Cut(act, ": ")
					cmd, _, _ = strings.Cut(cmd, ",")
					cmd = strings.TrimSpace(cmd)
					if !hasIndentedLine(instrLines, cmd) {
						t.Errorf("instructions lack the indented command %q", cmd)
					}
				case strings.HasPrefix(act, "never"):
					if !strings.Contains(strings.ToLower(s.Instructions), strings.ToLower(act)) {
						t.Errorf("instructions lack %q", act)
					}
				}
			}
			if !sawHelp {
				t.Errorf("runtime %s has no help: act line", r.name)
			}
			if r.name == "codex" && !strings.Contains(s.Instructions, "a workspace admin enables it for a workspace") {
				t.Errorf("instructions lack the workspace-admin clause")
			}
		})
	}
}

// hasIndentedLine reports whether lines holds an indented line that is exactly text once trimmed.
func hasIndentedLine(lines []string, text string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, " ") && strings.TrimSpace(l) == text {
			return true
		}
	}
	return false
}

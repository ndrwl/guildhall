package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/strictjson"
	"github.com/ndrwl/guildhall/internal/workflow"
)

func (a *App) renderPrompt(args []string) int {
	if len(args) == 1 && args[0] == "--help" {
		_, err := fmt.Fprintln(a.Stdout, "Usage: guildhall render <request.json>\nRender an Artisan prompt; request fields: Stage, Inputs, Baseline, Managed, Director.\nExample: guildhall render attempt.json\nWrites the prompt manifest as JSON to stdout.")
		if err != nil {
			return 1
		}
		return 0
	}
	if len(args) != 1 {
		return a.usageError("render requires <request.json>; use render --help")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return a.fail(err)
	}
	var request struct {
		Stage    render.Stage
		Inputs   []render.Input
		Baseline render.Baseline
		Managed  string
		Director string
	}
	if err := strictjson.Decode(data, &request, strictjson.Options{}); err != nil {
		return a.usageError("render: " + err.Error())
	}
	if err := workflow.ValidateStepSettings(request.Stage.Step); err != nil {
		return a.usageError("render: " + err.Error())
	}
	if a.InstallSkills == nil {
		return a.fail(fmt.Errorf("release skills installer is not configured"))
	}
	dir, err := os.MkdirTemp("", "guildhall-release-*")
	if err != nil {
		return a.fail(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := a.InstallSkills(dir); err != nil {
		return a.fail(err)
	}
	roots := []render.SkillRoot{{Name: "release", Dir: dir}}
	for _, root := range []render.SkillRoot{{Name: "managed", Dir: request.Managed}, {Name: "director", Dir: request.Director}} {
		if root.Dir != "" {
			roots = append(roots, root)
		}
	}
	result, err := render.Prompt(request.Stage, request.Inputs, request.Baseline, roots)
	if err != nil {
		return a.fail(err)
	}
	if err := json.NewEncoder(a.Stdout).Encode(result.Manifest); err != nil {
		return 1
	}
	return 0
}

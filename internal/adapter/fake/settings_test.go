package fake

import (
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
)

// TestSettingsReachTheFake: the fake honours every model and effort, passes a spec's as --model and --effort,
// each only when set, and its worker records exactly what it was given, the zero Settings included.
func TestSettingsReachTheFake(t *testing.T) {
	a := Adapter{Executable: "/opt/guildhall"}
	if err := a.CheckSettings(adapter.Settings{Model: "any model", Effort: "any"}); err != nil {
		t.Errorf("CheckSettings = %v; the fake honours every setting", err)
	}
	dir := t.TempDir()
	spec := adapter.Spec{Identity: testID, WorkDir: dir, PromptFile: filepath.Join(dir, "p.md"), ResultFile: filepath.Join(dir, "r.json"),
		Settings: adapter.Settings{Model: "opus", Effort: "high"}}
	cmd, err := a.Command(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/opt/guildhall", "fake-artisan", "--task", "task-1", "--attempt", "attempt-1", "--prompt", spec.PromptFile, "--result", spec.ResultFile,
		"--model", "opus", "--effort", "high"}
	if !slices.Equal(cmd.Argv, want) {
		t.Errorf("argv = %q\nwant %q", cmd.Argv, want)
	}
	parsed, err := ParseArgs(cmd.Argv[2:])
	if err != nil || parsed.Settings != spec.Settings {
		t.Fatalf("ParseArgs(Command) = %+v, %v; want settings %+v", parsed, err, spec.Settings)
	}
	for name, s := range map[string]adapter.Settings{"none": {}, "effort": {Effort: "low"}, "both": spec.Settings} {
		t.Run(name, func(t *testing.T) {
			args := setup(t, "Do it.\n")
			args.Settings = s
			if _, err := RecordedSettings(args.ResultFile); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("RecordedSettings before the run = %v, want fs.ErrNotExist", err)
			}
			if _, err := run(t, args); err != nil {
				t.Fatal(err)
			}
			if got, err := RecordedSettings(args.ResultFile); err != nil || got != s {
				t.Errorf("RecordedSettings = %+v, %v; want %+v", got, err, s)
			}
		})
	}
}

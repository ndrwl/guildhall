// Command guildhall is the Guildhall command-line tool.
package main

import (
	"fmt"
	"os"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/cli"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/skills"
)

func main() {
	if err := cli.CheckReleaseEnv(os.Getenv); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "guildhall: %v\n", err)
		os.Exit(1)
	}
	// The variable is the launcher's word to this process alone: what this release starts by its own path, such
	// as a supervisor's pre-flight of a newer release, must not inherit a version that is not its own.
	_ = os.Unsetenv(release.Env)
	app := cli.New()
	app.InstallSkills = skills.Install
	app.InstallBaseline = baseline.Install
	os.Exit(app.Run(os.Args[1:]))
}

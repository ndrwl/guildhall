// Command guildhall is the Guildhall command-line tool.
package main

import (
	"os"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/cli"
	"github.com/ndrwl/guildhall/skills"
)

func main() {
	app := cli.New()
	app.InstallSkills = skills.Install
	app.InstallBaseline = baseline.Install
	os.Exit(app.Run(os.Args[1:]))
}

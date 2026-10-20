// Command guildhall is the Guildhall command-line tool.
package main

import (
	"os"

	"github.com/ndrwl/guildhall/internal/cli"
)

func main() {
	os.Exit(cli.New().Run(os.Args[1:]))
}

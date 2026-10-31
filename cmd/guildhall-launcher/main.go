// Command guildhall-launcher is the PATH launcher (docs/releases.md#the-path-launcher): installed as the guildhall
// on PATH by guildhall release adopt or guildhall release install --launcher, it runs the release the selected copy
// pins.
package main

import (
	"os"

	"github.com/ndrwl/guildhall/internal/launcher"
)

func main() {
	os.Exit(launcher.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The Herdr stand-in is this test binary started as herdr: the built binary's wake types into the Director's pane
// through the herdr program on PATH, and no Herdr server runs in CI. It plays one session, herdrSession, holding
// the Director's bound tab herdrTab in workspace herdrWorkspace with root pane herdrPane, in which the runtime,
// not the shell, is the foreground program, so a wake may type.
//
//   - `tab get herdrTab` reports the tab in herdrWorkspace.
//   - `pane process-info --pane herdrPane` reports shell pid 10 and the runtime as pid 11.
//   - `pane run herdrPane <line>` appends line to the file typed in the directory HerdrStubEnv names, accepting
//     it as Herdr accepts typed input; the check plays the pane's terminal and hands it to the runtime.
//
// Every call must name herdrSession with --session. Another tab or pane is refused as Herdr refuses one it does
// not have, {"error":{"code":"<kind>_not_found",...}} on stderr with exit 1, and any other call exits 2.
const (
	herdrSession   = "lab"
	herdrWorkspace = "w1"
	herdrTab       = "w1:t1"
	herdrPane      = "w1:p1"
)

// HerdrStubEnv names the directory the Herdr stand-in records typed lines in.
const HerdrStubEnv = "GUILDHALL_SAFETY_HERDR"

// herdrBinding is the director start flags binding a session in the terminal to the stand-in's pane.
var herdrBinding = []string{"--herdr-session", herdrSession, "--herdr-workspace", herdrWorkspace, "--herdr-tab", herdrTab, "--herdr-pane", herdrPane}

// runHerdrStubIf plays the Herdr stand-in and exits when the test binary was started as herdr.
func runHerdrStubIf() {
	if strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") != "herdr" {
		return
	}
	os.Exit(herdrStub(os.Args[1:]))
}

func herdrStub(args []string) int {
	n := len(args)
	if n < 4 || args[n-2] != "--session" || args[n-1] != herdrSession {
		fmt.Fprintf(os.Stderr, "stub herdr: want --session %s last: %q\n", herdrSession, args)
		return 2
	}
	args = args[:n-2]
	fail := func(code, message string) int {
		fmt.Fprintf(os.Stderr, `{"error":{"code":%q,"message":%q}}`, code, message)
		return 1
	}
	switch {
	case len(args) == 3 && args[0] == "tab" && args[1] == "get":
		if args[2] != herdrTab {
			return fail("tab_not_found", "tab "+args[2]+" not found")
		}
		fmt.Printf(`{"id":"cli:tab:get","result":{"tab":{"label":"director","number":1,"pane_count":1,"tab_id":%q,"workspace_id":%q},"type":"tab_info"}}`, herdrTab, herdrWorkspace)
		return 0
	case len(args) == 4 && args[0] == "pane" && args[1] == "process-info" && args[2] == "--pane":
		if args[3] != herdrPane {
			return fail("pane_not_found", "pane "+args[3]+" not found")
		}
		fmt.Printf(`{"id":"cli:pane:process_info","result":{"process_info":{"pane_id":%q,"shell_pid":10,"foreground_processes":[{"pid":11,"name":"runtime","argv":["runtime"]}]},"type":"pane_process_info"}}`, herdrPane)
		return 0
	case len(args) == 4 && args[0] == "pane" && args[1] == "run":
		if args[2] != herdrPane {
			return fail("pane_not_found", "pane "+args[2]+" not found")
		}
		f, err := os.OpenFile(filepath.Join(os.Getenv(HerdrStubEnv), "typed"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = fmt.Fprintln(f, args[3])
			err = errors.Join(err, f.Close())
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "stub herdr: %v\n", err)
			return 2
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "stub herdr: unexpected call %q\n", args)
	return 2
}

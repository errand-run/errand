// Command errand is one program: the command line that is typed, the agent of a place, the page and the screen, and the small
// tools a run calls ( `say`, `require` ). Which one it is, its first word says.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/agent"
	"github.com/errand-run/errand/internal/command"
	"github.com/errand-run/errand/internal/download"
	"github.com/errand-run/errand/internal/page"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/system"
	"github.com/errand-run/errand/internal/tui"
)

func main() {
	args := os.Args[1:]
	if len(args) == 4 && args[0] == "serve" && args[1] == "kept-lease" { // what the system starts for a port it keeps ( --always )
		os.Exit(agent.KeptLease(args[2], args[3]))
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") || isVerb(args[0]) || typedVerb(args[0]) {
		words, answered := asTyped(args)
		if answered {
			return
		}
		args = words
	}
	switch {
	case len(args) >= 1 && args[0] == "web":
		os.Exit(page.WebProgram(args[1:]))
	case len(args) >= 1 && args[0] == "open":
		os.Exit(page.WebProgram(args))
	case len(args) >= 1 && args[0] == "tui":
		os.Exit(tui.TuiMain())
	case len(args) >= 1 && len(args) <= 2 && args[0] == "build-dir" && os.Getenv("ERRAND_BUILDS") != "" && (len(args) == 1 || !strings.HasPrefix(args[1], "-")):
		os.Exit(agent.BuildDirMain(args[1:]))
	case len(args) >= 1 && carried[args[0]]:
		os.Exit(cliMain(args[0], args[1:]))
	case len(args) == 2 && args[0] == "ctrl-c": // ( how a Windows is told Ctrl-C: from a process that can lose its console )
		pid, err := strconv.Atoi(args[1])
		if err == nil {
			err = system.CtrlC(pid)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		time.Sleep(200 * time.Millisecond)
	case len(args) >= 1 && args[0] == "place-again":
		os.Exit(command.AgainMain(args[1:]))
	case len(args) >= 1 && args[0] == "quit":
		os.Exit(command.QuitMain(args[1:]))
	case len(args) == 1 && args[0] == "peek":
		os.Exit(agent.PeekMain())
	case len(args) >= 1 && args[0] == "there":
		os.Exit(reach.ThereMain(args[1:]))
	case len(args) >= 1 && args[0] == "place":
		os.Exit(agent.PlaceMain(args[1:]))
	case len(args) >= 1 && args[0] == "download":
		os.Exit(download.DownloadMain(args[1:]))
	case len(args) == 2 && args[0] == "forked":
		os.Exit(agent.ForkedMain(args[1:]))
	case len(args) >= 1 && args[0] == "say":
		os.Exit(agent.SayMain(args[1:]))
	case len(args) >= 1 && (args[0] == "queue" || args[0] == "wait" || args[0] == "hold" || args[0] == "require"):
		os.Exit(agent.QueueMain(args[0], args[1:]))
	default:
		fmt.Fprintln(os.Stderr, "errand: what is there to do? ( errand --help )")
		os.Exit(2)
	}
}

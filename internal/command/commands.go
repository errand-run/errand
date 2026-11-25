// The commands of errand, carried out by the agent of the machine they are typed on ( ADR 0035 ).
//
// There is no orchestrator apart: a place's agent knows how to do nearly everything, and with relays between
// versions nothing is gained by a second kind of daemon. Whoever types `errand run` starts a small program ( cmd/errand/cli.go ),
// which finds this machine's agent -- the one process per machine that holds its jobs and its locks -- and hands it
// what was typed. The agent reads nothing of its own process for it: the directory, the surroundings, what is given
// to read and the signals are the command's, sent with it ( reach/term.go ).
//
// The agent chooses environments and the grid, reaches the agents of other machines ( links kept between commands:
// the second command to a machine logs in nowhere ), pushes, starts jobs there or here, follows them and brings their
// files back. Nothing it starts depends on it staying: a job is its place's agent's.

package command

import (
	"errors"
	"path/filepath"

	"github.com/errand-run/errand/internal/agent"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/reach"
)

func init() { agent.CarryOut = command }

// command: what a verb does, on the terminal it was typed on
func command(t *reach.Term, verb string, args []string) int {
	defer t.Close()
	switch verb {
	case "run":
		return skipped(runMain(t, args))
	case "serve":
		return serveMain(t, args)
	case "list":
		return listMain(t, args)
	case "shell":
		return shellMain(t, args)
	case "init":
		return initMain(t, args)
	case "machines":
		return reach.MachinesMain(t, args)
	case "agents":
		return agentsMain(t, args)
	case "build-dir":
		return buildDirMain(t, args)
	case "check":
		return checkMain(t, args)
	case "caches":
		return cachesCommand(t, args)
	case "attach":
		return skipped(standingMain(t, verb, args))
	case "status", "stop", "forget":
		return standingMain(t, verb, args)
	case "setup", "envs":
		return envsMain(t, verb, args)
	case "watch":
		return watchMain(t, args)
	case "vm":
		return vmMain(t, args)
	}
	t.Sayf("errand: %s: not a command of errand", verb)
	return 2
}

// skipped: how a command ends whose run was skipped -- said, and no failure
func skipped(code int) int {
	if code == 77 {
		return 0
	}
	return code
}

// declared: what the project at `root` declares -- asked of whoever typed the command, which is where the Python
// that reads a project's files is ( the agent starts none )
func declared(t *reach.Term, root string) (*project.Document, error) {
	declared := true
	if root == "" {
		root, declared = project.FindRoot(t.Cwd)
	} else {
		if !filepath.IsAbs(root) {
			root = filepath.Join(t.Cwd, root)
		}
		if found, _ := filepath.Glob(filepath.Join(root, "errand-*.py")); len(found) == 0 && t.Getenv("ERRAND_PROVIDERS") == "" {
			declared = false
		}
	}
	if !declared {
		return project.LoadIn(root, false, t.Environ)
	}
	t.Event(map[string]any{"declare": root})
	select {
	case got := <-t.Answers:
		if why, _ := got["error"].(string); why != "" {
			return nil, errors.New(why)
		}
		var doc project.Document
		if err := reach.DecodeInto(got["declared"], &doc); err != nil || doc.V != 1 {
			return nil, errors.New("the project's declarations, as the command line read them, cannot be read")
		}
		return doc.Within(t.Environ), nil
	case <-t.Gone:
		return nil, errors.New("whoever typed the command went")
	}
}

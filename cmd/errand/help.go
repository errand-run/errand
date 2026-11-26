// What the command line is asked: the verb among what was typed, and the help.

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/system"
)

// verbs: each, in a line; its options, when it has some
var verbs = []struct{ name, what, usage, options string }{
	{"run", "run a file, a command or entries, in one or several environments", "errand run [options] [target [what the target takes…]]", `
  -r, --runner NAME            the runner that takes every target, by its name in errand.runners
  -e, --entry-tags EXPR        an expression over the tags written on errand's own entries
  -j, --jobs N                 N runs at a time ( auto: as many as cores )
  --grid SEP                   several values, split on SEP, make a matrix: --grid=, --env a,b t.py::x --n=1,2
  --batch                      launch, and give the shell back
  -o                           an output directory of its own for each run
  --out PATTERN                the same under another tree, or a pattern ( {path} {name} {when} {place} {env} {params} )
  --plain                      the output as the programs wrote it: no gutter, no colors
  --no-setup                   the environment used as it is
  --no-queue                   this run does not wait, and does not hold
  --pull DIR                   on another machine: bring DIR back afterwards. Repeatable
  --force                      on another machine: what is in the way there and is not errand's is moved aside, not waited for
  -x, --exclusive              the machine to itself
  --cpus N  --ram 8G  --gpus N  --gpu-ram 40G|75%   what it needs
  --ranks N                    N MPI processes, each running the target
  --time T                     how long it may take, for a batch system
  -m, --mem 24G                memory ceiling of the command
  -n, --label NAME             its name in errand queue
  -l, --log FILE               its output goes there too
  -q                           no banners
  -O, --open                   each run's page opened in the browser as it starts

  x.py and x run as they are ( a .py by the place's python ); x::y goes to the entry runners`},
	{"list", "what a target designates, with its parameters", "errand list [options] [target]", `
  -r, --runner NAME            the runner that takes every target
  -e, --entry-tags EXPR        an expression over the tags written on errand's own entries
  --no-setup                   the environment used as it is`},
	{"shell", "an interactive shell in an environment", "errand shell [--env NAME]", `
  --pull DIR                   on another machine: bring DIR back afterwards. Repeatable
  --force                      on another machine: what is in the way there and is not errand's is moved aside
  --no-setup                   the environment used as it is`},
	{"envs", "the declared environments, and the state they are in", "errand envs [--json]", `
  --json                       everything the project declares, as one document`},
	{"setup", "build or update environments now, and nothing else", "errand setup [NAME,…] [--env NAME] [--force] [--dry-run]", `
  --force                      rebuild from scratch
  --dry-run                    say what it would run, and run nothing`},
	{"init", "write a starting errand-envs.py, and suggest runners for what is here", "errand init [--force]", ""},
	{"tui", "the screen: what runs where, since when, and what waits", "errand tui", ""},
	{"web", "the same, as a page in the browser, served by this machine's agent", "errand web [token|keep|unkeep|stop]", ""},
	{"open", "a directory opened as it is ( its index.html ), served beside errand web", "errand open DIR", ""},
	{"queue", "what this machine has, and what is holding it", "errand queue", ""},
	{"wait", "wait until nothing holds the machine", "errand wait", ""},
	{"status", "what runs on every machine work was sent to, then the --batch work", "errand status [--mine]", `
  --mine                       this project in full, one line per other project`},
	{"watch", "the same, live", "errand watch", ""},
	{"stop", "stop what runs, by the id errand status shows, on any machine", "errand stop [ID...] | errand stop --mine", `
  --mine                       everything this project runs`},
	{"forget", "drop a run that is over from the list, or every one: errand forget finished", "errand forget ID | errand forget finished", ""},
	{"attach", "follow again what a command left running, and bring it back", "errand attach [ID]", ""},
	{"machines", "the machines errand asks what runs there", "errand machines [forget NAME… | rename NAME LABEL | watch NAME on|off|auto | gauges NAME on|off]", ""},
	{"agents", "what is kept between commands", "errand agents [stop [NAME]] [restart] [--force]", ""},
	{"vm", "the virtual machines kept warm between commands", "errand vm stop", ""},
	{"serve", "a server kept while used, under the queue", "errand serve [NAME | -- CMD] | list | stop NAME | release NAME [--now] | connect HASH [PORT] | authorize --to pages|key:NAME [NAME#VERSION] [-- CMD] | authorized | withdraw HASH | dev REPOSITORY PATH | source URL | look | fetch | probe", `
  --gpus N --gpu-ram S --ram S --cpus N -x -n LABEL   what it needs
  --name NAME                  what it is called ( default: the label, else the command's first word )
  --port [NAME=]ANNOUNCED[:SERVER]   a port, repeated for several; empty: errand chooses, the announced side is kept for it
  --keep T                     how long its port stays once it was last used ( 1d )
  --var NAME=VALUE             a variable of the server's; repeated
  --cwd DIR                    where it starts ( default: the home )
  --version-of FILE            a file whose contents make another server; repeated
  --server-version V           raised when something errand does not see has changed
  --idle T                     how long without connection nor busy before it stops ( 10m )
  --on HOST                    where it runs: another machine; its port is on this one
  --env E                      where it runs: an environment of the project, its layers in full
  --busy-http PATH[:FIELD]     the server is busy when that field of what it answers is not empty
  --busy-if LINE               a shell command, exit 0 when the server is busy; {port} is its port
  --ready-http, --ready-if     likewise, that it answers ( default: its ports accept )
  --others                     let other accounts of this machine connect
  --listen ADDR                let other machines connect: the port listened on at that address too ( 0.0.0.0 )
  --always                     the system listens on the port in errand's stead, and starts errand at a connection
  --hold-end T                 of the port, or of connect: a client's end held until the server was silent that long
  --grace T                    between asking it to stop and killing it ( 10s )
  --wait                       started, and answered once it answers
  --ensure                     the same, nothing said
  --json                       for a program: url, ports, until, again_in, state, why
  --spec -                     the whole request, as JSON, on the standard input
  --url [PORT]                 only its URL
  -f                           stay until it stops; Ctrl-C stops it
  --to pages|key:NAME          authorize: who may ask for it, by its hash ( a key ) or its NAME#VERSION ( a page )
  --host H                     list, stop, release, connect, authorize, authorized, withdraw: on another machine`},
	{"hold", "a claim held while stdin is open", "errand hold [--gpus N] [--gpu-ram S] [--ram S] [--cpus N] [-x] [-n LABEL]", ""},
	{"download", "a file into the project's cache, as errand.download does", "errand download URL [--sha256 H]", ""},
	{"caches", "the caches of the libraries the runs use, and what they no longer need, removed", "errand caches [MACHINE...] [--dry-run] [--unused DAYS]", ""},
	{"build-dir", "where what a project builds goes, made: cmake -B \"$( errand build-dir )\"", "errand build-dir [--env ENV] [NAME]", `
  --env ENV                    the environment it is built in, each its own; its machine's path, when it is elsewhere`},
	{"check", "what errand believes, against what is there: the copies, what the next push would do, the runs in one place only", "errand check [MACHINE...] [--fix] [--bring] [--all] [--json]", `
  --fix                        put right what loses nothing: records of what is gone, locks nobody holds
  --bring                      bring back the runs that are there and not here
  --all                        the machines no work was sent to for a day too
  --json                       as one document`},
	{"say", "inside a run, in any language: a step, a view, a result", "errand say step|view|result …", ""},
	{"require", "inside a run: more of the machine, asked while it runs", "errand require [--gpus N] [--gpu-ram S] [--ram S] [--cpus N] [--ranks N] [-x] [--extra]", ""},
}

const everybodys = `
options, before or after the verb ( before the target, for run and list ):
  --env NAME                   the environment by name; several under --grid=SEP
  -t, --env-tags EXPR          choose environments by tag expression
  --<tag> VALUE                one flag per tag name used in the errand-*.py files
  --root PATH                  project root ( default: found from the cwd )
  -h, --help                   this help; after a verb, that verb's
  -V, --version                which errand this is
  -v, --verbose                say every command errand starts`

func isVerb(name string) bool {
	for _, v := range verbs {
		if v.name == name {
			return true
		}
	}
	return false
}

func helpText(verb string) string {
	for _, v := range verbs {
		if v.name == verb {
			out := "usage: " + v.usage + "\n\n" + v.what + "\n"
			if v.options != "" {
				out += "\noptions:" + v.options + "\n"
			}
			return out + everybodys + "\n"
		}
	}
	out := "usage: errand [options] <verb> [arguments]\n\nRun your work in any environment -- built and kept current from what it declares,\n" +
		"here, in a container, on another machine -- with your files synced there and back.\n\nverbs:\n"
	for _, v := range verbs {
		out += fmt.Sprintf("  %-9s %s\n", v.name, v.what)
	}
	return out + everybodys + "\n\n  errand <verb> --help   what that verb takes\n"
}

// typedVerb: whether a word is one a person would type as a verb -- not one of those errand starts itself with
func typedVerb(word string) bool {
	switch word {
	case "place", "place-again", "there", "peek", "quit", "keeper", "agent", "table", "manifest", "sync", "storage", "forked", "ctrl-c":
		return false
	}
	return !strings.ContainsAny(word, "/\\")
}

// asTyped: what was typed, with the verb first -- the options everybody takes may stand before it. Help and the version
// are answered here -> the words, and whether they were answered.
func asTyped(args []string) ([]string, bool) {
	var before []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			if i+1 < len(args) && isVerb(args[i+1]) {
				fmt.Print(helpText(args[i+1]))
			} else {
				fmt.Print(helpText(""))
			}
			return nil, true
		case a == "-V" || a == "--version":
			fmt.Println("errand " + system.Version + " ( release " + reach.Release + " ), " + kit.SelfPath())
			return nil, true
		case !strings.HasPrefix(a, "-"):
			rest := args[i+1:]
			if !isVerb(a) && typedVerb(a) {
				fmt.Fprintf(os.Stderr, "errand: unknown verb '%s'   ( errand --help lists them )\n", a)
				os.Exit(2)
			}
			if isVerb(a) {
				for _, r := range rest {
					if r == "--" || !strings.HasPrefix(r, "-") && a == "run" {
						break
					}
					if r == "-h" || r == "--help" {
						fmt.Print(helpText(a))
						return nil, true
					}
				}
			}
			return append(append([]string{a}, before...), rest...), false
		default:
			before = append(before, a)
			if a != "-v" && a != "--verbose" && !strings.Contains(a, "=") && i+1 < len(args) {
				i++
				before = append(before, args[i])
			}
		}
	}
	if len(before) > 0 {
		fmt.Fprintf(os.Stderr, "errand: %s: which verb? ( errand --help )\n", strings.Join(before, " "))
		return nil, true
	}
	fmt.Print(helpText(""))
	return nil, true
}

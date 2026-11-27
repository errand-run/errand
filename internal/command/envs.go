// The environments of a project, from the command line ( the guide's "Automatic installations" ):
//
//	errand envs                           what is declared, and what state it is in
//	errand setup [ENV…] [--force] [--dry-run]   build or update now, and nothing else
//
// Both are asked of the agent of the place each environment lives on: this machine's, or the one its `Ssh` reaches
// ( the project pushed there first, since what an environment is built from are files of the project ).

package command

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
)

// placeOf: where an environment lives, reached -- the link to its agent, the project's root there, the rest of its
// stack, and the request's fields that say so. `pushing`: the project pushed there first, when it is elsewhere.
func placeOf(t *reach.Term, doc *project.Document, name string, env project.Env, pushing, force bool) (*reach.Link, []project.Layer, map[string]any, error) {
	if len(env.Stack) > 0 && env.Stack[0].Type() == "Ssh" {
		if !pushing {
			h := reach.HostOf(env.Stack[0])
			l, err := reach.LinkTo(t, &h)
			root := env.Stack[0].String("root")
			if root == "" {
				root = doc.Root
			}
			return l, env.Stack[1:], map[string]any{"root": root, "tags": env.Tags}, err
		}
		l, _, root, err := pushTo(t, doc, name, env.Stack, true, false, force)
		return l, env.Stack[1:], map[string]any{"root": root, "tags": env.Tags}, err
	}
	l, err := reach.LinkTo(t, nil)
	return l, env.Stack, map[string]any{"root": doc.Root, "outputs": doc.OutputsDir(), "tags": env.Tags, "environ": t.Environ}, err
}

func envsMain(t *reach.Term, verb string, args []string) int {
	root, force, dry, asJSON := "", false, false, false
	var names []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--force":
			force = true
		case a == "--dry-run":
			dry = true
		case a == "--json" && verb == "envs":
			asJSON = true
		case a == "-v" || a == "--verbose":
		case strings.HasPrefix(a, "--grid"):
		case strings.HasPrefix(a, "--env="):
			names = append(names, strings.Split(a[6:], ",")...)
		case a == "--root" && i+1 < len(args):
			i++
			root = args[i]
		case a == "--env" && i+1 < len(args):
			i++
			names = append(names, strings.Split(args[i], ",")...)
		case strings.HasPrefix(a, "-"):
			t.Sayf("errand %s: %s: not an option of it\n", verb, a)
			return 2
		default:
			names = append(names, a)
		}
	}
	doc, err := declared(t, root)
	if err != nil {
		t.Sayln("errand:", err)
		return 1
	}
	if asJSON {
		envsJSON(t, doc)
		return 0
	}
	if len(names) == 0 && verb == "setup" { // the default environment
		if doc.DefaultEnv != "" {
			names = []string{doc.DefaultEnv}
		}
	}
	for i, n := range names { // `setup a,b`
		if parts := strings.Split(n, ","); len(parts) > 1 {
			names = append(append(append([]string{}, names[:i]...), parts...), names[i+1:]...)
			break
		}
	}
	if len(names) == 0 {
		for name := range doc.Envs {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	for _, name := range names {
		if _, ok := doc.Envs[name]; !ok {
			t.Sayf("errand %s: no environment %q in this project\n", verb, name)
			return 1
		}
	}
	if verb == "envs" {
		if len(names) == 0 {
			t.Println("  no environment is declared: commands run on this machine as it is")
			return 0
		}
		for _, name := range names {
			env := doc.Envs[name]
			state := "ok"
			if st := windowsState(t, env); st != "" {
				state = st
			} else if l, stack, where, err := placeOf(t, doc, name, env, false, false); err != nil {
				state = "unreachable ( " + strings.SplitN(err.Error(), "\n", 2)[0] + " )"
			} else if len(layer.Artefacts(stack)) > 0 {
				where["op"], where["stacks"] = "envs", map[string][]project.Layer{name: stack}
				if got, err := l.Ask(where); err != nil {
					state = "unknown ( " + err.Error() + " )"
				} else {
					states, _ := got["states"].(map[string]any)
					state, _ = states[name].(string)
				}
			}
			if layer.MissingOf(env.Stack) != nil {
				state = "missing"
			}
			line := layer.Describe(env.Stack)
			if len(env.Tags) > 0 {
				line += "  " + runinfo.TagsSaid(env.Tags)
			}
			if name == doc.DefaultEnv {
				line += "  <- default"
			}
			t.Printf("  %-16s %-12s %s\n", name, state, line)
		}
		return 0
	}
	worst := 0
	for _, name := range names {
		env := doc.Envs[name]
		if len(env.Stack) > 0 && env.Stack[0]["windows"] != nil { // the Windows it leads to, before what is built there
			// ( forced: what the environment builds inside it, when it builds something -- a Windows is an hour to install, and
			// another environment's as well )
			inside := len(layer.Artefacts(env.Stack[1:])) > 0
			if code := setupWindows(t, doc, name, env, force && !inside, dry); code != 0 {
				worst = code
				continue
			}
			if !inside {
				continue
			}
			if dry && reach.HostOf(env.Stack[0]).VM != nil { // ( it is not booted to say what it would do )
				for _, a := range layer.Artefacts(env.Stack[1:]) {
					t.Sayf("  would bring up to date, in it: %s\n", a.Name)
				}
				continue
			}
		}
		l, stack, where, err := placeOf(t, doc, name, env, !dry, false) // ( its `--force` rebuilds: it is no word about what is there )
		if err != nil {
			t.Sayf("errand: %s: %v\n", name, err)
			worst = 1
			continue
		}
		if len(layer.Artefacts(stack)) == 0 {
			continue // nothing it declares is built
		}
		if !dry && builtElsewhere(stack) { // an image that is built in another environment: made there, and brought here, first
			var dest *reach.HostSpec
			if len(env.Stack) > 0 && env.Stack[0].Type() == "Ssh" {
				h := reach.HostOf(env.Stack[0])
				dest = &h
			}
			if err := carryImages(t, doc, dest, l, where, stack, force, runOptions{flags: map[string][]string{}}); err != nil {
				if errors.Is(err, reach.ErrStopped) {
					return 130
				}
				t.Sayf("errand: %s: %v\n", name, err)
				worst = 1
				continue
			}
		}
		where["op"], where["stack"], where["force"], where["dry_run"] = "setup", stack, force, dry
		if code := followSetup(t, l, where, lockTaker(t, doc)); code != 0 {
			worst = code
		}
	}
	return worst
}

// followSetup: an installation asked of an agent, and what it says of it said as it says it -> how it ended.
// Ctrl-C leaves it: the agent is told, and stops the step it is at.
func followSetup(t *reach.Term, l *reach.Link, req map[string]any, lock func(any)) int {
	c := l.Open(req)
	if _, err := c.Reply(); err != nil {
		c.Close()
		t.Sayln("errand:", err)
		return 1
	}
	told := true // how the channel is left: told ( the agent stops the step it is at ), or without a word
	defer func() {
		if told {
			c.Close()
		} else {
			c.Abandon()
		}
	}()
	got := make(chan reach.LinkMsg, 16)
	go func() {
		for {
			kind, payload, err := c.Recv()
			if err != nil {
				close(got)
				return
			}
			got <- reach.LinkMsg{Kind: kind, Payload: payload}
			if kind == kit.EXIT {
				return
			}
		}
	}()
	for {
		select {
		case <-t.Sigs:
			return 130 // ( the channel closed on the way out: said, not silence )
		case <-t.Gone: // whoever asked went, without a word: it goes on, by the agent
			told = false
			return 1
		case m, ok := <-got:
			if !ok {
				t.Say("errand: the connection was lost while it was being prepared: it goes on over there")
				return 1
			}
			var said map[string]any
			_ = json.Unmarshal(m.Payload, &said)
			switch m.Kind {
			case kit.NOTE:
				if text, _ := said["say"].(string); text != "" {
					t.Say(text)
				}
				if lk, ok := said["lock"]; ok && lock != nil {
					lock(lk)
				}
			case kit.EXIT:
				return int(kit.Num(said["code"], 1))
			}
		}
	}
}

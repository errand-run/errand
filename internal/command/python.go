package command

import (
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
)

// `errand python [--env ENV]`: the interpreter an environment runs, on its machine ( ADR 0044 ) -- for an editor, or
// `$( errand python ) script.py`. An environment of errand's is in its store, and the packages one works on in an overlay
// over it: this says which python sees both.

func pythonMain(t *reach.Term, args []string) int {
	env, root := "", ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "--env" || a == "--root") && i+1 < len(args):
			i++
			if a == "--env" {
				env = args[i]
			} else {
				root = args[i]
			}
		case strings.HasPrefix(a, "--env="):
			env = a[6:]
		case strings.HasPrefix(a, "--root="):
			root = a[7:]
		case a == "-v" || a == "--verbose":
		default:
			t.Sayf("errand python: %s: only --env", a)
			return 2
		}
	}
	doc, err := declared(t, root)
	if err != nil {
		t.Sayln("errand:", err)
		return 1
	}
	envName, e, err := doc.Env(env)
	if err != nil {
		t.Sayln("errand:", err)
		return 1
	}
	l, stack, where, err := placeOf(t, doc, envName, e, false, false)
	if err == nil {
		where["op"], where["stack"] = "python", stack
		delete(where, "environ")
		var got map[string]any
		if got, err = l.Ask(where); err == nil {
			t.Println(kit.Text(got["path"]))
			return 0
		}
	}
	t.Sayln("errand:", err)
	return 1
}

package command

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/reach"
)

// `errand build-dir [--env ENV] [NAME]`: where what a project builds in an environment goes ( ADR 0039 ), made, and printed
// -- where the environment is, when that is another machine

func buildDirMain(t *reach.Term, args []string) int {
	env, root, name := "", "", project.MainBuild
	named := false
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
		case strings.HasPrefix(a, "-"), named:
			t.Sayf("errand build-dir: %s: one name, and --env", a)
			return 2
		default:
			name, named = a, true
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
	if doc.Settings.Build != "" && name == project.MainBuild { // the project's to choose, and to tidy
		at := filepath.FromSlash(doc.BuildHere(envName))
		if !isElsewhere(e.Stack) {
			if !filepath.IsAbs(at) {
				at = filepath.Join(doc.Root, at)
			}
			if err := os.MkdirAll(at, 0o755); err != nil {
				t.Sayln("errand:", err)
				return 1
			}
			t.Println(at)
			return 0
		}
	}
	l, _, where, err := placeOf(t, doc, envName, e, false, false)
	if err == nil {
		where["op"], where["env"], where["name"] = "build_dir", project.BuildName(envName), project.BuildName(name)
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

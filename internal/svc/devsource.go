// Dev sources ( ADR 0028 §3 ): a page's services run from a checkout one works on.
//
// On a machine, the user's file says that a repository is represented by a checkout:
//
//	{ "dev_sources": { "github.com/sdot-team/sdot": { "path": "~/Projects/sdot-ffi", "env": "prj-jax" } } }    ( ~/.config/errand/config.json )
//
// A page whose description says `source: { git: REPO, dev: true }` can then be authorized to run from the checkout, in the
// env named ( ADR 0041 ). A copy of a project carries, beside it, where it comes
// from and the dev sources inside it: on the machine that holds the copy, they are dev sources too.

package svc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/queue"
)

const devOff = "dev-off.json" // in the user's configuration: the dev sources turned off, by their paths

// devCheckout is a checkout that represents a repository here
type devCheckout struct {
	Repo, Path, Env, Origin string
	Prefix, Python          string // the env named, where it is
	Synced                  any
	Copy                    bool
	Off                     bool
	Why                     string // why it cannot be used; "" when it can
}

func atHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
	}
	return p
}

// envNamed: an env by its name ( conda, micromamba ) or its prefix -> its prefix, its Python
func envNamed(env string) (string, string) {
	if env == "" {
		return "", ""
	}
	tries := []string{atHome(env)}
	if !strings.ContainsAny(env, `/\~`) {
		tries = nil
		for _, root := range mambaRoots() {
			tries = append(tries, filepath.Join(root, "envs", env))
		}
	}
	for _, prefix := range tries {
		if p := pythonIn(prefix); p != "" {
			return prefix, p
		}
	}
	return "", ""
}

func repoURL(repo string) string {
	if !strings.Contains(repo, "://") {
		repo = "https://" + repo
	}
	return httpsURL(repo)
}

// devSources: those this machine's file declares, then those its copies of projects carry
func devSources(declared map[string]DevSource) []devCheckout {
	var off []string
	if data, err := os.ReadFile(filepath.Join(configDir(nil), devOff)); err == nil {
		_ = kit.JsonUnmarshal(data, &off)
	}
	one := func(repo, at, env, origin string) devCheckout {
		d := devCheckout{Repo: repoURL(repo), Path: atHome(at), Env: env, Origin: origin}
		d.Prefix, d.Python = envNamed(env)
		for _, p := range off {
			d.Off = d.Off || p == d.Path
		}
		switch {
		case !kit.IsDir(d.Path):
			d.Why = d.Path + " is not here"
		case d.Off:
			d.Why = "turned off ( storage tab )"
		}
		return d
	}
	var out []devCheckout
	for _, repo := range sortedKeys(declared) {
		c := declared[repo]
		out = append(out, one(repo, c.Path, c.Env, "declared"))
	}
	for _, rec := range caches.ProjectCopies() {
		root := kit.Text(rec["path"])
		o := caches.ReadObject(filepath.Join(root, filesync.CopyOrigin))
		for _, x := range caches.ListOf(o["dev"]) {
			c := ObjectOf(x)
			env := kit.Text(ObjectOf(c["envs"])[machineName()])
			if env == "" {
				env = kit.Text(c["env"])
			}
			if !filepath.IsLocal(filepath.FromSlash(kit.Text(c["rel"]))) {
				continue
			}
			d := one(kit.Text(c["repo"]), filepath.Join(root, filepath.FromSlash(kit.Text(c["rel"]))), env, fmt.Sprintf("copy of %s:%s", kit.Text(o["machine"]), kit.Text(o["path"])))
			d.Synced, d.Copy = rec["synced"], true
			out = append(out, d)
		}
	}
	return out
}

func machineName() string { return queue.Host() }

func init() {
	filesync.SaidOfCopy = func(root string) map[string]any {
		return originFor(root, ReadConfig().Dev)
	}
}

// originFor: what a push says of a project to the machine that holds its copy -- this machine, its path, the dev sources
// declared here that are inside it
func originFor(root string, declared map[string]DevSource) map[string]any {
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	dev := []any{}
	for _, repo := range sortedKeys(declared) {
		c := declared[repo]
		at, err := filepath.EvalSymlinks(atHome(c.Path))
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(root, at); err == nil && filepath.IsLocal(rel) {
			dev = append(dev, map[string]any{"repo": repoURL(repo), "rel": filepath.ToSlash(rel), "env": c.Env, "envs": c.Envs})
		}
	}
	return map[string]any{"machine": machineName(), "path": root, "dev": dev}
}

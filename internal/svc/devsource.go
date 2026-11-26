// Dev sources ( ADR 0028 §3 ): a page's services run from a checkout one works on.
//
// On a machine, the user's file says that a repository is represented by a checkout:
//
//	{ "dev_sources": { "github.com/sdot-team/sdot": { "path": "~/Projects/sdot-ffi", "env": "prj-jax" } } }    ( ~/.config/errand/config.json )
//
// A store that asks for a service of that repository can then have it run from the checkout, whatever its version, as the
// CHECKOUT describes it, in the env named, under the key `NAME#dev`. A copy of a project carries, beside it, where it comes
// from and the dev sources inside it: on the machine that holds the copy, they are dev sources too.

package svc

import (
	"crypto/sha256"
	"encoding/hex"
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

type devLook struct {
	Repo    string  `json:"repo"`
	Path    string  `json:"path"`
	Env     string  `json:"env"`
	Origin  string  `json:"origin"`
	Synced  any     `json:"synced"`
	Off     bool    `json:"off"`
	Key     string  `json:"key"`
	State   string  `json:"state"`
	Why     string  `json:"why"`
	Changed float64 `json:"changed"`
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

// DevFor: the dev source of the repository a service comes from -- one that can be used, when there is one
func DevFor(want AskedService, declared map[string]DevSource) *devCheckout {
	git := kit.Text(want.Source["git"])
	if git == "" {
		return nil
	}
	repo := httpsURL(git)
	var found *devCheckout
	for _, d := range devSources(declared) {
		if d.Repo == repo && (found == nil || found.Why != "" && d.Why == "") {
			found = &d
		}
	}
	return found
}

// Described: the service's directory in the checkout, and what the CHECKOUT says of it
func (d devCheckout) Described(name string, want AskedService) (provider, error) {
	dir := filepath.Join(d.Path, filepath.FromSlash(kit.Text(want.Source["dir"])))
	p := provider{Env: d.Prefix, Python: d.Python, Version: "dev", Dir: dir, Editable: d.Path, Data: described(dir)}
	if p.Data == nil {
		return p, fmt.Errorf("%s is not there, or cannot be read", p.file())
	}
	p.Package = kit.Text(ObjectOf(p.Data["package"])["name"])
	if p.Entry(name) == nil {
		return p, fmt.Errorf("the checkout %s describes no service %q", d.Path, name)
	}
	return p, nil
}

// Signature: a hash of the dates and sizes of the files a service's version is computed over, and the latest date
func (p provider) Signature(name string) (string, float64) {
	h, last := sha256.New(), 0.0
	for _, rel := range kit.Strs(p.Entry(name)["version_of"]) {
		st, err := os.Stat(filepath.Join(p.Dir, filepath.FromSlash(rel)))
		if err != nil {
			fmt.Fprintf(h, "%s:gone", rel)
			continue
		}
		fmt.Fprintf(h, "%s:%d:%d", rel, st.ModTime().UnixNano(), st.Size())
		last = max(last, kit.Seconds(st.ModTime()))
	}
	return hex.EncodeToString(h.Sum(nil))[:16], last
}

func (d devCheckout) Command(p provider, name string) ([]string, error) {
	paths := map[string]string{}
	for t, f := range toolsHere(ObjectOf(p.Entry(name)["tools"]), nil) {
		paths[t] = f.path
	}
	words, err := p.compose(name, paths, true)
	if err != nil && d.Python == "" && strings.Contains(err.Error(), "no Python") {
		how := ""
		if d.Copy {
			how = fmt.Sprintf(", or its `envs` in config.json for %q", machineName())
		}
		err = fmt.Errorf("no env for it here ( errand serve dev … --env NAME%s )", how)
	}
	return words, err
}

// devLook: what the diagnosis says of the dev source of a service; nil when there is none
func (l *looker) devLook(name string, want AskedService) (*devLook, error) {
	d := DevFor(want, l.dev)
	if d == nil {
		return nil, nil
	}
	out := &devLook{Repo: d.Repo, Path: d.Path, Env: d.Env, Origin: d.Origin, Synced: d.Synced, Off: d.Off, Key: name + "#dev", State: "unavailable", Why: d.Why}
	if d.Why != "" {
		return out, nil
	}
	p, err := d.Described(name, want)
	if err == nil {
		_, out.Changed = p.Signature(name)
		_, err = d.Command(p, name)
	}
	if err != nil {
		out.Why = err.Error()
		return out, nil
	}
	if out.State = "ready"; l.running[out.Key] != "" {
		out.State = "running"
	}
	return out, nil
}

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

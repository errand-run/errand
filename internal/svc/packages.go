// The packages of a machine that describe services ( ADR 0027 §1-2 ): found, read and composed, nothing imported or run.
//
// A package describes its services in a file it ships, `errand-services.json`, beside their code; an entry point of the
// group `errand.services` names the module that holds it. Everything here reads files: an env's `*.dist-info`, the
// editable finders' mapping, the package's own files.

package svc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/kit"
)

const (
	describedFile = "errand-services.json"
	sourceFile    = ".errand-source.json" // in a repository errand fetched: { git, rev, at, dirs }
)

// serviceVersion: the version of `name#version` -- the producer computes the same when it writes a store
func serviceVersion(name string, entry map[string]any, base string) (string, error) {
	h := sha256.New()
	protocol := entry["protocol"]
	if protocol == nil {
		protocol = 1
	}
	fmt.Fprintf(h, "errand-service/1 %s protocol %v\n", name, protocol)
	for _, rel := range kit.Strs(entry["version_of"]) {
		data, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s %d\n", rel, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:8], nil
}

// splitKey: `blender#3fa29c1e` -> blender, 3fa29c1e; a bare name -> name, ""
func splitKey(key string) (string, string) {
	name, v, _ := strings.Cut(key, "#")
	return name, v
}

// ── the envs of this machine ─────────────────────────────────────────────────

func sites(prefix string) []string {
	var out []string
	for _, pattern := range []string{filepath.Join(prefix, "lib", "python3*", "site-packages"), filepath.Join(prefix, "Lib", "site-packages")} {
		got, _ := filepath.Glob(pattern)
		for _, g := range got {
			if kit.IsDir(g) {
				out = append(out, g)
			}
		}
	}
	sort.Strings(out)
	if len(out) == 2 && caches.SameFile(out[0], out[1]) {
		out = out[:1]
	}
	return out
}

func mambaRoots() []string {
	var out []string
	for _, v := range []string{"MAMBA_ROOT_PREFIX", "CONDA_ROOT", "CONDA_PREFIX"} {
		if p := os.Getenv(v); p != "" {
			out = append(out, p)
		}
	}
	home, _ := os.UserHomeDir()
	for _, name := range []string{".mamba", "micromamba", ".micromamba", "miniforge3", "miniforge", "mambaforge", "miniconda3", "anaconda3", ".conda"} {
		out = append(out, filepath.Join(home, name))
	}
	return out
}

// userEnvs: the user's own envs -- never run from, only read for the repositories they were installed from
func userEnvs() []string {
	var out []string
	for _, root := range mambaRoots() {
		inside, _ := filepath.Glob(filepath.Join(root, "envs", "*"))
		for _, e := range append([]string{root}, inside...) {
			if kit.IsDir(e) && len(sites(e)) > 0 {
				out = append(out, e)
			}
		}
	}
	return out
}

func serviceEnvsDir() string { return filepath.Join(kit.LocalCache(), "svc-envs") }

// serviceEnvs: errand's own service envs, one per service. A service never runs in an environment of the user's taken as
// it is ( ADR 0027, amendment ).
func serviceEnvs() []string {
	dirs, _ := filepath.Glob(filepath.Join(serviceEnvsDir(), "*"))
	sort.Strings(dirs)
	var out []string
	seen := map[string]bool{}
	for _, d := range dirs {
		if real, err := filepath.EvalSymlinks(d); err == nil {
			d = real
		}
		if !seen[d] && kit.IsDir(d) && len(sites(d)) > 0 {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

func pythonIn(prefix string) string {
	for _, c := range []string{"bin/python3", "bin/python", "python.exe", "Scripts/python.exe"} {
		if p := filepath.Join(prefix, filepath.FromSlash(c)); kit.IsFile(p) {
			return p
		}
	}
	return ""
}

// ── the packages that describe services ──────────────────────────────────────

// provider is a package that describes services
type provider struct {
	Env      string // the env's prefix, or the repository's directory
	Python   string // its interpreter
	Package  string
	Version  string // the distribution's
	Dir      string // where the module that describes is
	Editable string // the checkout, for an editable install
	Data     map[string]any
}

func (p provider) file() string { return filepath.Join(p.Dir, describedFile) }

func (p provider) Entry(name string) map[string]any {
	services, _ := p.Data["services"].(map[string]any)
	e, _ := services[name].(map[string]any)
	return e
}

// gives: the version of a service it describes; "" when it describes none, or a file is missing
func (p provider) gives(name string) string {
	if e := p.Entry(name); e != nil {
		if v, err := serviceVersion(name, e, p.Dir); err == nil {
			return v
		}
	}
	return ""
}

func described(dir string) map[string]any {
	return caches.ReadObject(filepath.Join(dir, describedFile))
}

func entryPointModules(dist string) []string {
	data, err := os.ReadFile(filepath.Join(dist, "entry_points.txt"))
	if err != nil {
		return nil
	}
	group := ""
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || line[0] == '#' || line[0] == ';':
		case line[0] == '[' && strings.HasSuffix(line, "]"):
			group = strings.TrimSpace(line[1 : len(line)-1])
		case group == "errand.services" && strings.Contains(line, "="):
			_, v, _ := strings.Cut(line, "=")
			v, _, _ = strings.Cut(strings.TrimSpace(v), ":")
			out = append(out, v)
		}
	}
	return out
}

func distSaid(dist string) (string, string) {
	name, version := filepath.Base(dist), ""
	if i := strings.LastIndex(name, "-"); i > 0 {
		name = name[:i]
	}
	data, _ := os.ReadFile(filepath.Join(dist, "METADATA"))
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if v, ok := strings.CutPrefix(line, "Name:"); ok {
			name = strings.TrimSpace(v)
		} else if v, ok := strings.CutPrefix(line, "Version:"); ok {
			version = strings.TrimSpace(v)
		} else if strings.TrimSpace(line) == "" {
			break
		}
	}
	return name, version
}

// pathOfURL: the path a `file:` URL names, as this system writes it -- `file:///C:/x` is `C:\x`
func pathOfURL(u *url.URL) string {
	p := u.Path
	if runtime.GOOS == "windows" && len(p) > 2 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

// installedFrom: what an installation says of where it comes from -- its checkout when it is one, and its repository
func installedFrom(dist string) (editable, repository string) {
	d := caches.ReadObject(filepath.Join(dist, "direct_url.json"))
	from := kit.Text(d["url"])
	if d == nil || from == "" {
		return "", ""
	}
	if d["vcs_info"] != nil {
		return "", httpsURL(strings.TrimPrefix(from, "git+"))
	}
	if u, err := url.Parse(from); err == nil && u.Scheme == "file" {
		if info, _ := d["dir_info"].(map[string]any); info["editable"] == true {
			editable = pathOfURL(u)
		}
		return editable, originOf(pathOfURL(u))
	}
	return "", ""
}

var (
	remoteOrigin = regexp.MustCompile(`\[remote "origin"\][^\[]*?url\s*=\s*(\S+)`)
	gitSSH       = regexp.MustCompile(`^(?:ssh://)?git@([^:/]+)[:/](.+?)(?:\.git)?/?$`)
	finderMap    = regexp.MustCompile(`MAPPING\s*(?::[^=]*)?=\s*(\{[^}]*\})`)
	finderPair   = regexp.MustCompile(`['"]([^'"]+)['"]\s*:\s*['"]([^'"]+)['"]`)
	notAName     = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

func originOf(checkout string) string {
	data, err := os.ReadFile(filepath.Join(checkout, ".git", "config"))
	if err != nil {
		return ""
	}
	if m := remoteOrigin.FindStringSubmatch(string(data)); m != nil {
		return httpsURL(m[1])
	}
	return ""
}

// httpsURL: `git@github.com:owner/repo.git` -> `https://github.com/owner/repo`; a trailing `.git` or `/` dropped
func httpsURL(from string) string {
	from = strings.TrimSpace(from)
	if m := gitSSH.FindStringSubmatch(from); m != nil {
		return "https://" + m[1] + "/" + m[2]
	}
	return strings.TrimSuffix(strings.TrimSuffix(from, "/"), ".git")
}

// mappedTo: where an editable install puts a top-level package -- its finders' MAPPING, the plain paths of `.pth` files
func mappedTo(site, top string) []string {
	var out []string
	a, _ := filepath.Glob(filepath.Join(site, "__editable__*finder.py"))
	b, _ := filepath.Glob(filepath.Join(site, "_*editable*.py"))
	for _, f := range append(a, b...) {
		data, _ := os.ReadFile(f)
		if m := finderMap.FindStringSubmatch(string(data)); m != nil {
			for _, kv := range finderPair.FindAllStringSubmatch(m[1], -1) {
				if kv[1] == top {
					out = append(out, kv[2])
				}
			}
		}
	}
	pth, _ := filepath.Glob(filepath.Join(site, "*.pth"))
	for _, f := range pth {
		data, _ := os.ReadFile(f)
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); filepath.IsAbs(line) {
				out = append(out, filepath.Join(line, top))
			}
		}
	}
	return out
}

func moduleDir(site, module, editable string) string {
	parts := strings.Split(module, ".")
	tries := []string{filepath.Join(append([]string{site}, parts...)...)}
	for _, top := range mappedTo(site, parts[0]) {
		tries = append(tries, filepath.Join(append([]string{top}, parts[1:]...)...))
	}
	if editable != "" {
		rel := filepath.Join(parts...)
		tries = append(tries, filepath.Join(editable, "src", rel), filepath.Join(editable, rel))
	}
	for _, c := range tries {
		if kit.IsFile(filepath.Join(c, describedFile)) {
			return c
		}
	}
	return ""
}

func providersIn(prefix string) []provider {
	var out []provider
	for _, site := range sites(prefix) {
		dists, _ := filepath.Glob(filepath.Join(site, "*.dist-info"))
		sort.Strings(dists)
		for _, dist := range dists {
			for _, module := range entryPointModules(dist) {
				editable, _ := installedFrom(dist)
				dir := moduleDir(site, module, editable)
				if dir == "" {
					continue
				}
				data := described(dir)
				if data == nil {
					continue
				}
				name, version := distSaid(dist)
				out = append(out, provider{Env: prefix, Python: pythonIn(prefix), Package: name, Version: version, Dir: dir, Editable: editable, Data: data})
			}
		}
	}
	return out
}

func reposDir() string { return filepath.Join(kit.LocalCache(), "repos") }

// repoDir: where the files of a repository at a commit are kept
func repoDir(from, rev string) string {
	out := reposDir()
	u, err := url.Parse(httpsURL(from))
	host, path := "local", ""
	if err == nil {
		path = u.Path
		if u.Host != "" {
			host = u.Host
		}
	}
	for _, x := range append(append([]string{host}, strings.Split(strings.Trim(path, "/"), "/")...), rev) {
		if x != "" {
			out = filepath.Join(out, notAName.ReplaceAllString(x, "_"))
		}
	}
	return out
}

// fetchedCode: the directories of the repositories errand fetched, each with what it says of itself
func fetchedCode() map[string]map[string]any {
	out := map[string]map[string]any{}
	_ = filepath.WalkDir(reposDir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if said := caches.ReadObject(filepath.Join(p, sourceFile)); said != nil {
			out[p] = said
			return filepath.SkipDir
		}
		return nil
	})
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func providersFetched() []provider {
	var out []provider
	code := fetchedCode()
	for _, root := range sortedKeys(code) {
		said := code[root]
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}
			if err != nil || d.Name() != describedFile {
				return nil
			}
			data := described(filepath.Dir(p))
			if data == nil {
				return nil
			}
			pkg, _ := data["package"].(map[string]any)
			rev := kit.Text(said["rev"])
			out = append(out, provider{Env: root, Package: kit.Text(pkg["name"]), Version: kit.Text(said["git"]) + "@" + rev[:min(len(rev), 12)], Dir: filepath.Dir(p), Data: data})
			return nil
		})
	}
	return out
}

// providers: every package of errand's service envs, and every repository it fetched, that describes services
func providers() []provider {
	var out []provider
	for _, e := range serviceEnvs() {
		out = append(out, providersIn(e)...)
	}
	return append(out, providersFetched()...)
}

// providing: those that give exactly `name#version`
func providing(key string) []provider {
	name, v := splitKey(key)
	var out []provider
	for _, p := range providers() {
		if p.Entry(name) != nil && (v == "" || p.gives(name) == v) {
			out = append(out, p)
		}
	}
	return out
}

var siteRepos struct {
	sync.Mutex
	kept map[string]siteRead
}

type siteRead struct {
	at   int64
	from []string
}

// knownRepos: the repositories of the packages installed on this machine -- a store that names one of them names a source
// this machine already took code from. A site-packages is read again only when it changed: thousands of files in all.
func knownRepos() map[string]bool {
	out := map[string]bool{}
	siteRepos.Lock()
	defer siteRepos.Unlock()
	if siteRepos.kept == nil {
		siteRepos.kept = map[string]siteRead{}
	}
	for _, e := range append(serviceEnvs(), userEnvs()...) {
		for _, site := range sites(e) {
			st, err := os.Stat(site)
			if err != nil {
				continue
			}
			got, ok := siteRepos.kept[site]
			if at := st.ModTime().UnixNano(); !ok || got.at != at {
				got = siteRead{at: at}
				dists, _ := filepath.Glob(filepath.Join(site, "*.dist-info"))
				for _, d := range dists {
					if _, from := installedFrom(d); from != "" {
						got.from = append(got.from, from)
					}
				}
				siteRepos.kept[site] = got
			}
			for _, r := range got.from {
				out[r] = true
			}
		}
	}
	return out
}

// ── composing a command ──────────────────────────────────────────────────────

var describedWord = regexp.MustCompile(`\{(python|file:[^{}]+|tool:[A-Za-z0-9_.-]+|port(?::[a-z][a-z0-9_]*)?)\}`)

// compose: the words of a service on this machine -- `{python}` the env's, `{file:REL}` the package's file, `{tool:NAME}`
// what was found; `{port}` and `{port:NAME}` stay for whoever keeps it
func (p provider) compose(name string, tools map[string]string, check bool) ([]string, error) {
	entry := p.Entry(name)
	if entry == nil {
		who := p.Package
		if who == "" {
			who = p.Dir
		}
		return nil, fmt.Errorf("%s describes no service %q", who, name)
	}
	run := kit.Strs(entry["run"])
	if len(run) == 0 || len(run) != len(caches.ListOf(entry["run"])) {
		return nil, fmt.Errorf("%s: `run` is a list of words", name)
	}
	var bad error
	var out []string
	for _, w := range run {
		out = append(out, describedWord.ReplaceAllStringFunc(w, func(f string) string {
			what := f[1 : len(f)-1]
			switch {
			case what == "python":
				if p.Python == "" {
					bad = fmt.Errorf("%s: no Python in %s", name, p.Env)
				}
				return p.Python
			case strings.HasPrefix(what, "file:"):
				at := filepath.Join(p.Dir, filepath.FromSlash(what[5:]))
				if check {
					real, err := filepath.EvalSymlinks(at)
					if err != nil {
						bad = fmt.Errorf("%s: %s is not in %s", name, what[5:], p.Dir)
						return f
					}
					at = real
				}
				return at
			case strings.HasPrefix(what, "tool:"):
				if tools[what[5:]] == "" {
					bad = fmt.Errorf("%s: no %s found", name, what[5:])
				}
				return tools[what[5:]]
			}
			return f
		}))
	}
	return out, bad
}

// DescribedNeeds: what a description asks of a declaration
func DescribedNeeds(entry map[string]any) map[string]any {
	out := map[string]any{}
	needs, _ := entry["needs"].(map[string]any)
	for _, k := range []string{"gpus", "gpu_ram", "ram", "cpus"} {
		if needs[k] != nil {
			out[k] = needs[k]
		}
	}
	if entry["idle"] != nil && entry["idle"] != "" {
		out["idle"] = entry["idle"]
	}
	return out
}

// The envs of a machine, the services' own envs, and the code errand fetched ( ADR 0027, 0041 ): found and read, nothing
// imported or run. `{file:REL}`, `{python}` and `{tool:NAME}` are what a page's description says of them.

package svc

import (
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
	"github.com/errand-run/errand/internal/layer"
)

const sourceFile = ".errand-source.json" // in a repository errand fetched: { git, rev, at, dirs }

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

// venvPython: where a venv has its interpreter, made or still to make
func venvPython(prefix string) string {
	if layer.OnWindows {
		return filepath.Join(prefix, "Scripts", "python.exe")
	}
	return filepath.Join(prefix, "bin", "python")
}

func pythonIn(prefix string) string {
	for _, c := range []string{"bin/python3", "bin/python", "python.exe", "Scripts/python.exe"} {
		if p := filepath.Join(prefix, filepath.FromSlash(c)); kit.IsFile(p) {
			return p
		}
	}
	return ""
}

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

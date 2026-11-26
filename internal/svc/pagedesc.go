// A service as a page describes it ( ADR 0041 ): portable, in full, as data. Its hash is its identity; what it runs is
// known only on a machine, once its placeholders are resolved there -- and runs only once a person authorized it there.
//
//	{ "name": "blender", "protocol": 1, "text": "…",
//	  "run": [ "{tool:blender}", "-b", "-P", "{file:blender.py}", "--", "--port", "{port}" ],
//	  "source": { "git": "https://github.com/sdot-team/sdot", "rev": "807e184", "dir": "src/sdot/viz/services" },
//	  "tools": { "blender": { "min": "5.0", "look": { … }, "version_from": [ … ], "fetch": { … } } },
//	  "needs": { "gpus": 1 }, "idle": "10m" }
//
// Placeholders, a closed set: `{tool:NAME}` a program found or fetched there, `{file:REL}` a file of the code at `source`,
// `{python}` the service's own env made from `source` ( or the checkout's, in development ), `{port}` / `{port:NAME}`.

package svc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/queue"
)

// PageRepo: a repository, at a commit or as a checkout of the machine's ( ADR 0028 dev_sources, chosen when authorizing )
type PageRepo struct {
	Git       string   `json:"git"`
	Rev       string   `json:"rev,omitempty"`
	Dev       bool     `json:"dev,omitempty"`
	Dir       string   `json:"dir,omitempty"`        // files: where the service's files are in it, `{file:REL}` under it
	Package   string   `json:"package,omitempty"`    // python: where the package is in it; "": its root
	VersionOf []string `json:"version_of,omitempty"` // a checkout's files whose contents make another server ( `**` goes down )
}

// PageRequirement is what an env installs: a requirement of the index ( "numpy>=2" ), or a repository.
type PageRequirement struct {
	Pip  string
	Repo *PageRepo
}

func (r *PageRequirement) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &r.Pip)
	}
	var repo PageRepo
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&repo); err != nil {
		return fmt.Errorf("python.install: a requirement ( \"numpy>=2\" ), or a repository { git, rev | dev, package, version_of }: %v", err)
	}
	r.Repo = &repo
	return nil
}

func (r PageRequirement) MarshalJSON() ([]byte, error) {
	if r.Repo != nil {
		return json.Marshal(r.Repo)
	}
	return json.Marshal(r.Pip)
}

// PagePython: what the service's own env installs, in one go ( a repository given provides its package: the index's is not taken )
type PagePython struct {
	Install []PageRequirement `json:"install"`
	Index   string            `json:"index,omitempty"`
}

// PageDesc: a service as a page describes it
type PageDesc struct {
	Name     string            `json:"name"`
	Protocol any               `json:"protocol,omitempty"`
	Text     string            `json:"text,omitempty"`
	Run      []string          `json:"run"`
	Vars     map[string]string `json:"vars,omitempty"`
	Files    *PageRepo         `json:"files,omitempty"`  // for `{file:REL}`: files of a repository, nothing installed
	Python   *PagePython       `json:"python,omitempty"` // for `{python}`: an env of its own
	Tools    map[string]any    `json:"tools,omitempty"`
	Needs    map[string]any    `json:"needs,omitempty"` // gpus ( or "0-1": as many as the machine has, up to 1 ), gpu_ram, ram, cpus
	Busy     any               `json:"busy,omitempty"`
	Ready    any               `json:"ready,omitempty"`
	Idle     span              `json:"idle,omitempty"`
	Grace    span              `json:"grace,omitempty"`
}

// Repos: every repository it names, files first
func (d PageDesc) Repos() []*PageRepo {
	var out []*PageRepo
	if d.Files != nil {
		out = append(out, d.Files)
	}
	if d.Python != nil {
		for _, r := range d.Python.Install {
			if r.Repo != nil {
				out = append(out, r.Repo)
			}
		}
	}
	return out
}

var aRequirement = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(\[[A-Za-z0-9,._-]+\])?\s*([<>=!~]=?\s*[A-Za-z0-9.*+!_-]+(\s*,\s*[<>=!~]=?\s*[A-Za-z0-9.*+!_-]+)*)?$`)

func (r PageRepo) check(what string) error {
	switch {
	case r.Git == "":
		return fmt.Errorf("%s: the repository ( git )", what)
	case r.Rev != "" && r.Dev:
		return fmt.Errorf("%s: a rev, or dev, not both", what)
	case r.Rev == "" && !r.Dev:
		return fmt.Errorf("%s: a rev ( a commit ), or dev ( a checkout )", what)
	case r.Rev != "" && !aCommit.MatchString(r.Rev):
		return fmt.Errorf("%s.rev: a commit, 7 to 40 hex digits ( got %q )", what, r.Rev)
	case r.Dir != "" && !relative(r.Dir), r.Package != "" && !relative(r.Package):
		return fmt.Errorf("%s: dir and package are paths in the repository", what)
	}
	for _, f := range r.VersionOf {
		if !relative(f) {
			return fmt.Errorf("%s.version_of: %q, a path in the repository", what, f)
		}
	}
	return nil
}

var (
	pageName  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`)
	aCommit   = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	anyField  = regexp.MustCompile(`\{[^{}]*\}`)
	needNames = map[string]bool{"gpus": true, "gpu_ram": true, "ram": true, "cpus": true}
)

// ParsePageDesc: what a page sends -> its description, and the machine it names ( `on`, not of the description )
func ParsePageDesc(data []byte) (PageDesc, string, error) {
	var d PageDesc
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return d, "", fmt.Errorf("a description is one JSON object: %v", err)
	}
	on := ""
	if raw, ok := all["on"]; ok {
		if json.Unmarshal(raw, &on) != nil {
			return d, "", errors.New("on: a machine's id, from .errand/machines")
		}
		delete(all, "on")
	}
	rest, _ := json.Marshal(all)
	dec := json.NewDecoder(bytes.NewReader(rest))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, on, fmt.Errorf("a description that cannot be read: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return d, on, errors.New("a description is one JSON object")
	}
	return d, on, d.Check()
}

func relative(p string) bool {
	return p != "" && !strings.HasPrefix(p, "/") && !strings.Contains(p, "\\") && !hasDots(p)
}

// Check: what can be said of it anywhere
func (d PageDesc) Check() error {
	if !pageName.MatchString(d.Name) {
		return fmt.Errorf("name: letters, digits, _ . - ( got %q )", d.Name)
	}
	if len(d.Run) == 0 {
		return errors.New("run: the words of its command, a list")
	}
	if d.Files != nil {
		if err := d.Files.check("files"); err != nil {
			return err
		}
		if d.Files.Package != "" {
			return errors.New("files: package is python.install's")
		}
	}
	if d.Python != nil {
		if len(d.Python.Install) == 0 {
			return errors.New("python.install: what its env installs, one at least")
		}
		for i, r := range d.Python.Install {
			switch {
			case r.Repo != nil:
				if err := r.Repo.check(fmt.Sprintf("python.install[%d]", i)); err != nil {
					return err
				}
				if r.Repo.Dir != "" {
					return fmt.Errorf("python.install[%d]: dir is files'; package says where the package is", i)
				}
			case !aRequirement.MatchString(r.Pip):
				return fmt.Errorf("python.install[%d]: a requirement of the index, as pip reads it ( sdot==2026.11.3, numpy>=2; got %q )", i, r.Pip)
			}
		}
	}
	for _, w := range d.Run {
		for _, f := range anyField.FindAllString(w, -1) {
			what := f[1 : len(f)-1]
			switch {
			case !describedWord.MatchString(f):
				return fmt.Errorf("run: %s is none of {python}, {file:REL}, {tool:NAME}, {port}, {port:NAME}", f)
			case what == "python" && d.Python == nil:
				return errors.New("run: {python} is its env's: python.install says what is in it")
			case strings.HasPrefix(what, "file:") && d.Files == nil:
				return fmt.Errorf("run: %s is a file of a repository: files says which", f)
			case strings.HasPrefix(what, "file:") && !relative(what[5:]):
				return fmt.Errorf("run: %s: a path in files' dir", f)
			case strings.HasPrefix(what, "tool:") && d.Tools[what[5:]] == nil:
				return fmt.Errorf("run: %s is not in tools", f)
			}
		}
	}
	for k, v := range d.Needs {
		if !needNames[k] {
			return fmt.Errorf("needs: %s is none of gpus, gpu_ram, ram, cpus", k)
		}
		if k == "gpus" {
			if _, _, err := gpuRange(v); err != nil {
				return err
			}
		}
	}
	return nil
}

// Hash: of all it says ( `on` is not of it )
func (d PageDesc) Hash() string {
	b, _ := json.Marshal(d) // ( a map's keys in order: one description, one text )
	return Short("errand-page/1 " + string(b))
}

// Key: how pages and authorizations name it, and its address does: NAME~HASH
func (d PageDesc) Key() string { return d.Name + "~" + d.Hash() }

// SplitPageKey: NAME~HASH -> NAME, HASH
func SplitPageKey(key string) (string, string, bool) {
	name, hash, ok := strings.Cut(key, "~")
	return name, hash, ok && pageName.MatchString(name) && hashWord.MatchString(hash)
}

var hashWord = regexp.MustCompile(`^[0-9a-f]{8}$`)

// ── on a machine ─────────────────────────────────────────────────────────────

// Needed is what a machine lacks for a service, and can be given.
type Needed struct {
	Kind      string   `json:"kind"`  // tool, code, env, store, checkout
	What      string   `json:"what"`  // in full, for the window
	Short     string   `json:"short"` // the idea of it, for the page: "sdot's code, to fetch"
	Source    string   `json:"source,omitempty"`
	Trusted   bool     `json:"trusted"`
	Untrusted []string `json:"untrusted,omitempty"` // the sources errand does not know, a person accepts each
	Sha256    string   `json:"sha256,omitempty"`
	Size      any      `json:"size,omitempty"`
	fetch     func(trust []string) error
}

// Probe is a program whose version no file says: run once, with these words, if a person asks.
type Probe struct {
	Tool string   `json:"tool"`
	Path string   `json:"path"`
	Argv []string `json:"argv"`
}

// Part is one thing a service needs of a machine, as the window shows it: what the machine has of it, or lacks.
type Part struct {
	Kind      string   `json:"kind"` // code, env, tool, machine, store
	Name      string   `json:"name,omitempty"`
	Title     string   `json:"title"`
	State     string   `json:"state"` // ok; todo, errand sets it up when it is authorized; missing, a person is to say something; unavailable
	Says      []string `json:"says"`  // what was found
	Why       string   `json:"why,omitempty"`
	Missing   *Needed  `json:"missing,omitempty"`
	Probes    []Probe  `json:"probes,omitempty"`
	Path      string   `json:"path,omitempty"`      // what a person may set: a tool's program, the checkout
	Described any      `json:"described,omitempty"` // what the description says of it
}

// PageLook: what a machine has for a description, what it lacks, and what it would run
type PageLook struct {
	Key      string            `json:"key"`
	Name     string            `json:"name"`
	Text     string            `json:"text,omitempty"`
	Machine  string            `json:"machine"`
	State    string            `json:"state"` // authorized, ready ( nothing lacks: to authorize ), missing, unavailable
	Why      string            `json:"why,omitempty"`
	Parts    []Part            `json:"parts"`
	Found    []string          `json:"found"`
	Missing  []Needed          `json:"missing"`
	Probes   []Probe           `json:"probes"`
	Command  []string          `json:"command,omitempty"`
	Vars     map[string]string `json:"vars,omitempty"`
	Needs    string            `json:"needs,omitempty"`
	Store    map[string]any    `json:"store"`
	Checkout string            `json:"checkout,omitempty"`
	spec     ServiceSpec
}

// envMade: the file that says a service's env was made, and from what
const envMade = "errand-made.json"

// LookPage: a description, on this machine -- `paths`: places a person gave for its tools; `store`: the view's files, as
// StoreOf says them ( nil: none needed )
func LookPage(d PageDesc, paths map[string][]string, store map[string]any) PageLook {
	key := d.Key()
	l := PageLook{Key: key, Name: d.Name, Text: d.Text, Machine: queue.Host(), Parts: []Part{}, Found: []string{}, Missing: []Needed{}, Probes: []Probe{}, Vars: d.Vars}
	if err := d.Check(); err != nil {
		l.State, l.Why = "unavailable", err.Error()
		return l
	}
	uses := strings.Join(d.Run, "\x00")
	code, python := "", ""
	add := func(p Part) {
		if p.Says == nil {
			p.Says = []string{}
		}
		switch {
		case p.Why != "":
			p.State = "unavailable"
		case p.Missing != nil && (p.Missing.fetch != nil || p.Missing.Kind == "store"):
			p.State = "todo" // ( errand's to do, once: downloaded, built, copied )
		case p.Missing != nil:
			p.State = "missing"
		default:
			p.State = "ok"
		}
		l.Parts = append(l.Parts, p)
	}

	// the checkouts, one part per repository in development
	known := devSources(ReadConfig().Dev)
	checkouts, said := map[string]string{}, map[string]bool{}
	checkout := func(r *PageRepo) string {
		repo := repoURL(r.Git)
		if !said[repo] {
			said[repo] = true
			var found *devCheckout
			for _, c := range known {
				if c.Repo == repo && (found == nil || found.Why != "" && c.Why == "") {
					c := c
					found = &c
				}
			}
			p := Part{Kind: "code", Name: repo, Title: repoName(repo) + ", from a checkout of yours", Described: r}
			short := "a checkout of " + repoName(repo) + ", to say where it is"
			switch {
			case found == nil:
				p.Missing = &Needed{Kind: "checkout", What: fmt.Sprintf("a checkout of %s on %s: where is it? ( errand serve dev %s PATH )", repoName(repo), queue.Host(), strings.TrimPrefix(repo, "https://")),
					Short: short, Source: repo, Trusted: true}
			case found.Why != "":
				p.Path = found.Path
				p.Missing = &Needed{Kind: "checkout", What: "the checkout of " + repoName(repo) + ": " + found.Why, Short: short, Source: repo, Trusted: true}
			default:
				p.Path, checkouts[repo] = found.Path, found.Path
				p.Says = append(p.Says, fmt.Sprintf("%s ( %s )", shortPath(found.Path), found.Origin))
				if l.Checkout == "" {
					l.Checkout = found.Path
				}
			}
			add(p)
		}
		return checkouts[repo]
	}

	// the files
	if f := d.Files; f != nil && strings.Contains(uses, "{file:") {
		repo := repoURL(f.Git)
		if f.Dev {
			if at := checkout(f); at != "" {
				code = filepath.Join(at, filepath.FromSlash(f.Dir))
			}
		} else {
			code = filepath.Join(repoDir(repo, f.Rev), filepath.FromSlash(f.Dir))
			p := Part{Kind: "code", Name: repo, Title: fmt.Sprintf("%s's files at %s", repoName(repo), short7(f.Rev)), Described: f}
			if kit.IsDir(code) {
				p.Says = append(p.Says, "in errand's cache on "+queue.Host())
			} else {
				only := []string{}
				if f.Dir != "" {
					only = append(only, f.Dir)
				}
				p.Missing = &Needed{Kind: "code", What: fmt.Sprintf("the files of %s at %s ( %s ), into errand's cache", repoName(repo), short7(f.Rev), orRoot(f.Dir)),
					Short: repoName(repo) + "'s files, to fetch", Source: repo, Trusted: trusted(repo), fetch: func(trust []string) error {
						_, err := fetchRepository(repo, f.Rev, only, trust)
						return err
					}}
			}
			add(p)
		}
	}

	// the env: all of it in one pip install ( a repository given provides its package: the index's is not taken )
	if py := d.Python; py != nil && strings.Contains(uses, "{python}") {
		index := py.Index
		if index == "" {
			index = pypi
		}
		var words [][]string
		var shown, untrusted []string
		ready := true
		for _, r := range py.Install {
			switch {
			case r.Repo == nil:
				words, shown = append(words, []string{r.Pip}), append(shown, r.Pip)
			case r.Repo.Dev:
				at := checkout(r.Repo)
				if at == "" {
					ready = false
					continue
				}
				at = filepath.Join(at, filepath.FromSlash(r.Repo.Package))
				words, shown = append(words, []string{"-e", at}), append(shown, "-e "+shortPath(at))
			default:
				repo := repoURL(r.Repo.Git)
				spec := "git+" + repo + "@" + r.Repo.Rev
				if r.Repo.Package != "" {
					spec += "#subdirectory=" + r.Repo.Package
				}
				words, shown = append(words, []string{spec}), append(shown, repoName(repo)+" at "+short7(r.Repo.Rev))
				if !trusted(repo) {
					untrusted = append(untrusted, repo)
				}
			}
		}
		if !trusted(index) {
			untrusted = append(untrusted, index)
		}
		prefix := serviceEnv(key)
		python = filepath.Join(prefix, "bin", "python")
		if p := pythonIn(prefix); p != "" {
			python = p
		}
		made, _ := json.Marshal(words)
		p := Part{Kind: "env", Title: "its Python env", Described: py}
		switch {
		case !ready:
			p.Missing = &Needed{Kind: "env", What: "an env of its own, built once errand knows the checkouts above", Short: "a Python env of its own, to build"}
		case pythonIn(prefix) != "" && kit.Text(ObjectOf(readJSONFile(filepath.Join(prefix, envMade)))["installed"]) == string(made):
			p.Says = append(p.Says, fmt.Sprintf("%s, with %s", shortPath(prefix), strings.Join(shown, ", ")))
		default:
			source := index
			if len(untrusted) > 0 {
				source = untrusted[0]
			}
			p.Missing = &Needed{Kind: "env", What: fmt.Sprintf("an env of its own, %s, with %s; the rest from %s", shortPath(prefix), strings.Join(shown, ", "), index),
				Short: "a Python env of its own, to build", Source: source, Untrusted: untrusted, Trusted: len(untrusted) == 0, fetch: func(trust []string) error {
					if _, err := fetchEnv(key, words, index, trust); err != nil {
						return err
					}
					return kit.WriteWholeJSON(filepath.Join(prefix, envMade), map[string]any{"installed": string(made), "index": index, "at": time.Now().Format(stamp)})
				}}
		}
		add(p)
	}

	// the tools
	tools := map[string]string{}
	for _, name := range sortedKeys(d.Tools) {
		if !strings.Contains(uses, "{tool:"+name+"}") {
			continue
		}
		t := toolsHere(map[string]any{name: d.Tools[name]}, paths)[name]
		p := Part{Kind: "tool", Name: name, Title: name, Path: t.path, Described: d.Tools[name]}
		least := ""
		if t.min != "" {
			least = " " + t.min + " or later"
			p.Title += least
		}
		for _, c := range t.candidates {
			switch {
			case c.path == t.path:
				p.Says = append(p.Says, fmt.Sprintf("%s · %s", orUnknown(c.version), c.path))
			case !c.known:
				p.Says = append(p.Says, fmt.Sprintf("%s · its version is said by no file", c.path))
				p.Probes = append(p.Probes, Probe{Tool: name, Path: c.path, Argv: []string{c.path, "--version"}})
			default:
				p.Says = append(p.Says, fmt.Sprintf("%s · %s · too old", c.version, c.path))
			}
		}
		if t.path != "" {
			tools[name] = t.path
			add(p)
			continue
		}
		url, sha, run := kit.Text(t.fetch["url"]), kit.Text(t.fetch["sha256"]), kit.Text(t.fetch["run"])
		if url == "" || sha == "" || run == "" {
			p.Why = fmt.Sprintf("no %s%s found on %s, and none to download for %s: give its path", name, least, queue.Host(), platformName())
			add(p)
			continue
		}
		tools[name] = filepath.Join(toolDir(sha), filepath.FromSlash(run))
		version := kit.Text(t.fetch["version"])
		p.Missing = &Needed{Kind: "tool", What: strings.TrimSpace(name+" "+version) + ", downloaded into errand's cache", Short: strings.TrimSpace(name+" "+version) + ", to download",
			Source: url, Trusted: trusted(url), Sha256: sha, Size: t.fetch["size"], fetch: func(trust []string) error {
				_, err := fetchFile(url, sha, trust)
				return err
			}}
		add(p)
	}

	// what it runs
	for _, w := range d.Run {
		l.Command = append(l.Command, describedWord.ReplaceAllStringFunc(w, func(f string) string {
			what := f[1 : len(f)-1]
			switch {
			case what == "python":
				return python
			case strings.HasPrefix(what, "file:"):
				if code == "" {
					return f
				}
				return filepath.Join(code, filepath.FromSlash(what[5:]))
			case strings.HasPrefix(what, "tool:"):
				if tools[what[5:]] == "" {
					return f
				}
				return tools[what[5:]]
			}
			return f
		}))
	}
	if code != "" && kit.IsDir(code) {
		for _, w := range d.Run {
			for _, f := range describedWord.FindAllString(w, -1) {
				if rel := strings.TrimSuffix(strings.TrimPrefix(f, "{file:"), "}"); strings.HasPrefix(f, "{file:") && !kit.Exists(filepath.Join(code, filepath.FromSlash(rel))) {
					for i := range l.Parts {
						if l.Parts[i].Kind == "code" {
							l.Parts[i].Why, l.Parts[i].State = fmt.Sprintf("%s is not in %s", rel, shortPath(code)), "unavailable"
						}
					}
				}
			}
		}
	}

	// what it asks of the machine
	spec := ServiceSpec{Name: d.Name, Label: d.Name, Command: l.Command, Env: map[string]string{}, Needs: map[string]any{}, Version: d.Hash(),
		Busy: d.Busy, Ready: d.Ready, Idle: float64(d.Idle), Grace: float64(d.Grace)}
	for k, v := range d.Vars {
		spec.Env[k] = v
	}
	for k, v := range d.Needs {
		spec.Needs[k] = fmt.Sprint(v)
	}
	m := Part{Kind: "machine", Described: d.Needs} // ( said only when the machine cannot give it )
	if v, ok := d.Needs["gpus"]; ok {              // ( a range: what this machine has, within it )
		least, most, _ := gpuRange(v)
		cards := queue.Machine().GPUs
		n := min(most, cards)
		switch {
		case n < least:
			m.Title = fmt.Sprintf("%d card%s at least", least, map[bool]string{true: "", false: "s"}[least == 1])
			m.Why = fmt.Sprintf("%s has %d errand counts", queue.Host(), cards)
		case n == 0:
			delete(spec.Needs, "gpus")
			delete(spec.Needs, "gpu_ram")
		default:
			spec.Needs["gpus"] = fmt.Sprint(n)
		}
	}
	if code != "" && kit.IsDir(code) {
		spec.Cwd = code
	}
	for _, r := range d.Repos() { // ( a checkout's files that make another server: patterns, read again each time )
		if at := checkouts[repoURL(r.Git)]; r.Dev && at != "" {
			for _, f := range r.VersionOf {
				spec.VersionOf = append(spec.VersionOf, filepath.Join(at, filepath.FromSlash(f)))
			}
		}
	}
	if spec.Idle == 0 {
		spec.Idle = 600
	}
	spec.Complete()
	l.Needs = spec.NeedsText()
	if why := spec.TooMuch(queue.Machine()); why != "" && m.Why == "" {
		m.Title, m.Why = "more than "+queue.Host()+" has", why
	}
	if m.Why != "" {
		add(m)
	}
	l.spec = spec

	// the view's files
	if store != nil {
		l.Store = storeSeen(store)
		p := Part{Kind: "store", Title: "the view's files"}
		if path := kit.Text(l.Store["path"]); path != "" {
			p.Says = append(p.Says, fmt.Sprintf("seen there ( %s ): %s", kit.Text(l.Store["seen"]), path))
		} else {
			p.Missing = &Needed{Kind: "store", What: fmt.Sprintf("the view's files, %v files, copied into errand's cache here, and kept up to date while the view is open", l.Store["files"]),
				Short: "the view's files, to copy", Size: l.Store["size"], Trusted: true}
		}
		add(p)
	}

	// as a whole
	l.State = "ready"
	for _, p := range l.Parts {
		l.Found = append(l.Found, p.Says...)
		l.Probes = append(l.Probes, p.Probes...)
		if p.Missing != nil {
			l.Missing = append(l.Missing, *p.Missing)
			if l.State == "ready" {
				l.State = "missing"
			}
		}
		if p.Why != "" && l.State != "unavailable" {
			l.State, l.Why = "unavailable", p.Why
		}
	}
	if PageAuthorized()[key] != nil {
		l.State = "authorized"
	}
	return l
}

var aRange = regexp.MustCompile(`^\s*(\d+)\s*-\s*(\d+)\s*$`)

// gpuRange: `1` ( that many ), or `"0-1"` ( as many as the machine has, between them )
func gpuRange(v any) (int, int, error) {
	bad := fmt.Errorf(`needs.gpus: a number, or "0-1" ( as many as the machine has, up to 1; got %v )`, v)
	switch x := v.(type) {
	case float64:
		if x < 0 || x != float64(int(x)) {
			return 0, 0, bad
		}
		return int(x), int(x), nil
	case string:
		if m := aRange.FindStringSubmatch(x); m != nil {
			a, _ := strconv.Atoi(m[1])
			b, _ := strconv.Atoi(m[2])
			if a <= b {
				return a, b, nil
			}
		}
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil && n >= 0 {
			return n, n, nil
		}
	case int:
		return x, x, nil
	}
	return 0, 0, bad
}

func orRoot(dir string) string {
	if dir == "" {
		return "the whole of it"
	}
	return dir
}

func orUnknown(v string) string {
	if v == "" {
		return "( version unknown )"
	}
	return v
}

func readJSONFile(path string) any {
	var out any
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

// AuthorizePage: a description given to pages on this machine, by a person -- what lacks fetched first ( `trust`: the
// sources the person accepted beside errand.sources ); the view's files are the caller's to copy. -> what it is now.
func AuthorizePage(d PageDesc, paths map[string][]string, trust []string, say func(string)) (PageLook, error) {
	l := LookPage(d, paths, nil)
	if l.State == "unavailable" {
		return l, errors.New(l.Why)
	}
	for _, n := range l.Missing {
		if n.fetch == nil {
			continue
		}
		for _, src := range append(n.Untrusted, map[bool]string{true: "", false: n.Source}[n.Trusted || len(n.Untrusted) > 0]) {
			if src != "" && !trustedBy(src, trust) {
				return l, fmt.Errorf("%s: an unknown source, not accepted", src)
			}
		}
		say("fetching " + n.What)
		if err := n.fetch(append(append(trust, n.Source), n.Untrusted...)); err != nil {
			return l, fmt.Errorf("%s: %v", n.What, err)
		}
	}
	if l = LookPage(d, paths, nil); l.State == "unavailable" || len(l.Missing) > 0 {
		why := l.Why
		for _, n := range l.Missing {
			why = strings.TrimSpace(why + "; still missing: " + n.What)
		}
		return l, errors.New(why)
	}
	how := Authorization{Page: l.Key, Described: ObjectOf(toAny(d)), Dev: l.Checkout}
	if _, err := Authorize(l.spec, "pages", how); err != nil {
		return l, err
	}
	l.State = "authorized"
	return l, nil
}

func trustedBy(source string, trust []string) bool {
	for _, t := range trust {
		if startsWithSource(source, t) {
			return true
		}
	}
	return false
}

func toAny(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// ── the view's files ─────────────────────────────────────────────────────────

// StoreOf: a view's files as another machine is told of them, to say whether it sees them: their paths, a few files with
// their sizes, all of their size, where errand's copy goes
func StoreOf(dir, vid string) map[string]any {
	var size int64
	files := 0
	_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err == nil && e.Type().IsRegular() {
			if st, err := e.Info(); err == nil {
				size += st.Size()
				files++
			}
		}
		return nil
	})
	check := [][]any{}
	entries, _ := os.ReadDir(dir)
	names := []string{}
	for _, e := range entries {
		if e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, rel := range names[:min(len(names), 4)] {
		if st, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			check = append(check, []any{rel, st.Size()})
		}
	}
	store := map[string]any{"paths": []string{dir}, "original": false, "copy": nil, "check": check, "size": size, "files": files}
	if vid != "" {
		store["copy"] = "views/" + vid
	}
	return ObjectOf(toAny(store)) // ( as it is read, once sent )
}

// StoreSeen: where this machine sees a view's files; "" when it does not
func StoreSeen(store map[string]any) string { return kit.Text(storeSeen(store)["path"]) }

// SameRepo: whether two names of repositories are one
func SameRepo(a, b string) bool { return a != "" && repoURL(a) == repoURL(b) }

// DeclareCheckout: a checkout of a repository stands for it on this machine, in `env` ( ADR 0028 dev_sources ) -- what
// `errand serve dev REPO PATH --env ENV` writes
func DeclareCheckout(repo, path, env string) error {
	at := atHome(path)
	if !kit.IsDir(at) {
		return fmt.Errorf("%s: no such directory on %s", path, queue.Host())
	}
	key := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(httpsURL(repo), "https://"), "http://"), ".git")
	return ChangeConfig(func(c *Config) { c.Dev[key] = DevSource{Path: path, Env: env} })
}

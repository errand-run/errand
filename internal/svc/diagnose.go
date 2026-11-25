// What a machine has for the services a page asks for ( ADR 0027 §4-5 ): `errand serve look`, and what a page shows.
//
// It reads files and runs nothing of what it looks at: the declarations, the envs and the packages that describe
// services, the tools and their versions, the cards, and whether the store is seen. From that, a state per service and the
// LINES that would make it ready here, each with what it does; a person types them.
//
// A description a store carries is used to look and to PROPOSE; to run, only a package of this machine counts.

package svc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
)

// ── versions, from files ─────────────────────────────────────────────────────

var digits = regexp.MustCompile(`\d+`)

// meets: whether a version is `least` or later -> and whether that can be said
func meets(v, least string) (bool, bool) {
	if least == "" {
		return true, true
	}
	if v == "" {
		return false, false
	}
	a, b := digits.FindAllString(v, 4), digits.FindAllString(least, 4)
	for i := 0; i < len(a) || i < len(b); i++ {
		if i >= len(a) {
			return false, true
		}
		if i >= len(b) {
			return true, true
		}
		x, _ := strconv.Atoi(a[i])
		y, _ := strconv.Atoi(b[i])
		if x != y {
			return x > y, true
		}
	}
	return true, true
}

func dpkgVersion(pkg, at string) string {
	if !strings.HasPrefix(at, "/usr/") {
		return ""
	}
	data, _ := os.ReadFile("/var/lib/dpkg/status")
	for _, block := range strings.Split(string(data), "\n\n") {
		version, found := "", false
		for _, line := range strings.Split(block, "\n") {
			found = found || line == "Package: "+pkg
			if v, ok := strings.CutPrefix(line, "Version: "); ok {
				if _, after, epoch := strings.Cut(v, ":"); epoch {
					v = after
				}
				version, _, _ = strings.Cut(v, "-")
			}
		}
		if found && strings.Contains(block, "Status: install ok installed") {
			return version
		}
	}
	return ""
}

func snapVersion(name, at string) string {
	if !strings.HasPrefix(at, "/snap/") {
		return ""
	}
	data, _ := os.ReadFile("/snap/" + name + "/current/meta/snap.yaml")
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "version:"); ok {
			return strings.Trim(strings.TrimSpace(v), `'"`)
		}
	}
	return ""
}

// plistValue: a text of a property list
func plistValue(file, key string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	if strings.HasPrefix(string(data), "bplist") {
		if data, err = exec.Command("plutil", "-convert", "xml1", "-o", "-", file).Output(); err != nil {
			return ""
		}
	}
	m := regexp.MustCompile(`<key>` + regexp.QuoteMeta(key) + `</key>\s*<string>([^<]*)</string>`).FindSubmatch(data)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// programVersion: the version of a program, from files only -> and how it is known
func programVersion(at string, rules []any, fetched map[string]string) (string, string) {
	real, err := filepath.EvalSymlinks(at)
	if err != nil {
		real = at
	}
	if known := ObjectOf(caches.ReadObject(probesFile())[real]); known != nil && kit.Text(known["stamp"]) == fileStamp(real) {
		return kit.Text(known["version"]), kit.Text(known["how"])
	}
	tools, _ := filepath.EvalSymlinks(filepath.Join(kit.LocalCache(), "tools"))
	if rel, err := filepath.Rel(tools, real); err == nil && tools != "" && filepath.IsLocal(rel) {
		sha, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
		if v := fetched[sha]; v != "" {
			return v, "fetched by errand"
		}
	}
	for _, x := range rules {
		r := ObjectOf(x)
		switch {
		case r["plist"] != nil:
			file := filepath.Join(filepath.Dir(real), filepath.FromSlash(kit.Text(r["plist"])))
			key := kit.Text(r["key"])
			if key == "" {
				key = "CFBundleShortVersionString"
			}
			if v := plistValue(file, key); v != "" {
				return v, "Info.plist ( " + file + " )"
			}
		case r["dirname"] != nil:
			re, err := regexp.Compile(kit.Text(r["dirname"]))
			if err != nil {
				continue
			}
			parts := strings.Split(filepath.ToSlash(real), "/")
			for i := len(parts) - 1; i >= 0; i-- {
				if m := re.FindStringSubmatch(parts[i]); len(m) > 1 {
					return m[1], "its directory's name ( " + parts[i] + " )"
				}
			}
		case r["dpkg"] != nil:
			if v := dpkgVersion(kit.Text(r["dpkg"]), at); v != "" {
				return v, "dpkg ( package " + kit.Text(r["dpkg"]) + " )"
			}
		case r["snap"] != nil:
			if v := snapVersion(kit.Text(r["snap"]), real); v != "" {
				return v, "snap ( " + kit.Text(r["snap"]) + " )"
			}
		}
	}
	return "", ""
}

// ── tools ────────────────────────────────────────────────────────────────────

type toolFound struct {
	path, version, how string
	ok, known          bool
}

type toolState struct {
	path       string // the first that will do; "" when none does
	min        string
	candidates []toolFound
	fetch      map[string]any
}

func onPlatform(pattern string) bool {
	ok, _ := path.Match(pattern, platformName())
	return ok
}

func toolCandidates(desc map[string]any, given []string) []toolFound {
	var out []toolFound
	seen := map[string]bool{}
	fetched := map[string]string{}
	fetches := ObjectOf(desc["fetch"])
	for _, k := range sortedKeys(fetches) {
		f := ObjectOf(fetches[k])
		fetched[strings.ToLower(kit.Text(f["sha256"]))] = kit.Text(f["version"])
	}
	add := func(at, version string) {
		real, err := filepath.EvalSymlinks(at)
		if err != nil || seen[real] {
			return
		}
		st, err := os.Stat(real)
		if err != nil || !st.Mode().IsRegular() || runtime.GOOS != "windows" && st.Mode()&0o111 == 0 {
			return
		}
		seen[real] = true
		c := toolFound{path: at, version: version, how: "given"}
		if version == "" {
			c.version, c.how = programVersion(at, caches.ListOf(desc["version_from"]), fetched)
		}
		c.ok, c.known = meets(c.version, kit.Text(desc["min"]))
		out = append(out, c)
	}
	home, _ := os.UserHomeDir()
	for _, g := range given {
		at, v, _ := strings.Cut(g, "@")
		if strings.HasPrefix(at, "~/") {
			at = filepath.Join(home, at[2:])
		}
		add(at, v)
	}
	looks := kit.Strs(desc["look"])
	if by := ObjectOf(desc["look"]); by != nil {
		for _, pattern := range sortedKeys(by) {
			if onPlatform(pattern) {
				looks = append(looks, kit.Strs(by[pattern])...)
			}
		}
	}
	for _, c := range looks {
		if filepath.IsAbs(c) {
			add(c, "")
		} else if p, err := exec.LookPath(c); err == nil {
			add(p, "")
		}
	}
	for _, k := range sortedKeys(fetches) {
		f := ObjectOf(fetches[k])
		if sha, run := kit.Text(f["sha256"]), kit.Text(f["run"]); sha != "" && run != "" {
			add(filepath.Join(toolDir(sha), filepath.FromSlash(run)), "")
		}
	}
	return out
}

// toolsHere: each tool a description asks for, where it may be here, and the first that will do
func toolsHere(tools map[string]any, given map[string][]string) map[string]toolState {
	out := map[string]toolState{}
	for name, x := range tools {
		desc := ObjectOf(x)
		s := toolState{min: kit.Text(desc["min"]), candidates: toolCandidates(desc, given[name]), fetch: ObjectOf(ObjectOf(desc["fetch"])[platformName()])}
		for _, c := range s.candidates {
			if c.ok && s.path == "" {
				s.path = c.path
			}
		}
		out[name] = s
	}
	return out
}

// ── the machine ──────────────────────────────────────────────────────────────

func cardsHere() []any {
	out := []any{}
	for _, x := range kit.GpusNow() {
		g := ObjectOf(x)
		out = append(out, map[string]any{"name": g["name"], "kind": "cuda", "total": g["total"], "used": g["used"]})
	}
	if len(out) == 0 && runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		out = append(out, map[string]any{"name": "Apple GPU", "kind": "metal"})
	}
	return out
}

// storeSeen: where this machine sees a store: one of its paths, holding the files it is checked by; errand's copy; or not
func storeSeen(store map[string]any) map[string]any {
	if len(store) == 0 {
		return map[string]any{"seen": "none"}
	}
	same := func(root string) bool {
		if !kit.IsDir(root) {
			return false
		}
		for _, x := range caches.ListOf(store["check"]) {
			pair := caches.ListOf(x)
			if len(pair) != 2 {
				return false
			}
			st, err := os.Stat(filepath.Join(root, filepath.FromSlash(kit.Text(pair[0]))))
			if err != nil || float64(st.Size()) != kit.Num(pair[1], -1) {
				return false
			}
		}
		return true
	}
	for i, p := range kit.Strs(store["paths"]) {
		if same(p) {
			seen := "shared"
			if i == 0 && store["original"] == true {
				seen = "original"
			}
			return map[string]any{"seen": seen, "path": p}
		}
	}
	if c := kit.Text(store["copy"]); c != "" && filepath.IsLocal(c) {
		if root := filepath.Join(kit.LocalCache(), filepath.FromSlash(c)); same(root) {
			return map[string]any{"seen": "copy", "path": root}
		}
	}
	return map[string]any{"seen": "none", "size": store["size"], "files": store["files"]}
}

// ── a service, here ──────────────────────────────────────────────────────────

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}

var bareField = regexp.MustCompile(`^\{port(:[a-z][a-z0-9_]*)?\}$`)

// typedWords: words as a person types them -- quoted for a shell, `{port}` as it is, paths in full ( a `~` typed here
// would be this machine's home, not the one over there )
func typedWords(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		if out[i] = w; !bareField.MatchString(w) {
			out[i] = quoteAll([]string{w})
		}
	}
	return strings.Join(out, " ")
}

func short7(rev string) string { return rev[:min(len(rev), 7)] }

func repoName(from string) string {
	return strings.TrimSuffix(path.Base(strings.TrimRight(from, "/")), ".git")
}

// madeWith: what a page was made with, in words
func madeWith(want AskedService) string {
	if git, rev := kit.Text(want.Source["git"]), kit.Text(want.Source["rev"]); git != "" && rev != "" {
		out := repoName(git) + " at commit " + short7(rev)
		if want.Source["dirty"] == true {
			out += " plus changes not committed"
		}
		return out
	}
	if name := kit.Text(want.Package["name"]); name != "" {
		return strings.TrimSpace(name + " " + kit.Text(want.Package["version"]))
	}
	return "its own version"
}

type proposedLine struct {
	Comment     string `json:"comment"`
	Command     string `json:"command"`
	DescribedBy string `json:"described_by"`
	Unknown     bool   `json:"unknown_source"`
}

// serviceLook is what is said of one service here
type serviceLook struct {
	Key     string         `json:"key"`
	State   string         `json:"state"`
	Why     string         `json:"why"`
	Summary string         `json:"summary"`
	Found   []string       `json:"found"`
	Missing []string       `json:"missing"` // what stands between it and ready, first
	Lines   []proposedLine `json:"lines"`
	Dev     *devLook       `json:"dev,omitempty"`
	DevWhy  string         `json:"dev_error,omitempty"`
}

// summary: one or two sentences -- where the service stands, and what the lines do -- in words that need no knowledge of
// errand's insides
func (l serviceLook) summary(want AskedService, machine string) string {
	made, todo := madeWith(want), ""
	switch n := len(l.Lines); {
	case n == 1:
		todo = " The line below does it; the page runs nothing itself."
	case n > 1:
		todo = fmt.Sprintf(" The %d lines below do it; the page runs nothing itself.", n)
	}
	switch l.State {
	case "running":
		return "Running on " + machine + "."
	case "ready":
		return "Ready on " + machine + ": it starts when the page needs it."
	case "installable":
		return fmt.Sprintf("Not set up on %s yet. This page was made with %s: that code is fetched, then the service declared.%s", machine, made, todo)
	case "unverified":
		if todo == "" {
			todo = " Fetch that code, then declare it again."
		}
		return fmt.Sprintf("Declared on %s by hand, but errand holds no code of the version this page needs ( %s ) to check that declaration against.%s", machine, made, todo)
	case "wrong version":
		return fmt.Sprintf("Declared on %s, but its code changed since: it is not the version this page was made with ( %s ).%s", machine, made, todo)
	case "unavailable":
		return fmt.Sprintf("Cannot run on %s: %s.", machine, l.Why)
	case "unknown":
		return fmt.Sprintf("errand cannot tell yet whether %s can run it: %s.%s", machine, l.Why, todo)
	}
	return l.Why
}

// looker is one look of a machine: what it needs once for all the services asked about
type looker struct {
	machine   string
	given     map[string][]string
	cards     []any
	declared  map[string]ServiceSpec
	running   map[string]string // the services the agents of this machine hold, and how each is
	providers []provider
	dev       map[string]devSource
}

func sayNumber(v any) string {
	switch x := v.(type) {
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

func (l *looker) service(name string, want AskedService, describedBy string) serviceLook {
	out := serviceLook{Key: want.Key(name), Found: []string{}, Missing: []string{}, Lines: []proposedLine{}}
	l.fill(&out, name, want, describedBy)
	out.Summary = out.summary(want, l.machine)
	dev, err := l.devLook(name, want)
	if out.Dev = dev; err != nil {
		out.DevWhy = err.Error()
	}
	return out
}

func (l *looker) fill(out *serviceLook, name string, want AskedService, describedBy string) {
	key, v := out.Key, want.Version
	stop := func(state, why string) { out.State, out.Why = state, why }
	found := func(format string, args ...any) { out.Found = append(out.Found, fmt.Sprintf(format, args...)) }
	missing := func(format string, args ...any) { out.Missing = append(out.Missing, fmt.Sprintf(format, args...)) }

	// the declarations of this name
	var mine *ServiceSpec
	for _, k := range sortedKeys(l.declared) {
		s := l.declared[k]
		n, kv := splitKey(k)
		if n != name {
			continue
		}
		if kv == v {
			mine = &s
		}
		line := fmt.Sprintf("%s declared ( %s )", k, s.Source)
		if bad := WrongVersion(k, s.Provider); bad != "" {
			line += " — " + bad
		}
		if st := l.running[k]; st != "" {
			line += ", " + st
		}
		found("%s", line)
	}

	// the packages that describe it
	var provs, exact []provider
	gives := map[string]bool{}
	for _, p := range l.providers {
		if p.Entry(name) == nil {
			continue
		}
		provs = append(provs, p)
		got := p.gives(name)
		gives["#"+got] = true
		where := fmt.Sprintf("%s %s in %s", p.Package, p.Version, p.Env)
		if p.Editable != "" {
			where += " ( editable: " + p.Editable + " )"
		}
		switch {
		case got == v && v != "":
			where += fmt.Sprintf(" — provides %s#%s, the version this store asks for", name, got)
		case v != "":
			where += fmt.Sprintf(" — provides %s#%s, the store asks #%s", name, got, v)
		default:
			where += fmt.Sprintf(" — provides %s#%s", name, got)
		}
		found("%s", where)
		if v == "" || got == v {
			exact = append(exact, p)
		}
	}
	git, rev := kit.Text(want.Source["git"]), kit.Text(want.Source["rev"])
	if len(provs) == 0 {
		tail := ""
		switch {
		case git != "":
			tail = fmt.Sprintf(" ( the store was written from %s at %s", git, rev[:min(len(rev), 12)])
			if want.Source["dirty"] == true {
				tail += ", with changes not committed"
			}
			tail += " )"
		case kit.Text(want.Package["name"]) != "":
			tail = fmt.Sprintf(" ( the store was written by %s %s )", kit.Text(want.Package["name"]), kit.Text(want.Package["version"]))
		}
		found("no service env of errand's here has %s%s", name, tail)
	}
	if len(exact) == 0 {
		tail := ""
		if len(provs) > 0 {
			tail = " ( here: " + strings.Join(sortedKeys(gives), ", ") + " )"
		}
		missing("a package on %s that provides %s%s", l.machine, key, tail)
	}

	// its description: of a package here that provides it, else the one that came with the question
	entry := want.Described
	if len(exact) > 0 {
		entry, describedBy = exact[0].Entry(name), "this machine"
	}
	needs := ObjectOf(entry["needs"])
	if plats := kit.Strs(needs["platforms"]); len(plats) > 0 { // needs that nothing typed can meet
		ok := false
		for _, p := range plats {
			ok = ok || onPlatform(p)
		}
		if !ok {
			stop("unavailable", platformName()+": not one of "+strings.Join(plats, ", "))
			return
		}
	}
	gpus := sayNumber(needs["gpus"])
	if n, _ := strconv.Atoi(gpus); n > 0 {
		kind, fit := kit.Text(needs["gpu_kind"]), 0
		for _, x := range l.cards {
			if kind == "" || kind == "any" || kit.Text(ObjectOf(x)["kind"]) == kind {
				fit++
			}
		}
		if fit < n {
			what := gpus + " card(s)"
			if kind != "" && kind != "any" {
				what += " of kind " + kind
			}
			missing("%s", what)
			if fit == 0 {
				stop("unavailable", "no card")
			} else {
				stop("unavailable", fmt.Sprintf("%d card(s), %d needed", fit, n))
			}
			return
		}
	} else {
		gpus = ""
	}

	// the tools
	tools := toolsHere(ObjectOf(entry["tools"]), l.given)
	names := sortedKeys(tools)
	paths, fetches := map[string]string{}, map[string]map[string]any{}
	var unknown []toolFound
	var blocked []string
	for _, t := range names {
		f := tools[t]
		if len(f.candidates) == 0 {
			found("no %s on the PATH, nor in its usual places, nor in errand's cache", t)
		}
		var bad []string
		undecided := false
		for _, c := range f.candidates {
			line := shortPath(c.path) + " · "
			if c.version == "" {
				line += "version unknown"
			} else {
				line += c.version
			}
			if c.how != "" && c.version != "" {
				line += " ( " + c.how + " )"
			}
			switch {
			case !c.known:
				undecided = true
				line += " — its version cannot be read from its files"
			case !c.ok:
				bad = append(bad, c.version)
				line += " — this service needs " + f.min + " or later"
			}
			found("%s", line)
		}
		if paths[t] = f.path; f.path != "" {
			continue
		}
		delete(paths, t)
		line := t
		if f.min != "" {
			line += " " + f.min + " or later"
		}
		if len(bad) > 0 {
			line += " ( here: " + strings.Join(bad, ", ") + " )"
		} else if undecided {
			line += " ( or its version: it cannot be read from its files )"
		}
		missing("%s", line)
		for _, c := range f.candidates {
			if !c.known {
				unknown = append(unknown, c)
			}
		}
		switch {
		case f.fetch != nil:
			fetches[t] = f.fetch
		case undecided:
			blocked = append(blocked, t)
		default:
			stop("unavailable", "no "+t+" for "+platformName())
			return
		}
	}

	// its state
	unverified := false
	if mine != nil {
		bad := WrongVersion(key, mine.Provider)
		switch {
		case bad != "":
			missing("%s as it was declared: %s", key, bad)
			stop("wrong version", bad)
		case kit.Text(mine.Provider) == "" && mine.Source == "installed": // nothing here to check it against
			unverified = true
		default:
			if stop("ready", ""); l.running[key] != "" {
				stop("running", "")
			}
			return
		}
	} else {
		missing("the declaration of %s on %s", key, l.machine)
	}

	// the lines
	propose := func(comment, command, from string) {
		out.Lines = append(out.Lines, proposedLine{comment, command, describedBy, from != "" && !trusted(from)})
	}
	for _, c := range unknown {
		propose(fmt.Sprintf("runs %s --version once, on %s, to read its version", shortPath(c.path), l.machine),
			fmt.Sprintf("errand serve probe --on %s -- %s", l.machine, typedWords([]string{c.path, "--version"})), "")
	}
	if len(blocked) > 0 { // only a version that cannot be read stands in the way
		stop("unknown", fmt.Sprintf("the version of %s cannot be read from its files: probe it, or give it ( a path with @VERSION )", strings.Join(blocked, ", ")))
		return
	}
	for _, t := range sortedKeys(fetches) {
		f := fetches[t]
		from, sha := kit.Text(f["url"]), strings.ToLower(kit.Text(f["sha256"]))
		if from == "" || len(sha) < 8 {
			continue
		}
		host := from
		if parts := strings.Split(from, "/"); len(parts) > 2 {
			host = parts[2]
		}
		size := " ("
		if n := kit.Num(f["size"], 0); n > 0 {
			size = " ( " + caches.Human(n) + ","
		}
		propose(fmt.Sprintf("%s %s from %s into errand's cache on %s%s checked against its sha256 %s…%s ); nothing in it is run", t, kit.Text(f["version"]), host, l.machine, size, sha[:4], sha[len(sha)-4:]),
			fmt.Sprintf("errand serve fetch --on %s %s#sha256=%s", l.machine, from, sha), from)
		paths[t] = filepath.Join(toolDir(sha), filepath.FromSlash(kit.Text(f["run"])))
	}
	flags, what := "", ""
	if gpus != "" {
		flags, what = " --gpus "+gpus, ", on "+gpus+" card"
		if gpus != "1" {
			what += "s"
		}
	}
	if idle := sayNumber(entry["idle"]); idle != "" {
		flags, what = flags+" --idle "+idle, what+", stopped after "+idle+" unused"
	}
	declared := false
	if len(exact) > 0 {
		p := exact[0]
		words, err := p.compose(name, paths, true)
		switch {
		case err == nil:
			propose(fmt.Sprintf("declare the service, as %s %s in %s runs it%s", p.Package, p.Version, filepath.Base(p.Env), what),
				fmt.Sprintf("errand serve install --on %s%s %s -- %s", l.machine, flags, key, typedWords(words)), "")
			declared = true
		case git == "" && kit.Text(want.Package["name"]) == "":
			stop("unavailable", err.Error())
			return
		case len(want.Described) > 0: // what is here cannot run it ( a repository's files, no Python ): fetched as for none
			entry = want.Described
		}
	}
	if !declared {
		if err := l.fetching(name, key, entry, want, flags, what, paths, propose); err != nil {
			stop("unavailable", err.Error())
			return
		}
	}
	switch {
	case unverified:
		stop("unverified", "declared by hand: no package here provides this version")
	case out.State != "wrong version":
		stop("installable", "")
	}
}

// pathsOf: what a service with no Python needs of its repository -- its directory, and the files out of it that its
// version is computed over
func pathsOf(dir string, entry map[string]any) []string {
	out := []string{strings.Trim(dir, "/")}
	for _, rel := range kit.Strs(entry["version_of"]) {
		p := path.Join(dir, rel)
		if !strings.HasPrefix(p, "..") && p != out[0] && !strings.HasPrefix(p, out[0]+"/") {
			out = append(out, p)
		}
	}
	return out
}

// fetching: the lines that bring the code of a service here, then declare it ( ADR 0027, amendment ) -- from the
// repository at the store's commit, into the service's own env, or, for a service with no Python, its files only
func (l *looker) fetching(name, key string, entry map[string]any, want AskedService, flags, what string, tools map[string]string, propose func(comment, command, from string)) error {
	python := false
	for _, w := range kit.Strs(entry["run"]) {
		python = python || strings.Contains(w, "{python}")
	}
	env, src, pkg := serviceEnv(key), want.Source, want.Package
	install := func(words []string) string {
		c := fmt.Sprintf("errand serve install --on %s%s %s", l.machine, flags, key)
		if words != nil {
			c += " -- " + typedWords(words)
		}
		return c
	}
	asks := "declare the service" + what + ": errand shows the command it runs, then asks"
	if kit.Text(src["git"]) != "" && kit.Text(src["rev"]) != "" {
		from, rev, dir := httpsURL(kit.Text(src["git"])), kit.Text(src["rev"]), kit.Text(src["dir"])
		short, dirty := repoName(from)+" at commit "+short7(rev), src["dirty"] == true
		sub := ""
		if root := kit.Text(src["root"]); root != "" {
			sub = "#subdirectory=" + root
		}
		fetched := fmt.Sprintf("errand serve fetch --on %s --for %s git+%s@%s%s", l.machine, key, from, rev, sub)
		composed := func(root string) ([]string, error) {
			p := provider{Env: root, Package: kit.Text(pkg["name"]), Dir: filepath.Join(root, filepath.FromSlash(dir)), Data: map[string]any{"services": map[string]any{name: entry}}}
			return p.compose(name, tools, false)
		}
		uncommitted := fmt.Sprintf("the store was written from changes not committed after %s: no commit holds %s. ", rev[:min(len(rev), 12)], key)
		switch {
		case dirty && python:
			propose(fmt.Sprintf("an env of its own for %s on %s ( %s ), with %s: the nearest commit", key, l.machine, env, short), fetched, from)
			propose(uncommitted+fmt.Sprintf("Put a checkout of %s that has them in that env ( replace CHECKOUT ); nothing else is touched", l.machine),
				typedWords([]string{filepath.Join(env, "bin", "python")})+" -m pip install --no-deps -e CHECKOUT", "")
			propose(asks, install(nil), "")
		case dirty:
			words, err := composed("CHECKOUT")
			if err != nil {
				return err
			}
			propose(uncommitted+fmt.Sprintf("With a checkout of %s that has them ( replace CHECKOUT ); declared as typed, unverified%s", l.machine, what), install(words), "")
		case python:
			propose(fmt.Sprintf("fetch %s into an env of its own on %s ( touches no other env )", short, l.machine), fetched, from)
			propose(asks, install(nil), "")
		default:
			words, err := composed(repoDir(from, rev))
			if err != nil {
				return err
			}
			only := ""
			if dir != "" {
				only = "#paths=" + strings.Join(pathsOf(dir, entry), ",")
			}
			propose(fmt.Sprintf("fetch the files of %s into errand's cache on %s ( nothing in them is run )", short, l.machine),
				fmt.Sprintf("errand serve fetch --on %s git:%s@%s%s", l.machine, from, rev, only), from)
			propose("declare the service, run from those files"+what, install(words), "")
		}
		return nil
	}
	said := ObjectOf(entry["package"])
	pip := kit.Text(said["pip"])
	if pip == "" {
		pip = kit.Text(pkg["name"])
	}
	if pip == "" || !python {
		return errors.New("no package here provides " + key + ", and the store says no repository to take it from")
	}
	if v := kit.Text(pkg["version"]); v != "" {
		pip += "==" + v
	}
	index, option := kit.Text(said["index"]), ""
	if index == "" {
		index = pypi
	}
	if index != pypi {
		option = " --index " + index
	}
	host := index
	if parts := strings.Split(index, "/"); len(parts) > 2 {
		host = parts[2]
	}
	propose(fmt.Sprintf("%s from %s into an env of its own for %s on %s ( %s ); touches no other env", pip, host, key, l.machine, env),
		fmt.Sprintf("errand serve fetch --on %s --for %s pip:%s%s", l.machine, key, pip, option), index)
	propose(asks, install(nil), "")
	return nil
}

// Look: the question a page asks, answered. `running`: the services the agents of this machine hold.
func Look(req map[string]any, running map[string]string) map[string]any {
	machine := kit.Text(req["machine"])
	if machine == "" {
		machine = "here"
	}
	given := map[string][]string{}
	for k, v := range ObjectOf(req["paths"]) {
		given[k] = kit.Strs(v)
	}
	decl, err := DeclaredServices(kit.LocalCache(), nil)
	if err != nil { // ( a file that does not load: what it declares is not here, and that is said )
		decl = Declarations{Services: map[string]ServiceSpec{}, Said: []string{err.Error()}}
	}
	l := &looker{machine: machine, given: given, cards: cardsHere(), declared: decl.Services, dev: decl.Dev, running: running, providers: providers()}
	services := map[string]any{}
	for name, x := range ObjectOf(req["services"]) {
		var want struct {
			AskedService
			DescribedBy string `json:"described_by"`
		}
		if err := reach.DecodeInto(x, &want); err != nil {
			services[name] = serviceLook{State: "unknown", Why: err.Error(), Found: []string{}, Lines: []proposedLine{}}
			continue
		}
		if want.DescribedBy == "" {
			want.DescribedBy = "store"
		}
		services[name] = l.service(name, want.AskedService, want.DescribedBy)
	}
	out := map[string]any{"platform": platformName(), "cards": l.cards, "store": storeSeen(ObjectOf(req["store"])), "services": services}
	if len(decl.Said) > 0 {
		out["said"] = decl.Said
	}
	return out
}

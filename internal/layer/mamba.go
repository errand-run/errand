// An environment of errand's ( ADR 0044 ): a `Micromamba` or `Conda` layer that declares its contents. It is named by its
// resolution, kept in a store per machine, and never changed once built; what the user only names is the user's.
//
//	<errand's directory>/envs/
//	    mamba/<id>/             a prefix, complete once `conda-meta/errand.json` is there; `<id>-2`… when built again
//	    mamba/<id>.json         what it was built from, when, its stamp, when it was last used
//	    mamba/<id>.lock/        who builds it now
//	    pkgs/                   the packages micromamba unpacks, on the same file system: a prefix is hard links
//	    refs/<key>.json         a layer of a project, and the prefix it runs in on this machine
//	<errand's directory>/micromamba/<version>/micromamba   errand's own, pinned
//
// The resolution is written down in the project, `errand-envs.lock`: what a declaration came to on a machine with these
// virtual packages. Another machine takes it when it has the same and meets what its packages require.

package layer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/system"
)

// MambaVersion: the micromamba errand fetches and builds with, everywhere ( a release of mamba-org/micromamba-releases )
const MambaVersion = "2.10.0-0"

// LockName: the file of the project that holds the resolutions, beside `errand-envs.py`
const LockName = "errand-envs.lock"

const lockFormat = "errand-envs.lock/1"

// MambaOwned: a `Micromamba` or `Conda` layer that declares what it holds, and so is errand's to build. One that only
// names an environment names the user's.
func MambaOwned(l project.Layer) bool {
	if t := l.Type(); t != "Micromamba" && t != "Conda" {
		return false
	}
	return l.String("python") != "" || len(l.Strings("packages")) > 0 || len(l.Strings("pip")) > 0 ||
		l.String("requirements") != "" || len(l.Strings("develop")) > 0
}

// MambaLabel: what an environment of errand's is called, as a person reads it
func MambaLabel(l project.Layer) string {
	if l.Type() == "Uv" {
		if n := l.String("name"); n != "" {
			return "uv:" + n
		}
		return "uv"
	}
	if n := l.String("name"); n != "" {
		return "micromamba:" + n
	}
	return "micromamba"
}

// StoreOwned: a layer whose environment is errand's, in its store ( ADR 0044 ) -- a conda one that declares its contents,
// and every `Uv`
func StoreOwned(l project.Layer) bool { return MambaOwned(l) || l.Type() == "Uv" }

// UvLocked: the hash of the project's `uv.lock` and `pyproject.toml` with the python asked, when a `Uv` layer is the
// project's own environment ( it declares no packages of its own ): that lock is its identity
func UvLocked(l project.Layer, root string) (string, bool) {
	if l.Type() != "Uv" || l.String("requirements") != "" || len(l.Strings("pip")) > 0 {
		return "", false
	}
	return UvLockHash(root, l.String("python"))
}

// UvLockHash: what a project's uv.lock and pyproject.toml are, with the python asked -> false without a uv.lock
func UvLockHash(root, python string) (string, bool) {
	lock, err := os.ReadFile(filepath.Join(root, "uv.lock"))
	if err != nil {
		return "", false
	}
	py, _ := os.ReadFile(filepath.Join(root, "pyproject.toml"))
	h := sha256.New()
	fmt.Fprintf(h, "uv.lock\x00%s\x00", python)
	h.Write(lock)
	h.Write([]byte{0})
	h.Write(py)
	return hex.EncodeToString(h.Sum(nil))[:16], true
}

// MambaStore: where this machine keeps the environments errand builds
func MambaStore() string { return filepath.Join(system.Cache(), "envs") }

// MambaExe: the micromamba an environment of errand's is built and entered with -- errand's, unless the layer names one
func MambaExe(l project.Layer) string {
	if e := l.String("executable"); e != "" {
		return e
	}
	return filepath.Join(system.Cache(), "micromamba", MambaVersion, "micromamba")
}

// ── the declaration ──────────────────────────────────────────────────────────

// Declaration: what an environment is asked to hold -- its hash says when to resolve again
type Declaration struct {
	Kind     string   `json:"kind"`     // mamba, uv
	Python   string   `json:"python"`   // for uv: the version asked ( conda has it among its packages )
	Channels []string `json:"channels"` // in order: it is their priority
	Conda    []string `json:"conda"`    // `python=…`, `pip`, the packages
	Pip      []string `json:"pip"`      // the pip list, the lines of the requirements, what the packages of `develop` need
	NoCuda   bool     `json:"no_cuda,omitempty"`
	// not part of the hash: what is installed over the environment, in a project's overlay
	Develop []Develop `json:"-"`
}

// Develop: a package one works on, installed `-e` in an overlay
type Develop struct {
	Path   string   // as declared, from the project's root
	Extras []string // `../sdot[viz]`
	Name   string   // its distribution's name, normalised
}

// Hash: what a declaration is, in a word
func (d Declaration) Hash() string {
	b, _ := json.Marshal(d)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

var extrasOf = regexp.MustCompile(`^(.*?)\[([^\]]*)\]$`)

// DeclarationOf: the declaration of an environment of errand's, as the files of the project say it at `root`
func DeclarationOf(l project.Layer, root string) (Declaration, error) {
	d := Declaration{Kind: "mamba"}
	if l.Type() == "Uv" {
		d.Kind, d.Python = "uv", l.String("python")
	} else {
		d.Channels = l.Strings("channels")
		if len(d.Channels) == 0 {
			d.Channels = []string{"conda-forge"}
		}
		if v := l.String("python"); v != "" {
			d.Conda = append(d.Conda, "python="+v)
		}
		d.Conda = append(d.Conda, "pip")
		d.Conda = append(d.Conda, l.Strings("packages")...)
		sort.Strings(d.Conda)
	}
	if cuda, ok := l["cuda"].(bool); ok && !cuda {
		d.NoCuda = true
	}
	pip := append([]string{}, l.Strings("pip")...)
	if r := l.String("requirements"); r != "" {
		data, err := os.ReadFile(filepath.Join(root, r))
		if err != nil {
			return d, fmt.Errorf("the requirements %s: %v", r, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if i := strings.Index(line, " #"); i >= 0 {
				line = line[:i]
			}
			if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "-e ") || strings.HasPrefix(line, "--editable") {
				return d, fmt.Errorf("the requirements %s install %q as editable: it goes in `develop = [ … ]`", r, line)
			}
			pip = append(pip, line)
		}
	}
	for _, p := range l.Strings("develop") {
		dev := Develop{Path: p}
		if m := extrasOf.FindStringSubmatch(p); m != nil {
			dev.Path = m[1]
			for _, e := range strings.Split(m[2], ",") {
				if e = strings.TrimSpace(e); e != "" {
					dev.Extras = append(dev.Extras, e)
				}
			}
		}
		d.Develop = append(d.Develop, dev)
	}
	mine := map[string]bool{}
	var needs []string
	for i, dev := range d.Develop {
		py, err := readPyproject(filepath.Join(root, dev.Path, "pyproject.toml"))
		if err != nil {
			return d, fmt.Errorf("develop %s: %v", dev.Path, err)
		}
		if py.name == "" {
			return d, fmt.Errorf("develop %s: its pyproject.toml has no [project] name", dev.Path)
		}
		for _, dyn := range py.dynamic {
			if dyn == "dependencies" || dyn == "optional-dependencies" {
				return d, fmt.Errorf("develop %s: its %s are dynamic; declare them in `pip = [ … ]` of the layer", dev.Path, dyn)
			}
		}
		d.Develop[i].Name = normalName(py.name)
		mine[d.Develop[i].Name] = true
		needs = append(needs, py.deps...)
		for _, e := range dev.Extras {
			got, ok := py.extras[normalName(e)]
			if !ok {
				return d, fmt.Errorf("develop %s: no extra %q in its pyproject.toml", dev.Path, e)
			}
			needs = append(needs, got...)
		}
		needs = append(needs, py.build...)
	}
	for _, n := range needs { // the packages one works on are not fetched under themselves
		if !mine[normalName(requirementName(n))] {
			pip = append(pip, n)
		}
	}
	seen := map[string]bool{}
	for _, p := range pip {
		if !seen[p] {
			seen[p] = true
			d.Pip = append(d.Pip, p)
		}
	}
	sort.Strings(d.Pip)
	return d, nil
}

var notNameChar = regexp.MustCompile(`[-_.]+`)

// normalName: a distribution's name as PEP 503 compares them
func normalName(n string) string {
	return notNameChar.ReplaceAllString(strings.ToLower(strings.TrimSpace(n)), "-")
}

var requirementHead = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)`)

func requirementName(r string) string {
	if m := requirementHead.FindStringSubmatch(r); m != nil {
		return m[1]
	}
	return r
}

// ── pyproject.toml, as much of it as says what a package needs ───────────────

type pyproject struct {
	name    string
	deps    []string
	extras  map[string][]string
	build   []string
	dynamic []string
}

var tomlString = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"|'([^']*)'`)

// readPyproject: the name, the dependencies, the extras and the build's requirements of a package -- a reading of the
// few keys that say them, strings and arrays of strings, not a whole TOML
func readPyproject(path string) (pyproject, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return pyproject{}, err
	}
	out := pyproject{extras: map[string][]string{}}
	lines := strings.Split(string(data), "\n")
	table := ""
	for i := 0; i < len(lines); i++ {
		line := stripComment(lines[i])
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && !strings.Contains(trimmed, "=") {
			table = strings.Trim(trimmed, "[] ")
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.Trim(strings.TrimSpace(line[:eq]), `"'`)
		value := line[eq+1:]
		for depth(value) > 0 && i+1 < len(lines) { // an array over several lines
			i++
			value += "\n" + stripComment(lines[i])
		}
		var strs []string
		for _, m := range tomlString.FindAllStringSubmatch(value, -1) {
			if m[1] != "" || strings.HasPrefix(m[0], `"`) {
				s, err := strconv.Unquote(`"` + m[1] + `"`)
				if err != nil {
					s = m[1]
				}
				strs = append(strs, s)
			} else {
				strs = append(strs, m[2])
			}
		}
		switch {
		case table == "project" && key == "name" && len(strs) > 0:
			out.name = strs[0]
		case table == "project" && key == "dependencies":
			out.deps = strs
		case table == "project" && key == "dynamic":
			out.dynamic = strs
		case table == "project.optional-dependencies":
			out.extras[normalName(key)] = strs
		case table == "build-system" && key == "requires":
			out.build = strs
		}
	}
	return out, nil
}

// stripComment: a line without what follows a `#` outside a string
func stripComment(line string) string {
	in := byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case in != 0 && c == '\\' && in == '"':
			i++
		case in != 0 && c == in:
			in = 0
		case in == 0 && (c == '"' || c == '\''):
			in = c
		case in == 0 && c == '#':
			return line[:i]
		}
	}
	return line
}

// depth: how many brackets are left open, outside strings
func depth(s string) int {
	n, in := 0, byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case in != 0 && c == '\\' && in == '"':
			i++
		case in != 0 && c == in:
			in = 0
		case in == 0 && (c == '"' || c == '\''):
			in = c
		case in == 0 && (c == '[' || c == '{'):
			n++
		case in == 0 && (c == ']' || c == '}'):
			n--
		}
	}
	return n
}

// ── the lock ─────────────────────────────────────────────────────────────────

// Lock: the file of resolutions of a project. JSON, indented and sorted: yaml reads it too.
type Lock struct {
	Format string                 `json:"format"`
	Envs   map[string]*LockedDecl `json:"environments"` // by the hash of their declaration
}

// LockedDecl: what one declaration came to, machine by machine
type LockedDecl struct {
	Name        string       `json:"name"`
	Resolutions []Resolution `json:"resolutions"`
}

// Resolution: the packages a declaration came to on a machine, and what that machine had
type Resolution struct {
	ID         string   `json:"id"`
	Platform   string   `json:"platform"`         // "" for uv: one resolution for every platform ( `--universal` )
	Python     string   `json:"python,omitempty"` // for uv: the interpreter's exact version
	Cuda       bool     `json:"cuda"`             // whether `__cuda` was there to resolve with
	Requires   []string `json:"requires"`         // what its packages need of a machine ( their `depends` on virtual packages )
	Constrains []string `json:"constrains"`       // what they need of it when it is there ( their `constrains` )
	Conda      []string `json:"conda"`            // `<url>#sha256:<hex>`, the order micromamba gave
	Pip        []PipPin `json:"pip"`
	Resolved   string   `json:"resolved"` // when, and on which machine
}

// PipPin: a distribution, at one version, and the hashes of every file of that version
type PipPin struct {
	Pin    string   `json:"pin"` // `name==version`
	Hashes []string `json:"hashes"`
}

// Identify: the resolution's id, from what it installs and where
func (r *Resolution) Identify() {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%v\x00%s\x00", r.Platform, r.Cuda, r.Python)
	for _, c := range r.Conda {
		fmt.Fprintf(h, "%s\x00", c)
	}
	for _, p := range r.Pip {
		fmt.Fprintf(h, "%s %s\x00", p.Pin, strings.Join(p.Hashes, " "))
	}
	r.ID = hex.EncodeToString(h.Sum(nil))[:16]
}

// ReadLock: the lock of the project at `root`; an empty one when it has none
func ReadLock(root string) (*Lock, error) {
	l := &Lock{Format: lockFormat, Envs: map[string]*LockedDecl{}}
	data, err := os.ReadFile(filepath.Join(root, LockName))
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	if err := json.Unmarshal(data, l); err != nil {
		return l, fmt.Errorf("%s cannot be read: %v", LockName, err)
	}
	if l.Envs == nil {
		l.Envs = map[string]*LockedDecl{}
	}
	return l, nil
}

// Write: the lock, at the project's root -- written whole and renamed, never half
func (l *Lock) Write(root string) error {
	l.Format = lockFormat
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(root, LockName)
	tmp := fmt.Sprintf("%s.%d.part", path, os.Getpid())
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Has: whether the lock holds that resolution of that declaration
func (l *Lock) Has(decl, id string) bool {
	if e := l.Envs[decl]; e != nil {
		for _, r := range e.Resolutions {
			if r.ID == id {
				return true
			}
		}
	}
	return false
}

// Add: a resolution of a declaration, unless it is there -> whether it was added
func (l *Lock) Add(decl, name string, r Resolution) bool {
	if l.Has(decl, r.ID) {
		return false
	}
	e := l.Envs[decl]
	if e == nil {
		e = &LockedDecl{}
		l.Envs[decl] = e
	}
	e.Name = name
	e.Resolutions = append(e.Resolutions, r)
	return true
}

// Keep: only the declarations that are still declared
func (l *Lock) Keep(current map[string]bool) bool {
	changed := false
	for d := range l.Envs {
		if !current[d] {
			delete(l.Envs, d)
			changed = true
		}
	}
	return changed
}

// ── the machine, and whether a resolution suits it ───────────────────────────

// Machine: what the solver sees of a machine
type Machine struct {
	Platform string
	Virtual  map[string][2]string // `__glibc` -> its version and build
}

// MachineFrom: a machine as `micromamba info --json` says it
func MachineFrom(info map[string]any) Machine {
	m := Machine{Virtual: map[string][2]string{}}
	m.Platform, _ = info["platform"].(string)
	vs, _ := info["virtual packages"].([]any)
	for _, v := range vs {
		parts := strings.SplitN(fmt.Sprint(v), "=", 3)
		for len(parts) < 3 {
			parts = append(parts, "")
		}
		m.Virtual[parts[0]] = [2]string{parts[1], parts[2]}
	}
	return m
}

// WithoutCuda: the machine as an environment that asks for no card sees it
func (m Machine) WithoutCuda() Machine {
	out := Machine{Platform: m.Platform, Virtual: map[string][2]string{}}
	for k, v := range m.Virtual {
		if k != "__cuda" {
			out.Virtual[k] = v
		}
	}
	return out
}

// HasCuda: whether the solver would see a card
func (m Machine) HasCuda() bool { _, ok := m.Virtual["__cuda"]; return ok }

// Suits: whether a resolution can be installed on this machine as it was made -- the same platform, a card or none as it
// had, and everything its packages need of the machine ( ADR 0044 )
func (r Resolution) Suits(m Machine) bool {
	if r.Platform == "" { // uv's, for every platform: each machine takes the files that suit it
		return true
	}
	if r.Platform != m.Platform || r.Cuda != m.HasCuda() {
		return false
	}
	for _, req := range r.Requires {
		if !m.meets(req, true) {
			return false
		}
	}
	for _, req := range r.Constrains {
		if !m.meets(req, false) {
			return false
		}
	}
	return true
}

// Unmet: what of a resolution this machine does not meet, said
func (r Resolution) Unmet(m Machine) []string {
	var out []string
	if r.Platform != m.Platform {
		out = append(out, "platform "+r.Platform)
	}
	if r.Cuda != m.HasCuda() {
		out = append(out, map[bool]string{true: "a card ( __cuda )", false: "no card"}[r.Cuda])
	}
	for _, req := range r.Requires {
		if !m.meets(req, true) {
			out = append(out, req)
		}
	}
	for _, req := range r.Constrains {
		if !m.meets(req, false) {
			out = append(out, req)
		}
	}
	return out
}

// meets: `__glibc >=2.17,<3.0.a0`, `__osx >=11.0`, `__archspec 1 x86_64_v3` -- a dependency on a virtual package.
// `needed`: whether it must be there ( a `depends` ), or only agree when it is ( a `constrains` ).
func (m Machine) meets(spec string, needed bool) bool {
	f := strings.Fields(spec)
	if len(f) == 0 {
		return true
	}
	have, ok := m.Virtual[f[0]]
	if !ok {
		return !needed
	}
	if len(f) > 1 && !versionMatches(have[0], f[1]) {
		return false
	}
	if len(f) > 2 {
		if matched, err := filepath.Match(f[2], have[1]); err != nil || !matched {
			return false
		}
	}
	return true
}

// versionMatches: a version against a conda match spec's version part -- `|` or, `,` and, `>=`, `<`, `==`, `!=`, `=`,
// a glob. Anything else does not match: the machine then resolves for itself, which costs a resolution, never a wrong
// environment.
func versionMatches(v, spec string) bool {
	for _, alt := range strings.Split(spec, "|") {
		all := true
		for _, c := range strings.Split(alt, ",") {
			if !versionClause(v, strings.TrimSpace(c)) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func versionClause(v, c string) bool {
	if c == "" || c == "*" {
		return true
	}
	for _, op := range []string{">=", "<=", "==", "!=", ">", "<", "="} {
		if strings.HasPrefix(c, op) {
			want := strings.TrimSpace(c[len(op):])
			if strings.HasSuffix(want, ".*") || op == "=" {
				prefix := strings.TrimSuffix(want, ".*")
				starts := v == prefix || strings.HasPrefix(v, prefix+".")
				switch op {
				case "=", "==":
					return starts
				case "!=":
					return !starts
				}
				want = prefix
			}
			cmp := compareVersions(v, want)
			switch op {
			case ">=":
				return cmp >= 0
			case "<=":
				return cmp <= 0
			case ">":
				return cmp > 0
			case "<":
				return cmp < 0
			case "==":
				return cmp == 0
			case "!=":
				return cmp != 0
			}
		}
	}
	if strings.ContainsAny(c[:1], "~^@") { // ( a form this does not read )
		return false
	}
	if strings.ContainsAny(c, "*?") {
		matched, err := filepath.Match(c, v)
		return err == nil && matched
	}
	return compareVersions(v, c) == 0 || strings.HasPrefix(v, c+".")
}

var versionPart = regexp.MustCompile(`\d+|[A-Za-z]+`)

// compareVersions: conda's order, near enough for virtual packages -- numbers as numbers, and a letter part ( `a0`, `dev` )
// before any number at the same place
func compareVersions(a, b string) int {
	pa, pb := versionPart.FindAllString(a, -1), versionPart.FindAllString(b, -1)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y string
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x == y {
			continue
		}
		nx, ex := strconv.Atoi(x)
		ny, ey := strconv.Atoi(y)
		switch {
		case x == "" && ey == nil:
			return cmpInt(0, ny)
		case y == "" && ex == nil:
			return cmpInt(nx, 0)
		case x == "": // `1.0` against `1.0.a0`: the letter part is earlier
			return 1
		case y == "":
			return -1
		case ex == nil && ey == nil:
			return cmpInt(nx, ny)
		case ex == nil:
			return 1
		case ey == nil:
			return -1
		default:
			return strings.Compare(x, y)
		}
	}
	return 0
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Choose: the resolution of a declaration this machine takes, if one suits it
func (l *Lock) Choose(decl string, m Machine) (Resolution, bool) {
	if e := l.Envs[decl]; e != nil {
		for _, r := range e.Resolutions {
			if r.Suits(m) {
				return r, true
			}
		}
	}
	return Resolution{}, false
}

// ── what a project's layer runs in, on this machine ──────────────────────────

// MambaRef: a layer of a project, and the environment it runs in here
type MambaRef struct {
	Root        string `json:"root"`
	Name        string `json:"name"`
	Declaration string `json:"declaration"`
	ID          string `json:"id"`
	Prefix      string `json:"prefix"`
	Exe         string `json:"exe"`
	Overlay     string `json:"overlay,omitempty"` // the python of the packages one works on, over the environment
	Develop     string `json:"develop,omitempty"` // what the overlay was made of
	UvLock      bool   `json:"uv_lock,omitempty"` // named by the project's uv.lock, with this python
	Python      string `json:"python,omitempty"`
	Seen        string `json:"seen"`
}

// RefPath: where the store says what a layer of the project at `root` runs in
func RefPath(root string, l project.Layer) string {
	b, _ := json.Marshal(l)
	sum := sha256.Sum256(append([]byte(filepath.Clean(root)+"\x00"), b...))
	return filepath.Join(MambaStore(), "refs", hex.EncodeToString(sum[:])[:20]+".json")
}

// ReadRef: what a layer of the project runs in here, once errand built it
func ReadRef(root string, l project.Layer) (MambaRef, error) {
	var r MambaRef
	data, err := os.ReadFile(RefPath(root, l))
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(data, &r)
	return r, err
}

// wrapMamba: the command, in an environment of errand's
func wrapMamba(l project.Layer, c Command, ctx Context) (Command, error) {
	ref, err := ReadRef(ctx.Root, l)
	if err != nil || !isDir(ref.Prefix) {
		return c, ErrDeclared(MambaLabel(l) + " is not built on this machine: `errand setup` builds it ( a run does, unless --no-setup )")
	}
	if l.Type() == "Uv" { // a venv: its interpreter is enough
		python := VenvPython(ref.Prefix)
		if ref.Overlay != "" {
			python = ref.Overlay
		}
		return Command{under(c.Argv, python), c.Env}, nil
	}
	python := "python"
	if ref.Overlay != "" {
		python = ref.Overlay
	} else if ctx.getenv("CONDA_PREFIX") == ref.Prefix {
		return c, nil // already active: nothing to wrap
	}
	argv := []string{ref.Exe, "run", "-r", filepath.Join(MambaStore(), "root"), "-p", ref.Prefix}
	return Command{append(argv, under(c.Argv, python)...), c.Env}, nil
}

// VenvPython: the interpreter of an environment, a venv or a conda prefix, as this system lays it out
func VenvPython(prefix string) string {
	if OnWindows {
		if _, err := os.Stat(filepath.Join(prefix, "python.exe")); err == nil { // ( a conda prefix )
			return filepath.Join(prefix, "python.exe")
		}
		return filepath.Join(prefix, "Scripts", "python.exe")
	}
	return filepath.Join(prefix, "bin", "python")
}

// Interpreter: the python a layer of errand's runs, on this machine -- the overlay's when there is one
func Interpreter(root string, l project.Layer) (string, error) {
	ref, err := ReadRef(root, l)
	if err != nil {
		return "", ErrDeclared(MambaLabel(l) + " is not built on this machine: `errand setup` builds it")
	}
	if ref.Overlay != "" {
		return ref.Overlay, nil
	}
	return VenvPython(ref.Prefix), nil
}

func isDir(p string) bool { st, err := os.Stat(p); return err == nil && st.IsDir() }

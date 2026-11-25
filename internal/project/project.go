// Package project reads what a project declares ( ADR 0031 ): its `errand-*.py`, evaluated by errand's Python where
// a command is typed, as one JSON document. This program never reads a project's file itself; a command that changes
// nothing starts no Python: the document is kept beside the project's state, with what it was made from.
package project

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/errand-run/errand/internal/system"
	"github.com/errand-run/errand/lib"
)

// Layer is one layer of a stack, as written: `{ "type": …, its fields }`.
type Layer map[string]any

func (l Layer) Type() string { t, _ := l["type"].(string); return t }

func (l Layer) String(name string) string { s, _ := l[name].(string); return s }

func (l Layer) Strings(name string) []string {
	raw, _ := l[name].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, fmt.Sprint(v))
	}
	return out
}

// Env is where work runs: a stack of layers, read outside in, and the tags saying what it is.
type Env struct {
	Stack    []Layer        `json:"stack"`
	Prepare  []Layer        `json:"prepare"`
	Tags     map[string]any `json:"tags"`
	Tools    map[string]any `json:"tools"`
	Needs    map[string]any `json:"needs"`
	Provided bool           `json:"provided"`
}

type File struct {
	Path   string  `json:"path"`
	Size   int64   `json:"size"`
	Mtime  float64 `json:"mtime"`
	SHA256 string  `json:"sha256"`
}

type Settings struct {
	Src        []string          `json:"src"`
	Exclude    []string          `json:"exclude"`
	Build      string            `json:"build"`
	OutputsDir string            `json:"outputs_directory"`
	BuildsDir  *string           `json:"builds_directory"`
	Zones      map[string]string `json:"zones"`
	Output     string            `json:"output"`
	Open       bool              `json:"open"`
}

type NotData struct {
	Where string `json:"where"`
	Why   string `json:"why"`
}

// Document is everything a project declares.
type Document struct {
	V          int               `json:"v"`
	Rev        int               `json:"rev"` // of what wrote it: 2 says the runners as data
	Root       string            `json:"root"`
	Errand     string            `json:"errand"`
	Files      []File            `json:"files"`
	Settings   Settings          `json:"settings"`
	DefaultEnv string            `json:"default_env"`
	FirstEnv   string            `json:"first_env"` // the default, when none is named
	DefaultRun any               `json:"default_run"`
	Envs       map[string]Env    `json:"envs"`
	Runners    map[string]Runner `json:"runners"`
	NotData    []NotData         `json:"not_data"`
	// the surroundings it was read in: those of whoever typed the command ( nil: this process's )
	environ []string
}

// Within: the document, as read in those surroundings ( where errand writes for the project may be said there )
func (d *Document) Within(environ []string) *Document {
	d.environ = environ
	return d
}

// lookup: a variable, in some surroundings -- the last said of a name being the one heard
func lookup(environ []string, name string) string {
	if environ == nil {
		return os.Getenv(name)
	}
	out := ""
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			out = v
		}
	}
	return out
}

// FindRoot: the nearest directory, from `start` up, that holds a project file ( `errand-*.py` ); `start` itself, and
// false, when there is none: a directory nobody declared anything about is a project all the same, with nothing in it.
func FindRoot(start string) (string, bool) {
	here, err := filepath.Abs(start)
	if err != nil {
		return start, false
	}
	for d := here; ; d = filepath.Dir(d) {
		if found, _ := filepath.Glob(filepath.Join(d, "errand-*.py")); len(found) > 0 {
			return d, true
		}
		if d == filepath.Dir(d) {
			return here, false
		}
	}
}

// OutputsDir: where everything errand writes for the project goes ( `errand-outputs/` unless moved )
func (d *Document) OutputsDir() string {
	if o := lookup(d.environ, "ERRAND_OUTPUTS_DIR"); o != "" {
		return o
	}
	out := d.Settings.OutputsDir
	if out == "" {
		out = "errand-outputs"
	}
	if filepath.IsAbs(out) {
		return out
	}
	return filepath.Join(d.Root, out)
}

// AllZones: the project's zones and its runners', which the project's own have the last word on ( ADR 0011 ). A runner's
// may name the environment: one for each that is declared, since they share the copy.
func (d *Document) AllZones() map[string]string {
	out := map[string]string{}
	for _, r := range d.Runners {
		for p, kind := range r.Zones {
			if !strings.Contains(p, "{") {
				out[p] = kind
				continue
			}
			names := []string{"local"}
			for name := range d.Envs {
				names = append(names, name)
			}
			for _, name := range names {
				out[strings.NewReplacer("{env}", name, "{build_dir}", d.BuildDir(name)).Replace(p)] = kind
			}
		}
	}
	for p, kind := range d.Settings.Zones {
		out[p] = kind
	}
	return out
}

// MainBuild: the name of the build directory that is nobody's in particular
const MainBuild = "main"

// Builds: where the builds of a project are, in errand's outputs for it ( ADR 0039 ): `<env>/<name>` under it
const Builds = "caches/build"

// BuildName: an environment's name, or a build's, as a directory is called
func BuildName(name string) string {
	if name == "" {
		name = "local"
	}
	name = strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.", c) {
			return c
		}
		return '_'
	}, name)
	if strings.Trim(name, ".") == "" {
		return strings.Repeat("_", len(name))
	}
	return name
}

// BuildDir: where the builds of an environment go on another machine, from the root of the copy there
func (d *Document) BuildDir(env string) string {
	if d.Settings.Build != "" {
		return filepath.ToSlash(filepath.Join(d.Settings.Build, BuildName(env)))
	}
	return path.Join("errand-outputs", Builds, BuildName(env), MainBuild)
}

// BuildHere: the same on this machine -- from the root when it is in it, as a path otherwise
func (d *Document) BuildHere(env string) string {
	if d.Settings.Build != "" {
		return d.BuildDir(env)
	}
	at := filepath.Join(d.OutputsDir(), filepath.FromSlash(Builds), BuildName(env), MainBuild)
	if rel, err := filepath.Rel(d.Root, at); err == nil && filepath.IsLocal(rel) {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(at)
}

// Preparing: the stack an environment is prepared with -- its own list when it declares one, whose layers may be
// those of the run's
func (e Env) Preparing() []Layer {
	if len(e.Prepare) == 0 {
		return e.Stack
	}
	var out []Layer
	for _, l := range e.Prepare {
		if at, ok := l["same"]; ok {
			if i, ok := at.(float64); ok && int(i) < len(e.Stack) {
				out = append(out, e.Stack[int(i)])
			}
			continue
		}
		out = append(out, l)
	}
	return out
}

// Env: the environment `name` ( "" : the project's default, else `local` ). `local`, declared by nobody, is this
// machine as it is.
func (d *Document) Env(name string) (string, Env, error) {
	if name == "" {
		if name = d.DefaultEnv; name == "" {
			name = "local"
		}
	}
	if e, ok := d.Envs[name]; ok {
		return name, e, nil
	}
	if name == "local" {
		return name, Env{}, nil
	}
	names := make([]string, 0, len(d.Envs))
	for n := range d.Envs {
		names = append(names, n)
	}
	sort.Strings(names)
	return name, Env{}, fmt.Errorf("no environment %q: the project declares %s", name, strings.Join(names, ", "))
}

func empty(root string) *Document {
	return &Document{V: 1, Rev: 6, Root: root, Envs: map[string]Env{}, Runners: Builtin(), Settings: Settings{OutputsDir: "errand-outputs"}}
}

func kept(d *Document) string { return filepath.Join(d.OutputsDir(), "state", "declared.json") }

func hashOf(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	_, _ = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}

// fresh: the document still says what the files say -- the same project files, each as it was ( its size and date,
// or, its date having moved, its contents )
func fresh(d *Document, root string) bool {
	if d.V != 1 || d.Rev < 6 || d.Root != root || len(d.NotData) > 0 { // ( an older writer said less: asked again )
		return false
	}
	known := map[string]bool{}
	for _, f := range d.Files {
		path := f.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		known[path] = true
		st, err := os.Stat(path)
		if err != nil || st.Size() != f.Size {
			return false
		}
		if float64(st.ModTime().UnixNano())/1e9 != f.Mtime && hashOf(path) != f.SHA256 {
			return false
		}
	}
	here, _ := filepath.Glob(filepath.Join(root, "errand-*.py"))
	for _, p := range here {
		if !known[p] {
			return false
		}
	}
	return true
}

// Python: the interpreter that reads declarations, as an absolute path or a name: the one that is said; that of the
// environment errand is installed in, where the packages that declare environments are; the one on the path
func Python(environ []string) string {
	if p := lookup(environ, "ERRAND_DECLARE_PYTHON"); p != "" {
		return p
	}
	for _, dir := range system.Installed(os.Getenv) {
		for _, name := range []string{"python3", "python", "python.exe"} {
			at := filepath.Join(dir, name)
			if st, err := os.Stat(at); err == nil && !st.IsDir() {
				return at
			}
		}
	}
	return "python3"
}

// Load: what the project at `root` declares, read in this process's surroundings
func Load(root string, declared bool) (*Document, error) { return LoadIn(root, declared, nil) }

// LoadIn: what the project at `root` declares, read in the surroundings given ( nil: this process's ). `declared`
// false: a directory with no project file, whose document is the empty one, and no Python is asked anything.
func LoadIn(root string, declared bool, environ []string) (*Document, error) {
	if !declared {
		d := empty(root)
		d.environ = environ
		return d, nil
	}
	guess := empty(root)
	guess.environ = environ
	named := lookup(environ, "ERRAND_PROVIDERS") != "" // what it declares depends on who asks: not kept, nor read from what was
	if data, err := os.ReadFile(kept(guess)); err == nil && !named {
		var d Document
		if json.Unmarshal(data, &d) == nil && fresh(&d, root) {
			d.environ = environ
			return &d, nil
		}
	}
	interpreter := Python(environ)
	cmd := exec.Command(interpreter, "-m", "errand.declared", root)
	var err error
	if cmd.Env, err = lib.With(environ); err != nil {
		return nil, err
	}
	if environ != nil { // found, and run, as whoever typed the command would
		if !strings.ContainsRune(interpreter, '/') {
			for _, dir := range filepath.SplitList(lookup(environ, "PATH")) {
				if st, err := os.Stat(filepath.Join(dir, interpreter)); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
					cmd.Path, cmd.Err = filepath.Join(dir, interpreter), nil
					break
				}
			}
		}
	}
	var out, said bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &said
	if err := cmd.Run(); err != nil {
		why := strings.TrimSpace(said.String())
		var missing *exec.Error
		if errors.As(err, &missing) {
			why = fmt.Sprintf("%s: %v ( a Python, 3.8 or later, reads the project's files: name one with ERRAND_DECLARE_PYTHON )", interpreter, missing.Err)
		}
		return nil, fmt.Errorf("the project's declarations could not be read:\n%s", why)
	}
	var d Document
	if err := json.Unmarshal(out.Bytes(), &d); err != nil {
		return nil, fmt.Errorf("the project's declarations, as errand's Python wrote them, cannot be read: %v", err)
	}
	if d.V != 1 {
		return nil, fmt.Errorf("the declarations' document is of version %d: this program reads version 1", d.V)
	}
	d.environ = environ
	if len(d.NotData) == 0 && !named { // ( something that is not data is asked again each time: its answer may change )
		if err := os.MkdirAll(filepath.Dir(kept(&d)), 0o755); err == nil {
			part := kept(&d) + ".part"
			if os.WriteFile(part, out.Bytes(), 0o644) == nil {
				_ = os.Rename(part, kept(&d))
			}
		}
	}
	return &d, nil
}

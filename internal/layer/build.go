// What a layer builds ( the guide's "Automatic installations", ADR 0004 ): a venv, a micromamba or conda environment,
// a uv project. Each says what it is made from ( a fingerprint of its declaration ), whether it is there, what it
// looks like from outside ( a stamp ), and the steps that make it or bring it up to date.
//
// Everything is a line of shell, run from the project's root on the machine that holds the thing, in the
// surroundings its commands will have: only that machine can say what it has.

package layer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/errand-run/errand/internal/project"
)

// Artefact is a layer that builds something.
type Artefact struct {
	Layer project.Layer
	Name  string // as a person reads it: `venv:.venv/bin/python`, `micromamba:jax`
}

// an environment's packages all land in one directory, whichever installed them
const siteStamp = `import os, sysconfig; s = os.stat( sysconfig.get_paths()[ 'purelib' ] ); print( int( s.st_mtime ), s.st_nlink )`

// what leaves in `$uv` the uv of the PATH, of its usual places, or of errand's own directory -- fetched there from
// astral.sh when there is none: never on the PATH, never in a shell profile
// UvFind: what leaves in `$uv` a uv to run ( see uvFind )
const UvFind = uvFind

const uvFind = `uv=$( command -v uv 2>/dev/null ); base="${ERRAND_CACHE_DIR:-$HOME/.cache/errand}"; ` +
	`for c in "$HOME/.local/bin/uv" "$HOME/.cargo/bin/uv" "$base/uv/uv"; do [ -z "$uv" ] && [ -x "$c" ] && uv="$c"; done; ` +
	`if [ -z "$uv" ]; then echo "errand: no uv here: fetching one into $base/uv" >&2; mkdir -p "$base"; ` +
	`if command -v curl >/dev/null 2>&1; then curl -LsSf https://astral.sh/uv/install.sh; else wget -qO- https://astral.sh/uv/install.sh; fi ` +
	`| env UV_UNMANAGED_INSTALL="$base/uv" sh >&2; uv="$base/uv/uv"; fi; [ -x "$uv" ]`

func sh(argv ...string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = Quote(a)
	}
	return strings.Join(out, " ")
}

// Artefacts: the layers of a stack that have something to build. One that declares nothing errand could ever
// install ( an interpreter named, and no more ) is not one: there is nothing to keep current.
func Artefacts(stack []project.Layer) []Artefact {
	var out []Artefact
	for _, l := range stack {
		switch l.Type() {
		case "Micromamba", "Conda", "Uv": // only what declares its contents: what only names an environment names the user's ( ADR 0044 )
			if StoreOwned(l) {
				out = append(out, Artefact{l, MambaLabel(l)})
			}
		case "Apptainer", "Docker", "Podman":
			if l.String("image") != "" || (l.Type() == "Apptainer" && l.String("recipe") != "") { // ( a layer that only names where to find an image has nothing of its own )
				out = append(out, Artefact{l, containerName(l)})
			}
		}
	}
	return out
}

// Kept: what the artefacts of a stack build into the project, from its root -- for a push to leave alone: it is not
// a source to copy, and over there a push would take it for something that appeared by itself
func Kept(stack []project.Layer) []string {
	var out []string
	for _, a := range Artefacts(stack) {
		out = append(out, a.kept()...)
	}
	return out
}

var notAWord = regexp.MustCompile(`[^\w.+-]+`)

// Slug: a file name for that artefact -- readable, and hashed over what it is
func (a Artefact) Slug() string {
	identity := a.Name + "\x00" + a.Layer.String("root_prefix") + "\x00" + a.Layer.String("executable")
	if a.isContainer() { // a/cuda.sif and b/cuda.sif are two things
		identity += "\x00" + a.Layer.String("image") + "\x00" + a.Layer.String("recipe")
	}
	sum := sha256.Sum256([]byte(identity))
	return strings.Trim(notAWord.ReplaceAllString(a.Name, "-"), "-") + "-" + hex.EncodeToString(sum[:])[:8]
}

// Inputs: the files of the declaration, from the root -- `root` is where they are read
func (a Artefact) Inputs(root string) []string {
	if a.isContainer() {
		return a.containerInputs(root)
	}
	if r := a.Layer.String("requirements"); r != "" {
		return []string{r}
	}
	return nil
}

// Fingerprint: what the artefact is built from -- what the layer declares, and the contents of the files it names
func (a Artefact) Fingerprint(ctx Context) string {
	h := sha256.New()
	fields := []string{"type", "python", "channels", "packages", "requirements", "pip", "path", "create", "name"}
	if a.isContainer() {
		fields = a.containerFields()
	}
	for _, field := range fields {
		b, _ := json.Marshal(a.Layer[field])
		h.Write(append(b, 0))
	}
	for _, rel := range a.Inputs(ctx.Root) {
		hashInput(h, ctx.Root, rel)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// hashInput: a file of the declaration into a hash -- its contents, or for a directory each file's path and contents
func hashInput(h io.Writer, root, rel string) {
	at := filepath.Join(root, rel)
	st, err := os.Stat(at)
	if err != nil {
		fmt.Fprintf(h, "<missing %s>", rel)
		return
	}
	if !st.IsDir() {
		if data, err := os.ReadFile(at); err == nil {
			fmt.Fprintf(h, "%s\x00", rel)
			h.Write(data)
		}
		return
	}
	_ = filepath.WalkDir(at, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		if data, err := os.ReadFile(p); err == nil {
			r, _ := filepath.Rel(root, p)
			fmt.Fprintf(h, "%s\x00", filepath.ToSlash(r))
			h.Write(data)
		}
		return nil
	})
}

// OnWindows: whether lines are run where there is no shell ( a variable: a test says so too )
var OnWindows = runtime.GOOS == "windows"

var binPython = regexp.MustCompile(`/bin/python[0-9.]*$`)

// venvPython: where a venv has its interpreter, as declared for any system: `.venv/bin/python` is `.venv/Scripts/python.exe`
// on Windows
func venvPython(p string) string {
	if OnWindows {
		return binPython.ReplaceAllString(filepath.ToSlash(p), "/Scripts/python.exe")
	}
	return p
}

// Simple: whether what makes it, and what it looks like, are lines with no shell in them: single commands, which Windows
// can start
func (a Artefact) Simple() bool { return a.Layer.Type() == "Uv" }

// Probe: a line whose exit code says whether the thing is there
func (a Artefact) Probe(ctx Context) string {
	switch a.Layer.Type() {
	case "Apptainer", "Docker", "Podman":
		return a.containerProbe(ctx.Root)
	}
	return "false" // ( an environment of errand's is looked at by the agent, in its store: agent/mamba.go )
}

// Stamp: a line that prints what the thing looks like from outside -- by convention the date it was last written
// first, as an epoch
func (a Artefact) Stamp(ctx Context) string {
	switch a.Layer.Type() {
	case "Apptainer", "Docker", "Podman":
		return a.containerStamp(ctx.Root)
	}
	return "" // ( an environment of errand's has its stamp in its store: agent/mamba.go )
}

// Steps: what makes the thing, or brings it up to date -- each a line of shell, run from the project's root. An
// environment of errand's has none: it is resolved, built and never changed, by the agent ( agent/mamba.go ).
func (a Artefact) Steps(ctx Context) []string {
	l := a.Layer
	switch l.Type() {
	case "Apptainer", "Docker", "Podman":
		return a.containerSteps(ctx.Root)
	}
	return nil
}

// MissingSays: what a `Missing` layer says is lacking, and what to write for it
func MissingSays(l project.Layer) string {
	out := "needs " + l.String("what")
	if hint := l.String("hint"); hint != "" {
		out += "\n  " + hint
	}
	return out
}

// MissingOf: the `Missing` layer of a stack, if it has one
func MissingOf(stack []project.Layer) project.Layer {
	for _, l := range stack {
		if l.Type() == "Missing" {
			return l
		}
	}
	return nil
}

// Check: whether this program knows every layer of a stack, without doing anything -- said before a run is made
func Check(stack []project.Layer) error {
	for _, l := range stack {
		switch l.Type() {
		case "Vars", "Venv", "Module", "Wrap", "Uv", "Slurm", "Mpi", "Nix", "Guix", "Preload", "WindowsTools":
		case "Missing": // what this machine lacks to reach the environment: said, rather than nothing declared
			return ErrDeclared(MissingSays(l))
		case "Apptainer":
			if l.String("image") == "" && l.String("run_from") == "" && l.String("recipe") == "" {
				return ErrDeclared("an Apptainer layer names its image")
			}
		case "Docker", "Podman":
			if l.String("image") == "" {
				return ErrDeclared("a " + l.Type() + " layer names its image")
			}
		case "Micromamba", "Conda":
			if l.String("name") == "" {
				return ErrDeclared("a " + l.Type() + " layer names its environment")
			}
		default:
			return ErrNotYet{l.Type()}
		}
	}
	return nil
}

type ErrDeclared string

func (e ErrDeclared) Error() string { return string(e) }

// IsContainer: a layer that leads inside a container, where files and tools are not the machine's
func IsContainer(l project.Layer) bool { return l.Type() == "Apptainer" || isEngine(l.Type()) }

// Enter: the command that enters a container and starts `argv` there -- with what must be seen from inside at the
// same paths ( `binds` ), and what the layer mounts
func Enter(l project.Layer, ctx Context, binds []string, argv []string) (Command, error) {
	if isEngine(l.Type()) {
		return enterEngine(l, ctx, binds, argv), nil
	}
	if l.Type() != "Apptainer" {
		return Command{}, ErrNotYet{l.Type()}
	}
	exe := l.String("exe")
	if exe == "" {
		exe = "apptainer"
	}
	enter := append([]string{exe, "exec"}, l.Strings("flags")...)
	if mounts, ok := l["mounts"].(map[string]any); ok {
		names := make([]string, 0, len(mounts))
		for src := range mounts {
			names = append(names, src)
		}
		sort.Strings(names)
		for _, src := range names {
			from := src
			if !filepath.IsAbs(from) {
				from = filepath.Join(ctx.Root, from)
			}
			enter = append(enter, "--bind", from+":"+fmt.Sprint(mounts[src]))
		}
	}
	for _, b := range binds {
		enter = append(enter, "--bind", b)
	}
	if from := l.String("run_from"); from != "" { // a path to expand where it runs: through a shell
		return Command{[]string{"sh", "-c", "exec " + sh(enter...) + " " + from + ` "$@"`, "sh"}, map[string]string{}}, nil
	}
	image := ImageOf(l, ctx.Root)
	if !filepath.IsAbs(image) {
		image = filepath.Join(ctx.Root, image)
	}
	if _, err := os.Stat(image); err != nil {
		return Command{}, ErrDeclared("the image " + image + " is not there" + map[bool]string{true: " ( errand setup builds it )", false: ", and no recipe builds it"}[l.String("recipe") != ""])
	}
	return Command{append(append(enter, image), argv...), map[string]string{}}, nil
}

// IsScheduler: a layer that hands the work to a batch system, which decides where and when it runs
func IsScheduler(l project.Layer) bool { return l.Type() == "Slurm" }

// SlurmSeconds: `MM`, `MM:SS`, `HH:MM:SS`, `D-HH`, `D-HH:MM`, `D-HH:MM:SS` -- slurm's ways of saying how long
func SlurmSeconds(spelt string) int {
	days, rest := 0, spelt
	if i := strings.LastIndex(spelt, "-"); i >= 0 {
		fmt.Sscan(spelt[:i], &days)
		rest = spelt[i+1:]
	}
	var parts []int
	for _, p := range strings.Split(rest, ":") {
		n := 0
		fmt.Sscan(p, &n)
		parts = append(parts, n)
	}
	h, m, sec := 0, 0, 0
	switch {
	case days > 0 || strings.Contains(spelt, "-"): // after a day, the first number is hours
		for len(parts) < 3 {
			parts = append(parts, 0)
		}
		h, m, sec = parts[0], parts[1], parts[2]
	case len(parts) == 1:
		m = parts[0]
	case len(parts) == 2:
		m, sec = parts[0], parts[1]
	default:
		h, m, sec = parts[0], parts[1], parts[2]
	}
	return ((days*24+h)*60+m)*60 + sec
}

// SlurmFlags: what a Slurm layer declares, filled in from what the work asks for. A batch system is a queue that
// already exists: errand holds none on top of it, and hands the numbers over. What the environment states wins: who
// wrote `cpus = 16` knew something about the partition the work does not. `asked`: the time asked for this command.
func SlurmFlags(l project.Layer, cpus float64, ramMB float64, gpus int, exclusive bool, asked string, ranks int) []string {
	many := ranks > 1 // one task per rank, and what the work asks for is what one rank needs
	var out []string
	add := func(flag string, value any) {
		if value == nil {
			return
		}
		switch v := value.(type) {
		case string:
			if v != "" {
				out = append(out, flag, v)
			}
		case float64:
			if v == float64(int64(v)) {
				out = append(out, flag, fmt.Sprint(int64(v)))
			} else {
				out = append(out, flag, fmt.Sprint(v))
			}
		default:
			out = append(out, flag, fmt.Sprint(v))
		}
	}
	duration := asked
	if duration == "" {
		duration = l.String("time")
	}
	qos := ""
	switch q := l["qos"].(type) {
	case string:
		qos = q
	case []any: // a table: up to which time, which name -- the last line for anything longer
		for _, row := range q {
			pair, _ := row.([]any)
			if len(pair) != 2 {
				continue
			}
			limit, _ := pair[0].(string)
			if pair[0] == nil || (duration != "" && SlurmSeconds(duration) <= SlurmSeconds(limit)) {
				qos = fmt.Sprint(pair[1])
				break
			}
		}
	}
	add("--partition", l["partition"])
	add("--nodes", l["nodes"])
	if many {
		add("--ntasks", float64(ranks))
		add("--ntasks-per-node", l["ntasks_per_node"])
	}
	if l["cpus"] != nil {
		add("--cpus-per-task", l["cpus"])
	} else if cpus > 1 {
		add("--cpus-per-task", cpus)
	}
	cards := "--gpus"
	if many {
		cards = "--gpus-per-task"
	}
	if l["gpus"] != nil {
		add(cards, l["gpus"])
	} else if gpus > 0 {
		add(cards, float64(gpus))
	}
	add("--time", duration)
	add("--account", l["account"])
	add("--qos", qos)
	switch n := l["nodelist"].(type) {
	case string:
		add("--nodelist", n)
	case []any:
		add("--nodelist", strings.Join(l.Strings("nodelist"), ","))
	}
	if ramMB > 0 && many {
		per := cpus
		if v, ok := l["cpus"].(float64); ok {
			per = v
		}
		if per < 1 {
			per = 1
		}
		out = append(out, "--mem-per-cpu", fmt.Sprintf("%dM", int64(ramMB/per+0.999)))
	} else if ramMB > 0 {
		out = append(out, "--mem", fmt.Sprintf("%dM", int64(ramMB)))
	}
	if exclusive {
		out = append(out, "--exclusive")
	}
	return append(out, l.Strings("extra")...)
}

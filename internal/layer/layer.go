// Package layer says what each layer of an environment does to the command about to run ( the guide's "Layers" ).
// A stack is read outside in; the command is wrapped from the inside out.
//
// There is always a current interpreter, and most layers change it: a layer that selects one replaces a first word
// that names one ( `python`, `python3` ) and leaves any other first word alone.
package layer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/errand-run/errand/internal/project"
)

// Command is what will be started: its words, and the variables added to its environment.
type Command struct {
	Argv []string
	Env  map[string]string
}

// Context is where a stack is applied.
type Context struct {
	Root string         // the project's root where the command runs
	Tags map[string]any // the tags selected
	// the surroundings the command will have, when they are not this process's ( an agent starts a command typed
	// elsewhere, on another day ): `NAME=value`, the last said of a name being the one heard
	Environ []string
	// the machine's name, for a container that would otherwise be given its own ( what a run says of where it ran )
	Hostname string
}

func (c Context) getenv(name string) string {
	if c.Environ == nil {
		return os.Getenv(name)
	}
	out := ""
	for _, kv := range c.Environ {
		if k, v, ok := strings.Cut(kv, "="); ok && (k == name || OnWindows && strings.EqualFold(k, name)) {
			out = v
		}
	}
	return out
}

// look: the program a word names, on the PATH of those surroundings
func (c Context) look(word string) (string, error) {
	if c.Environ == nil || strings.ContainsRune(word, '/') {
		return exec.LookPath(word)
	}
	for _, dir := range filepath.SplitList(c.getenv("PATH")) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, word)
		if OnWindows {
			if got, err := exec.LookPath(p); err == nil { // ( with the ends a program's name has there )
				return got, nil
			}
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}

// ErrNotYet: a layer errand does not know
type ErrNotYet struct{ Layer string }

func (e ErrNotYet) Error() string {
	return fmt.Sprintf("%s is not a layer errand knows", e.Layer)
}

func namesPython(word string) bool {
	return word == "python" || word == "python3"
}

// under: `argv` run by `python` when its first word names an interpreter
func under(argv []string, python string) []string {
	if len(argv) > 0 && namesPython(argv[0]) {
		return append([]string{python}, argv[1:]...)
	}
	return argv
}

// Quote: a word as a POSIX shell reads it back
func Quote(word string) string {
	if word != "" && strings.IndexFunc(word, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%+=:,./-_", r))
	}) < 0 {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", `'"'"'`) + "'"
}

func shell(c Command) string {
	words := make([]string, 0, len(c.Argv)+len(c.Env))
	names := make([]string, 0, len(c.Env))
	for k := range c.Env {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		words = append(words, k+"="+Quote(c.Env[k]))
	}
	for _, w := range c.Argv {
		words = append(words, Quote(w))
	}
	return strings.Join(words, " ")
}

// values: a `Vars`'s, for the tags selected -- a mapping, or one per combination of the tags it is a function of
func values(l project.Layer, tags map[string]any) (map[string]any, error) {
	v, _ := l["values"].(map[string]any)
	keys, byTags := v["by_tags"].([]any)
	if !byTags {
		return v, nil
	}
	table, _ := v["of"].([]any)
	for _, row := range table {
		pair, _ := row.([]any)
		if len(pair) != 2 {
			continue
		}
		given, _ := pair[0].(map[string]any)
		match := true
		for _, k := range keys {
			name := fmt.Sprint(k)
			have, ok := tags[name]
			if !ok {
				have = given[name]
			}
			match = match && fmt.Sprint(have) == fmt.Sprint(given[name])
		}
		if match {
			got, _ := pair[1].(map[string]any)
			return got, nil
		}
	}
	return nil, fmt.Errorf("no values declared for the tags %v", tags)
}

func mambaRoot(l project.Layer, ctx Context) string {
	if r := l.String("root_prefix"); r != "" {
		return r
	}
	// Not pinning it is a trap: the shell hook exports MAMBA_ROOT_PREFIX from an rc file, which only interactive shells
	// read; started from a plain process, micromamba falls back to the directory beside its own binary, and `-n x`
	// silently names another environment than the same command typed by hand.
	if r := ctx.getenv("MAMBA_ROOT_PREFIX"); r != "" {
		return r
	}
	home := ctx.getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	for _, c := range []string{".mamba", "micromamba"} {
		if st, err := os.Stat(filepath.Join(home, c, "envs")); err == nil && st.IsDir() {
			return filepath.Join(home, c)
		}
	}
	return ""
}

func wrap(l project.Layer, c Command, ctx Context) (Command, error) {
	switch l.Type() {
	case "Vars":
		vals, err := values(l, ctx.Tags)
		if err != nil {
			return c, err
		}
		env := map[string]string{}
		for k, v := range c.Env {
			env[k] = v
		}
		for k, v := range vals { // ( of two layers that set one variable, the outer one is heard )
			env[k] = strings.ReplaceAll(fmt.Sprint(v), "{root}", ctx.Root)
		}
		return Command{c.Argv, env}, nil
	case "Venv":
		python := l.String("python")
		if python == "" {
			python = "python"
		}
		if !filepath.IsAbs(python) && strings.ContainsRune(python, '/') {
			python = filepath.Join(ctx.Root, python) // a venv of the project
		}
		python = venvPython(python)
		return Command{under(c.Argv, python), c.Env}, nil
	case "Uv": // a venv of errand's, in its store
		return wrapMamba(l, c, ctx)
	case "Micromamba", "Conda":
		if MambaOwned(l) { // errand's: in its store
			return wrapMamba(l, c, ctx)
		}
		exe := l.String("executable")
		if exe == "" {
			exe = strings.ToLower(l.Type())
		}
		name := l.String("name")
		if name == "" {
			return c, fmt.Errorf("a %s layer names its environment", l.Type())
		}
		if name == ctx.getenv("CONDA_DEFAULT_ENV") {
			return c, nil // already active: nothing to wrap
		}
		if _, err := ctx.look(exe); err != nil {
			return c, fmt.Errorf("the environment %s is %s's, and there is no %s here ( `executable = …` names one that is not on PATH )", name, l.Type(), exe)
		}
		argv := []string{exe}
		if root := mambaRoot(l, ctx); root != "" && l.Type() == "Micromamba" {
			argv = append(argv, "--root-prefix", root)
		}
		argv = append(argv, "run", "-n", name)
		if l.Type() == "Conda" {
			argv = append(argv, "--no-capture-output") // ( conda keeps what the command prints until its end, otherwise )
		}
		return Command{append(argv, under(c.Argv, "python")...), c.Env}, nil
	case "Module":
		// `module` is a shell function, not a program: it exists only in a login shell. `exec` takes a program, not
		// assignments: `env` sets them, after the login shell's profile, which would otherwise be free to undo them.
		var loads []string
		for _, n := range l.Strings("names") {
			loads = append(loads, "module load "+Quote(n))
		}
		run := shell(c)
		if len(c.Env) > 0 {
			run = "env " + run
		}
		return Command{[]string{"sh", "-lc", strings.Join(append(loads, "exec "+run), " && ")}, map[string]string{}}, nil
	case "Nix":
		flake := l.String("flake")
		if flake == "" {
			flake = "."
		}
		if s := l.String("shell"); s != "" {
			flake += "#" + s
		}
		return Command{append([]string{"nix", "develop", flake, "-c"}, c.Argv...), c.Env}, nil
	case "Guix":
		spec := l.Strings("packages")
		if m := l.String("manifest"); m != "" {
			spec = []string{"-m", m}
		}
		return Command{append(append(append([]string{"guix", "shell"}, spec...), "--"), c.Argv...), c.Env}, nil
	case "Missing":
		return c, ErrDeclared(MissingSays(l))
	case "WindowsTools":
		return windowsTools(l, c, ctx)
	case "Mpi", "Slurm", "Preload":
		// they wrap nothing: one says how several ranks are started, the other where the work is handed ( agent/ranks.go,
		// agent/slurm.go read them )
		return c, nil
	case "Wrap":
		// words put before the command, variables added ( ADR 0031 )
		env := map[string]string{}
		for k, v := range c.Env {
			env[k] = v
		}
		if more, ok := l["env"].(map[string]any); ok {
			for k, v := range more {
				env[k] = fmt.Sprint(v)
			}
		}
		return Command{append(l.Strings("words"), c.Argv...), env}, nil
	}
	return c, ErrNotYet{l.Type()}
}

// Wrap: the command as the whole stack makes it, from its innermost layer out
func Wrap(stack []project.Layer, c Command, ctx Context) (Command, error) {
	if c.Env == nil {
		c.Env = map[string]string{}
	}
	for i := len(stack) - 1; i >= 0; i-- {
		var err error
		if c, err = wrap(stack[i], c, ctx); err != nil {
			return c, err
		}
	}
	return c, nil
}

// Describe: a stack in a line, as a banner says it
func Describe(stack []project.Layer) string {
	if len(stack) == 0 || len(stack) == 1 && stack[0].Type() == "WindowsTools" {
		return "here"
	}
	var parts []string
	for _, l := range stack {
		if l.Type() == "WindowsTools" { // ( the machine before it says it is one )
			continue
		}
		what := strings.ToLower(l.Type())
		switch l.Type() {
		case "Micromamba", "Conda":
			if n := l.String("name"); n != "" || !MambaOwned(l) {
				what += ":" + n
			}
		case "Venv":
			what += ":" + l.String("python")
		case "Uv":
			if n := l.String("name"); n != "" {
				what += ":" + n
			}
		case "Nix":
			if f := l.String("flake"); f != "" {
				what += ":" + f
			} else {
				what += ":."
			}
		case "Guix":
			if m := l.String("manifest"); m != "" {
				what += ":" + m
			} else {
				what += ":" + strings.Join(l.Strings("packages"), " ")
			}
		case "Preload":
			if m := l.Strings("modules"); len(m) > 0 {
				what += ":" + strings.Join(m, ",")
			}
		case "Missing":
			what += ": " + l.String("what")
		case "Module":
			what += ":" + strings.Join(l.Strings("names"), ",")
		case "Ssh":
			what += ":" + l.String("host")
			if w, ok := l["windows"].(map[string]any); ok {
				if what = l.String("host"); w["box"] == true {
					what = "windows-box:" + what
				}
			}
		case "Apptainer":
			what += ":" + filepath.Base(l.String("image"))
		case "Docker", "Podman":
			what = containerName(l)
		case "Mpi":
			if launcher := l.String("launcher"); launcher != "" {
				what += ":" + launcher
			}
		case "Slurm":
			if part := l.String("partition"); part != "" {
				what += ":" + part
			} else {
				what += ":default partition"
			}
		}
		parts = append(parts, what)
	}
	return strings.Join(parts, " -> ")
}

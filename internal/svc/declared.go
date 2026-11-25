package svc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/lib"
)

// ServicePort is one port of a service, and its two sides; 0: errand chooses.
type ServicePort struct {
	Name      string `json:"name"`
	Announced int    `json:"announced"`
	Server    int    `json:"server"`
}

// ServiceSpec is a service, as data.
type ServiceSpec struct {
	Name      string            `json:"name"`
	Command   []string          `json:"command"`
	Env       map[string]string `json:"env"`
	Needs     map[string]any    `json:"needs"`
	Exclusive bool              `json:"exclusive"`
	Label     string            `json:"label"`
	Busy      any               `json:"busy"` // nothing, a line of shell, { "http_json": PATH }, { "function": true }
	Ready     any               `json:"ready"`
	Idle      float64           `json:"idle"`
	Grace     float64           `json:"grace"`
	Ports     []ServicePort     `json:"ports"`
	Others    bool              `json:"others"`
	Cwd       string            `json:"cwd"`
	Source    string            `json:"source"`
	From      string            `json:"from,omitempty"` // the directory its declaration was read around
	Provider  any               `json:"provider,omitempty"`
}

func (s ServiceSpec) Asks() queue.Needs {
	var words []string
	for _, name := range []string{"gpus", "gpu_ram", "ram", "cpus"} {
		switch v := s.Needs[name].(type) {
		case nil:
		case float64:
			words = append(words, "--"+strings.ReplaceAll(name, "_", "-"), strconv.FormatFloat(v, 'f', -1, 64))
		default:
			words = append(words, "--"+strings.ReplaceAll(name, "_", "-"), fmt.Sprint(v))
		}
	}
	n, _, _, _ := kit.NeedsFrom(words)
	return n
}

func (s ServiceSpec) Claims() bool { return !s.Asks().IsZero() || s.Exclusive }

var portField = regexp.MustCompile(`\{port(?::([a-z][a-z0-9_]*))?\}`)

// FillPorts: `{port:NAME}` -> the server's side of port NAME; `{port}` when there is one
func FillPorts(text string, server map[string]int) (string, error) {
	var bad error
	out := portField.ReplaceAllStringFunc(text, func(f string) string {
		name := portField.FindStringSubmatch(f)[1]
		if name == "" {
			if len(server) != 1 {
				bad = fmt.Errorf("{port} with several ports: {port:NAME}")
				return f
			}
			for _, p := range server {
				return strconv.Itoa(p)
			}
		}
		p, ok := server[name]
		if !ok {
			bad = fmt.Errorf("{port:%s}: no port %q", name, name)
			return f
		}
		return strconv.Itoa(p)
	})
	return out, bad
}

var serviceName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*(#[A-Za-z0-9_.-]+)?$`)

// Check: said when it is declared, not when it starts
func (s ServiceSpec) Check() error {
	if !serviceName.MatchString(s.Name) {
		return fmt.Errorf("a service's name: letters, digits, _ . -, a version after # ( got %q )", s.Name)
	}
	if len(s.Command) == 0 {
		return errors.New("a service needs a command")
	}
	if len(s.Ports) == 0 {
		return errors.New("a service listens on one port at least")
	}
	seen, dummy := map[int]bool{}, map[string]int{}
	for _, p := range s.Ports {
		dummy[p.Name] = 1
		for _, n := range []int{p.Announced, p.Server} {
			if n == 0 {
				continue
			}
			if n < 0 || n > 65535 {
				return fmt.Errorf("port %s: %d is not a port", p.Name, n)
			}
			if seen[n] {
				return fmt.Errorf("port %s: %d is given twice ( errand listens on the announced side and relays to the server's: they differ )", p.Name, n)
			}
			seen[n] = true
		}
	}
	texts := append([]string{}, s.Command...)
	for _, v := range s.Env {
		texts = append(texts, v)
	}
	if b, ok := s.Busy.(string); ok {
		texts = append(texts, b)
	}
	for _, t := range texts {
		if _, err := FillPorts(t, dummy); err != nil {
			return err
		}
	}
	return nil
}

// Duration: `"10m"`, `"30s"`, `"2h"`, `"90"` ( seconds )
func Duration(text string) (float64, error) {
	m := regexp.MustCompile(`^\s*([0-9.]+)\s*(ms|s|m|min|h|d)?\s*$`).FindStringSubmatch(text)
	if m == nil {
		return 0, fmt.Errorf("a duration: 30s, 10m, 2h ( got %q )", text)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	return v * map[string]float64{"": 1, "ms": 1e-3, "s": 1, "m": 60, "min": 60, "h": 3600, "d": 86400}[m[2]], err
}

// ParsePort: `[NAME=]ANNOUNCED[:SERVER]`, either side empty for errand to choose
func ParsePort(text string) (ServicePort, error) {
	p := ServicePort{Name: "port"}
	if name, rest, ok := strings.Cut(text, "="); ok {
		p.Name, text = name, rest
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(p.Name) {
		return p, fmt.Errorf("a port's name: lower case letters, digits, _ ( got %q )", p.Name)
	}
	a, s, _ := strings.Cut(text, ":")
	for _, side := range []struct {
		text string
		into *int
	}{{a, &p.Announced}, {s, &p.Server}} {
		if side.text = strings.TrimSpace(side.text); side.text == "" {
			continue
		}
		n, err := strconv.Atoi(side.text)
		if err != nil {
			return p, fmt.Errorf("port %s: ANNOUNCED:SERVER, either side may be empty ( got %q )", p.Name, text)
		}
		*side.into = n
	}
	return p, nil
}

// ── what is declared on this machine ─────────────────────────────────────────

// Declarations is what errand's Python said is declared on this machine.
type Declarations struct {
	Services map[string]ServiceSpec
	Sources  []string
	Dev      map[string]devSource
	Said     []string
	Python   []string // what reads it, for a probe that is a function
	Environ  []string
}

type devSource struct {
	Path string            `json:"path"`
	Env  string            `json:"env"`
	Envs map[string]string `json:"envs"`
}

// servicePython: the Python that reads declarations on this machine, and its surroundings, where errand's package is
func servicePython(environ []string) (string, []string, error) {
	if len(environ) == 0 {
		environ = os.Environ()
	}
	env, err := lib.With(environ)
	if err != nil {
		return "", nil, err
	}
	for _, name := range []string{project.Python(environ), "python"} {
		if path, err := kit.LookIn(name, env, "."); err == nil {
			return path, env, nil
		}
	}
	return "", env, errors.New("there is no Python here to read the services' declarations with")
}

func servicesFiles(environ []string) []string {
	if len(environ) == 0 {
		environ = os.Environ()
	}
	base, _ := kit.EnvOf(environ, "XDG_CONFIG_HOME")
	if base == "" {
		home, _ := kit.EnvOf(environ, "HOME")
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		base = filepath.Join(home, ".config")
	}
	return []string{filepath.Join(base, "errand", "services.py"), filepath.Join(base, "errand", "services-installed.json")}
}

var declaredKept struct {
	sync.Mutex
	got map[string]Declarations
}

// DeclaredServices: what is declared on this machine, around `cwd` -- the user's file, what was installed ( the user's file
// wins ), the project's files. A file that does not load hides nothing of the others: it is said.
func DeclaredServices(cwd string, environ []string) (Declarations, error) {
	if len(environ) == 0 { // asked from another machine: this agent's surroundings
		environ = os.Environ()
	}
	got := Declarations{Services: map[string]ServiceSpec{}, Environ: environ}
	var mark strings.Builder
	mark.WriteString(cwd)
	files := servicesFiles(environ)[:1]
	if root, declared := project.FindRoot(cwd); declared {
		here, _ := filepath.Glob(filepath.Join(root, "errand-*.py"))
		files = append(files, here...)
	}
	found := false
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			found = true
			fmt.Fprintf(&mark, "|%s %d %d", f, st.Size(), st.ModTime().UnixNano())
		}
	}
	if found { // else no Python is asked anything
		declaredKept.Lock()
		kept, ok := declaredKept.got[mark.String()]
		if !ok {
			var err error
			if kept, err = readServices(cwd, environ); err != nil {
				kept = Declarations{Said: []string{err.Error()}, Environ: environ}
			} else {
				if len(declaredKept.got) > 32 || declaredKept.got == nil {
					declaredKept.got = map[string]Declarations{}
				}
				declaredKept.got[mark.String()] = kept
			}
		}
		declaredKept.Unlock()
		got = kept
		got.Services = map[string]ServiceSpec{}
		for k, s := range kept.Services {
			got.Services[k] = s
		}
	}
	for k, d := range InstalledHere(environ) {
		if _, said := got.Services[k]; said {
			continue
		}
		spec, err := specOf(k, d)
		if err != nil {
			got.Said = append(got.Said, fmt.Sprintf("%s: %v", k, err))
			continue
		}
		spec.From = cwd
		got.Services[k] = spec
	}
	return got, nil
}

// readServices: the files that declare services, read by errand's Python
func readServices(cwd string, environ []string) (Declarations, error) {
	interpreter, env, err := servicePython(environ)
	if err != nil {
		return Declarations{}, err
	}
	cmd := exec.Command(interpreter, "-m", "errand.declared", "--services", cwd)
	cmd.Env = env
	if kit.IsDir(cwd) {
		cmd.Dir = cwd
	}
	var said bytes.Buffer
	cmd.Stderr = &said
	out, err := cmd.Output()
	if err != nil {
		return Declarations{}, fmt.Errorf("the services' declarations could not be read:\n%s", kit.EndOf(said.String(), 1500))
	}
	var doc struct {
		Services map[string]struct {
			ServiceSpec
			Ports [][]any `json:"ports"`
		} `json:"services"`
		Sources []string             `json:"sources"`
		Dev     map[string]devSource `json:"dev"`
		Said    []string             `json:"said"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return Declarations{}, fmt.Errorf("the services' declarations cannot be read: %v", err)
	}
	got := Declarations{Services: map[string]ServiceSpec{}, Sources: doc.Sources, Dev: doc.Dev, Said: doc.Said, Python: []string{interpreter}, Environ: env}
	for name, s := range doc.Services {
		spec := s.ServiceSpec
		spec.Name, spec.From = name, cwd
		spec.Ports = nil
		for _, p := range s.Ports {
			if len(p) == 3 {
				spec.Ports = append(spec.Ports, ServicePort{Name: kit.Text(p[0]), Announced: int(kit.Num(p[1], 0)), Server: int(kit.Num(p[2], 0))})
			}
		}
		got.Services[name] = spec
	}
	return got, nil
}

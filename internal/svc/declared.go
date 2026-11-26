package svc

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/queue"
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
	Busy      any               `json:"busy"` // nothing, a line of shell, or { "http": PATH, "field": NAME, "port": NAME }
	Ready     any               `json:"ready"`
	Idle      float64           `json:"idle"`
	Grace     float64           `json:"grace"`
	Ports     []ServicePort     `json:"ports"`
	Others    bool              `json:"others"`
	Listen    string            `json:"listen,omitempty"`   // of the port: the address it is listened on, beside 127.0.0.1's
	Always    bool              `json:"always,omitempty"`   // of the port: the system listens on it in errand's stead
	HoldEnd   float64           `json:"hold_end,omitempty"` // of the port: a client's end, passed on once the server was silent that long
	Cwd       string            `json:"cwd"`
	On        []project.Layer   `json:"on,omitempty"` // where it runs: a machine, and layers, in full
	Source    string            `json:"source"`
	Provider  any               `json:"provider,omitempty"`
	Keep      float64           `json:"keep,omitempty"`       // how long its ports stay after they were last used; 0: a day
	Version   string            `json:"version,omitempty"`    // raised when something errand does not see has changed
	VersionOf []string          `json:"version_of,omitempty"` // files whose contents make another server
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

// NeedsText: what it asks of the queue, as it was declared
func (s ServiceSpec) NeedsText() string {
	var words []string
	for _, name := range []string{"gpus", "gpu_ram", "ram", "cpus"} {
		if v := s.Needs[name]; v != nil {
			words = append(words, fmt.Sprintf("%s=%v", name, v))
		}
	}
	if s.Exclusive {
		words = append(words, "exclusive")
	}
	return strings.Join(words, "  ")
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
	if s.Listen != "" && net.ParseIP(s.Listen) == nil {
		return fmt.Errorf("listen: an address of this machine, as numbers ( 0.0.0.0, 100.64.1.2; got %q )", s.Listen)
	}
	texts := append([]string{}, s.Command...)
	for _, v := range s.Env {
		texts = append(texts, v)
	}
	for _, p := range []any{s.Busy, s.Ready} {
		if line, ok := p.(string); ok {
			texts = append(texts, line)
		}
	}
	for _, t := range texts {
		if _, err := FillPorts(t, dummy); err != nil {
			return err
		}
	}
	for _, p := range []any{s.Busy, s.Ready} {
		_, h, err := ProbeOf(p)
		if err != nil {
			return err
		}
		if h != nil && h.Port != "" && dummy[h.Port] == 0 {
			return fmt.Errorf("a probe: no port %q", h.Port)
		}
	}
	_, _, err := s.Where()
	if err != nil {
		return err
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

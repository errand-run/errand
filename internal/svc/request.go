// A request for a server as one JSON object ( `errand serve --spec -`, ADR 0040 ): what a library says, in any language.

package svc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/errand-run/errand/internal/project"
)

type span float64 // seconds, or `"10m"`

func (s *span) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case nil:
	case float64:
		*s = span(x)
	case string:
		d, err := Duration(x)
		*s = span(d)
		return err
	default:
		return fmt.Errorf("a duration: 30s, 10m, 2h, or seconds ( got %s )", b)
	}
	if *s < 0 {
		return fmt.Errorf("a duration: 30s, 10m, 2h, or seconds ( got %s )", b)
	}
	return nil
}

type request struct {
	Command   []string          `json:"command"`
	On        json.RawMessage   `json:"on"` // a machine's name, or layers
	Vars      map[string]string `json:"vars"`
	Cwd       string            `json:"cwd"`
	GPUs      any               `json:"gpus"`
	GPURam    any               `json:"gpu_ram"`
	RAM       any               `json:"ram"`
	CPUs      any               `json:"cpus"`
	Exclusive bool              `json:"exclusive"`
	Ports     []json.RawMessage `json:"ports"` // [ NAME, "ANNOUNCED:SERVER" | number | null ], in order
	Others    bool              `json:"others"`
	Listen    string            `json:"listen"`
	Always    bool              `json:"always"`
	HoldEnd   span              `json:"hold_end"`
	Busy      any               `json:"busy"`
	Ready     any               `json:"ready"`
	Idle      span              `json:"idle"`
	Grace     span              `json:"grace"`
	Keep      span              `json:"keep"`
	Label     string            `json:"label"`
	Version   any               `json:"version"`
	VersionOf []string          `json:"version_of"`
}

// ParseRequest: a request as JSON -> what it asks for, and the machine it names, when it is by its name
func ParseRequest(data []byte) (ServiceSpec, string, error) {
	var r request
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return ServiceSpec{}, "", fmt.Errorf("a request that cannot be read: %v", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return ServiceSpec{}, "", fmt.Errorf("a request is one JSON object")
	}
	s := ServiceSpec{Command: r.Command, Env: r.Vars, Cwd: r.Cwd, Exclusive: r.Exclusive, Others: r.Others, Listen: r.Listen, Always: r.Always, HoldEnd: float64(r.HoldEnd), Busy: r.Busy, Ready: r.Ready, VersionOf: r.VersionOf,
		Idle: float64(r.Idle), Grace: float64(r.Grace), Keep: float64(r.Keep), Label: r.Label, Needs: map[string]any{}}
	if s.Env == nil {
		s.Env = map[string]string{}
	}
	if r.Version != nil {
		s.Version = fmt.Sprint(r.Version)
	}
	for k, v := range map[string]any{"gpus": r.GPUs, "gpu_ram": r.GPURam, "ram": r.RAM, "cpus": r.CPUs} {
		if v != nil {
			s.Needs[k] = v
		}
	}
	for _, raw := range r.Ports {
		var pair []any
		if json.Unmarshal(raw, &pair) != nil || len(pair) != 2 {
			return s, "", fmt.Errorf("ports: a list of [ name, spec ] ( got %s )", raw)
		}
		name, ok := pair[0].(string)
		if !ok {
			return s, "", fmt.Errorf("ports: a list of [ name, spec ] ( got %s )", raw)
		}
		text := ""
		if pair[1] != nil {
			text = fmt.Sprint(pair[1])
		}
		p, err := ParsePort(name + "=" + text)
		if err != nil {
			return s, "", err
		}
		s.Ports = append(s.Ports, p)
	}
	machine := ""
	if len(r.On) > 0 && string(r.On) != "null" && json.Unmarshal(r.On, &machine) != nil {
		var on []project.Layer
		if err := json.Unmarshal(r.On, &on); err != nil {
			return s, "", fmt.Errorf("on: a machine's name, or a list of layers ( got %s )", r.On)
		}
		s.On = on
	}
	return s, machine, nil
}

var notInName = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// Complete: what is not said of it: a port, and what it is called -- its label, else its command's first word
func (s *ServiceSpec) Complete() {
	if len(s.Ports) == 0 {
		s.Ports = []ServicePort{{Name: "port"}}
	}
	if s.Name != "" {
		return
	}
	word := s.Label
	for i := 0; word == "" && i < len(s.Command); i++ { // ( `env A=b ollama serve` is ollama )
		if w := s.Command[i]; filepath.Base(w) != "env" && !strings.Contains(w, "=") {
			word = filepath.Base(w)
		}
	}
	s.Name = strings.Trim(notInName.ReplaceAllString(word, "-"), "-.")
}

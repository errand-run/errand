// Where a server runs ( ADR 0040 ): a machine, and the layers its command is started in, written in full.

package svc

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/project"
)

// Where: the machine it is to run on ( nil: this one ), and what is asked of that machine
func (s ServiceSpec) Where() (project.Layer, ServiceSpec, error) {
	on := s.On
	var machine project.Layer
	if len(on) > 0 && on[0].Type() == "Ssh" {
		machine, on = on[0], on[1:]
		if machine["windows"] != nil {
			return nil, s, fmt.Errorf("a server on a Windows: not yet")
		}
	}
	for _, l := range on {
		switch l.Type() {
		case "Vars", "Module", "Venv", "Uv", "Micromamba", "Conda":
		case "Ssh":
			return nil, s, fmt.Errorf("a server is reached by one machine: Ssh comes first, once ( Ssh( …, jump = … ) goes through another )")
		default:
			return nil, s, fmt.Errorf("a server in a %s layer: not yet", l.Type())
		}
	}
	s.On = on
	return machine, s, layer.Check(on)
}

// Started: the words and the variables it is started with, its layers around it, in the surroundings of a machine. `more`: the
// variables errand gives it ( its ports, its cards ).
func (s ServiceSpec) Started(command []string, vars, more map[string]string, environ []string) ([]string, map[string]string, error) {
	env := map[string]string{}
	for k, v := range more {
		env[k] = v
	}
	for k, v := range vars {
		env[k] = v
	}
	c, err := layer.Wrap(s.On, layer.Command{Argv: command, Env: env}, layer.Context{Root: s.Dir(), Environ: environ})
	if err != nil {
		return nil, nil, err
	}
	for k, v := range vars { // ( what is written for it comes after its layers )
		c.Env[k] = v
	}
	return c.Argv, c.Env, nil
}

// programs: the files its command leads to, outside in -- what its layers start, and, in an environment, the program itself
func (s ServiceSpec) programs(environ []string) ([]string, error) {
	argv, _, err := s.Started(s.Command, s.Env, nil, environ)
	if err != nil {
		return nil, err
	}
	first, err := kit.LookIn(argv[0], environ, s.Dir())
	if err != nil {
		return nil, fmt.Errorf("%s: %v", argv[0], err)
	}
	out := []string{first}
	for _, l := range s.On {
		if t := l.Type(); t == "Micromamba" || t == "Conda" {
			prefix, _ := envNamed(l.String("name"))
			if root := l.String("root_prefix"); prefix == "" && root != "" {
				prefix = filepath.Join(atHome(root), "envs", l.String("name"))
			}
			if prefix == "" { // ( one this errand does not find: its program is not followed )
				continue
			}
			if at := filepath.Join(prefix, "bin", s.Command[0]); !strings.ContainsRune(s.Command[0], '/') && kit.IsFile(at) {
				out = append(out, at)
			}
		}
	}
	return out, nil
}

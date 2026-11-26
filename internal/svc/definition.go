// What makes a server ( ADR 0040 ): two requests whose definitions are equal have one server.

package svc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/queue"
)

// Dir: where it starts -- the home directory, if nothing is said
func (s ServiceSpec) Dir() string {
	home, _ := os.UserHomeDir()
	switch {
	case s.Cwd == "" || s.Cwd == "~":
		return home
	case strings.HasPrefix(s.Cwd, "~/"):
		return filepath.Join(home, s.Cwd[2:])
	case !filepath.IsAbs(s.Cwd):
		return filepath.Join(home, s.Cwd)
	}
	return filepath.Clean(s.Cwd)
}

// Hash: of what can change its answers. Not of how long it is kept, of its label, of where its clients come. On the machine it runs on: its
// files are read.
func (s ServiceSpec) Hash() (string, error) { return s.hash(true) }

// Text: the same of what is written alone, on any machine
func (s ServiceSpec) Text() string { h, _ := s.hash(false); return h }

func (s ServiceSpec) hash(read bool) (string, error) {
	vars := s.Env
	if vars == nil {
		vars = map[string]string{}
	}
	var ports []string
	fixed := map[string]int{}
	for _, p := range s.Ports { // ( in order: a probe that names none is of the first )
		ports = append(ports, p.Name)
		if p.Server > 0 {
			fixed[p.Name] = p.Server
		}
	}
	files := map[string]string{}
	for _, f := range s.VersionOf {
		if !read {
			files[f] = ""
			continue
		}
		at := f
		if strings.HasPrefix(at, "~/") {
			home, _ := os.UserHomeDir()
			at = filepath.Join(home, at[2:])
		} else if !filepath.IsAbs(at) {
			at = filepath.Join(s.Dir(), at)
		}
		sum, err := kit.HashOfFile(at)
		if err != nil {
			return "", fmt.Errorf("version_of: %v", err)
		}
		files[f] = sum
	}
	b, err := json.Marshal(map[string]any{"command": s.Command, "vars": vars, "cwd": s.written(read), "needs": s.Asks().Normalized(1), "exclusive": s.Exclusive,
		"ports": ports, "server": fixed, "busy": s.Busy, "ready": s.Ready, "on": append([]project.Layer{}, s.On...), "version_of": files, "version": s.Version})
	if err != nil {
		return "", err
	}
	return Short(string(b)), nil
}

// written: its directory -- as it is said, where the home is another machine's
func (s ServiceSpec) written(read bool) string {
	if read {
		return s.Dir()
	}
	return s.Cwd
}

// Short: a hash, as it is read and typed
func Short(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:8]
}

// PortHash: of what makes a port of it -- the definition, and what concerns its connections
func (s ServiceSpec) PortHash(definition string) string {
	asked := ""
	for _, p := range s.Ports {
		if p.Announced > 0 {
			asked += fmt.Sprintf(" %s=%d", p.Name, p.Announced)
		}
	}
	if s.Listen != "" {
		asked += " listen=" + s.Listen
	}
	if s.Always {
		asked += " always"
	}
	if s.HoldEnd > 0 {
		asked += fmt.Sprintf(" hold_end=%g", s.HoldEnd)
	}
	if asked == "" && !s.Others {
		return definition
	}
	return Short(fmt.Sprintf("%s others=%v%s", definition, s.Others, asked))
}

// Program: the file its command leads to, and what tells it from another: a program that was updated is another server
func (s ServiceSpec) Program(environ []string) (string, string, error) {
	if len(s.Command) == 0 {
		return "", "", errors.New("a service needs a command")
	}
	paths, err := s.programs(environ)
	if err != nil {
		return "", "", err
	}
	said := ""
	for _, path := range paths {
		at, err := filepath.EvalSymlinks(path)
		if err != nil {
			at = path
		}
		st, err := os.Stat(at)
		if err != nil {
			return "", "", err
		}
		said += fmt.Sprintf("%s %d %d\n", at, st.Size(), st.ModTime().UnixNano())
	}
	return paths[0], Short(said)[:4], nil
}

// TooMuch: what it asks for that the machine does not have; ""
func (s ServiceSpec) TooMuch(c queue.Capacity) string {
	n := s.Asks().Normalized(1)
	number := func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
	switch {
	case n.CPUs > c.CPUs:
		return fmt.Sprintf("it needs %s cpus, the machine has %s", number(n.CPUs), number(c.CPUs))
	case n.RAM > c.RAM:
		return fmt.Sprintf("it needs %s MB of memory, the machine has %s", number(n.RAM), number(c.RAM))
	case n.GPUs > c.GPUs:
		return fmt.Sprintf("it needs %d gpus, the machine has %d", n.GPUs, c.GPUs)
	}
	return ""
}

// ShownVars: its variables, the values of those that hold a secret aside
func (s ServiceSpec) ShownVars() map[string]string {
	out := map[string]string{}
	for k, v := range s.Env {
		up := strings.ToUpper(k)
		for _, w := range []string{"TOKEN", "KEY", "SECRET", "PASSWORD"} {
			if strings.Contains(up, w) {
				v = "…"
			}
		}
		out[k] = v
	}
	return out
}

// ── where a server writes ────────────────────────────────────────────────────

// ServingDir: a server's directory on this machine -- `LABEL~KEY@HOST` ( a home may be several machines' )
func ServingDir(label, key string) string {
	return filepath.Join(kit.LocalCache(), "serving", label+"~"+key+"@"+queue.Host())
}

// ServingDirs: those of this machine, the last written to first: their label, their key
func ServingDirs() []ServingPlace {
	var out []ServingPlace
	dirs, _ := filepath.Glob(filepath.Join(kit.LocalCache(), "serving", "*@"+queue.Host()))
	for _, d := range dirs {
		label, key, ok := strings.Cut(strings.TrimSuffix(filepath.Base(d), "@"+queue.Host()), "~")
		st, err := os.Stat(filepath.Join(d, "keeper.log"))
		if !ok || err != nil {
			continue
		}
		at := ServingPlace{Dir: d, Label: label, Key: key, Last: st.ModTime(), Size: st.Size()}
		i := 0
		for i < len(out) && out[i].Last.After(at.Last) {
			i++
		}
		out = append(out[:i], append([]ServingPlace{at}, out[i:]...)...)
	}
	return out
}

type ServingPlace struct {
	Dir, Label, Key string
	Last            time.Time
	Size            int64 // of its journal
}

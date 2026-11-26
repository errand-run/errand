package svc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/errand-run/errand/internal/queue"
)

func TestWhatMakesAServer(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(file, []byte("1"), 0o644)
	base := func() ServiceSpec {
		return ServiceSpec{Name: "a", Command: []string{"sh", "{port}"}, Env: map[string]string{"A": "1", "B": "2"}, Needs: map[string]any{"ram": "10G"},
			Ports: []ServicePort{{Name: "port"}}, VersionOf: []string{file}}
	}
	hash := func(s ServiceSpec) string {
		h, err := s.Hash()
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	was := hash(base())
	for what, c := range map[string]struct {
		how  func(s *ServiceSpec)
		same bool
	}{
		"its label":       {func(s *ServiceSpec) { s.Name, s.Label = "b", "c" }, true},
		"how it is kept":  {func(s *ServiceSpec) { s.Idle, s.Grace, s.Keep = 1, 2, 3 }, true},
		"its port":        {func(s *ServiceSpec) { s.Ports[0].Announced, s.Others = 9000, true }, true},
		"the same memory": {func(s *ServiceSpec) { s.Needs["ram"] = "10240M" }, true},
		"its command":     {func(s *ServiceSpec) { s.Command = []string{"sh", "{port}", "x"} }, false},
		"a variable":      {func(s *ServiceSpec) { s.Env["A"] = "2" }, false},
		"its directory":   {func(s *ServiceSpec) { s.Cwd = "/tmp" }, false},
		"its needs":       {func(s *ServiceSpec) { s.Needs["cpus"] = "1" }, false},
		"a port's name":   {func(s *ServiceSpec) { s.Ports[0].Name = "http" }, false},
		"its probe":       {func(s *ServiceSpec) { s.Busy = "true" }, false},
		"its version":     {func(s *ServiceSpec) { s.Version = "2" }, false},
		"a file":          {func(s *ServiceSpec) { _ = os.WriteFile(file, []byte("2"), 0o644) }, false},
	} {
		_ = os.WriteFile(file, []byte("1"), 0o644)
		s := base()
		c.how(&s)
		if (hash(s) == was) != c.same {
			t.Errorf("%s: same server %v", what, !c.same)
		}
	}
	a, b := base(), base()
	b.Others = true
	if a.PortHash("x") != "x" || b.PortHash("x") == "x" {
		t.Error("whom a port lets in makes another port, of the same server")
	}
	if why := base().TooMuch(queue.Capacity{CPUs: 1, RAM: 1024}); why == "" {
		t.Error("10 GB of a machine that has 1")
	}
	s := ServiceSpec{Env: map[string]string{"HF_TOKEN": "x", "LEVEL": "3"}}
	if v := s.ShownVars(); v["HF_TOKEN"] != "…" || v["LEVEL"] != "3" {
		t.Errorf("%v", v)
	}
}

package tags

import (
	"reflect"
	"strings"
	"testing"
)

func TestExpressions(t *testing.T) {
	gpu := map[string]any{"cuda": true, "driver": "jax", "fp": "32|64", "n": 64.0}
	cpu := map[string]any{"cuda": false, "driver": "torch"}
	for expr, want := range map[string][2]bool{
		"":                      {true, true},
		"cuda":                  {true, true}, // the tag is present: the environment did mention it
		"cuda=True":             {true, false},
		"cuda=yes":              {true, false},
		"cuda=1":                {true, false},
		"cuda=no":               {false, true},
		"cuda=0":                {false, true},
		"!cuda":                 {false, false},
		"fp=64":                 {true, false},
		"fp=16":                 {false, false},
		"n=64":                  {true, false},
		"!fp":                   {false, true},
		"driver=jax & fp=64":    {true, false},
		"driver=jax & !cuda":    {false, false},
		"driver=torch | fp=32":  {true, true},
		"a & !b | driver=torch": {false, true},
		" driver = jax ":        {true, false},
		"cuda |":                {true, true}, // a stray `|` adds an alternative that matches everything
		"& cuda=True":           {true, false},
	} {
		for i, tags := range []map[string]any{gpu, cpu} {
			if got, err := Matches(expr, tags); err != nil || got != want[i] {
				t.Errorf("%q against %v: %v, %v; want %v", expr, tags, got, err, want[i])
			}
		}
	}
	for _, bad := range []string{"=64", "!", "cuda & !"} {
		if _, err := Matches(bad, gpu); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
}

func TestSelect(t *testing.T) {
	envs := map[string]map[string]any{
		"local":   {},
		"gpu":     {"driver": "jax", "cuda": true},
		"cluster": {"driver": "jax", "cuda": true, "fp": "64"},
		"both":    {"fp": "32|64"},
	}
	names := func(cs []Choice) string {
		var out []string
		for _, c := range cs {
			one := c.Env
			if fp, ok := c.Tags["fp"]; ok {
				one += ":" + said(fp)
			}
			out = append(out, one)
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct {
		what string
		a    Asked
		want string
	}{
		{"nothing said: the default", Asked{Default: "local"}, "local"},
		{"by name, in the order named", Asked{Envs: []string{"gpu", "local"}}, "gpu local"},
		{"a tag's flag: every environment that can be it", Asked{Flags: map[string][]string{"driver": {"jax"}}}, "both:32|64 cluster:64 gpu local"},
		{"... one that says otherwise cannot", Asked{Flags: map[string][]string{"cuda": {"no"}}}, "both:32|64 local"},
		{"saying nothing means every value", Asked{Flags: map[string][]string{"fp": {"32"}}}, "both:32 gpu:32 local:32"},
		{"one that pins it is selected for that value only", Asked{Flags: map[string][]string{"fp": {"64"}}}, "both:64 cluster:64 gpu:64 local:64"},
		{"a grid of a tag crosses what covers it", Asked{Envs: []string{"cluster", "both"}, Flags: map[string][]string{"fp": {"64", "32"}}}, "cluster:64 both:64 both:32"},
		{"an expression", Asked{Expr: "cuda=True & !fp"}, "gpu"},
		{"an expression and a flag", Asked{Expr: "cuda", Flags: map[string][]string{"fp": {"64"}}}, "cluster:64 gpu:64"},
	} {
		got, err := Select(envs, c.a)
		if err != nil || names(got) != c.want {
			t.Errorf("%s: %q, %v; want %q", c.what, names(got), err, c.want)
		}
	}
	if _, err := Select(envs, Asked{Envs: []string{"cluster"}, Flags: map[string][]string{"fp": {"32"}}}); err == nil || !strings.Contains(err.Error(), "cannot be fp=32") {
		t.Errorf("an environment named that cannot have the value: %v", err)
	}
	if _, err := Select(envs, Asked{Envs: []string{"nope"}}); err == nil || !strings.Contains(err.Error(), "both, cluster, gpu, local") {
		t.Errorf("an environment nobody declared: %v", err)
	}
	if got, _ := Select(map[string]map[string]any{}, Asked{Default: "local"}); len(got) != 1 || got[0].Env != "local" {
		t.Errorf("`local`, declared by nobody: %v", got)
	}
	got, _ := Select(envs, Asked{Envs: []string{"gpu"}, Flags: map[string][]string{"fp": {"32"}}})
	if !reflect.DeepEqual(got[0].Tags, map[string]any{"driver": "jax", "cuda": true, "fp": "32"}) {
		t.Errorf("its tags, with what was chosen: %v", got[0].Tags)
	}
}

package agent

import "testing"

func TestWhichVersionIsAhead(t *testing.T) {
	order := []string{"0+source", "2026.11.25.1a1", "2026.11.25.1rc1", "2026.11.25.1rc2", "2026.11.25.1", "2026.11.25.1+dev.1.g3e4580c",
		"2026.11.25.1+dev.12.gabc.dirty", "2026.11.25.2", "2026.12.0a1", "2026.12.0", "2026.12.1.1"}
	for i, a := range order {
		for k, b := range order {
			if got := versionBefore(a, b); got != (i < k) {
				t.Errorf("%s before %s: %v", a, b, got)
			}
		}
	}
}

func TestWhatTheStableNameGoesTo(t *testing.T) {
	was := currentNote{"h1", "1", "2026.12.0", "linux/amd64", "lmo"}
	for what, c := range map[string]struct {
		n    currentNote
		goes bool
	}{
		"a later version":                     {currentNote{"h2", "1", "2026.12.1", "linux/amd64", "lmo"}, true},
		"an earlier one":                      {currentNote{"h2", "1", "2026.11.25.1", "linux/amd64", "lmo"}, false},
		"the same, another build":             {currentNote{"h2", "1", "2026.12.0", "linux/amd64", "lmo"}, true},
		"a later release":                     {currentNote{"h2", "2", "2026.11.25.1", "linux/amd64", "lmo"}, true},
		"another kind of machine, one home":   {currentNote{"h2", "1", "2026.12.1", "linux/arm64", "node"}, false},
		"the machine itself, of another kind": {currentNote{"h2", "1", "2026.12.1", "linux/arm64", "lmo"}, true},
	} {
		if got := c.n.replaces(was); got != c.goes {
			t.Errorf("%s: %v", what, got)
		}
	}
}

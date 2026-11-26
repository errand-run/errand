package agent

import (
	"testing"

	"github.com/errand-run/errand/internal/reach"
)

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
	was := currentNote{"h1", "1", "2026.12.0", "linux/amd64", "lmo", 2}
	for what, c := range map[string]struct {
		n    currentNote
		goes bool
	}{
		"a later version":                     {currentNote{"h2", "1", "2026.12.1", "linux/amd64", "lmo", 2}, true},
		"an earlier one":                      {currentNote{"h2", "1", "2026.11.25.1", "linux/amd64", "lmo", 2}, false},
		"the same, another build":             {currentNote{"h2", "1", "2026.12.0", "linux/amd64", "lmo", 2}, true},
		"the same, built later":               {currentNote{"h2", "1", "2026.12.0", "linux/amd64", "lmo", 3}, true},
		"the same, built before":              {currentNote{"h2", "1", "2026.12.0", "linux/amd64", "lmo", 1}, false},
		"a later release":                     {currentNote{"h2", "2", "2026.11.25.1", "linux/amd64", "lmo", 2}, true},
		"another kind of machine, one home":   {currentNote{"h2", "1", "2026.12.1", "linux/arm64", "node", 2}, false},
		"the machine itself, of another kind": {currentNote{"h2", "1", "2026.12.1", "linux/arm64", "lmo", 2}, true},
	} {
		if got := c.n.replaces(was); got != c.goes {
			t.Errorf("%s: %v", what, got)
		}
	}
}

func TestWhichAgentComesBefore(t *testing.T) {
	v := func(release, errand string, built int64) reach.AgentVersion {
		return reach.AgentVersion{Release: release, Errand: errand, Built: built}
	}
	for what, c := range map[string]struct {
		a, b   reach.AgentVersion
		before bool
	}{
		"a release before":               {v("1", "2026.12.1", 9), v("2", "2026.11.1", 1), true},
		"a version before":               {v("1", "2026.11.1", 9), v("1", "2026.12.0", 1), true},
		"one built before, same version": {v("1", "0+dev.4.gabc.dirty", 1), v("1", "0+dev.4.gabc.dirty", 2), true},
		"one built later":                {v("1", "0+dev.4.gabc.dirty", 2), v("1", "0+dev.4.gabc.dirty", 1), false},
		"no stamp, before one":           {v("1", "0+source", 0), v("1", "0+source", 5), true},
		"the same: neither":              {v("1", "0+source", 5), v("1", "0+source", 5), false},
	} {
		if got := c.a.Before(c.b); got != c.before {
			t.Errorf("%s: %v", what, got)
		}
	}
}

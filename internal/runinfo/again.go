package runinfo

import (
	"strings"

	"github.com/errand-run/errand/internal/project"
)

// Redo is what it takes to make the run of a file's entries again with some of them: how its tool is told which.
type Redo struct {
	Run   project.Running `json:"run"`
	File  string          `json:"file"`
	Rest  []string        `json:"rest,omitempty"` // what followed the target, which is the tool's
	Tools map[string]any  `json:"tools,omitempty"`
}

// madeAgain is what the record of a run says of one made after it: which, and where.
type madeAgain struct {
	ID    string `json:"id"`
	Leaf  string `json:"leaf"`
	Host  string `json:"host,omitempty"`
	Asked string `json:"asked"`
}

func AsksSaid(asks []Ask) string {
	var parts []string
	seen := map[string]bool{}
	for _, a := range asks {
		if w := a.Said(); !seen[w] {
			seen[w] = true
			parts = append(parts, w)
		}
	}
	return strings.Join(parts, " and ")
}

// theMade: what a record says of the runs made again
func theMade(next []Launched, said []string) []madeAgain {
	var out []madeAgain
	for i, l := range next {
		w := ""
		if i < len(said) {
			w = said[i]
		}
		out = append(out, madeAgain{ID: l.Note.ID, Leaf: l.Note.Leaf, Host: l.Host, Asked: w})
	}
	return out
}

// NoteAsked: what a run's record says once it asked -- what, and what was done about it. `again` and `refused` are
// what `a.again` said. A run that stopped itself to be run again was not a failure; one that is not run again is.
func (r *Record) NoteAsked(asks []Ask, stopped bool, next []Launched, said []string, refused string) {
	r.Asked = asks
	switch {
	case refused != "":
		why := refused
		r.Status, r.Error = "FAIL", &why
	default:
		r.Again = theMade(next, said)
		if stopped && r.Status == "FAIL" { // stopped to be run again, which is not how a run fails
			where := ""
			if len(next) > 0 {
				where = " ( " + next[0].Note.Leaf + " )"
			}
			why := "asked for " + AsksSaid(asks) + ": run again" + where
			r.Status, r.Error = "SKIP", &why
		}
	}
}

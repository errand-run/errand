package page

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
)

// The views of a run that goes on another machine ( ADR 0017 ): a directory with its own page is read here, from its copy.
// It is brought while somebody looks at the run, and what changed in it each time the run names it again.

type bringing struct {
	under  bool    // on its way
	have   float64 // the naming it was brought at
	failed float64 // the naming it could not be brought at
}

// seenAt: where a view opened as it is is served here, and whether it is ready; "" while it is not here
func (u *Ui) seenAt(row *UiRow, m *UiMachine, leaf, p string, v uiView) (string, bool) {
	if m.Here() {
		return servedAt(true, leaf, p, v.Static)
	}
	rel, inside := strings.CutPrefix(p, leaf+"/")
	if !inside || !filepath.IsLocal(filepath.FromSlash(rel)) {
		return "", false // ( what a run names is in its directory: nothing is written elsewhere here )
	}
	here := u.placeHere(row, leaf)
	at := filepath.Join(here, filepath.FromSlash(rel))
	u.Mu.Lock()
	b := u.bringing[at]
	if b == nil {
		b = &bringing{}
		u.bringing[at] = b
	}
	ask := !b.under && b.have < v.At && b.failed < v.At
	if ask {
		b.under = true
	}
	u.Mu.Unlock()
	if ask {
		go func() {
			_, err := u.Ask(nil, map[string]any{"op": "bring_view", "host": m.M.Spec(), "from": p, "to": at, "last": v.Last})
			u.Mu.Lock()
			b.under = false
			if err == nil {
				b.have = v.At
			} else {
				b.failed = v.At
			}
			u.Mu.Unlock()
			if err != nil {
				u.Said(path.Base(p) + " not brought here: " + kit.FirstLine(err.Error()))
			}
			u.Changed(false, []string{row.Key}) // ( drawn again: ready; or named again meanwhile, and brought again )
		}()
	}
	if st, err := os.Stat(at); err != nil || !st.IsDir() {
		return "", false
	}
	return servedAt(true, here, at, v.Static)
}

// placeHere: the directory of a run that is elsewhere, here -- where it will come back when it ends
func (u *Ui) placeHere(row *UiRow, leaf string) string {
	if r, ok := row.What.(*reach.TableRow); ok {
		var n runinfo.RunNote
		if data, err := os.ReadFile(filepath.Join(runinfo.NotesDir(), path.Base(r.ID)+".json")); err == nil && json.Unmarshal(data, &n) == nil && n.Leaf != "" && n.There != nil {
			return n.Leaf
		}
		return filepath.Join(u.runsHere(r.Project), filepath.FromSlash(relOf(leaf)))
	}
	return filepath.Join(u.runsHere(""), filepath.FromSlash(relOf(leaf)))
}

// runsHere: where a project's runs are here -- beside those that are, else under errand's cache
func (u *Ui) runsHere(project string) string {
	if project != "" {
		for _, o := range u.runs() {
			if o.There == "" && reach.SameProject(o.Project, project) {
				if i := strings.Index(filepath.ToSlash(o.Leaf), "/runs/"); i >= 0 {
					return o.Leaf[:i+6]
				}
			}
		}
	}
	return filepath.Join(kit.LocalCache(), "brought", path.Base(filepath.ToSlash(project)), "errand-outputs", "runs") + "/"
}

// The project's lock ( ADR 0044 ): a machine that resolves an environment of errand's says what it came to, and the side
// that drives the command writes it into `errand-envs.lock`, where the next push carries it. Only this side writes it:
// a file of the project changed over there would stop the next push ( ADR 0038 ).

package command

import (
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/reach"
)

// lockTaker: what writes a resolution said by an agent into the lock of the project `doc` declares -- nil without one
func lockTaker(t *reach.Term, doc *project.Document) func(any) {
	if doc == nil || doc.Root == "" {
		return nil
	}
	return func(payload any) {
		var got struct {
			Declaration string           `json:"declaration"`
			Name        string           `json:"name"`
			Resolution  layer.Resolution `json:"resolution"`
		}
		if reach.DecodeInto(payload, &got) != nil || got.Declaration == "" || got.Resolution.ID == "" {
			return
		}
		lock, err := layer.ReadLock(doc.Root)
		if err != nil {
			t.Say("errand: " + err.Error() + ": the resolution of " + got.Name + " is not written down")
			return
		}
		added := lock.Add(got.Declaration, got.Name, got.Resolution)
		pruned := false
		if current, ok := mambaDeclared(doc); ok {
			pruned = lock.Keep(current)
		}
		if !added && !pruned {
			return
		}
		if err := lock.Write(doc.Root); err != nil {
			t.Say("errand: " + layer.LockName + " could not be written: " + err.Error())
			return
		}
		if added {
			t.Say("  " + got.Name + ": resolved on " + got.Resolution.Resolved[len("2006-01-02 on "):] + ", written into " + layer.LockName + " ( to commit )")
		}
	}
}

// mambaDeclared: the declarations of every environment of errand's the project has -> false when one cannot be read here,
// and nothing is to be taken out of the lock
func mambaDeclared(doc *project.Document) (map[string]bool, bool) {
	out := map[string]bool{}
	for _, env := range doc.Envs {
		for _, stack := range [][]project.Layer{env.Stack, env.Prepare} {
			for _, l := range stack {
				if _, own := layer.UvLocked(l, doc.Root); !layer.StoreOwned(l) || own {
					continue
				}
				d, err := layer.DeclarationOf(l, doc.Root)
				if err != nil {
					return nil, false
				}
				out[d.Hash()] = true
			}
		}
	}
	return out, true
}

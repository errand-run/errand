package agent

import (
	"time"

	"github.com/errand-run/errand/internal/download"
	"github.com/errand-run/errand/internal/reach"
)

// roots: the projects this agent has runs going in
func (a *Place) busyRoots() map[string]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]bool{}
	for _, j := range a.jobs {
		j.mu.Lock()
		if j.code == nil {
			out[j.rec.Project] = true
		}
		j.mu.Unlock()
	}
	for _, b := range a.batch { // a run on a node may have no way out, where this agent has one: it sees the same disk
		b.mu.Lock()
		if b.code == nil {
			out[b.meta.Project] = true
		}
		b.mu.Unlock()
	}
	return out
}

// watchWants: the requests of this agent's runs, looked for and answered, for as long as the agent lives
func (a *Place) watchWants() {
	for {
		time.Sleep(700 * time.Millisecond)
		seen := map[string]bool{}
		for root := range a.busyRoots() {
			for _, w := range download.WantsIn(root) {
				seen[w.ID] = true
				a.wants.Mu.Lock()
				last, tried := a.wants.Tried[w.ID]
				again := !tried || time.Since(last) > time.Minute
				if again {
					a.wants.Tried[w.ID] = time.Now()
				}
				a.wants.Mu.Unlock()
				if again {
					go a.answer(w)
				}
			}
		}
		a.wants.Mu.Lock()
		changed := false
		for id := range a.wants.Pending { // answered by somebody, or given up by its run
			if !seen[id] {
				delete(a.wants.Pending, id)
				delete(a.wants.Tried, id)
				changed = true
			}
		}
		a.wants.Mu.Unlock()
		if changed {
			a.touched()
		}
	}
}

// answer: a request answered by this agent if it has a way, else by the agent it depends on; left for whoever
// follows otherwise
func (a *Place) answer(w download.Want) {
	if _, ok := download.AnswerWant(w); ok {
		return
	}
	if a.parent != "" {
		if got, err := reach.AskAgent(a.parent, map[string]any{"op": "want", "want": w}); err == nil && got["answered"] == true {
			return
		}
	}
	a.wants.Mu.Lock()
	_, known := a.wants.Pending[w.ID]
	a.wants.Pending[w.ID] = w
	a.wants.Mu.Unlock()
	if !known {
		a.wakeJobs()
	}
}

// wakeJobs: whoever follows a run here looks again at what this agent has to say
func (a *Place) wakeJobs() {
	a.mu.Lock()
	all := make([]*job, 0, len(a.jobs))
	for _, j := range a.jobs {
		all = append(all, j)
	}
	a.mu.Unlock()
	for _, j := range all {
		j.mu.Lock()
		j.wake()
		j.mu.Unlock()
	}
}

// pendingFor: the requests of a project nobody here has a way to
func (a *Place) pendingFor(root string) []download.Want {
	a.wants.Mu.Lock()
	defer a.wants.Mu.Unlock()
	var out []download.Want
	for _, w := range a.wants.Pending {
		if len(w.Dir) >= len(root) && w.Dir[:len(root)] == root {
			out = append(out, w)
		}
	}
	return out
}

// serveWant: a request of an agent that depends on this one
func (a *Place) serveWant(req map[string]any, f *reach.FarChan) {
	var w download.Want
	if err := reach.DecodeInto(req["want"], &w); err != nil || w.ID == "" {
		f.Fail("a request that cannot be read")
		return
	}
	ans, ok := download.AnswerWant(w)
	f.Reply(map[string]any{"ok": true, "answered": ok, "error": ans.Error})
}

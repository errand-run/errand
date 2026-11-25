package runinfo

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
)

// FarRun is what is known here of a run that is elsewhere.
type FarRun struct {
	Name    string   `json:"name"`    // the machine, as it names itself
	Leaf    string   `json:"leaf"`    // its directory, there
	Root    string   `json:"root"`    // the project's copy, there
	Outputs string   `json:"outputs"` // where errand writes for the project, there
	LeafRel string   `json:"leaf_rel"`
	Pull    []string `json:"pull,omitempty"`   // what else comes back with it, from the copy's root
	Caches  []string `json:"caches,omitempty"` // the caches to bring back: only what is missing here
}

// what is no part of a copy, and what is built over there, as a sync reads them ( ADR 0006 )
var (
	NotCopied  = []string{".git", "__pycache__", "*.pyc", ".mypy_cache", ".pytest_cache", ".errand-work"}
	BuiltThere = []string{"build", "dist", "target", "node_modules", ".venv", "*.egg-info", "*.so", "*.o"}
)

func Counted(what string, r *filesync.Report, since time.Time) string {
	return fmt.Sprintf("%s: %d file%s, %s, %s", what, len(r.Sent), reach.Plural(len(r.Sent)), Sized(r.Bytes), Took(time.Since(since).Seconds()))
}

func Sized(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d B", n)
	case n < 1e6:
		return fmt.Sprintf("%.0f kB", float64(n)/1e3)
	case n < 1e9:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	}
	return fmt.Sprintf("%.1f GB", float64(n)/1e9)
}

// Collect: what a run that ended over there wrote, brought back -- its directory, what the project declares as
// coming back, its caches -- and nothing of its keeper kept there: it was looked at
func Collect(t *reach.Term, l *reach.Link, note RunNote, quiet bool) error {
	far := note.There
	outputs := strings.TrimSuffix(note.Leaf, filepath.FromSlash(far.LeafRel))
	since := time.Now()
	defer t.Working("work", "↓ pull ← "+far.Name)()
	got, err := filesync.Pull(l, far.Outputs, outputs, []string{far.LeafRel}, nil, false, far.Name, t.Say)
	if err != nil {
		return err
	}
	if len(far.Pull) > 0 {
		more, err := filesync.PullBack(l, far.Root, note.Project, far.Pull, far.Name, t.Say)
		if err != nil {
			return err
		}
		got.Sent, got.Bytes = append(got.Sent, more.Sent...), got.Bytes+more.Bytes
	}
	if len(far.Caches) > 0 {
		keep, _ := filesync.NewSelection(NotCopied, false, nil)
		if _, err := filesync.Pull(l, far.Root, note.Project, far.Caches, keep, true, far.Name, t.Say); err != nil {
			return err
		}
	}
	if !quiet && time.Since(since) > 2*time.Second {
		t.Say(Counted("  ↓ pull ← "+far.Name, got, since))
	}
	Settled(note.Leaf, note.Host, far.Leaf)
	_, _ = l.Ask(map[string]any{"op": "forget", "id": note.ID})
	return nil
}

// State: how a run is, asked of the agent that holds it -- running, waiting ( and for what ), how it ended, "gone"
// when nothing of it is left, "unreachable" when its machine does not answer. One that ended elsewhere is collected on
// the way: from then on it is here.
func (n RunNote) State(t *reach.Term) (string, string) {
	if n.Host != nil {
		if r, ok := ReadRecord(filepath.Join(n.Leaf, ResultName)); ok {
			return "ended", r.Status
		}
	}
	at := n.Host
	if at != nil && at.VM != nil { // a machine that went is not booted to be asked
		still := *at
		still.NoBoot = true
		at = &still
	}
	l, err := reach.LinkTo(t, at)
	if err != nil {
		return "unreachable", strings.SplitN(err.Error(), "\n", 2)[0]
	}
	got, err := l.Ask(map[string]any{"op": "state", "ids": []string{n.ID}})
	if err != nil {
		return "unreachable", err.Error()
	}
	states, _ := got["states"].(map[string]any)
	pair := kit.Strs(states[n.ID])
	if len(pair) != 2 {
		return "unreachable", "it did not say"
	}
	if pair[0] == "ended" && n.Host != nil {
		if err := Collect(t, l, n, true); err != nil {
			return "ended", pair[1] + ", not brought back: " + err.Error()
		}
	}
	return pair[0], pair[1]
}

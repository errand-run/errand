package runinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
)

// RunNote is what this machine knows of a run: where its keeper's record is, and what it is.
type RunNote struct {
	V       int     `json:"v"`
	ID      string  `json:"id"`
	Leaf    string  `json:"leaf"` // the run's directory
	What    string  `json:"what"`
	Env     string  `json:"env"`
	Project string  `json:"project"`
	Started float64 `json:"started"`
	Batch   bool    `json:"batch,omitempty"`
	// when it runs elsewhere: how that machine is reached, and where it is there. `Leaf` is then where its
	// directory comes back, here.
	Host  *reach.HostSpec `json:"host,omitempty"`
	There *FarRun         `json:"there,omitempty"`
}

func NotesDir() string { return filepath.Join(kit.LocalCache(), "runs") }

func (n RunNote) path() string { return filepath.Join(NotesDir(), n.ID+".json") }

func (n RunNote) Write() {
	if os.MkdirAll(NotesDir(), 0o755) == nil {
		_ = kit.WriteRecord(n.path(), n)
	}
}

func (n RunNote) Forget() { _ = os.Remove(n.path()) }

// Notes: the runs this machine knows, the oldest first. One nothing is left of is forgotten as it is noticed.
func Notes(t *reach.Term) []RunNote {
	var out []RunNote
	files, _ := filepath.Glob(filepath.Join(NotesDir(), "*.json"))
	for _, f := range files {
		var n RunNote
		data, err := os.ReadFile(f)
		if err != nil || kit.JsonUnmarshal(data, &n) != nil || n.ID == "" {
			continue
		}
		if st, _ := n.State(t); st == "gone" {
			n.Forget()
			continue
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started < out[j].Started })
	return out
}

// NoteOf: the run `id` names -- its id, or a beginning of it that names one run only
func NoteOf(t *reach.Term, id string) (RunNote, error) {
	var found []RunNote
	for _, n := range Notes(t) {
		if n.ID == id {
			return n, nil
		}
		if strings.HasPrefix(n.ID, id) {
			found = append(found, n)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return RunNote{}, fmt.Errorf("no run %s here ( `errand status` lists them )", id)
	}
	return RunNote{}, fmt.Errorf("%s is the beginning of %d runs' ids: say more of it", id, len(found))
}

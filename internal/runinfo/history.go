// The runs of this machine, for whoever looks at them ( ADR 0023, 0025 ): one line of JSON per run that ended here or
// came back here, appended to `<cache>/history.jsonl` -- what its record says, shortly. A run's directory is the
// record; a line whose directory is gone says nothing any more.

package runinfo

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
)

// PastRun is a run, as the history says it.
type PastRun struct {
	Leaf     string         `json:"leaf"`
	Rel      string         `json:"rel"`  // its directory, from the tree of runs it is in
	File     string         `json:"file"` // what ran, from the project's root
	Name     string         `json:"name"` // what of it: an entry, a selection; "" all of it
	What     string         `json:"what"`
	Project  string         `json:"project"`
	Env      string         `json:"env"`
	Host     string         `json:"host"`
	Status   string         `json:"status"`
	Started  float64        `json:"started"`
	Duration *float64       `json:"duration,omitempty"`
	Params   map[string]any `json:"params,omitempty"`
	Results  map[string]any `json:"results,omitempty"`
	Error    string         `json:"error,omitempty"`
	Counts   [3]int         `json:"counts,omitempty"` // entries passed, failed, skipped
	// where it ran, when that was elsewhere: the machine it is reached by, and its directory there
	Far     *reach.HostSpec `json:"far,omitempty"`
	FarLeaf string          `json:"far_leaf,omitempty"`

	There string `json:"-"` // found on another machine only, by a check
}

// RunsOf: the tree of runs of a project, or of its copy
func RunsOf(root string) string { return filepath.Join(root, "errand-outputs", "runs") }

// LetGo: a run whose directory where it ran is there no more: the history says it is nowhere but here
func (r PastRun) LetGo() error {
	r.FarLeaf = ""
	f, err := os.OpenFile(HistoryPath(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(r)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	return err
}

func HistoryPath() string { return filepath.Join(kit.LocalCache(), "history.jsonl") }

// PastRuns: the runs whose directory is still there, the oldest first
func PastRuns() []PastRun {
	f, err := os.Open(HistoryPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	by := map[string]PastRun{}
	in := bufio.NewScanner(f)
	in.Buffer(make([]byte, 1<<20), 16<<20)
	for in.Scan() {
		var r PastRun
		if json.Unmarshal(in.Bytes(), &r) == nil && r.Leaf != "" {
			by[r.Leaf] = r
		}
	}
	out := make([]PastRun, 0, len(by))
	for _, r := range by {
		if _, err := os.Stat(r.Leaf); err == nil {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started < out[j].Started })
	return out
}

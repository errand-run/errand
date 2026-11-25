package runinfo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
)

// Settled: a run ended here, or came back here -- said beside it ( the summary of its siblings ), and in the history
func Settled(leaf string, far *reach.HostSpec, farLeaf string) {
	Summarize(filepath.Dir(leaf))
	r, ok := ReadRecord(filepath.Join(leaf, ResultName))
	if !ok {
		return
	}
	p := PastRun{Leaf: leaf, File: r.File, What: r.Name, Project: r.Project, Env: r.Env, Host: r.Host, Status: r.Status, Started: r.Started,
		Duration: r.Duration, Params: r.Params, Far: far, FarLeaf: farLeaf}
	if far != nil { // it came back: its project is the one here
		if i := strings.Index(filepath.ToSlash(leaf), "/errand-outputs/"); i >= 0 {
			p.Project = leaf[:i]
		}
	}
	if l, err := readLabel(leaf); err == nil && l != "" {
		p.What = l
	}
	if i := strings.Index(filepath.ToSlash(leaf), "/runs/"); i >= 0 {
		p.Rel = filepath.ToSlash(leaf)[i+6:]
	}
	if r.File != "" && r.Name != r.File {
		p.Name = strings.TrimPrefix(strings.TrimPrefix(r.Name, r.File), "/")
	}
	if p.File == "" {
		p.File = r.Name
	}
	if len(r.Results) < 40 {
		p.Results = map[string]any{}
		for k, v := range r.Results {
			if len(r.Entries) > 0 && (k == "passed" || k == "failed" || k == "skipped") {
				continue // ( said as counts )
			}
			switch v.(type) {
			case float64, int, int64, bool, string:
				p.Results[k] = v
			}
		}
	}
	if r.Error != nil {
		p.Error = kit.FirstLine(*r.Error)
	}
	for _, e := range r.Entries {
		switch e.Status {
		case "PASS":
			p.Counts[0]++
		case "FAIL":
			p.Counts[1]++
		default:
			p.Counts[2]++
		}
	}
	_ = os.MkdirAll(kit.LocalCache(), 0o755)
	f, err := os.OpenFile(HistoryPath(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(p)
	_, _ = f.Write(append(b, '\n'))
}

// readLabel: what a run is called, as its launch said it
func readLabel(leaf string) (string, error) {
	data, err := os.ReadFile(filepath.Join(leaf, LaunchName))
	if err != nil {
		return "", err
	}
	var l struct {
		Label string `json:"label"`
	}
	err = json.Unmarshal(data, &l)
	return l.Label, err
}

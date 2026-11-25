// Where a run says it is, read as it goes ( ADR 0032, ADR 0016 ): the agent that holds a run reads its journal,
// `steps.jsonl`, from where it stopped, and says the state it comes to -- the text it last gave, how far it says it is,
// the last value of each curve, and how long is left when two points tell how fast it goes. Whoever drives draws it
// as a line under the last one of the run on its terminal. Nothing of this is read unless somebody who draws it follows the run.

package draw

import (
	"bytes"
	"encoding/json"
	"os"

	"github.com/errand-run/errand/internal/kit"
)

type StepTail struct {
	Path  string
	at    int64 // how far the file was read: always at the start of a line
	state StepState
	first *[2]float64 // when and how far it said it was, the first time it said how far
	last  float64     // when it spoke last
	said  bool        // anything said yet
}

// Poll: the state after what was written since the last look -> it, and whether it changed
func (s *StepTail) Poll() (*StepState, bool) {
	f, err := os.Open(s.Path)
	if err != nil {
		return nil, false // ( made before the run was handed what it writes: not looked for before it exists )
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() <= s.at {
		return nil, false
	}
	data := make([]byte, st.Size()-s.at)
	n, _ := f.ReadAt(data, s.at)
	data = data[:n]
	if end := bytes.LastIndexByte(data, '\n'); end < 0 {
		return nil, false // a line not whole yet
	} else {
		data = data[:end+1]
	}
	s.at += int64(len(data))
	changed := false
	for _, line := range bytes.Split(data, []byte("\n")) {
		var e map[string]any
		if len(bytes.TrimSpace(line)) == 0 || json.Unmarshal(line, &e) != nil {
			continue
		}
		if _, transfer := e["transfer"]; transfer {
			continue // what crossed to get the run there and back is errand's, not the run's steps
		}
		t := kit.Num(e["t"], s.last)
		for name, v := range e {
			switch name {
			case "t", "view", "title", "last", "open", "dir":
			case "text":
				if text, _ := v.(string); text != s.state.Text {
					s.state.Text, changed = text, true
				}
			case "done":
				d := kit.Num(v, 0)
				if s.state.Done == nil || *s.state.Done != d {
					s.state.Done, changed = &d, true
				}
				if s.first == nil {
					s.first = &[2]float64{t, d}
				} else if rate := (d - s.first[1]) / (t - s.first[0]); rate > 0 && t > s.first[0] {
					eta := (1 - d) / rate
					s.state.Eta, changed = &eta, true
				}
			default:
				if x, ok := v.(float64); ok {
					if s.state.Curves == nil {
						s.state.Curves = map[string]float64{}
					}
					if old, there := s.state.Curves[name]; !there || old != x {
						s.state.Curves[name], changed = x, true
					}
				}
			}
		}
		s.last = t
	}
	if !changed {
		return nil, false
	}
	copied := s.state
	copied.Curves = map[string]float64{}
	for k, v := range s.state.Curves {
		copied.Curves[k] = v
	}
	return &copied, true
}

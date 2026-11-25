// What a run asks for from its code, and who asks ( ADR 0014, 0032, 0036 ): one line a request, in a file of the run's
// directory that errand makes before the run is told of it ( `ERRAND_ASKS`: over a disk the nodes share, whoever will
// read a file makes it before whoever will write it is told ).
//
//	{"entry":"tests/test_a.py::test_x","ranks":4,"why":"4 ranks asked, this run has 1"}
//
// A request has two answers. When the claim can grow where the run is, `require` waits and returns ( agent/queuetools.go ).
// Otherwise it appends a line here, says so, and exits 75: the run stops -- in its tool's words, a skip for a pytest
// test --, and the agent that holds it, which sees how it ended, launches again what asked, with what it asked claimed
// up front ( agent/rerun.go ). This file is what makes the two meet: nothing else travels from the code to the agent.

package runinfo

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/errand-run/errand/internal/queue"
)

const (
	EnvAsks  = "ERRAND_ASKS"  // the file a request is appended to
	EnvRanks = "ERRAND_RANKS" // how many processes the run is, when it is several
)

// Ask is one request: what is asked, and by which entry.
type Ask struct {
	Entry     string      `json:"entry,omitempty"` // as its tool names it, from the root: `tests/test_a.py::TestK::test_m`
	Ranks     int         `json:"ranks,omitempty"`
	Needs     queue.Needs `json:"needs"`
	Exclusive bool        `json:"exclusive,omitempty"`
	Extra     bool        `json:"extra,omitempty"` // on top of what the run was started with
	Why       string      `json:"why,omitempty"`   // what stopped it, in words
}

// Said: what is asked, in words
func (a Ask) Said() string {
	var parts []string
	if a.Ranks > 0 {
		parts = append(parts, fmt.Sprintf("%d ranks", a.Ranks))
	}
	if a.Needs.GPUs > 0 {
		parts = append(parts, fmt.Sprintf("%d gpus", a.Needs.GPUs))
	}
	if !a.Needs.GPURam.IsZero() {
		parts = append(parts, "gpu_ram "+a.Needs.GPURam.String())
	}
	if a.Needs.CPUs > 0 {
		parts = append(parts, fmt.Sprintf("%g cpus", a.Needs.CPUs))
	}
	if a.Needs.RAM > 0 {
		parts = append(parts, fmt.Sprintf("%.0f MB of ram", a.Needs.RAM))
	}
	if a.Exclusive {
		parts = append(parts, "the machine to itself")
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, ", ")
}

// Same: whether two requests ask for the same thing -- those of one invocation that do go together
func (a Ask) Same(b Ask) bool {
	return a.Ranks == b.Ranks && a.Needs == b.Needs && a.Exclusive == b.Exclusive && a.Extra == b.Extra
}

// AskerOf: the entry that is asking, as its tool names it. Said by `--entry`, else by what the tool says of the one it
// runs: pytest sets `PYTEST_CURRENT_TEST` to `tests/test_a.py::test_x (call)`. "": the run itself asks.
func AskerOf(flag string) string {
	if flag != "" {
		return flag
	}
	if held := os.Getenv("ERRAND_ENTRY"); held != "" { // errand's own entries, which their tool names
		return held
	}
	current := os.Getenv("PYTEST_CURRENT_TEST")
	if i := strings.LastIndex(current, " ("); i > 0 && strings.HasSuffix(current, ")") {
		current = current[:i]
	}
	return current
}

// ReadAsks: what a run asked, in order, each different request once -- several ranks ask the same thing at once, and
// an entry that asks twice asks once
func ReadAsks(path string) []Ask {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []Ask
	for _, line := range strings.Split(string(data), "\n") {
		var a Ask
		if json.Unmarshal([]byte(line), &a) != nil || (a.Ranks == 0 && a.Needs.IsZero() && !a.Exclusive) {
			continue
		}
		seen := false
		for _, o := range out {
			seen = seen || (o.Entry == a.Entry && o.Same(a))
		}
		if !seen {
			out = append(out, a)
		}
	}
	return out
}

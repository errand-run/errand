// A run asked for what it did not have, and stopped: it is launched again ( ADR 0036, "what a run needs is asked from
// its code, by any tool" ).
//
// Who makes it is the point. Not the command line that happens to follow the run -- there may be none, under
// `--batch` --, but **the agent that was asked to launch the run in its place**: the one that holds the job, or, for a
// run handed to a batch system, the one that submitted it. It learns of the request by what already brings a run's
// end back to it ( a job ends in the agent; a batch job's end is read from the disk it shares with its node ), reads
// the file the code wrote its requests in ( runinfo/asks.go ), and launches again, through the same launch as any run
// ( `route`: a container's agent, ranks to the place's launcher or `srun`, a batch system's allocation when the kept
// one does not fit ), what asked:
//
//   - the entries of a file that asked for the same thing in one invocation go together, the other entries of the
//     first invocation having gone on; a script, a command, an entry that did not say who it is, is run again whole;
//   - what was asked is claimed up front: the ranks, the needs and the exclusivity of the launch;
//   - one that asks again for what it was given fails, and that is said -- it would only be run again for ever.
//
// Whoever follows the first run is carried to those made again ( a note, `again`, before its end ), so that the end
// of the command is that of the last one; the first run's record says what was asked and where it was run again.

package agent

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
)

const asksName = "asks.jsonl"

func asksPath(leaf string) string { return filepath.Join(leaf, asksName) }

// maxAgain: how many times a run can be made again, each time for more than the one before
const maxAgain = 8

// given: whether a run had what a request asks for
func given(s runinfo.LaunchSpec, a runinfo.Ask) bool {
	if a.Ranks > max(s.Ranks, 1) {
		return false
	}
	have, want := s.Needs, a.Needs
	if want.GPUs > have.GPUs || want.CPUs > have.CPUs || want.RAM > have.RAM {
		return false
	}
	if !want.GPURam.IsZero() && (have.GPURam.IsZero() && have.GPUs == 0 || want.GPURam.Percent > have.GPURam.Percent || want.GPURam.MB > have.GPURam.MB) {
		return false
	}
	return !a.Exclusive || s.Exclusive
}

// grown: what a launch claims once it takes a request in -- the larger of each, or, for what is asked on top of what a
// run was started with, the sum
func grown(have queue.Needs, a runinfo.Ask) queue.Needs {
	n := a.Needs
	if a.Extra {
		have.CPUs += n.CPUs
		have.RAM += n.RAM
		have.GPUs += n.GPUs
	} else {
		have.CPUs, have.RAM, have.GPUs = max(have.CPUs, n.CPUs), max(have.RAM, n.RAM), max(have.GPUs, n.GPUs)
	}
	if !n.GPURam.IsZero() {
		have.GPURam = n.GPURam
	}
	return have
}

// planAgain: from what a run asked, the runs that follow it -> their specs, or why there are none. `s` is the spec the
// run was launched with, as whoever launched it said it.
func planAgain(s runinfo.LaunchSpec, asks []runinfo.Ask) ([]runinfo.LaunchSpec, []string, string) {
	type group struct {
		ask   runinfo.Ask
		names []string // entries of the file, as its tool names them
		whole bool     // one that asked did not say who: the run is made again whole
	}
	var groups []*group
	for _, a := range asks {
		if s.Gen > 0 && given(s, a) {
			return nil, nil, fmt.Sprintf("it asked for %s, which it was given: it asked again, and is not run again for that", a.Said())
		}
		var g *group
		for _, o := range groups {
			if o.ask.Same(a) {
				g = o
			}
		}
		if g == nil {
			g = &group{ask: a}
			groups = append(groups, g)
		}
		name := ""
		if _, after, ok := strings.Cut(a.Entry, "::"); ok {
			name = after
		}
		if name == "" || s.Redo == nil {
			g.whole = true
		} else {
			g.names = append(g.names, name)
		}
	}
	if s.Gen >= maxAgain {
		return nil, nil, fmt.Sprintf("it has been run again %d times, each time for more: not once more", s.Gen)
	}
	var out []runinfo.LaunchSpec
	var said []string
	for _, g := range groups {
		n := s
		n.Gen = s.Gen + 1
		n.ID, n.Leaf, n.Waits, n.Refused = "", "", "", ""
		n.When = kit.Seconds(time.Now()) // a run of its own, made now: the newest of its siblings
		n.Ranks = max(s.Ranks, g.ask.Ranks)
		n.Needs = grown(s.Needs, g.ask)
		n.Exclusive = s.Exclusive || g.ask.Exclusive
		n.Params = map[string]any{}
		for k, v := range s.Params {
			n.Params[k] = v
		}
		n.Params["again"] = n.Gen
		n.Label = strings.TrimSpace(s.Label) + "  · again, " + g.ask.Said()
		if s.Redo != nil && !g.whole {
			sort.Strings(g.names)
			var names, seen = []string{}, map[string]bool{}
			for _, e := range g.names {
				if !seen[e] {
					seen[e] = true
					names = append(names, e)
				}
			}
			entries, exact := Told(s.Redo.Run, s.Redo.File, names, true)
			n.Words = Fill(s.Redo.Run.Words, s.Redo.Tools, map[string][]string{"entries": entries, "exact": exact, "args": s.Redo.Rest, "file": {s.Redo.File}}, nil)
			n.Sel = strings.Join(g.names, ",")
		}
		out = append(out, n)
		said = append(said, g.ask.Said())
	}
	return out, said, ""
}

// again: a run of this agent's that asked, launched again -> what was made, or why it is not. The specs are made from
// the first launch's, as whoever typed it said it -- a container's agent asks the agent of the machine, which routes
// them as it routed the first.
func (a *Place) again(s runinfo.LaunchSpec, asks []runinfo.Ask) ([]runinfo.Launched, []string, string) {
	base := s
	if s.Outer != nil {
		base = *s.Outer
	}
	specs, said, why := planAgain(base, asks)
	if why != "" {
		return nil, nil, why
	}
	var out []runinfo.Launched
	for _, n := range specs {
		var got runinfo.Launched
		var err error
		if s.Outer != nil && a.parent != "" {
			var answer map[string]any
			if answer, err = reach.AskAgent(a.parent, map[string]any{"op": "launch", "spec": n}); err == nil {
				err = reach.DecodeInto(answer["launched"], &got)
			}
		} else {
			got, err = a.route(n, func(string) {}, nil)
		}
		if err != nil {
			return out, said, fmt.Sprintf("it asked for %s, and could not be run again: %v", runinfo.AsksSaid(asks), err)
		}
		out = append(out, got)
		if a.kind == "" { // this machine's own: whoever looks for the run here finds it, nobody following it or not
			got.Note.Write()
		}
	}
	return out, said, ""
}

// asksOf: what the code of a run asked and is still to be given, when it is for the agent to make it again. The file of
// requests says what was asked; whether the run still needs it is what the run says of how it ended -- not its exit
// code alone, which a launcher changes ( a rank's 75 is its own code ), but a run that passed asked for nothing it
// lacked: a script that went on, or an entry that asked and passed, is not run again.
//
//   - a script, a command: if it did not pass;
//   - the entries of a file: those that asked and were not reported as passed ( skipped by the request, failed after
//     it ); one that did not say who it is, if the run did not pass.
func asksOf(s runinfo.LaunchSpec, leaf string, rec runinfo.Record, code int) []runinfo.Ask {
	if s.V != 1 {
		return nil
	}
	asks := runinfo.ReadAsks(asksPath(leaf))
	if s.Redo == nil || len(rec.Entries) == 0 {
		if code == 0 && rec.Status == "PASS" {
			return nil
		}
		return asks
	}
	var out []runinfo.Ask
	for _, a := range asks {
		_, name, ok := strings.Cut(a.Entry, "::")
		keep := !ok || rec.Status != "PASS"
		for _, e := range rec.Entries {
			if ok && e.Name == name {
				keep = e.Status != "PASS"
			}
		}
		if keep {
			out = append(out, a)
		}
	}
	return out
}

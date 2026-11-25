// A run, as data ( ADR 0034, 0035 ): whoever types `errand run` says what the run is -- `launchSpec` -- and the agent
// of the place that runs it starts it as one of its jobs ( agent/place.go, agent/job.go ): this machine's, or another's, reached
// by a link. The two do the same thing.

package runinfo

import (
	"time"

	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/queue"
)

// LaunchSpec is what a run is, said before it exists.
type LaunchSpec struct {
	V         int             `json:"v"`
	Root      string          `json:"root"`    // the project, where it runs ( as ssh's `cd` reads it: from the home folder )
	Outputs   string          `json:"outputs"` // where errand writes for it; "": `errand-outputs` in the root
	Cwd       string          `json:"cwd"`     // where the command starts: from the root, unless absolute
	File      string          `json:"file"`    // the target, from the root, when it is a file of the project
	Words     []string        `json:"words"`   // the target, and what is the target's
	Env       string          `json:"env"`
	Stack     []project.Layer `json:"stack"` // what is left of the environment's, on this machine
	Tags      map[string]any  `json:"tags"`
	Params    map[string]any  `json:"params"` // what tells this run from its siblings: the tags chosen
	When      float64         `json:"when"`   // one stamp for the whole command
	Label     string          `json:"label"`
	Own       bool            `json:"own"`
	Needs     queue.Needs     `json:"needs"`
	Exclusive bool            `json:"exclusive"`
	NoQueue   bool            `json:"no_queue"`
	Keep      bool            `json:"keep"`     // it goes on when nobody follows it
	Batch     bool            `json:"batch"`    // nobody follows it yet
	Python    string          `json:"python"`   // the interpreter to try first, when no layer chose one
	NoSetup   bool            `json:"no_setup"` // the environment used as it is: nothing built, nothing brought up to date
	Verbose   bool            `json:"verbose"`
	Time      string          `json:"time,omitempty"`  // how long it may take, for a batch system ( `--time` )
	Ranks     int             `json:"ranks,omitempty"` // how many processes of the program ( `--ranks` ): what it needs is one rank's
	MPI       string          `json:"mpi,omitempty"`   // what the batch system's launcher speaks to the ranks, when its layer says
	// what a run that asks for what it does not have is made again with ( agent/rerun.go ): how many times it was already, the
	// words that tell its tool which entries, and -- inside a container -- the spec as the machine's agent was given it,
	// which routes a run made again as it routed the first
	Redo  *Redo       `json:"redo,omitempty"`
	Gen   int         `json:"gen,omitempty"`
	Outer *LaunchSpec `json:"outer,omitempty"`
	// said by the agent that hands a run to another -- a batch system's node: the run's id and its directory, made
	// already, so that both know them before it starts
	ID   string `json:"id,omitempty"`
	Leaf string `json:"leaf,omitempty"`
	// where what the run waits for is said as well, for an agent that follows it from the disk they share
	Waits string `json:"waits,omitempty"`
	// a terminal for the run, when whoever typed the command has one ( ADR 0005, 0016 )
	Tty *TtySpec `json:"tty,omitempty"`
	// a run of entries ( ADR 0036 ): what it is filed under -- the file, from the root --, what tells it from its
	// siblings among what was typed after the target, and the report its tool leaves, read when it ends. `{report}`
	// in its words is that file, in the run's directory.
	As     string          `json:"as,omitempty"`
	Sel    string          `json:"sel,omitempty"` // what was asked of the file, when it is not all of it
	Shown  string          `json:"shown,omitempty"`
	Report *project.Report `json:"report,omitempty"`
	// what a run is handed of its project: its own sources, and where its environment's builds go, from the root
	Tools    map[string]any    `json:"tools,omitempty"` // the environment's own names for its tools
	Src      []string          `json:"src,omitempty"`
	BuildDir string            `json:"build_dir,omitempty"`
	Requires *project.Requires `json:"requires,omitempty"`
	Missing  string            `json:"missing,omitempty"` // what to say when its program is not there
	Out      string            `json:"out,omitempty"`     // its directory, under another tree than the outputs' runs, or as a pattern
	Mem      string            `json:"mem,omitempty"`     // a ceiling on its memory, where the system can hold one
	// a run errand makes for itself ( a listing ): filed apart, in errand's state, and in no summary
	Aside string `json:"aside,omitempty"`
	// why this run is not made, said by the agent that was to prepare what it needs: it is recorded as failed, with that
	Refused string `json:"refused,omitempty"`
	// the surroundings of whoever typed the command, when it was typed on the machine that runs it: an agent's own
	// are those of the day it was started
	Environ []string `json:"environ,omitempty"`
}

// TtySpec: what a run's terminal is -- the terminal of whoever typed the command, as it was when the run was asked for.
// Standard output and standard error are each a pseudo-terminal ( so that what each wrote is still known, and a
// gutter can be drawn on it ), as wide as the window less what the gutter takes; standard input is the first of them
// when whoever typed the command reads what it types, else it is what it is given.
type TtySpec struct {
	Cols   int    `json:"cols"`
	Rows   int    `json:"rows"`
	Term   string `json:"term"`
	Gutter int    `json:"gutter"`           // columns taken off the width
	Input  bool   `json:"input,omitempty"`  // standard input is the terminal too: what is typed reaches the run
	One    bool   `json:"one,omitempty"`    // one terminal for the three streams, left as a terminal is: a shell
	Colors bool   `json:"colors,omitempty"` // the terminal shows colours: a program that must be told is told
}

// Launched is a run that started.
type Launched struct {
	Note    RunNote `json:"note"`
	Banner  string  `json:"banner"`         // the layers, in words
	Host    string  `json:"host"`           // the machine's name
	Outputs string  `json:"outputs"`        // where errand writes for the project there
	Said    string  `json:"said,omitempty"` // what its agent has to say of how it was started
}

func StampOf(when float64) time.Time { return time.Unix(0, int64(when*1e9)) }

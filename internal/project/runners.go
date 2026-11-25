// What runs a kind of file, as data ( ADR 0031, ADR 0036 ): what a runner takes, how its entries are listed, how a
// set of them is run, how its report is read. Written by errand's Python from the runners a project holds
// ( `Runner.data()` ); read here, and never called: a runner has no code where this program runs.

package project

import (
	_ "embed"
	"encoding/json"
)

// Runner is one runner, by its name in `errand.runners`.
type Runner struct {
	Type     string            `json:"type"`
	Level    int               `json:"level"`   // how well it fits what it takes: the highest wins, a tie is an error
	Entries  bool              `json:"entries"` // it answers `x::y`; false: a file run whole, named without `::`
	Takes    Takes             `json:"takes"`
	Zones    map[string]string `json:"zones,omitempty"`
	Build    []string          `json:"build,omitempty"`   // lines, run once per command and environment before anything runs
	Binary   string            `json:"binary,omitempty"`  // what a file's entries are in, once built: `{binary}`
	Missing  string            `json:"missing,omitempty"` // what to say when it is not there
	Python   bool              `json:"python,omitempty"`  // its tool is errand's own Python: it must be on the path
	Requires *Requires         `json:"requires,omitempty"`
	List     *Listing          `json:"list,omitempty"`
	Run      *Running          `json:"run,omitempty"`
	Report   *Report           `json:"report,omitempty"`
	NotData  string            `json:"not_data,omitempty"` // why it is not one this program can read: a class of the project's own
}

// Takes says which files are a runner's: every condition it gives must hold.
type Takes struct {
	Suffixes []string  `json:"suffixes,omitempty"`
	Names    []string  `json:"names,omitempty"`     // the file's name matches one of these
	NotNames []string  `json:"not_names,omitempty"` // and none of these
	Dirs     []string  `json:"dirs"`                // under one of these, from the root; nil: anywhere
	Holds    string    `json:"holds,omitempty"`     // a pattern found in the first megabyte of the file
	Near     string    `json:"near,omitempty"`      // a file of this name beside it, or above, up to the root
	NearName string    `json:"near_name,omitempty"` // what to read in it: the first group, `{near_name}`
	Paths    []Variant `json:"paths,omitempty"`     // where the file is, from the directory of that one: one of these
}

// Variant is what a file is told its tool as, from where it is: `{variant}` in a runner's words. The named groups of
// the path are filled in its words.
type Variant struct {
	Path  string   `json:"path"`
	Words []string `json:"words"`
}

// Requires is what a runner's tool needs, installed when it is missing and the interpreter is not the system's.
type Requires struct {
	Python []struct {
		Module string `json:"module"`
		Spec   string `json:"spec"`
	} `json:"python"`
	Install any `json:"install"` // "auto", true, false
}

// Listing is how the entries of files are asked of the tool: `{files}` in the words, one entry per line that matches
// `Line` ( its groups `file` and `name` ).
type Listing struct {
	Words []string `json:"words"`
	Line  string   `json:"line"`
	Each  bool     `json:"each,omitempty"` // a file at a time: the lines do not say which
}

// Running is how a set of entries is run: `{entries}` in the words stands for each of them written as `Entry`
// ( `{file}::{name}` ), or for `Whole` when the set is everything the file holds.
type Running struct {
	Words  []string `json:"words"`
	Entry  string   `json:"entry,omitempty"`
	Whole  string   `json:"whole,omitempty"`
	Native bool     `json:"native,omitempty"` // a name is the tool's own expression, patterns included: handed as typed
	Join   string   `json:"join,omitempty"`   // several entries are one word, joined by this
	Escape string   `json:"escape,omitempty"` // in a listed name, these are written with a backslash before
	Exact  []string `json:"exact,omitempty"`  // `{exact}`: said when the names are those of a listing
	Tags   []string `json:"tags,omitempty"`   // `{tags}`: how `-e EXPR` is said, for a tool whose entries errand tags
	Params bool     `json:"params,omitempty"` // its entries declare parameters, which a grid may vary
}

// Report is what the tool leaves, and how it is read: `junit`, a file of that name in the run's directory; `exit`,
// nothing but its exit code. `Nothing`: the exit code that says no entry was selected.
type Report struct {
	Form    string `json:"form"`
	File    string `json:"file,omitempty"`
	Nothing *int   `json:"nothing,omitempty"`
}

// builtin is what errand's Python writes for the runners errand comes with: what a directory nobody declared anything
// about has, with no Python asked. A test holds the two together.
//
//go:embed runners.json
var builtin []byte

// Builtin: the runners errand comes with.
func Builtin() map[string]Runner {
	out := map[string]Runner{}
	if err := json.Unmarshal(builtin, &out); err != nil {
		panic(err)
	}
	return out
}

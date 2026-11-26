// The program of this machine, under a name that does not change:
//
//	<cache>/bin/errand -> <hash>/errand          what the machine's agent that came last runs
//	<cache>/bin/.errand                          which that is
//
// A machine errand was sent to has nothing installed, and a copy's name changes with each version. What must name errand
// there and is not a client of it -- the command a key of `authorized_keys` forces, a unit, a cron line -- names this link.
// Nobody keeps it: a newer client sends its program, whose agent points the link at itself as it starts. An older one
// leaves it where it is, and the round leaves what it names ( rounds.go ).

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/system"
)

type currentNote struct {
	Hash    string `json:"hash"`
	Release string `json:"release"`
	Errand  string `json:"errand"`
	System  string `json:"system"`
	Host    string `json:"host"`
	Built   int64  `json:"built,omitempty"`
}

func binDir() string { return filepath.Join(kit.LocalCache(), "bin") }

// currentHash: the copy the link names; "" when there is none
func currentHash() string {
	to, err := os.Readlink(filepath.Join(binDir(), "errand"))
	if err != nil {
		return ""
	}
	return filepath.Base(filepath.Dir(to))
}

// pointHere: the link, at the program of this agent -- unless the one it names is ahead of it
func pointHere() {
	if runtime.GOOS == "windows" {
		return // ( a link takes a right most accounts do not have )
	}
	bin, err := filepath.EvalSymlinks(binDir())
	self := kit.SelfPath()
	if err != nil || filepath.Dir(filepath.Dir(self)) != bin || filepath.Base(self) != "errand" {
		return // not a copy of errand's own ( it could not be made )
	}
	link, note := filepath.Join(bin, "errand"), filepath.Join(bin, ".errand")
	if st, err := os.Lstat(link); err == nil && st.Mode()&os.ModeSymlink == 0 {
		return // somebody else's file
	}
	mine := currentNote{filepath.Base(filepath.Dir(self)), reach.Release, system.Version, runtime.GOOS + "/" + runtime.GOARCH, queue.Host(), system.BuiltAt()}
	if _, err := os.Stat(link); err == nil { // ( a link to a copy that is gone is worth nothing )
		var was currentNote
		data, _ := os.ReadFile(note)
		if json.Unmarshal(data, &was) == nil && was.Hash == currentHash() && !mine.replaces(was) {
			return
		}
	}
	tmp := fmt.Sprintf("%s.%d.tmp", link, os.Getpid())
	_ = os.Remove(tmp)
	if os.Symlink(filepath.Join(mine.Hash, "errand"), tmp) != nil {
		return
	}
	if os.Rename(tmp, link) != nil {
		_ = os.Remove(tmp)
		return
	}
	_ = kit.WriteWholeJSON(note, mine)
}

// replaces: whether the link goes from `was` to this one -- a later release, version, build; two that say no more apart: the last
// to start
func (n currentNote) replaces(was currentNote) bool {
	if was.System != n.System && was.Host != n.Host {
		return false // a home shared with a machine of another kind, which could not run this one: first come
	}
	if a, b := reach.ReleaseOf(n.Release), reach.ReleaseOf(was.Release); a != b {
		return a > b
	}
	if versionBefore(n.Errand, was.Errand) || versionBefore(was.Errand, n.Errand) {
		return versionBefore(was.Errand, n.Errand)
	}
	return n.Built >= was.Built
}

// versionBefore: whether version `a` is an earlier one than `b`
func versionBefore(a, b string) bool { return system.VersionBefore(a, b) }

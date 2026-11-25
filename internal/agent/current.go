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
	"regexp"
	"runtime"
	"strconv"
	"strings"

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
	mine := currentNote{filepath.Base(filepath.Dir(self)), reach.Release, system.Version, runtime.GOOS + "/" + runtime.GOARCH, queue.Host()}
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

// replaces: whether the link goes from `was` to this one. Two of one version ( a build being worked on ): the last to start.
func (n currentNote) replaces(was currentNote) bool {
	if was.System != n.System && was.Host != n.Host {
		return false // a home shared with a machine of another kind, which could not run this one: first come
	}
	if a, b := reach.ReleaseOf(n.Release), reach.ReleaseOf(was.Release); a != b {
		return a > b
	}
	return !versionBefore(n.Errand, was.Errand)
}

var versionParts = regexp.MustCompile(`^([0-9.]*[0-9])(?:(a|b|rc)([0-9]+))?(?:\+[a-z]*\.?([0-9]*))?`)

// versionBefore: whether version `a` is an earlier one than `b` ( lib/version.py: 2026.11.25.1, its candidates 2026.11.25.1rc1,
// what comes after it 2026.11.25.1+dev.3.g3e4580c )
func versionBefore(a, b string) bool {
	x, y := versionKey(a), versionKey(b)
	for i := 0; i < len(x) || i < len(y); i++ {
		var p, q int
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			return p < q
		}
	}
	return false
}

// versionKey: six numbers for the release, then which candidate ( none: after them all ), then how many commits after
func versionKey(v string) []int {
	key := make([]int, 9)
	m := versionParts.FindStringSubmatch(v)
	if m == nil {
		return key
	}
	for i, n := range strings.Split(m[1], ".") {
		if i < 6 {
			key[i], _ = strconv.Atoi(n)
		}
	}
	key[6] = map[string]int{"a": 1, "b": 2, "rc": 3, "": 4}[m[2]]
	key[7], _ = strconv.Atoi(m[3])
	key[8], _ = strconv.Atoi(m[4])
	return key
}

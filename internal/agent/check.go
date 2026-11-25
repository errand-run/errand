package agent

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
)

// What `errand check` asks of a machine ( command/check.go ): what its agent can see of what errand keeps there, and the few
// things it can put right that lose nothing. Each is looked at again before it is done.

const runsSeen = 50000 // how many runs of a copy are looked at

// SeenHere is what a machine says when it is checked.
type SeenHere struct {
	Host   string              `json:"host"`
	Copies []SeenCopy          `json:"copies"`
	Builds []caches.BuildsSeen `json:"builds"`
}

type SeenCopy struct {
	filesync.CopySeen
	Record string   `json:"record"` // its record's file
	Runs   []string `json:"runs"`   // the runs that ended in it, from its tree of runs
}

func copyRecords() map[string]string { // a record's file -> the copy it names
	out := map[string]string{}
	files, _ := filepath.Glob(filepath.Join(kit.LocalCache(), "copies", "*.json"))
	for _, f := range files {
		if rec := kit.ReadJSON(f); rec != nil && kit.Text(rec["path"]) != "" {
			out[f] = kit.Text(rec["path"])
		}
	}
	return out
}

func seenHere() SeenHere {
	out := SeenHere{Host: queue.Host(), Copies: []SeenCopy{}, Builds: caches.SeeBuilds()}
	for f, root := range copyRecords() {
		c := SeenCopy{CopySeen: filesync.SeeCopy(root), Record: f, Runs: []string{}}
		if !c.Gone {
			c.Runs = runsIn(runinfo.RunsOf(root))
		}
		out.Copies = append(out.Copies, c)
	}
	sort.Slice(out.Copies, func(i, k int) bool { return out.Copies[i].Path < out.Copies[k].Path })
	return out
}

// runsIn: the runs that ended under a tree of runs -- the directories that hold a result
func runsIn(tree string) []string {
	out := []string{}
	if real, err := filepath.EvalSymlinks(tree); err == nil { // ( the outputs may be a link, to a scratch disk )
		tree = real
	}
	_ = filepath.WalkDir(tree, func(p string, e fs.DirEntry, err error) error {
		if err != nil || !e.IsDir() {
			return nil
		}
		if len(out) >= runsSeen {
			return filepath.SkipAll
		}
		for _, name := range []string{runinfo.ResultName, "result.yaml"} { // ( the errand of before wrote YAML )
			if kit.IsFile(filepath.Join(p, name)) {
				if rel, err := filepath.Rel(tree, p); err == nil && rel != "." {
					out = append(out, filepath.ToSlash(rel))
				}
				return filepath.SkipDir
			}
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func (a *Place) checkOp(req map[string]any, f *reach.FarChan) {
	switch req["op"] {
	case "check":
		b, _ := json.Marshal(seenHere())
		f.Reply(map[string]any{"ok": true, "seen": json.RawMessage(b)})
	case "check_there": // which of these are not there
		missing := []string{}
		for _, p := range kit.Strs(req["paths"]) {
			if _, err := os.Stat(p); os.IsNotExist(err) { // ( a disk that does not answer proves nothing )
				missing = append(missing, p)
			}
		}
		f.Reply(map[string]any{"ok": true, "missing": missing})
	case "check_fix":
		f.Reply(map[string]any{"ok": true, "done": fixHere(kit.Text(req["what"]), kit.Text(req["path"]))})
	}
}

func fixHere(what, path string) bool {
	for f, root := range copyRecords() {
		switch {
		case what == "record" && f == path:
			return !kit.IsDir(root) && os.Remove(f) == nil
		case what == "lock" && root == path:
			return filesync.Unlock(root)
		}
	}
	return false // only what errand wrote down is touched
}

// bringRuns: runs that are in a copy there only, into the tree of runs of their project here
func (a *Place) bringRuns(req map[string]any, f *reach.FarChan) {
	var h reach.HostSpec
	from, to, rels := kit.Text(req["from"]), kit.Text(req["to"]), kit.Strs(req["runs"])
	if reach.DecodeInto(req["host"], &h) != nil || !filepath.IsAbs(to) || from == "" {
		f.Fail("runs to bring back that cannot be read")
		return
	}
	t := &reach.Term{Environ: os.Environ(), Emit: func(byte, []byte) {}}
	defer t.Close()
	l, err := reach.LinkTo(t, &h)
	if err != nil {
		f.Fail("%v", err)
		return
	}
	brought := 0
	for len(rels) > 0 {
		n := min(100, len(rels))
		var those []string
		for _, rel := range rels[:n] {
			if filepath.IsLocal(filepath.FromSlash(rel)) && !kit.Lexists(filepath.Join(to, filepath.FromSlash(rel))) {
				those = append(those, rel)
			}
		}
		rels = rels[n:]
		if len(those) == 0 {
			continue
		}
		if err := os.MkdirAll(to, 0o755); err != nil {
			f.Fail("%v", err)
			return
		}
		if _, err := filesync.Pull(l, from, to, those, nil, false, h.Host, func(string) {}); err != nil {
			f.Fail("%v ( %d brought back )", err, brought)
			return
		}
		for _, rel := range those {
			runinfo.Settled(filepath.Join(to, filepath.FromSlash(rel)), &h, from+"/"+rel)
			brought++
		}
	}
	f.Reply(map[string]any{"ok": true, "brought": brought})
}

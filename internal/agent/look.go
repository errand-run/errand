// What the screen and the page ask of a machine's agent, beside its overview: files looked at, gauges, runs removed, the
// storage, and what is asked of another machine through this one's links.

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
	"github.com/errand-run/errand/internal/system"
)

// removable: whether a directory is a run's, and none that runs -- never anything else is removed
func (a *Place) removable(leaf string) bool {
	if !filepath.IsAbs(leaf) {
		return false
	}
	if _, err := os.Stat(filepath.Join(leaf, runinfo.LaunchName)); err != nil {
		return false
	}
	for _, r := range overview(a).Rows {
		if r.Leaf == leaf && r.State != "ended" {
			return false
		}
	}
	return true
}

func (a *Place) uiOp(req map[string]any, f *reach.FarChan) {
	defer func() {
		if r := recover(); r != nil {
			f.Fail("%s", kit.SaidError(r))
		}
	}()
	switch req["op"] {
	case "ls":
		if dirs, _ := req["dirs"].(bool); dirs {
			got, err := kit.LsEntries(kit.Text(req["path"]))
			if err != nil {
				f.Fail("%v", err)
				return
			}
			f.Reply(map[string]any{"ok": true, "entries": got})
			return
		}
		f.Reply(map[string]any{"ok": true, "files": kit.LsFiles(kit.Text(req["path"]))})
	case "tail":
		size := int64(kit.Num(req["size"], 1<<18))
		raw, _ := req["raw"].(bool)
		got, err := kit.TailFile(kit.Text(req["path"]), req["offset"], min(size, 4<<20), raw)
		if err != nil {
			f.Fail("%v", err)
			return
		}
		got["ok"] = true
		f.Reply(got)
	case "run_look": // a run's directory, in one answer: what is in it, and what was added to its output and its journal
		f.Reply(runLook(kit.Text(req["path"]), req["out"], req["steps"]))
	case "resources":
		f.Reply(map[string]any{"ok": true, "resources": kit.Measure(), "host": queue.Host()})
	case "history":
		f.Reply(map[string]any{"ok": true, "runs": runinfo.PastRuns(), "host": queue.Host()})
	case "storage":
		f.Reply(map[string]any{"ok": true, "storage": caches.CachesReport(req, nil)})
	case "runs_size":
		f.Reply(map[string]any{"ok": true, "sizes": kit.SizesOf(kit.Strs(req["leaves"]))})
	case "runs_remove":
		var removed, kept []string
		defer func() { // what was copied elsewhere of them goes with them
			if those := filesync.CopiesUnder(removed...); len(those) > 0 {
				go a.dropCopies(those, false)
			}
		}()
		for _, leaf := range kit.Strs(req["leaves"]) {
			if !a.removable(leaf) {
				kept = append(kept, leaf)
				continue
			}
			if os.RemoveAll(leaf) != nil {
				kept = append(kept, leaf)
				continue
			}
			removed = append(removed, leaf)
			dir := filepath.Dir(leaf)
			if target, err := os.Readlink(filepath.Join(dir, "latest")); err == nil && filepath.Base(target) == filepath.Base(leaf) {
				_ = os.Remove(filepath.Join(dir, "latest"))
			}
			runinfo.Summarize(dir)
			for d := dir; filepath.Base(d) != "runs"; d = filepath.Dir(d) { // what it leaves empty goes
				left, err := os.ReadDir(d)
				if err != nil || len(left) > 2 || d == filepath.Dir(d) {
					break
				}
				other := false
				for _, e := range left {
					other = other || (e.Name() != "latest" && !strings.HasPrefix(e.Name(), "summary."))
				}
				if other || os.RemoveAll(d) != nil {
					break
				}
			}
		}
		f.Reply(map[string]any{"ok": true, "removed": removed, "kept": kept})
	case "kill_group": // a process nobody holds: whoever looks at it asked
		pid := int(kit.Num(req["pid"], 0))
		known := false
		for _, k := range unowned() {
			known = known || k.Pid == pid
		}
		if !known || pid <= 1 {
			f.Fail("%d: not a process errand sees and does not hold", pid)
			return
		}
		_ = system.Signal(pid, syscall.SIGTERM)
		f.Reply(map[string]any{"ok": true})
	}
}

// bring: a run's directory, from the machine that has it, to its place here
func (a *Place) bring(req map[string]any, f *reach.FarChan) {
	var h reach.HostSpec
	from, to := kit.Text(req["from"]), kit.Text(req["to"])
	if reach.DecodeInto(req["host"], &h) != nil || !filepath.IsAbs(from) || !filepath.IsAbs(to) {
		f.Fail("a run to bring back that cannot be read")
		return
	}
	l, err := reach.LinkTo(&reach.Term{Environ: os.Environ(), Emit: func(byte, []byte) {}}, &h)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(to), 0o755)
	}
	if err == nil {
		_, err = filesync.Pull(l, filepath.Dir(from), filepath.Dir(to), []string{filepath.Base(from)}, nil, false, h.Host, func(string) {})
	}
	if err != nil {
		f.Fail("%v", err)
		return
	}
	runinfo.Settled(to, &h, from)
	f.Reply(map[string]any{"ok": true})
}

const (
	lookedOut   = 1 << 16 // how much of the end of an output is looked at
	lookedSteps = 4 << 20 // how much of a journal comes at a time
)

// runLook: `out` and `steps` are where what was read before ended ( none: the end of the output, the journal from its start )
func runLook(leaf string, out, steps any) map[string]any {
	got := map[string]any{"ok": true, "files": kit.LsFiles(leaf)}
	dirs := []any{}
	if entries, err := kit.LsEntries(leaf); err == nil {
		for _, x := range entries {
			if e, ok := x.(map[string]any); ok && e["dir"] == true {
				dirs = append(dirs, e)
			}
		}
	}
	got["dirs"] = dirs
	output := filepath.Join(leaf, runinfo.StreamNames["both"])
	if st, err := os.Stat(output); err == nil {
		if out != nil && st.Size()-int64(kit.Num(out, 0)) > lookedOut { // too much since: its end, as the first time
			out = nil
		}
		if more, err := kit.TailFile(output, out, lookedOut, true); err == nil {
			got["out"] = more
		}
	}
	journal := filepath.Join(leaf, "steps.jsonl")
	if kit.Exists(journal) {
		if steps == nil {
			steps = 0.0
		}
		if more, err := kit.TailFile(journal, steps, lookedSteps, true); err == nil {
			got["steps"] = more
		}
	}
	return got
}

// bringView: a view of a run that goes on another machine, as it is now, to its place here -- what differs only. The files
// `last` name others ( an index ): read first, put in place after the rest, so that one never names a file not there yet.
func (a *Place) bringView(req map[string]any, f *reach.FarChan) {
	var h reach.HostSpec
	from, to, last := kit.Text(req["from"]), kit.Text(req["to"]), kit.Strs(req["last"])
	if reach.DecodeInto(req["host"], &h) != nil || !filepath.IsAbs(from) || !filepath.IsAbs(to) {
		f.Fail("a view to bring that cannot be read")
		return
	}
	l, err := reach.LinkTo(&reach.Term{Environ: os.Environ(), Emit: func(byte, []byte) {}}, &h)
	if err == nil {
		err = viewBrought(l, h.Host, from, to, last)
	}
	if err != nil {
		f.Fail("%v", err)
		return
	}
	f.Reply(map[string]any{"ok": true, "here": to})
}

func viewBrought(l *reach.Link, host, from, to string, last []string) error {
	mute := func(string) {}
	name := filepath.Base(to)
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	var indexes, apart []string
	for _, p := range last {
		if p = strings.Trim(filepath.ToSlash(p), "/"); p != "" && filepath.IsLocal(filepath.FromSlash(p)) {
			indexes = append(indexes, name+"/"+p)
			apart = append(apart, "/"+p)
		}
	}
	stage := ""
	if len(indexes) > 0 {
		var err error
		if stage, err = os.MkdirTemp(filepath.Dir(to), ".errand-view-"); err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		if _, err := filesync.Pull(l, filepath.Dir(from), stage, indexes, nil, false, host, mute); err != nil {
			return err
		}
	}
	rest, err := filesync.NewSelection(apart, false, nil)
	if err == nil {
		_, err = filesync.Pull(l, filepath.Dir(from), filepath.Dir(to), []string{name}, rest, false, host, mute)
	}
	if err != nil {
		return err
	}
	for _, p := range indexes {
		at := filepath.Join(filepath.Dir(to), filepath.FromSlash(p))
		if os.MkdirAll(filepath.Dir(at), 0o755) == nil {
			_ = os.Rename(filepath.Join(stage, filepath.FromSlash(p)), at) // ( one that is not there yet there: next time )
		}
	}
	return nil
}

// at: what is asked of another machine's agent, through the link this one keeps to it
func (a *Place) at(req map[string]any, f *reach.FarChan) {
	var h reach.HostSpec
	inner, _ := req["req"].(map[string]any)
	if reach.DecodeInto(req["host"], &h) != nil || h.Host == "" || inner == nil {
		f.Fail("a request for another machine that cannot be read")
		return
	}
	environ := kit.Strs(req["environ"])
	if len(environ) == 0 {
		environ = os.Environ()
	}
	t := &reach.Term{Environ: environ, Emit: func(byte, []byte) {}}
	l, err := reach.LinkTo(t, &h)
	if err != nil {
		f.Fail("%v", err)
		return
	}
	c := l.Open(inner)
	defer c.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case m := <-f.Inbox:
				_ = c.Send(m.Kind, m.Payload)
			case <-f.Gone:
				c.Close()
				return
			case <-done:
				return
			}
		}
	}()
	for {
		kind, payload, err := c.Recv()
		if err != nil {
			f.Out(kit.EXIT, []byte(`{"code":1}`))
			return
		}
		select {
		case <-f.Gone:
			return
		default:
		}
		f.Out(kind, payload)
		if kind == kit.EXIT {
			return
		}
	}
}

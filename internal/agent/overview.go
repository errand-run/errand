// What there is on a machine, as errand sees it ( the guide's "What is running" ): what runs and what waits -- here, in
// containers, handed to a batch system --, where each run says it is, what is kept between commands, and what nobody
// holds any more. One document, asked of the machine's agent ( `overview` ) or, without starting any, of the agents
// that are there ( `errand peek` ): what `errand status`, the screen and the page are made of.

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/draw"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/system"
)

// table: the jobs this agent holds, the oldest first
func (a *Place) table() []reach.TableRow {
	a.mu.Lock()
	all := make([]*job, 0, len(a.jobs))
	for _, j := range a.jobs {
		all = append(all, j)
	}
	handed := make([]*batchJob, 0, len(a.batch))
	for _, b := range a.batch {
		handed = append(handed, b)
	}
	a.mu.Unlock()
	rows := []reach.TableRow{}
	for _, j := range all {
		st, more := j.state()
		row := reach.TableRow{ID: j.id, What: j.label, Env: j.spec.Env, Project: j.rec.Project, State: st, More: more, Started: j.spec.When,
			Leaf: j.leaf, Batch: j.spec.Batch, File: j.spec.As, Sel: j.spec.Sel, Aside: j.spec.Aside != ""}
		j.mu.Lock()
		row.Pid, row.Out, row.Marks = j.pid, j.sizes["both"], j.marks
		if st == "waiting" {
			row.Since = kit.Seconds(j.waitingSince)
		}
		j.mu.Unlock()
		if st == "running" {
			row.Step = j.step()
		}
		rows = append(rows, row)
	}
	for _, b := range handed {
		st, more := b.said()
		row := reach.TableRow{ID: b.meta.ID, What: b.meta.What, Env: b.meta.Env, Project: b.meta.Project, State: st, More: more, Started: b.meta.Started,
			Leaf: b.meta.Leaf, Batch: b.spec.Batch, Slurm: b.meta.Slurm, File: b.spec.As, Sel: b.spec.Sel, Aside: b.spec.Aside != ""}
		if st == "running" {
			row.Step = b.step()
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, k int) bool { return rows[i].Started < rows[k].Started })
	return rows
}

// step: where a run says it is, read from its journal since it was read last
func (j *job) step() *draw.StepState { return j.where.read(filepath.Join(j.leaf, "steps.jsonl")) }

func (b *batchJob) step() *draw.StepState {
	return b.where.read(filepath.Join(b.meta.Leaf, "steps.jsonl"))
}

type stepReader struct {
	mu   sync.Mutex
	tail draw.StepTail
	last *draw.StepState
}

func (r *stepReader) read(path string) *draw.StepState {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tail.Path = path
	if st, changed := r.tail.Poll(); changed {
		r.last = st
	}
	return r.last
}

func (a *Place) agentRow() reach.AgentRow {
	a.mu.Lock()
	idle, retired := time.Since(a.idleSince).Seconds(), a.retired
	a.mu.Unlock()
	v := reach.VersionOf(a.Dir)
	return reach.AgentRow{Dir: a.Dir, Kind: a.kind, Release: v.Release, Errand: v.Errand, Pid: os.Getpid(), Started: v.Started, Idle: idle,
		Keep: a.keep.Seconds(), Running: a.running(), Retired: retired, RSS: rssOf(os.Getpid())}
}

func rssOf(pid int) float64 {
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid)); err == nil {
		f := strings.Fields(string(data))
		if len(f) > 1 {
			pages, _ := strconv.ParseFloat(f[1], 64)
			return pages * float64(os.Getpagesize()) / (1 << 20)
		}
	}
	if pid == os.Getpid() {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.Sys) / (1 << 20)
	}
	return 0
}

// own: what this agent alone says of itself
func (a *Place) own() reach.OverviewDoc {
	return reach.OverviewDoc{V: 1, Host: queue.Host(), Now: kit.Seconds(time.Now()), Rows: a.table(), Agents: []reach.AgentRow{a.agentRow()}, Serving: a.serving(), VMs: a.vms()}
}

// overview: what there is on this machine -- what the agents that live here say, and what they cannot
func overview(self *Place) reach.OverviewDoc {
	host := queue.Host()
	name, _ := os.Hostname()
	if name == "" {
		name = host
	}
	out := reach.OverviewDoc{V: 1, Host: host, User: os.Getenv("USER"), System: runtime.GOOS, Now: kit.Seconds(time.Now()), Cache: kit.LocalCache(), Shared: sharedHere(),
		Rows: []reach.TableRow{}, Agents: []reach.AgentRow{}, Kept: []reach.KeptRow{}, Serving: []reach.ServingRow{}, VMs: []reach.VmRow{}}
	add := func(o reach.OverviewDoc) {
		out.Rows = append(out.Rows, o.Rows...)
		out.Agents = append(out.Agents, o.Agents...)
		out.Serving = append(out.Serving, o.Serving...)
		out.VMs = append(out.VMs, o.VMs...)
	}
	mine := ""
	if self != nil {
		mine = self.Dir
		add(self.own())
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, dir := range reach.Others(reach.PlaceRoot(), mine) {
		wg.Add(1)
		go func(dir string) {
			defer wg.Done()
			l, err := reach.LinkToAgent(dir)
			if err != nil {
				return
			}
			defer l.Close()
			got, err := l.Ask(map[string]any{"op": "own"})
			var o reach.OverviewDoc
			if err == nil && reach.DecodeInto(got["own"], &o) == nil {
				mu.Lock()
				add(o)
				mu.Unlock()
			}
		}(dir)
	}
	wg.Wait()
	out.Kept = append(out.Kept, allocationsKept()...)
	out.Kept = append(out.Kept, warmKept()...)
	out.Kept = append(out.Kept, unowned()...)
	out.Rows = onceEach(out.Rows)
	out.Rested = rested(out.Serving)
	return out
}

// onceEach: a run handed to a batch system is known by the agent that handed it and by its node's: one row of it
func onceEach(rows []reach.TableRow) []reach.TableRow {
	at := map[string]int{}
	out := []reach.TableRow{}
	for _, r := range rows {
		i, seen := at[r.ID]
		if !seen {
			at[r.ID] = len(out)
			out = append(out, r)
			continue
		}
		was := out[i]
		if r.Slurm != "" {
			was, r = r, was
		}
		if was.Step == nil {
			was.Step = r.Step
		}
		out[i] = was
	}
	sort.SliceStable(out, func(i, k int) bool { return out[i].Started < out[k].Started })
	return out
}

func allocationsKept() []reach.KeptRow {
	var out []reach.KeptRow
	dirs, _ := filepath.Glob(filepath.Join(allocsDir(), "*"))
	for _, dir := range dirs {
		al, ok := readAllocation(dir)
		if !ok {
			continue
		}
		st, why, node, left := slurmSays(al.Slurm)
		if st == "" {
			continue
		}
		text := "slurm job " + al.Slurm + " " + strings.ToLower(st)
		if node != "" {
			text += " on " + node
		}
		if left != "" {
			text += ", " + left + " left"
		}
		if why != "" && why != "None" {
			text += " ( " + why + " )"
		}
		out = append(out, reach.KeptRow{Kind: "allocation", ID: al.Slurm, Text: text})
	}
	return out
}

func warmKept() []reach.KeptRow {
	var out []reach.KeptRow
	pids, _ := filepath.Glob(filepath.Join(reach.PlaceRoot(), "*", "warm", "*", "pid"))
	for _, p := range pids {
		data, _ := os.ReadFile(p)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if pid <= 0 || system.Signal(pid, 0) != nil {
			continue
		}
		rss, _ := os.ReadFile(filepath.Join(filepath.Dir(p), "rss"))
		out = append(out, reach.KeptRow{Kind: "warm", ID: filepath.Base(filepath.Dir(p)), Pid: pid, Text: "imports kept warm ( " + strings.TrimSpace(string(rss)) + " MB )"})
	}
	return out
}

// unowned: what errand started on this machine that no record holds any more ( Linux: a process says it in its
// surroundings )
func unowned() []reach.KeptRow {
	var out []reach.KeptRow
	if runtime.GOOS != "linux" {
		return out
	}
	entries, _ := os.ReadDir("/proc")
	root := reach.PlaceRoot() + string(filepath.Separator)
	uid := os.Getuid()
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		st, err := os.Stat("/proc/" + e.Name())
		if err != nil {
			continue
		}
		if owner, ok := system.Owner(st); !ok || owner != uid {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/environ")
		if err != nil {
			continue
		}
		for _, kv := range strings.Split(string(data), "\x00") {
			dir, ok := strings.CutPrefix(kv, "ERRAND_PROC_DIR=")
			if !ok || !strings.HasPrefix(dir, root) {
				continue
			}
			if _, held := readJobMeta(dir); held && !func() bool { _, over := jobExit(dir); return over }() {
				continue
			}
			line, _ := os.ReadFile("/proc/" + e.Name() + "/cmdline")
			words := strings.Join(strings.Fields(strings.ReplaceAll(string(line), "\x00", " ")), " ")
			if len(words) > 60 {
				words = words[:60] + "…"
			}
			// ( a process a run left behind it, in a session of its own, is said; `kill` is for whoever reads it )
			out = append(out, reach.KeptRow{Kind: "unowned", Pid: pid, Text: words})
		}
	}
	return out
}

// PeekMain: what there is on this machine, said by the agents that are there -- none is started to answer
func PeekMain() int {
	b, _ := json.Marshal(overview(nil))
	fmt.Println(string(b))
	return 0
}

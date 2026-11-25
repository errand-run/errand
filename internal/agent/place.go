// A place's agent ( ADR 0035 ): the process that stays on a machine, owns the jobs started there, and offers what the
// place offers to whoever reaches it -- a command line on this machine by its socket, an agent elsewhere by a bridge at
// the end of an ssh ( reach/there.go ).
//
//	errand place --dir DIR [--keep SECONDS]      started by a bridge or a command, never by hand
//
//	<cache>/place/<this machine>/<program's hash>/   one agent per version of the program
//	    lock, sock, version, log
//	    jobs/<id>/                                   what it owns ( job.go )
//
// It lives while it has jobs, then for its `keep` with nothing asked.
//
// **An update is a relay.** An agent of another version that starts asks this one to retire: it takes nothing new,
// answers for its jobs -- through the new one, which finds a job's record under this directory and passes the request
// on -- and goes when its last job ends. An agent asked for again takes the place back the same way. An errand
// older than the agent it finds on a machine is told to update ( `release` ).
//
// **An agent killed outright loses its jobs** ( ADR 0008 ): the next one ends what is left of them, and says so in
// their record.

package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/download"
	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
	"github.com/errand-run/errand/internal/system"
)

const keepDays = 7 // what a job left of itself, when nobody came to look

type Place struct {
	Dir, Root string
	keep      time.Duration
	held      *filesync.SyncedCopies

	mu         sync.Mutex
	jobs       map[string]*job
	batch      map[string]*batchJob // the runs it handed to a batch system ( slurm.go )
	kind       string               // "" the machine's; "container": inside one, under the machine's
	parent     string               // the directory of the agent this one depends on
	innerMu    sync.Mutex
	links      int
	deps       int // agents that depend on this one, and have runs going
	claims     int // places in the machine's queue held for the runs of such agents
	busy       int // commands in progress
	idleSince  time.Time
	prunedAt   time.Time // the versions of services, last purged
	retired    bool
	leaving    bool                  // told to go: it answers nobody new
	told       chan struct{}         // that it is time to
	changed    chan struct{}         // closed, and made again, whenever something here may have changed
	services   map[string]*service   // by name ( serve.go )
	machinesUp map[string]*vmMachine // by their id ( vm.go )
	wants      download.WantsBook    // what its runs ask for and have no way to ( wants.go )
}

// watch: what this machine holds, said when it changes and only then ( ADR 0024 ) -- with what the other agents that
// live here hold ( retired, in a container ), which they say to this one. `own`: what this agent alone holds.
func (a *Place) watch(f *reach.FarChan) {
	f.Reply(map[string]any{"ok": true, "host": queue.Host()})
	own, _ := f.Req["own"].(bool)
	theirs := map[string]reach.OverviewDoc{} // by the directory of the agent
	var mu sync.Mutex
	poke := make(chan struct{}, 1)
	following := map[string]bool{}
	follow := func(dir string) {
		l, err := reach.LinkToAgent(dir)
		if err != nil { // ( it starts, or goes: tried at the next look )
			mu.Lock()
			delete(following, dir)
			mu.Unlock()
			return
		}
		c := l.Open(map[string]any{"op": "watch", "own": true})
		go func() {
			defer l.Close()
			tell := func() {
				select {
				case poke <- struct{}{}:
				default:
				}
			}
			defer func() {
				mu.Lock()
				delete(theirs, dir)
				delete(following, dir)
				mu.Unlock()
				tell()
			}()
			for {
				kind, payload, err := c.Recv()
				if err != nil {
					return
				}
				var said struct {
					Overview *reach.OverviewDoc `json:"overview"`
				}
				if kind == kit.NOTE && json.Unmarshal(payload, &said) == nil && said.Overview != nil {
					mu.Lock()
					theirs[dir] = *said.Overview
					mu.Unlock()
					tell()
				}
			}
		}()
	}
	last := ""
	var kept []reach.KeptRow
	var keptAt, sent time.Time
	for {
		a.mu.Lock()
		wait := a.changed
		a.mu.Unlock()
		doc := a.own()
		if !own {
			for _, dir := range reach.Others(a.Root, a.Dir) {
				mu.Lock()
				known := following[dir]
				following[dir] = true
				mu.Unlock()
				if !known {
					follow(dir)
				}
			}
			mu.Lock()
			for _, more := range theirs {
				doc.Rows, doc.Agents = append(doc.Rows, more.Rows...), append(doc.Agents, more.Agents...)
				doc.Serving, doc.VMs = append(doc.Serving, more.Serving...), append(doc.VMs, more.VMs...)
			}
			mu.Unlock()
			if time.Since(keptAt) > 15*time.Second { // ( what is asked of the system is asked less often )
				kept, keptAt = append(append(allocationsKept(), warmKept()...), unowned()...), time.Now()
			}
			doc.Kept = kept
			doc.System, doc.User, doc.Cache = runtime.GOOS, os.Getenv("USER"), kit.LocalCache()
			doc.Rows = onceEach(doc.Rows)
			doc.Rested = rested(doc.Serving)
		}
		handed := false // a run a batch system runs on another machine, its files on a disk they share: nothing tells
		for _, r := range doc.Rows {
			handed = handed || (r.State == "running" && r.Slurm != "")
		}
		// what is only the time that passes is not a change
		still := doc
		still.Now = 0
		still.Agents = append([]reach.AgentRow{}, doc.Agents...)
		for i := range still.Agents {
			still.Agents[i].Idle, still.Agents[i].RSS = 0, 0
		}
		said, _ := json.Marshal(still)
		if string(said) != last {
			last = string(said)
			b, _ := json.Marshal(map[string]any{"table": doc.Rows, "overview": doc})
			f.Out(kit.NOTE, b)
			sent = time.Now()
		}
		sweep := 60 * time.Second
		if handed {
			sweep = 2 * time.Second
		}
		select {
		case <-wait:
		case <-poke:
		case <-time.After(sweep): // ... and what does not pass through this agent
		case <-f.Gone:
			return
		}
		// what changes together is said together; a run that writes without a stop is said a few times a second
		select {
		case <-time.After(max(50*time.Millisecond, watchEvery-time.Since(sent))):
		case <-f.Gone:
			return
		}
	}
}

const watchEvery = 300 * time.Millisecond

func (a *Place) touched() {
	a.mu.Lock()
	a.idleSince = time.Now()
	a.mu.Unlock()
	a.stir()
}

// stir: something a watcher shows moved
func (a *Place) stir() {
	a.mu.Lock()
	close(a.changed)
	a.changed = make(chan struct{})
	a.mu.Unlock()
}

func (a *Place) running() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, j := range a.jobs {
		j.mu.Lock()
		if j.code == nil {
			n++
		}
		j.mu.Unlock()
	}
	for _, b := range a.batch { // what it submitted and that has not ended: it may be asked for by its node
		b.mu.Lock()
		if b.code == nil {
			n++
		}
		b.mu.Unlock()
	}
	return n
}

func makePlace(dir string, keep float64, kind, parent string) *Place {
	return &Place{Dir: dir, Root: filepath.Dir(dir), kind: kind, parent: parent, keep: time.Duration(keep * float64(time.Second)),
		jobs: map[string]*job{}, batch: map[string]*batchJob{},
		held: &filesync.SyncedCopies{Of: map[string]*filesync.Selection{}}, idleSince: time.Now(), changed: make(chan struct{}), told: make(chan struct{}, 8),
		wants: download.WantsBook{Tried: map[string]time.Time{}, Pending: map[string]download.Want{}}}
}

func PlaceMain(args []string) int {
	dir, keep := "", reach.KeepDflt
	if v := os.Getenv("ERRAND_KEEP_ALIVE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			keep = f
		}
	}
	kind, parent, alloc := "", "", ""
	for i := 0; i < len(args); i++ {
		value := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch args[i] {
		case "--dir":
			dir = value()
		case "--keep":
			keep, _ = strconv.ParseFloat(value(), 64)
		case "--alloc": // a batch job's step: the agent of the allocation, on the node it landed on ( slurm.go )
			alloc, kind = value(), "node"
			number := os.Getenv("SLURM_JOB_ID")
			if number == "" {
				number = fmt.Sprint(os.Getpid())
			}
			os.Setenv(queue.EnvAllocation, number) // the allocation is the machine, carved by errand's queue
			_ = reach.PlaceDir()
			dir = filepath.Join(reach.PlaceRoot(), reach.SelfHash+"+alloc-"+number)
			if al, ok := readAllocation(alloc); ok {
				keep = al.Keep
			}
		case "--inside":
			kind = "container"
		case "--parent":
			parent = value()
		}
	}
	if dir == "" {
		fmt.Fprintln(os.Stderr, "usage: errand place --dir DIR [--keep SECONDS]")
		return 2
	}
	a := makePlace(dir, keep, kind, parent)
	_ = os.MkdirAll(filepath.Join(dir, "jobs"), 0o755)
	// one agent per directory: its lock, held for its whole life
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil || system.TryLock(lock) != nil {
		return 0 // another one there: it answers
	}
	defer lock.Close() // ( and so held to the end: a file nothing names any more is closed, and its lock given up )
	_ = kit.WriteRecord(filepath.Join(dir, "version"), reach.AgentVersion{Kind: kind, Release: reach.Release, Errand: system.Version, Pid: os.Getpid(), Started: kit.Seconds(time.Now())})
	_ = os.Remove(filepath.Join(dir, "retired"))
	a.sweep()
	path := filepath.Join(dir, "sock")
	_ = os.Remove(path)
	addr, err := kit.SockAddress(path)
	if err == nil {
		var l net.Listener
		if l, err = net.Listen("unix", addr); err == nil {
			defer l.Close()
			_ = os.Chmod(path, 0o600)
			go a.accept(l)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "errand place:", err)
		return 1
	}
	switch {
	case alloc != "": // an allocation's: it takes the runs handed to that allocation, and gives it back when it goes
		if len(activeIn(alloc)) == 0 { // whoever asked for it went while it was queued: nobody waiting, it goes
			_ = os.RemoveAll(alloc)
			return 0
		}
		defer os.Remove(alloc + ".out") // ( beside the directory, which can then go: the batch system holds that file open )
		defer os.RemoveAll(alloc)
		defer os.RemoveAll(dir)
		looked := make(chan struct{})
		go a.serveAllocation(alloc, looked)
		<-looked // ( what it was handed is started before it is asked whether it is idle )
	case a.parent != "": // further than the machine's agent: under it, for as long as this one lives
		if err := a.dependOn(a.parent); err != nil {
			fmt.Fprintln(os.Stderr, "errand place: the agent of this machine does not answer:", err)
			return 1
		}
	default:
		pointHere()
		a.retireOthers()
		a.sweepServices()
		if sharedHere() && a.keep.Seconds() > sharedAgent {
			a.keep = time.Duration(sharedAgent * float64(time.Second))
		}
		go a.rounds()
	}
	system.HearCtrlC()
	go a.watchWants()
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	for {
		select {
		case sig := <-sigs:
			if sig == syscall.SIGHUP {
				continue // ( a terminal that closes is nothing to an agent )
			}
			a.stopAll() // told to go: what it holds is stopped first, and says so
			a.leave(path)
			return 0
		case <-a.told: // asked to go ( `errand agents stop` ): the same
			a.stopAll()
			a.leave(path)
			return 0
		case <-time.After(500 * time.Millisecond):
		}
		a.mu.Lock()
		idle, links, retired, deps := time.Since(a.idleSince), a.links+a.busy, a.retired, a.deps+a.claims+a.busy
		a.mu.Unlock()
		// It stays while what it holds is used ( inside.go ), and while it carries a command out. Retired, a link
		// somebody merely keeps to it does not hold it back.
		if a.running() > 0 || a.keeping() > 0 || deps > 0 || (links > 0 && !retired) {
			a.touched()
			continue
		}
		if retired || idle > a.keep { // retired: it goes with its last job, whoever is still connected to it
			a.leave(path)
			return 0
		}
	}
}

func (a *Place) leave(sock string) {
	_ = os.Remove(sock)
	a.held.CheckAll() // each copy it synced is checked once more: what changed there since is kept for the next push to say
}

func (a *Place) accept(l net.Listener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		a.mu.Lock()
		if a.leaving {
			a.mu.Unlock()
			conn.Close()
			continue
		}
		a.links++
		a.idleSince = time.Now()
		a.mu.Unlock()
		go func() {
			reach.ServeLink(conn, conn, a.serve)
			conn.Close()
			a.mu.Lock()
			a.links--
			a.idleSince = time.Now()
			a.mu.Unlock()
		}()
	}
}

// sweep: what an earlier life of this agent left -- a job with no end written is one its agent was killed over
func (a *Place) sweep() {
	dirs, _ := filepath.Glob(filepath.Join(a.Dir, "jobs", "*"))
	for _, dir := range dirs {
		m, ok := readJobMeta(dir)
		if !ok {
			if st, err := os.Stat(dir); err == nil && time.Since(st.ModTime()) > time.Hour {
				_ = os.RemoveAll(dir)
			}
			continue
		}
		if _, ended := jobExit(dir); !ended && m.Slurm != "" {
			a.adopt(dir, m) // the batch system holds it, not the agent that is gone: known again from its record
		} else if !ended {
			endOrphan(dir, m)
		} else if st, err := os.Stat(filepath.Join(dir, "exit")); err == nil && time.Since(st.ModTime()) > keepDays*24*time.Hour {
			_ = os.RemoveAll(dir) // ended long ago, and nobody came to look
		}
	}
}

// retireOthers: this agent is the one asked now -- those of other versions take nothing new from here on
func (a *Place) retireOthers() {
	for _, dir := range reach.Machines(a.Root, a.Dir) {
		if reach.ReleaseOf(reach.VersionOf(dir).Release) > reach.ReleaseOf(reach.Release) {
			continue // ( ahead of this one: a bridge of this version says so, and does not get this far )
		}
		if l, err := reach.LinkToAgent(dir); err == nil {
			_, _ = l.Ask(map[string]any{"op": "retire"})
			l.Close()
		}
	}
}

func (a *Place) stopAll() {
	a.mu.Lock()
	var all []*job
	for _, j := range a.jobs {
		all = append(all, j)
	}
	a.mu.Unlock()
	a.mu.Lock()
	var services []*service
	for _, s := range a.services {
		services = append(services, s)
	}
	a.mu.Unlock()
	for _, s := range services {
		s.stop()
	}
	a.stopVMs("", false)
	for _, s := range services {
		select {
		case <-s.done:
		case <-time.After(time.Duration((s.spec.Grace + 3) * float64(time.Second))):
		}
	}
	for _, j := range all {
		j.stop(syscall.SIGINT)
	}
	for _, j := range all {
		select {
		case <-j.done:
		case <-time.After(kit.Grace[1] + 2*time.Second):
		}
	}
}

// ── what it is asked ─────────────────────────────────────────────────────────

// owner: who holds the job `id` -- this agent ( the job ), another agent that lives ( its directory ), or nobody
// any more ( the job's record, when there is one )
func (a *Place) owner(id string) (j *job, other, record string) {
	a.mu.Lock()
	j = a.jobs[id]
	a.mu.Unlock()
	if j != nil || id == "" || strings.ContainsAny(id, "/.") {
		return j, "", ""
	}
	found, _ := filepath.Glob(filepath.Join(a.Root, "*", "jobs", id))
	if len(found) == 0 {
		return nil, "", ""
	}
	record = found[0]
	if dir := filepath.Dir(filepath.Dir(record)); dir != a.Dir && reach.AgentAlive(dir) {
		return nil, dir, record
	}
	return nil, "", record
}

// batchOf: the run `id`, when it was handed to a batch system and no living agent but this one knows it -- adopted
// from its record when the agent that submitted it is gone
func (a *Place) batchOf(id string) *batchJob {
	a.mu.Lock()
	b := a.batch[id]
	a.mu.Unlock()
	if b != nil || id == "" || strings.ContainsAny(id, "/.") {
		return b
	}
	found, _ := filepath.Glob(filepath.Join(a.Root, "*", "jobs", id))
	if len(found) == 0 {
		return nil
	}
	m, ok := readJobMeta(found[0])
	if !ok || m.Slurm == "" {
		return nil
	}
	if dir := filepath.Dir(filepath.Dir(found[0])); dir != a.Dir && reach.AgentAlive(dir) {
		return nil // another agent of this machine has it: asked of that one
	}
	if _, done := jobExit(found[0]); done {
		return nil // over: read from its record, as any run that ended
	}
	return a.adopt(found[0], m)
}

// ended: a job nobody holds, from its record -- one with no end written was its agent's, killed over it
func ended(record string) (jobMeta, int, bool) {
	m, ok := readJobMeta(record)
	if !ok {
		return m, 0, false
	}
	code, done := jobExit(record)
	if !done {
		endOrphan(record, m)
		code, _ = jobExit(record)
	}
	return m, code, true
}

func (a *Place) stateOf(id string) []string {
	if b := a.batchOf(id); b != nil {
		st, more := b.said()
		return []string{st, more}
	}
	j, other, record := a.owner(id)
	switch {
	case j != nil:
		st, more := j.state()
		return []string{st, more}
	case other != "":
		if l, err := reach.LinkToAgent(other); err == nil {
			defer l.Close()
			if got, err := l.Ask(map[string]any{"op": "state", "ids": []string{id}}); err == nil {
				states, _ := got["states"].(map[string]any)
				if pair := kit.Strs(states[id]); len(pair) == 2 {
					return pair
				}
			}
		}
		return []string{"running", "its agent does not answer"}
	case record != "":
		if m, code, ok := ended(record); ok {
			if r, ok := runinfo.ReadRecord(filepath.Join(m.Leaf, runinfo.ResultName)); ok {
				return []string{"ended", r.Status}
			}
			return []string{"ended", fmt.Sprintf("exit %d", code)}
		}
	}
	return []string{"gone", ""}
}

func (a *Place) serve(req map[string]any, f *reach.FarChan) {
	op, id := kit.Text(req["op"]), kit.Text(req["id"])
	a.mu.Lock()
	a.idleSince = time.Now()
	a.mu.Unlock()
	f.Req = req
	switch op {
	case "watch":
		a.watch(f)
	case "command":
		a.serveCommand(req, f)
	case "service", "service_stop", "service_list", "service_connect":
		a.serveService(req, f)
	case "vm_hold", "vm_in", "vm_stop", "vm_state", "vm_install":
		a.serveVM(req, f)
	case "ls", "tail", "run_look", "resources", "history", "storage", "runs_size", "runs_remove", "kill_group":
		a.uiOp(req, f)
	case "services_do":
		a.servicesDo(req, f)
	case "copies_drop":
		a.serveCopies(req, f)
	case "copy_view":
		a.copyView(req, f)
	case "at":
		a.at(req, f)
	case "bring":
		a.bring(req, f)
	case "build_dir":
		st := a.setting(req, nil)
		where, _ := cachesOf(st.outputs, st.root, nil)
		dir, err := caches.MakeBuild(filepath.Join(where, caches.BuildsCache), project.BuildName(kit.Text(req["env"])), project.BuildName(kit.Text(req["name"])))
		if err != nil {
			f.Fail("%v", err)
			return
		}
		f.Reply(map[string]any{"ok": true, "path": dir})
	case "check", "check_there", "check_fix":
		a.checkOp(req, f)
	case "bring_runs":
		a.bringRuns(req, f)
	case "bring_view":
		a.bringView(req, f)
	case "quit":
		a.quit(req, f)
	case "caches", "caches_hold", "caches_remove":
		serveCaches(req, f)
	case "own": // what this agent alone holds
		f.Reply(map[string]any{"ok": true, "own": a.own()})
	case "overview": // what there is on this machine
		f.Reply(map[string]any{"ok": true, "overview": overview(a)})
	case "hello":
		home, _ := os.UserHomeDir()
		f.Reply(map[string]any{"ok": true, "version": system.Version, "release": reach.Release, "host": queue.Host(), "home": home, "cache": kit.LocalCache(), "running": a.running()})
	case "want":
		a.serveWant(req, f)
	case "claim":
		a.serveClaim(req, f)
	case "depend":
		a.serveDepend(req, f)
	case "retire":
		a.mu.Lock()
		a.retired = true
		a.mu.Unlock()
		for _, dir := range reach.Others(a.Root, a.Dir) { // those that live under this one go with their last job too
			if v := reach.VersionOf(dir); v.Kind != "" && strings.HasPrefix(filepath.Base(dir), filepath.Base(a.Dir)+"+") {
				_, _ = reach.AskAgent(dir, map[string]any{"op": "retire"})
			}
		}
		_ = kit.WriteWhole(filepath.Join(a.Dir, "retired"), strconv.FormatInt(time.Now().Unix(), 10))
		f.Reply(map[string]any{"ok": true, "running": a.running()})
	case "prepare":
		root, err := reach.PrepareCopy(kit.Text(req["root"]), kit.Text(req["outputs"]))
		if err != nil {
			f.Fail("%v", err)
			return
		}
		f.Reply(map[string]any{"ok": true, "root": root})
	case "sync", "pull", "receive":
		s := filesync.NewSyncChan(req, f.Out, a.held, a.touched)
		go func() {
			for {
				select {
				case m := <-f.Inbox:
					if m.Kind == kit.IN {
						s.Put(m.Payload)
					} else {
						s.Leave()
					}
				case <-f.Gone:
					s.Leave()
					return
				case <-s.Done:
					return
				}
			}
		}()
		s.Run()
	case "resolve": // what targets with `::` designate, in the copy this machine holds ( runinfo/targets.go )
		var runners map[string]project.Runner
		if err := reach.DecodeInto(req["runners"], &runners); err != nil {
			f.Fail("runners this errand cannot read ( its version: %s )", system.Version)
			return
		}
		got, err := runinfo.ResolveTargets(reach.CopyRoot(kit.Text(req["root"])), kit.Text(req["cwd"]), kit.Strs(req["targets"]), runners, kit.Text(req["only"]), kit.Strs(req["exclude"]))
		if err != nil {
			f.Fail("%v", err)
			return
		}
		f.Reply(map[string]any{"ok": true, "taken": got})
	case "launch":
		var spec runinfo.LaunchSpec
		if err := reach.DecodeInto(req["spec"], &spec); err != nil || spec.V != 1 {
			f.Fail("a run this errand cannot read ( its version: %s )", system.Version)
			return
		}
		a.mu.Lock()
		was := a.retired
		a.retired = false
		a.mu.Unlock()
		if was { // asked for again: this agent is the one that takes what is new
			_ = os.Remove(filepath.Join(a.Dir, "retired"))
			a.retireOthers()
		}
		got, err := a.route(spec, func(text string) { // what is made ready is said to whoever asked, as it is done
			b, _ := json.Marshal(map[string]any{"say": text})
			f.Out(kit.NOTE, b)
		}, f.Said)
		if err != nil {
			f.Fail("%v", err)
			return
		}
		f.Reply(map[string]any{"ok": true, "launched": got})
	case "setup": // an environment built or brought up to date, by this agent, whoever asked staying or not
		var stack []project.Layer
		_ = reach.DecodeInto(req["stack"], &stack)
		say := func(text string) {
			b, _ := json.Marshal(map[string]any{"say": text})
			f.Out(kit.NOTE, b)
		}
		st := a.setting(req, say)
		st.force, _ = req["force"].(bool)
		st.dryRun, _ = req["dry_run"].(bool)
		st.stop = f.Said // told to stop, it stops; a connection that only drops stops nothing
		f.Reply(map[string]any{"ok": true})
		code := 0
		exit := func() {
			b, _ := json.Marshal(map[string]any{"code": code})
			f.Out(kit.EXIT, b)
		}
		at := containerAt(stack)
		outer := stack
		if at >= 0 { // the image is this agent's to build; what is built inside a container is built by that container's agent
			outer = stack[:at+1]
		}
		if err := layer.Check(outer); err != nil {
			say("errand: " + err.Error())
			code = 1
		} else if err := ensure(outer, st); err != nil {
			say("errand: " + err.Error())
			code = 1
		}
		if at < 0 || code != 0 || len(layer.Artefacts(stack[at+1:])) == 0 {
			exit()
			return
		}
		dir, rest, err := a.inside(stack, st.root, st.tags, st.environ)
		if err != nil {
			if !st.dryRun { // ( a dry run says what it would do of the image, and goes no further than it is )
				say("errand: " + err.Error())
				code = 1
			}
			exit()
			return
		}
		req["stack"], req["environ"], req["root"], req["outputs"] = rest, nil, st.root, st.outputs
		code = a.relaySetup(dir, req, f)
		exit()
	case "elsewhere": // what of a stack is built in another environment, and is wanted now
		var stack []project.Layer
		_ = reach.DecodeInto(req["stack"], &stack)
		st := a.setting(req, func(string) {})
		st.force, _ = req["force"].(bool)
		if at := containerAt(stack); at >= 0 { // the image is this agent's; what follows it is another agent's
			stack = stack[:at+1]
		}
		all, _ := req["all"].(bool) // ( a builder is asked about the image it makes, which is not built elsewhere for it )
		if ask, err := wanted(stack, st, all); err != nil {
			f.Fail("%v", err)
		} else {
			f.Reply(map[string]any{"ok": true, "wanted": ask})
		}
	case "carried": // images built in another environment are here now: what is done with them, and said to have been
		var stack []project.Layer
		_ = reach.DecodeInto(req["stack"], &stack)
		var said []string
		st := a.setting(req, func(text string) { said = append(said, text) })
		slugs := map[string]bool{}
		for _, slug := range kit.Strs(req["slugs"]) {
			slugs[slug] = true
		}
		if at := containerAt(stack); at >= 0 {
			stack = stack[:at+1]
		}
		for _, art := range layer.Artefacts(stack) {
			if slugs[art.Slug()] {
				if err := st.received(art, kit.Text(req["from"])); err != nil {
					f.Fail("%v", err)
					return
				}
			}
		}
		f.Reply(map[string]any{"ok": true, "said": said})
	case "envs": // how some environments are, on this machine
		var stacks map[string][]project.Layer
		_ = reach.DecodeInto(req["stacks"], &stacks)
		st := a.setting(req, func(string) {})
		states := map[string]string{}
		for name, stack := range stacks {
			if at := containerAt(stack); at >= 0 { // the image is this agent's; what follows it is asked of the container's agent
				states[name] = "unknown"
				if layer.Check(stack[:at+1]) != nil {
					continue
				}
				states[name] = stateOf(stack[:at+1], st)
				if states[name] != "ok" && states[name] != "adopted" || len(layer.Artefacts(stack[at+1:])) == 0 {
					continue
				}
				if dir, rest, err := a.inside(stack, st.root, st.tags, st.environ); err != nil {
					states[name] = "not built ( " + kit.FirstLine(err.Error()) + " )"
				} else if got, err := reach.AskAgent(dir, map[string]any{"op": "envs", "root": st.root, "outputs": st.outputs,
					"tags": req["tags"], "stacks": map[string][]project.Layer{name: rest}}); err == nil {
					inner, _ := got["states"].(map[string]any)
					if in, _ := inner[name].(string); in != "ok" {
						states[name] = in
					}
				}
			} else if err := layer.Check(stack); err != nil {
				states[name] = "unknown"
			} else {
				states[name] = stateOf(stack, st)
			}
		}
		f.Reply(map[string]any{"ok": true, "states": states, "host": queue.Host()})
	case "follow":
		both, _ := req["both"].(bool)
		if b := a.batchOf(id); b != nil {
			b.follow(req, f)
			return
		}
		j, other, record := a.owner(id)
		switch {
		case j != nil:
			j.follow(req, f)
		case other != "":
			a.pass(other, req, f)
		case record != "":
			if m, code, ok := ended(record); ok {
				replayJob(m.Leaf, code, both, f)
				return
			}
			fallthrough
		default:
			f.Fail("nothing of this run is left here to follow")
		}
	case "state":
		states := map[string][]string{}
		for _, one := range kit.Strs(req["ids"]) {
			states[one] = a.stateOf(one)
		}
		f.Reply(map[string]any{"ok": true, "states": states})
	case "stop":
		if b := a.batchOf(id); b != nil {
			if st, _ := b.said(); st == "ended" {
				f.Reply(map[string]any{"ok": true, "was": st})
				return
			}
			b.stop()
			for until := time.Now().Add(20 * time.Second); time.Now().Before(until); time.Sleep(200 * time.Millisecond) {
				if st, _ := b.said(); st == "ended" {
					f.Reply(map[string]any{"ok": true, "stopped": true})
					return
				}
			}
			f.Reply(map[string]any{"ok": true, "stopped": false})
			return
		}
		j, other, record := a.owner(id)
		switch {
		case j != nil:
			if st, _ := j.state(); st == "ended" {
				f.Reply(map[string]any{"ok": true, "was": st})
				return
			}
			j.stop(syscall.SIGINT)
			select {
			case <-j.done:
				f.Reply(map[string]any{"ok": true, "stopped": true})
			case <-time.After(kit.Grace[1] + 7*time.Second):
				f.Reply(map[string]any{"ok": true, "stopped": false})
			}
		case other != "":
			a.pass(other, req, f)
		case record != "":
			_, _, _ = ended(record)
			f.Reply(map[string]any{"ok": true, "was": "ended"})
		default:
			f.Reply(map[string]any{"ok": true, "was": "gone"})
		}
	case "forget": // it was looked at: nothing of it is kept where its machine kept it
		a.mu.Lock()
		if b := a.batch[id]; b != nil {
			if st, _ := b.said(); st == "ended" {
				delete(a.batch, id)
				_ = os.RemoveAll(b.dir)
			}
		}
		a.mu.Unlock()
		j, other, record := a.owner(id)
		switch {
		case j != nil:
			if st, _ := j.state(); st == "ended" {
				a.mu.Lock()
				delete(a.jobs, id)
				a.mu.Unlock()
				_ = os.RemoveAll(j.dir)
				if j.spec.Aside != "" { // a run errand made for itself: read, and gone
					_ = os.RemoveAll(j.leaf)
				}
				a.touched()
			}
		case other != "":
			a.pass(other, req, f)
			return
		case record != "":
			if _, done := jobExit(record); done {
				_ = os.RemoveAll(record)
			}
		}
		f.Reply(map[string]any{"ok": true})
	default:
		f.Fail("this errand ( %s ) does not know `%s`: the one that asks is newer", system.Version, op)
	}
}

// setting: where a request sets an environment up -- the project's root as this machine reads it, errand's outputs
// for it, the surroundings of whoever asked when they are this machine's
func (a *Place) setting(req map[string]any, say func(string)) setting {
	root := reach.CopyRoot(kit.Text(req["root"]))
	outputs := kit.Text(req["outputs"])
	if outputs == "" {
		outputs = filepath.Join(root, "errand-outputs")
	}
	var tags map[string]any
	_ = reach.DecodeInto(req["tags"], &tags)
	environ := kit.Strs(req["environ"])
	if len(environ) == 0 {
		environ = os.Environ()
	}
	return setting{root: root, outputs: outputs, tags: tags, environ: environ, say: say}
}

// pass: a request about a job another agent of this machine holds, passed on to it, and what it answers passed back
// -- both ways, until the request is through
func (a *Place) pass(dir string, req map[string]any, f *reach.FarChan) {
	l, err := reach.LinkToAgent(dir)
	if err != nil {
		f.Fail("the agent that holds this run does not answer: %v", err)
		return
	}
	defer l.Close()
	c := l.Open(req)
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
				l.Close()
				return
			case <-done:
				return
			}
		}
	}()
	stream := map[string]bool{"follow": true, "service": true, "service_connect": true, "vm_hold": true, "vm_in": true}[kit.Text(req["op"])]
	for {
		kind, payload, err := c.Recv()
		if err != nil {
			if stream { // it went with its last job: how that ended is in the job's record
				if _, _, record := a.owner(kit.Text(req["id"])); record != "" {
					if code, ok := jobExit(record); ok {
						b, _ := json.Marshal(map[string]any{"code": code})
						f.Out(kit.EXIT, b)
					}
				}
			}
			return
		}
		f.Out(kind, payload)
		if kind == kit.EXIT || (kind == kit.REPLY && (!stream || strings.Contains(string(payload), `"error"`))) {
			return
		}
	}
}

// relaySetup: an installation asked of another agent of this machine ( a container's ), what it says passed on as it says
// it -> how it ended. Its answer to the request is not passed on: this agent has given its own.
func (a *Place) relaySetup(dir string, req map[string]any, f *reach.FarChan) int {
	say := func(text string) {
		b, _ := json.Marshal(map[string]any{"say": text})
		f.Out(kit.NOTE, b)
	}
	l, err := reach.LinkToAgent(dir)
	if err != nil {
		say("errand: the agent inside does not answer: " + err.Error())
		return 1
	}
	defer l.Close()
	c := l.Open(req)
	defer c.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-f.Said:
			c.Close() // told to stop: so is the agent inside
		case <-done:
		}
	}()
	for {
		kind, payload, err := c.Recv()
		if err != nil {
			return 1
		}
		switch kind {
		case kit.REPLY:
			if strings.Contains(string(payload), `"error"`) {
				var said map[string]any
				_ = json.Unmarshal(payload, &said)
				say("errand: " + kit.Text(said["error"]))
				return 1
			}
		case kit.NOTE:
			f.Out(kit.NOTE, payload)
		case kit.EXIT:
			var e map[string]any
			_ = json.Unmarshal(payload, &e)
			return int(kit.Num(e["code"], 1))
		}
	}
}

// route: a run asked for, started where it goes -- a job of this agent, of a container's agent, ranks started outside
// a container, a batch system's allocation. A run made again goes the same way ( rerun.go ). `say` is told what is made
// ready for it; `stop`, when it is not nil, closes when whoever asked says to stop.
func (a *Place) route(spec runinfo.LaunchSpec, say func(string), stop <-chan struct{}) (runinfo.Launched, error) {
	outside := false // several ranks, started before the container: each enters it, and this agent holds the run ( ranks.go )
	if at := containerAt(spec.Stack); at >= 0 && spec.Ranks > 1 {
		mpi, _ := mpiOf(spec.Stack)
		outside = (mpi >= 0 && mpi < at) || (mpi < 0 && os.Getenv("SLURM_JOB_ID") != "")
	}
	at := containerAt(spec.Stack)
	if at < 0 || outside {
		return a.launch(spec)
	}
	// it runs inside a container: a job of that container's agent
	if err := layer.Check(append(append([]project.Layer{}, spec.Stack[:at]...), spec.Stack[at+1:]...)); err != nil {
		return runinfo.Launched{}, fmt.Errorf("the environment %s: %v", spec.Env, err)
	}
	if !spec.NoSetup { // the image, and what leads to it, made before it is entered: said to whoever asked, as it is done
		st := a.setting(map[string]any{"root": spec.Root, "outputs": spec.Outputs}, say)
		st.tags = spec.Tags
		if len(spec.Environ) > 0 { // the surroundings of whoever typed the command
			st.environ = spec.Environ
		}
		if stop != nil {
			st.stop = stop // told to stop, it stops; a connection that only drops stops nothing
		}
		if err := ensure(spec.Stack[:at+1], st); err != nil {
			select {
			case <-stop: // told to stop while it was made: nothing is run
				return runinfo.Launched{}, errors.New("stopped")
			default:
			}
			// a run that failed, as when any other environment cannot be built: its record says why
			spec.Refused = err.Error()
			return a.launch(spec)
		}
	}
	dir, rest, err := a.inside(spec.Stack, reach.CopyRoot(spec.Root), spec.Tags, spec.Environ)
	if err != nil {
		return runinfo.Launched{}, fmt.Errorf("the environment %s: %v", spec.Env, err)
	}
	banner := layer.Describe(spec.Stack)
	outer := spec // what the machine's agent was given: a run made again is routed from it
	outer.Outer = nil
	spec.Outer = &outer
	spec.Stack, spec.Environ = rest, nil  // inside, the surroundings are the container's
	spec.Root = reach.CopyRoot(spec.Root) // ...and so is its home: where a path is read from
	if spec.Outputs != "" {
		spec.Outputs = reach.CopyRoot(spec.Outputs)
	}
	answer, err := reach.AskAgent(dir, map[string]any{"op": "launch", "spec": spec})
	if err != nil {
		return runinfo.Launched{}, err
	}
	var got runinfo.Launched
	if err := reach.DecodeInto(answer["launched"], &got); err != nil {
		return runinfo.Launched{}, err
	}
	got.Banner = banner
	return got, nil
}

// launch: a run started, as a job of this agent -> what whoever asked keeps of it
func (a *Place) launch(s runinfo.LaunchSpec) (runinfo.Launched, error) {
	root := reach.CopyRoot(s.Root)
	outputs := s.Outputs
	if outputs == "" {
		outputs = filepath.Join(root, "errand-outputs")
	}
	cwd := s.Cwd
	if !filepath.IsAbs(cwd) {
		if cwd = filepath.Join(root, cwd); strings.HasPrefix(s.Cwd, "..") {
			cwd = root
		}
	}
	words := append([]string{}, s.Words...)
	if len(words) == 0 {
		return runinfo.Launched{}, errors.New("nothing to run")
	}
	if s.File != "" {
		words[0] = filepath.Join(root, filepath.FromSlash(s.File))
	}
	if given := runinfo.ToolWords(words[0], s.Tools); len(words[0]) > 0 && s.File == "" && (len(given) > 1 || given[0] != words[0]) {
		words = append(given, words[1:]...) // a tool the environment names its own way
	}
	argv, path, file := runinfo.Target(words, root, cwd)
	if argv[0] == "python" && words[0] != "python" { // a `.py`, run by the place's Python
		argv = append(append([]string{}, runinfo.ToolWords("python", s.Tools)...), argv[1:]...)
	}
	if s.As != "" { // a set of a file's entries: filed under the file and what was asked of it, whatever tool runs it
		path, file = s.As, s.As
		if s.Sel != "" {
			path += "/" + s.Sel
		}
	}
	if s.Aside != "" {
		path, file = s.Aside, ""
	}
	if err := layer.Check(s.Stack); err != nil { // said before a run is made: nothing runs in a stack this program cannot read
		return runinfo.Launched{}, fmt.Errorf("the environment %s: %w", s.Env, err)
	}
	when := runinfo.StampOf(s.When)
	host := queue.Host()
	place := s.Env + "@" + host
	params := map[string]any{}
	if rest := s.Words[1:]; len(rest) > 0 && s.As == "" && s.Aside == "" {
		params["args"] = strings.Join(rest, " ")
	}
	if s.Shown != "" {
		params["args"] = s.Shown
	}
	for k, v := range s.Params {
		params[k] = v
	}
	// the run's directory, known before it runs, and made by this run alone: the same command typed twice in a
	// second is two runs
	leaf := s.Leaf // ( made already by the agent that handed this run over: a batch system's login node )
	if leaf == "" {
		leaf = runinfo.RunLeaf(outputs, path, when, place, params)
		if s.Out != "" && s.Aside == "" {
			leaf = runinfo.OutLeaf(s.Out, root, path, when, s.Env, place, params)
		}
		if s.Aside != "" {
			leaf = filepath.Join(outputs, "state", "aside", filepath.Base(leaf))
		}
		if err := os.MkdirAll(filepath.Dir(leaf), 0o755); err != nil {
			return runinfo.Launched{}, err
		}
		for n, base := 2, leaf; ; n++ {
			err := os.Mkdir(leaf, 0o755)
			if err == nil {
				break
			}
			if !os.IsExist(err) || n > 99 {
				return runinfo.Launched{}, err
			}
			leaf = fmt.Sprintf("%s~%d", base, n)
		}
	} else if _, err := os.Stat(leaf); err != nil {
		return runinfo.Launched{}, fmt.Errorf("the run's directory, made where it was submitted, is not seen from %s ( %s ): the project's root must be on a file system the nodes share", host, leaf)
	}
	// what the run says of where it is: made before the run is handed its path, for whoever reads it from another
	// machine of a shared disk -- nothing is looked for before it exists
	// ( and where it says what it asks for, which whoever reads it from another machine of a shared disk finds the
	// same way: made now, before the run is told of it )
	for _, made := range []string{"steps.jsonl", asksName} {
		if f, err := os.OpenFile(filepath.Join(leaf, made), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644); err == nil {
			f.Close()
		}
	}
	if s.Report != nil && s.Report.File != "" { // where the tool leaves its report: known now that the run's directory is
		report := filepath.Join(leaf, s.Report.File)
		for i := range argv {
			argv[i] = strings.ReplaceAll(argv[i], "{report}", report)
		}
	}
	commit, dirty := runinfo.Provenance(root)
	tags := s.Tags
	if tags == nil {
		tags = map[string]any{}
	}
	rec := runinfo.Record{V: 1, Name: path, File: file, Argv: s.Words, Date: when.Format(time.RFC3339), Started: s.When,
		Env: s.Env, Place: place, Host: host, Project: root, Commit: commit, Dirty: dirty, Errand: system.Version,
		Params: params, Tags: tags, Results: map[string]any{}, Label: s.Label}
	if err := kit.WriteRecord(filepath.Join(leaf, runinfo.LaunchName), rec); err != nil {
		return runinfo.Launched{}, err
	}
	label := s.Label
	if label == "" {
		label = strings.Join(s.Words, " ")
	}
	id := s.ID
	if id == "" {
		id = kit.RandomID()
	}
	note := runinfo.RunNote{V: 1, ID: id, Leaf: filepath.ToSlash(leaf), What: label, Env: s.Env, Project: filepath.ToSlash(root), // ( read on other systems )
		Started: s.When, Batch: s.Batch}
	if at := schedulerAt(s.Stack); at >= 0 && s.Aside == "shell" && s.Tty != nil { // typed in: it is held from here, for as long as it is
		var err error
		if argv, err = attended(s, at, root); err != nil {
			_ = os.RemoveAll(leaf)
			return runinfo.Launched{}, err
		}
		s.Stack, s.NoQueue = s.Stack[:at], true
	}
	if at := schedulerAt(s.Stack); at >= 0 { // handed to a batch system: its node's agent will hold it
		if err := a.submit(s, at, id, leaf, label, root, cwd); err != nil {
			_ = os.RemoveAll(leaf)
			return runinfo.Launched{}, err
		}
		said := ""
		if s.Tty != nil {
			said = "errand: handed to a batch system, this run has no terminal ( nothing links the login node to its node ): its output goes through pipes, and nothing typed reaches it"
		}
		return runinfo.Launched{Note: note, Banner: layer.Describe(s.Stack), Host: host, Outputs: filepath.ToSlash(outputs), Said: said}, nil
	}
	j := &job{a: a, id: id, dir: filepath.Join(a.Dir, "jobs", id), leaf: leaf, outputs: outputs, spec: s, argv: argv, cwd: cwd,
		label: label, rec: rec, changed: make(chan struct{}), files: map[string]*os.File{}, sizes: map[string]int64{},
		done: make(chan struct{}), quit: make(chan struct{}), queued: make(chan struct{})}
	if err := os.MkdirAll(j.dir, 0o755); err != nil {
		return runinfo.Launched{}, err
	}
	j.saveMeta()
	a.mu.Lock()
	a.jobs[id] = j
	a.idleSince = time.Now()
	a.mu.Unlock()
	go j.run()
	return runinfo.Launched{Note: note, Banner: layer.Describe(s.Stack), Host: host, Outputs: filepath.ToSlash(outputs)}, nil
}

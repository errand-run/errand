// A job, as its place's agent holds it ( ADR 0035 ): no process of errand's per job. The agent takes the job's place
// in the machine's queue, starts what was asked in a session of its own, writes what it prints into the run's
// directory as it prints it, and writes how it ended.
//
//	<agent>/jobs/<id>/meta.json    what it is, its process and group, when that process started
//	<agent>/jobs/<id>/exit         its exit code, once it ended
//	<leaf>/stdout.txt, stderr.txt  each stream, as written to it
//	<leaf>/output.txt              both, as they came
//
// Whoever follows a job is fed from those files, from where it is in them: following live and reading what was
// missed are the same code, and a follower that does not read holds nothing back. A stop is INT, TERM after 2 s,
// KILL after 5 s, to the whole group ( ADR 0008 ) -- the agent's, whether or not anybody stays to see it.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/draw"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
	"github.com/errand-run/errand/internal/system"
)

type job struct {
	a       *Place
	id      string
	dir     string // its record, among this machine's agent's
	leaf    string // the run's directory
	outputs string // where errand writes for the project, on this machine
	spec    runinfo.LaunchSpec
	argv    []string          // the target's words, then what the layers make of them
	env     map[string]string // what the layers set
	cwd     string
	label   string
	rec     runinfo.Record

	mu           sync.Mutex
	changed      chan struct{} // closed, and made again, whenever something a follower waits for changed
	files        map[string]*os.File
	sizes        map[string]int64
	marks        int // how many times its directory was seen to change: its journal written, a file more
	open         int // streams not at their end yet
	stdin        io.WriteCloser
	pty          *jobPty // its terminal, when it has one
	cols         int     // the size of its terminal, as it was told last
	rows         int
	typed        [][]byte   // what was given it to read before its process was there
	noMore       bool       // its input ended
	inMu         sync.Mutex // what is written to it, in order
	pid          int
	code         *int
	waiting      string // what it waits for, while it waits for the machine
	waitingSince time.Time
	where        stepReader // where it says it is
	stopped      bool
	cancel       context.CancelFunc // of its wait for the machine
	skipped      string             // why it was not run, when that is not a failure
	next         []runinfo.Launched // the runs made after it, when it asked for what it did not have ( rerun.go )
	notes        []string           // errand's own words about it: what is prepared for it, what it waits for
	told         []map[string]any   // what whoever follows is to know: a resolution for the project's lock
	quit         chan struct{}      // closed when it is told to stop
	queued       chan struct{}      // closed once it has its place on the machine, or its turn in the queue for one
	inLine       sync.Once
	done         chan struct{}
}

// note: something errand says about a job, to whoever follows it -- not the job's output, and not kept with it
func (j *job) note(text string) {
	j.mu.Lock()
	j.notes = append(j.notes, text)
	j.wake()
	j.mu.Unlock()
	j.a.touched()
}

// tell: what whoever follows the job is to know, said to each of them
func (j *job) tell(m map[string]any) {
	j.mu.Lock()
	j.told = append(j.told, m)
	j.wake()
	j.mu.Unlock()
	j.a.touched()
}

func (j *job) wake() { // ( under j.mu )
	close(j.changed)
	j.changed = make(chan struct{})
}

// meta: what is written of a job where its machine keeps it -- enough for the next agent to end what an agent killed
// outright left running ( ADR 0008 )
type jobMeta struct {
	V       int     `json:"v"`
	ID      string  `json:"id"`
	Leaf    string  `json:"leaf"`
	What    string  `json:"what"`
	Env     string  `json:"env"`
	Project string  `json:"project"`
	Started float64 `json:"started"`
	Agent   int     `json:"agent"`
	Pid     int     `json:"pid,omitempty"`
	Since   any     `json:"since,omitempty"` // when that process started: a pid used again is somebody else
	Slurm   string  `json:"slurm,omitempty"` // the batch system's number for it, when it was handed to one
	Alloc   string  `json:"alloc,omitempty"` // the allocation it was handed to: its directory
}

func (j *job) saveMeta() {
	m := jobMeta{V: 1, ID: j.id, Leaf: j.leaf, What: j.label, Env: j.spec.Env, Project: j.rec.Project, Started: j.spec.When,
		Agent: os.Getpid(), Pid: j.pid}
	if j.pid != 0 {
		m.Since = kit.Started(j.pid)
	}
	_ = kit.WriteRecord(filepath.Join(j.dir, "meta.json"), m)
}

func readJobMeta(dir string) (jobMeta, bool) {
	var m jobMeta
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil || json.Unmarshal(data, &m) != nil || m.ID == "" {
		return m, false
	}
	return m, true
}

// jobExit: how a job ended, as its record says
func jobExit(dir string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "exit"))
	if err != nil {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return code, err == nil
}

// end: how it ended, written -- the run's result, then its exit code where its machine keeps it
func (j *job) end(code int, began time.Time, usage *syscall.Rusage, why string) {
	j.inLine.Do(func() { close(j.queued) })
	rec := j.rec
	rec.Ended(code, filepath.Join(j.leaf, runinfo.StreamNames["err"]))
	if why != "" {
		rec.Status, rec.Error = "FAIL", &why
	} else if j.skipped != "" {
		rec.Error = &j.skipped
	}
	if !began.IsZero() {
		d := time.Since(began).Seconds()
		rec.Duration = &d
	}
	if usage != nil {
		mb := system.MaxRSS(usage)
		rec.RAM = &mb
	}
	results := filepath.Join(j.leaf, "results.jsonl")
	rec.Results = runinfo.MergeResults(results)
	_ = os.Remove(results)
	if why == "" {
		rec.Reported(j.spec.Report, j.leaf, code) // what its tool says of the entries it ran
	}
	var next []runinfo.Launched
	if asks := asksOf(j.spec, j.leaf, rec, code); len(asks) > 0 && why == "" && !j.isStopped() {
		// It asked for what it did not have. Its agent -- this one, or the login node's when a batch system holds it --
		// launches what asked again, with it: the machine is already given back, so that what it asked for can be had.
		refused, said := "", []string(nil)
		if j.a.kind != "node" {
			if next, said, refused = j.a.again(j.spec, asks); refused != "" {
				j.say("errand: " + refused + "\n")
			} else {
				j.say("errand: asked for " + runinfo.AsksSaid(asks) + ": run again\n")
			}
		}
		rec.NoteAsked(asks, j.spec.Redo == nil, next, said, refused)
	}
	out := runinfo.StreamNames["both"]
	rec.Output = &out
	_ = kit.WriteRecord(filepath.Join(j.leaf, runinfo.ResultName), rec)
	if j.spec.Aside == "" {
		runinfo.Settled(j.leaf, nil, "")
	}
	caches.NoteBuilds(filepath.Join(j.outputs, "caches"))
	_ = kit.WriteWhole(filepath.Join(j.dir, "exit"), strconv.Itoa(code))
	_ = os.Remove(filepath.Join(j.dir, "waiting"))
	j.mu.Lock()
	j.code, j.next = &code, next
	for _, f := range j.files {
		f.Close()
	}
	j.wake()
	j.mu.Unlock()
	j.a.touched()
}

func (j *job) say(text string) { // errand's own words about a job go where the job's errors go
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, name := range []string{"err", "both"} {
		if f := j.files[name]; f != nil {
			n, _ := f.WriteString(text)
			j.sizes[name] += int64(n)
		}
	}
	j.wake()
}

// run: the job's whole life, in the agent
func (j *job) run() {
	defer close(j.done)
	for name, as := range runinfo.StreamNames {
		f, err := os.OpenFile(filepath.Join(j.leaf, as), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			j.end(126, time.Time{}, nil, err.Error())
			return
		}
		j.files[name] = f
	}
	ctx, cancel := context.WithCancel(context.Background())
	j.mu.Lock()
	j.cancel = cancel
	already := j.stopped
	j.mu.Unlock()
	if already {
		cancel()
	}
	if why := j.spec.Refused; why != "" { // what it needs could not be made: said, and the run ends as failed
		j.say("errand: " + why + "\n")
		j.end(1, time.Time{}, nil, strings.SplitN(why, "\n", 2)[0])
		return
	}
	env := j.spec.Environ // the surroundings of whoever typed the command, when it was typed on this machine
	if env == nil {
		env = os.Environ()
	}
	env = append([]string{}, env...)
	if runtime.GOOS == "windows" { // what it writes is read as UTF-8, whatever this system's code page is
		if _, said := kit.EnvOf(env, "PYTHONUTF8"); !said {
			env = append(env, "PYTHONUTF8=1")
		}
	}
	// its environment, built or brought up to date first: by this agent, whether or not who asked stays ( ADR 0035 )
	if !j.spec.NoSetup {
		st := setting{root: j.rec.Project, outputs: j.outputs, tags: j.rec.Tags, environ: env, say: j.note, tell: j.tell, stop: j.quit}
		built := j.spec.Stack
		if box := containerAt(built); box >= 0 { // ( the image is this agent's to build; what is inside is its own agent's )
			built = built[:box+1]
		}
		if err := ensure(built, st); err != nil {
			j.say("errand: " + err.Error() + "\n")
			j.end(1, time.Time{}, nil, strings.SplitN(err.Error(), "\n", 2)[0])
			return
		}
	}
	lctx := layer.Context{Root: j.rec.Project, Tags: j.rec.Tags, Environ: env}
	if j.spec.Requires != nil {
		if why := j.requireTool(j.spec.Stack, lctx, env); why != "" {
			j.say("errand: " + why + "\n")
			j.end(1, time.Time{}, nil, strings.SplitN(why, "\n", 2)[0])
			return
		}
	}
	wrapped, first, skip, err := j.command(j.spec.Stack, lctx, &env)
	if err != nil {
		j.say(fmt.Sprintf("errand: the environment %s: %v\n", j.spec.Env, err))
		j.end(126, time.Time{}, nil, "")
		return
	}
	if skip != "" { // nothing to start its ranks with: skipped, saying what to install
		j.say("errand: " + skip + "\n")
		j.skipped = skip
		j.end(77, time.Time{}, nil, "")
		return
	}
	target := j.argv
	j.argv, j.env = wrapped.Argv, wrapped.Env
	for k, v := range j.env {
		env = append(env, k+"="+v)
	}
	if first >= 0 && first < len(j.argv) && j.argv[first] == "python" { // no layer chose an interpreter: this machine's
		for _, python := range []string{j.spec.Python, "python", "python3"} {
			if python == "" {
				continue
			}
			if _, err := kit.LookIn(python, env, j.cwd); err == nil {
				j.argv[first] = python
				break
			}
		}
	}
	if j.spec.Mem != "" {
		// `systemd-run`, where there is one that works: a run goes without its ceiling rather than not at all
		scope := []string{"systemd-run", "--user", "--scope", "-q", "-p", "MemoryMax=" + j.spec.Mem}
		if path, err := kit.LookIn("systemd-run", env, j.cwd); err == nil {
			try := &exec.Cmd{Path: path, Args: append(append([]string{}, scope...), "true"), Env: env}
			if try.Run() == nil {
				j.argv = append(scope, j.argv...)
				if first >= 0 {
					first += len(scope)
				}
			}
		}
	}
	if j.spec.Verbose {
		j.note("errand: " + strings.Join(j.argv, " "))
	}
	// its place on the machine: waited for, then held until the work is done
	surroundings := map[string]string{}
	for _, kv := range j.spec.Environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			surroundings[k] = v
		}
	}
	// what one rank needs, as many times as there are ranks
	req := queue.Request{Needs: j.spec.Needs.Normalized(max(1, j.spec.Ranks)), Exclusive: j.spec.Exclusive, Label: j.label, NoQueue: j.spec.NoQueue,
		Waiting: func(what string) { // said to whoever looks at the run ( ADR 0009 )
			if what != "" {
				j.inLine.Do(func() { close(j.queued) })
			}
			j.mu.Lock()
			if what != "" && j.waiting == "" {
				j.waitingSince = time.Now()
			}
			j.waiting = what
			j.wake()
			j.mu.Unlock()
			if what == "" {
				_ = os.Remove(filepath.Join(j.dir, "waiting"))
			} else {
				_ = kit.WriteRecord(filepath.Join(j.dir, "waiting"), map[string]any{"what": what, "since": kit.Seconds(time.Now())})
			}
			if j.spec.Waits != "" { // said as well where an agent that follows it from a shared disk reads it
				_ = kit.AppendLine(j.spec.Waits, map[string]any{"waiting": what})
			}
			j.a.touched()
		}}
	if inherited, ok := surroundings[queue.EnvClaim]; ok { // started inside a run: the machine was granted already
		req.Inherited = &inherited
	}
	if surroundings[queue.EnvNoQueue] != "" {
		req.NoQueue = true
	}
	granted, err := j.a.claim(ctx, req) // by this agent, or asked of the one that holds the machine's resources
	j.inLine.Do(func() { close(j.queued) })
	if err != nil {
		why := "stopped while it waited for the machine"
		if ctx.Err() == nil {
			why = err.Error()
		}
		j.end(130, time.Time{}, nil, why)
		return
	}
	defer granted.Release()

	self, _ := os.Executable()
	set := func(k, v string) { env = append(env, k+"="+v) }
	for k, v := range granted.Env() {
		set(k, v)
	}
	if req.Inherited == nil {
		for k, v := range granted.HandedDown() {
			set(k, v)
		}
	}
	set("ERRAND_PROTOCOL", "1")
	set("ERRAND", self)
	set("ERRAND_STEPS", filepath.Join(j.leaf, "steps.jsonl"))
	set(runinfo.EnvAsks, asksPath(j.leaf))
	set(runinfo.EnvRanks, fmt.Sprint(max(1, j.spec.Ranks)))
	set("ERRAND_RESULTS", filepath.Join(j.leaf, "results.jsonl"))
	set("ERRAND_TAGS", runinfo.TagsSaid(j.rec.Tags))
	set("ERRAND_PROC_DIR", j.dir)
	set("ERRAND_ROOT", j.rec.Project)
	if j.spec.Own {
		set("ERRAND_OUT_DIR", j.leaf)
	}
	j.handed(env, set)
	if isPython(target) {
		j.argv = j.warmed(j.argv, first, env)
	}
	path, err := kit.LookIn(j.argv[0], env, j.cwd)
	if err != nil {
		if j.spec.Missing != "" {
			j.say("errand: " + j.spec.Missing + "\n")
			j.end(127, time.Time{}, nil, j.spec.Missing)
			return
		}
		// A target that designates nothing is an error, said where it would have run: only the place knows what it has.
		j.say(fmt.Sprintf("errand: %s: not a file of the project, nor a command here ( %v )\n", j.argv[0], err))
		j.end(127, time.Time{}, nil, "")
		return
	}
	cmd := &exec.Cmd{Path: path, Args: j.argv, Env: env, Dir: j.cwd, SysProcAttr: system.Apart()}
	var in io.WriteCloser
	var outR, errR, outW, errW *os.File // what it writes, read here; and its ends, handed to it
	if tty := j.spec.Tty; tty != nil && !j.spec.Batch && runtime.GOOS != "windows" {
		// A terminal of its own, as whoever typed the command has one ( ADR 0005, 0016 ): a pseudo-terminal for each
		// stream, so that what was written to which is known, with what is written left as it was ( no carriage return
		// added ). Standard input is the first when what is typed reaches the run, and the terminal that controls it.
		pty, err := openJobPty(tty)
		if err != nil {
			j.say(fmt.Sprintf("errand: a terminal for %s: %v\n", j.argv[0], err))
			j.end(126, time.Time{}, nil, "")
			return
		}
		j.mu.Lock()
		j.pty = pty
		if j.cols == 0 {
			j.cols, j.rows = tty.Cols, tty.Rows
		}
		pty.resize(j.cols, j.rows)
		j.mu.Unlock()
		outR, errR, outW, errW = pty.out, pty.err, pty.outSlave, pty.errSlave
		cmd.Env = append(cmd.Env, "TERM="+tty.Term)
		if tty.Colors {
			cmd.Env = append(cmd.Env, "FORCE_COLOR=1", "CLICOLOR_FORCE=1")
		}
		if tty.Input {
			cmd.Stdin, in = outW, ptyInput{outR}
			cmd.SysProcAttr = system.OnTerminal(0)
		} else {
			in, _ = cmd.StdinPipe()
			cmd.SysProcAttr = system.OnTerminal(1)
		}
	} else {
		in, _ = cmd.StdinPipe()
		outR, outW, _ = os.Pipe()
		errR, errW, _ = os.Pipe()
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	defer outR.Close() // ( a terminal's master is held until the run is over )
	defer errR.Close()
	j.mu.Lock()
	if j.stopped {
		j.mu.Unlock()
		outW.Close()
		errW.Close()
		j.end(130, time.Time{}, nil, "")
		return
	}
	began := time.Now()
	if err := cmd.Start(); err != nil {
		j.mu.Unlock()
		outW.Close()
		errW.Close()
		j.say(fmt.Sprintf("errand: %s: %v\n", j.argv[0], err))
		j.end(126, time.Time{}, nil, "")
		return
	}
	outW.Close()
	errW.Close()
	j.pid, j.stdin, j.open = cmd.Process.Pid, in, 2
	early := j.typed
	j.typed = nil
	if j.spec.Batch || j.noMore { // nobody will type to it ( any more ): its input ends here
		j.stdin = nil
	}
	over := j.stdin == nil
	j.inMu.Lock()
	go func() { // what was typed before it was there, then its end if that was said
		defer j.inMu.Unlock()
		for _, data := range early {
			_, _ = in.Write(data)
		}
		if over {
			in.Close()
		}
	}()
	j.saveMeta()
	j.mu.Unlock()
	j.a.touched()
	// what it says of itself is a file it appends to ( ADR 0032 ): the system says when it does
	defer system.Watch(j.leaf, "steps.jsonl", func() {
		j.mu.Lock()
		j.marks++
		j.wake()
		j.mu.Unlock()
		j.a.stir()
	})()
	var pumps sync.WaitGroup
	pumps.Add(2)
	go j.pump(outR, "out", &pumps)
	go j.pump(errR, "err", &pumps)
	_ = cmd.Wait()
	// The leader ended. What it left in its group is given a moment to end by itself ( a runtime unmounting an
	// image ), then goes with it: nothing runs unowned ( ADR 0008 ).
	written := make(chan struct{})
	go func() { pumps.Wait(); close(written) }()
	select {
	case <-written:
	case <-time.After(kit.FlushFor):
	}
	_ = system.SignalGroup(j.pid, syscall.SIGKILL)
	select {
	case <-written:
	case <-time.After(time.Second): // something outside its group still holds its streams: they are let go of
		outR.Close()
		errR.Close()
		<-written
	}
	code := 1
	var usage *syscall.Rusage
	if st := cmd.ProcessState; st != nil {
		if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			code = -int(ws.Signal())
		} else {
			code = st.ExitCode()
		}
		usage, _ = st.SysUsage().(*syscall.Rusage)
	}
	granted.Release() // ( before the record is written: the machine is free as soon as the work is done )
	j.end(code, began, usage, "")
}

// pump: what the job writes, into its files, as it writes it. Nobody is waited for: followers read the files.
func (j *job) pump(r *os.File, name string, done *sync.WaitGroup) {
	defer done.Done()
	buf := make([]byte, kit.Chunk)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			j.mu.Lock()
			_, _ = j.files[name].Write(buf[:n])
			_, _ = j.files["both"].Write(buf[:n])
			j.sizes[name] += int64(n)
			j.sizes["both"] += int64(n)
			j.wake()
			j.mu.Unlock()
			j.a.stir()
		}
		if err != nil {
			break
		}
	}
	j.mu.Lock()
	j.open--
	j.wake()
	j.mu.Unlock()
}

// stop: INT ( or `first` ), TERM after 2 s, KILL after 5 s, to the whole group; a job still waiting for the machine
// has nothing to wait for any more
func (j *job) stop(first syscall.Signal) {
	j.mu.Lock()
	if j.stopped || j.code != nil {
		pid := j.pid
		over := j.code != nil
		j.mu.Unlock()
		if !over && pid != 0 && first != syscall.SIGINT { // asked again, harder
			_ = system.SignalGroup(pid, first)
		}
		return
	}
	j.stopped = true
	close(j.quit)
	pid, cancel := j.pid, j.cancel
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if pid == 0 {
		return
	}
	_ = system.SignalGroup(pid, first)
	go func() {
		waits := []time.Duration{kit.Grace[0], kit.Grace[1] - kit.Grace[0]}
		for i, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
			select {
			case <-j.done:
				return
			case <-time.After(waits[i]):
			}
			if sig != first {
				_ = system.SignalGroup(pid, sig)
			}
		}
	}()
}

func (j *job) isStopped() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.stopped
}

func (j *job) state() (string, string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	switch {
	case j.code != nil:
		if r, ok := runinfo.ReadRecord(filepath.Join(j.leaf, runinfo.ResultName)); ok {
			return "ended", r.Status
		}
		return "ended", fmt.Sprintf("exit %d", *j.code)
	case j.waiting != "":
		return "waiting", j.waiting
	}
	return "running", ""
}

// follow: the job, on a channel -- what it wrote and writes, from its beginning, then how it ended; what the channel
// says is the job's to read. While the job waits for the machine, for what.
func (j *job) follow(req map[string]any, f *reach.FarChan) {
	both, _ := req["both"].(bool)
	mine, _ := req["mine"].(bool)
	f.Reply(map[string]any{"ok": true, "live": true, "tty": j.hasTty()})
	go func() {
		for {
			select {
			case m := <-f.Inbox:
				switch m.Kind {
				case kit.RESIZE: // the window of whoever follows it changed size
					var size map[string]any
					if json.Unmarshal(m.Payload, &size) == nil {
						j.resize(int(kit.Num(size["cols"], 0)), int(kit.Num(size["rows"], 0)))
					}
				case kit.IN:
					j.mu.Lock()
					in := j.stdin
					if j.pid == 0 && !j.noMore { // not started yet: kept for when it is
						j.typed = append(j.typed, m.Payload)
					}
					j.mu.Unlock()
					if in != nil {
						j.inMu.Lock()
						_, _ = in.Write(m.Payload)
						j.inMu.Unlock()
					}
				case kit.EOF:
					j.closeInput()
				case kit.SIGNAL:
					var s map[string]any
					_ = json.Unmarshal(m.Payload, &s)
					if keep, _ := s["keep"].(bool); keep { // it is let go of, not gone: its input stays as it is
						mine = false
					}
					if stop, _ := s["stop"].(bool); stop {
						j.stop(syscall.Signal(int(kit.Num(s["first"], float64(syscall.SIGINT)))))
					}
				}
			case <-f.Gone:
				if mine { // who was to type to it went: its input ends, and nothing else of it does
					j.closeInput()
				}
				return
			case <-j.done:
				return
			}
		}
	}()
	names := []string{"out", "err"}
	if both {
		names = []string{"both"}
	}
	kinds := map[string]byte{"out": kit.OUT, "err": kit.ERR, "both": kit.OUT}
	at := map[string]int64{}
	readers := map[string]*os.File{}
	defer func() {
		for _, r := range readers {
			r.Close()
		}
	}()
	buf := make([]byte, kit.Chunk)
	said, noted, toldN := "", 0, 0
	wanted := map[string]bool{}
	var steps *draw.StepTail // where it says it is, for whoever draws that
	if show, _ := req["status"].(bool); show {
		steps = &draw.StepTail{Path: filepath.Join(j.leaf, "steps.jsonl")}
	}
	for {
		j.mu.Lock()
		sizes := map[string]int64{}
		for _, n := range names {
			sizes[n] = j.sizes[n]
		}
		code, waiting, open, wait, next := j.code, j.waiting, j.open, j.changed, j.next
		notes := j.notes[noted:]
		told := j.told[toldN:]
		j.mu.Unlock()
		for _, text := range notes {
			b, _ := json.Marshal(map[string]any{"say": text})
			f.Out(kit.NOTE, b)
		}
		noted += len(notes)
		for _, m := range told {
			b, _ := json.Marshal(m)
			f.Out(kit.NOTE, b)
		}
		toldN += len(told)
		for _, w := range j.a.pendingFor(j.rec.Project) { // what nobody here has a way to: whoever follows may
			if !wanted[w.ID] {
				wanted[w.ID] = true
				b, _ := json.Marshal(map[string]any{"want": w})
				f.Out(kit.NOTE, b)
			}
		}
		if waiting != said {
			said = waiting
			b, _ := json.Marshal(map[string]any{"waiting": strings.TrimPrefix(waiting, "room on "+queue.Host()+": ")})
			f.Out(kit.NOTE, b)
		}
		if steps != nil {
			if st, ok := steps.Poll(); ok {
				b, _ := json.Marshal(map[string]any{"step": st})
				f.Out(kit.NOTE, b)
			}
		}
		behind := false
		for _, n := range names {
			for at[n] < sizes[n] {
				r := readers[n]
				if r == nil {
					var err error
					if r, err = os.Open(filepath.Join(j.leaf, runinfo.StreamNames[n])); err != nil {
						break
					}
					readers[n] = r
				}
				want := sizes[n] - at[n]
				if want > int64(len(buf)) {
					want = int64(len(buf))
				}
				got, err := r.ReadAt(buf[:want], at[n])
				if got > 0 {
					f.Out(kinds[n], append([]byte{}, buf[:got]...))
					at[n] += int64(got)
				}
				if err != nil {
					break
				}
				select {
				case <-f.Gone:
					return
				default:
				}
			}
			if at[n] < sizes[n] {
				behind = true
			}
		}
		if code != nil && open <= 0 && !behind {
			if len(next) > 0 { // whoever follows it is carried to what was made after it
				b, _ := json.Marshal(map[string]any{"again": next})
				f.Out(kit.NOTE, b)
			}
			b, _ := json.Marshal(map[string]any{"code": *code})
			f.Out(kit.EXIT, b)
			return
		}
		select {
		case <-wait:
		case <-f.Gone:
			return
		}
	}
}

func (j *job) closeInput() {
	j.mu.Lock()
	in := j.stdin
	j.stdin = nil
	j.noMore = true
	j.mu.Unlock()
	if in != nil {
		j.inMu.Lock()
		in.Close()
		j.inMu.Unlock()
	}
}

// replayJob: a job that ended and that no agent holds any more, said from what it left -- its output, then how it
// ended
func replayJob(leaf string, code int, both bool, f *reach.FarChan) {
	f.Reply(map[string]any{"ok": true, "live": false})
	say := func(kind byte, name string) {
		file, err := os.Open(filepath.Join(leaf, runinfo.StreamNames[name]))
		if err != nil {
			return
		}
		defer file.Close()
		buf := make([]byte, kit.Chunk)
		for {
			n, err := file.Read(buf)
			if n > 0 {
				f.Out(kind, append([]byte{}, buf[:n]...))
			}
			if err != nil {
				return
			}
		}
	}
	if both {
		say(kit.OUT, "both")
	} else {
		say(kit.OUT, "out")
		say(kit.ERR, "err")
	}
	b, _ := json.Marshal(map[string]any{"code": code})
	f.Out(kit.EXIT, b)
}

// endOrphan: a job its agent was killed over ( ADR 0008: the next agent ends it, it does not adopt it ) -- what is
// left of its group is killed, and its record says why
func endOrphan(dir string, m jobMeta) {
	if m.Pid != 0 && fmt.Sprint(kit.Started(m.Pid)) == fmt.Sprint(m.Since) && m.Since != nil {
		_ = system.SignalGroup(m.Pid, syscall.SIGTERM)
		time.Sleep(300 * time.Millisecond)
		_ = system.SignalGroup(m.Pid, syscall.SIGKILL)
	}
	if rec, ok := runinfo.ReadRecord(filepath.Join(m.Leaf, runinfo.LaunchName)); ok {
		if _, done := runinfo.ReadRecord(filepath.Join(m.Leaf, runinfo.ResultName)); !done {
			code := -int(syscall.SIGKILL)
			rec.Ended(code, filepath.Join(m.Leaf, runinfo.StreamNames["err"]))
			why := "the agent that held this run was killed, and the run with it"
			if rec.Error != nil {
				why += ": " + *rec.Error
			}
			rec.Status, rec.Error = "FAIL", &why
			out := runinfo.StreamNames["both"]
			rec.Output = &out
			rec.Results = runinfo.MergeResults(filepath.Join(m.Leaf, "results.jsonl"))
			_ = kit.WriteRecord(filepath.Join(m.Leaf, runinfo.ResultName), rec)
			runinfo.Settled(m.Leaf, nil, "")
		}
	}
	_ = kit.WriteWhole(filepath.Join(dir, "exit"), strconv.Itoa(-int(syscall.SIGKILL)))
}

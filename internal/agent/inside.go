// Agents that others depend on ( ADR 0035 ), and the first of those others: a container's agent.
//
// **An agent with a responsibility stays while something uses what it is responsible for.** The agent of a machine
// holds the machine's resources -- its queue: cores, memory, cards. An agent started further ( inside a container
// here ) says it depends on it ( `depend` ), and says when it has runs going and when it has none; the machine's
// agent does not go -- not after its keep, not when it is retired by another version -- while it holds a place for
// somebody or an agent under it has a run going. With neither, it goes at its keep as any agent, and those under it
// with it: they had nothing going.
//
// **Resources are granted by the agent that holds them**, never taken by the agent of the job: a job in a container
// asks the machine's agent for its place ( `claim` ), which waits for it in the machine's queue and holds it for
// the job. A container isolates neither cores, memory nor cards: they are the machine's.
//
// **A container's agent** is this program, started once through the layer that enters the container
// ( `apptainer exec … errand place --inside` ), and staying that command's own process: with apptainer an image is
// unmounted soon after it ends ( ADR 0029 ). It may go with its last job; the machine's agent stays.

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
	"github.com/errand-run/errand/internal/system"
)

// held is a place in a machine's queue, held for a job -- by this agent, or by the one it depends on.
type held interface {
	Env() map[string]string
	HandedDown() map[string]string
	Release()
}

// askedClaim is a place granted by the agent this one depends on, and held there while the channel is open.
type askedClaim struct {
	env, handed map[string]string
	l           *reach.Link
	c           *reach.Call
}

func (k *askedClaim) Env() map[string]string { return k.env }

func (k *askedClaim) HandedDown() map[string]string { return k.handed }

func (k *askedClaim) Release() {
	if k.c != nil {
		k.c.Close()
		k.l.Close()
		k.c = nil
	}
}

// claim: a job's place in the machine's queue -- taken by this agent when the machine's resources are its own, asked
// of the agent it depends on otherwise
func (a *Place) claim(ctx context.Context, r queue.Request) (held, error) {
	if a.parent == "" {
		return queue.Open().Acquire(ctx, r)
	}
	l, err := reach.LinkToAgent(a.parent)
	if err != nil {
		return nil, fmt.Errorf("the agent that holds this machine's resources does not answer: %w", err)
	}
	c := l.Open(map[string]any{"op": "claim", "needs": r.Needs, "exclusive": r.Exclusive, "label": r.Label, "no_queue": r.NoQueue,
		"inherited": r.Inherited})
	if _, err := c.Reply(); err != nil {
		l.Close()
		return nil, err
	}
	got := make(chan map[string]any, 4)
	go func() {
		defer close(got)
		for {
			kind, payload, err := c.Recv()
			if err != nil {
				return
			}
			var said map[string]any
			if kind == kit.NOTE && json.Unmarshal(payload, &said) == nil {
				got <- said
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			c.Close()
			l.Close()
			return nil, ctx.Err()
		case said, more := <-got:
			if !more {
				l.Close()
				return nil, errors.New("the agent that holds this machine's resources went away")
			}
			if what, ok := said["waiting"].(string); ok && r.Waiting != nil {
				r.Waiting(what)
			}
			if why, ok := said["refused"].(string); ok {
				c.Close()
				l.Close()
				return nil, errors.New(why)
			}
			if g, ok := said["granted"].(map[string]any); ok {
				k := &askedClaim{env: map[string]string{}, handed: map[string]string{}, l: l, c: c}
				_ = reach.DecodeInto(g["env"], &k.env)
				_ = reach.DecodeInto(g["handed"], &k.handed)
				return k, nil
			}
		}
	}
}

// serveClaim: a place in this machine's queue, waited for and held for a job of an agent that depends on this one --
// until that agent lets go of it, or goes
func (a *Place) serveClaim(req map[string]any, f *reach.FarChan) {
	r := queue.Request{Label: kit.Text(req["label"])}
	_ = reach.DecodeInto(req["needs"], &r.Needs)
	r.Exclusive, _ = req["exclusive"].(bool)
	r.NoQueue, _ = req["no_queue"].(bool)
	if inherited, ok := req["inherited"].(string); ok {
		r.Inherited = &inherited
	}
	note := func(v any) {
		b, _ := json.Marshal(v)
		f.Out(kit.NOTE, b)
	}
	r.Waiting = func(what string) { note(map[string]any{"waiting": what}) }
	f.Reply(map[string]any{"ok": true})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-f.Gone
		cancel()
	}()
	granted, err := queue.Open().Acquire(ctx, r)
	if err != nil {
		note(map[string]any{"refused": err.Error()})
		return
	}
	note(map[string]any{"granted": map[string]any{"env": granted.Env(), "handed": granted.HandedDown()}})
	a.mu.Lock()
	a.claims++ // held for somebody: this agent stays
	a.mu.Unlock()
	<-f.Gone
	granted.Release()
	a.mu.Lock()
	a.claims--
	a.mu.Unlock()
	a.touched()
}

// serveDepend: an agent says it depends on this one, for as long as it lives
func (a *Place) serveDepend(req map[string]any, f *reach.FarChan) {
	f.Reply(map[string]any{"ok": true, "responsible": responsibilities})
	a.mu.Lock()
	a.links-- // ( its connection is not somebody asking: it is counted by what it says it has going )
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.links++
		a.mu.Unlock()
	}()
	busy := false
	set := func(now bool) {
		if now != busy {
			busy = now
			a.mu.Lock()
			if now {
				a.deps++
			} else {
				a.deps--
			}
			a.idleSince = time.Now()
			a.mu.Unlock()
		}
	}
	for {
		select {
		case m := <-f.Inbox: // it says when it has runs going, and when it has none
			var said map[string]any
			if json.Unmarshal(m.Payload, &said) == nil {
				now, _ := said["busy"].(bool)
				set(now)
			}
		case <-f.Gone:
			set(false)
			return
		}
	}
}

// responsibilities: what the agent of a machine holds for the agents further
var responsibilities = []string{"resources"}

// dependOn: this agent lives under another -- told so, for as long as this one lives. That one gone, whatever made it
// go, this one stops what it holds and goes: nothing is left running under an agent that is not there ( ADR 0008 ).
func (a *Place) dependOn(parent string) error {
	l, err := reach.LinkToAgent(parent)
	if err != nil {
		return err
	}
	c := l.Open(map[string]any{"op": "depend", "kind": a.kind, "dir": a.Dir, "needs": []string{"resources"}})
	if _, err := c.Reply(); err != nil {
		l.Close()
		return err
	}
	go func() {
		<-l.Dead
		a.stopAll()
		os.Exit(1)
	}()
	go func() { // told when this agent has runs going, and when it has none: that is when it is needed
		busy := false
		for {
			a.mu.Lock()
			wait := a.changed
			a.mu.Unlock()
			if now := a.running() > 0; now != busy {
				busy = now
				_ = c.SendJSON(kit.IN, map[string]any{"busy": busy})
			}
			select {
			case <-wait:
			case <-l.Dead:
				return
			}
		}
	}()
	return nil
}

// ── a container's agent ──────────────────────────────────────────────────────

// containerAt: where a stack enters a container; -1 when it does not
func containerAt(stack []project.Layer) int {
	for i, l := range stack {
		if layer.IsContainer(l) {
			return i
		}
	}
	return -1
}

// inside: the agent of the container a stack enters, reached -- started through the layers that lead there when it is
// not there. -> its directory, and what is left of the stack once inside
func (a *Place) inside(stack []project.Layer, root string, tags map[string]any, environ []string) (string, []project.Layer, error) {
	at := containerAt(stack)
	if environ == nil {
		environ = os.Environ()
	}
	// what the image is, as it is now: an agent holds the image it was entered through for as long as it lives ( a
	// mounted file, a container ), so an image that was built again is another agent's, not that one's
	var image string
	if made := layer.Artefacts(stack[at : at+1]); len(made) > 0 {
		st := setting{root: root, tags: tags, environ: environ}
		_, image = st.line("( "+made[0].Stamp(st.ctx())+" ) 2>/dev/null", nil)
	}
	b, _ := json.Marshal([]any{stack[:at+1], tags, root, image})
	sum := sha256.Sum256(b)
	dir := filepath.Join(a.Root, filepath.Base(a.Dir)+"+"+hex.EncodeToString(sum[:])[:12])
	a.innerMu.Lock()
	defer a.innerMu.Unlock()
	if reach.AgentAlive(dir) {
		return dir, stack[at+1:], nil
	}
	ctx := layer.Context{Root: root, Tags: tags, Environ: environ, Hostname: queue.Host()}
	// what the agent needs to see from inside, at the same paths: this program, the agents' directories, the short
	// links their sockets are reached by
	binds := []string{filepath.Dir(kit.SelfPath()), a.Root}
	if real, err := filepath.EvalSymlinks(a.Root); err == nil && real != a.Root {
		binds = append(binds, real)
	}
	if os.Getenv("ERRAND_CACHE_DIR") != "" { // errand's directory, when it is not in the home an image is given
		binds = append(binds, kit.LocalCache())
	}
	if st, err := os.Stat(fmt.Sprintf("/tmp/errand-%d", os.Getuid())); err == nil && st.IsDir() {
		binds = append(binds, fmt.Sprintf("/tmp/errand-%d", os.Getuid()))
	}
	cmd, err := layer.Enter(stack[at], ctx, binds, []string{kit.SelfPath(), "place", "--dir", dir, "--inside", "--parent", a.Dir})
	if err == nil {
		cmd, err = layer.Wrap(stack[:at], cmd, ctx)
	}
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, err
	}
	logPath := filepath.Join(dir, "log")
	log, _ := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	path, err := kit.LookIn(cmd.Argv[0], environ, root)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %v", cmd.Argv[0], err)
	}
	proc := &exec.Cmd{Path: path, Args: cmd.Argv, Env: environ, Dir: root, Stdout: log, Stderr: log,
		SysProcAttr: system.Apart()}
	for k, v := range cmd.Env {
		proc.Env = append(proc.Env, k+"="+v)
	}
	if err := proc.Start(); err != nil {
		return "", nil, err
	}
	if log != nil {
		log.Close()
	}
	gone := make(chan struct{})
	go func() { _ = proc.Wait(); close(gone) }()
	for until := time.Now().Add(60 * time.Second); ; {
		if conn, err := kit.DialDir(dir, time.Second); err == nil {
			conn.Close()
			return dir, stack[at+1:], nil
		}
		select {
		case <-gone:
			return "", nil, fmt.Errorf("%s could not be entered: %s", layer.Describe(stack[at:at+1]), strings.TrimPrefix(runinfo.LastLines(logPath, 6), "errand place: "))
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(until) {
			_ = system.SignalGroup(proc.Process.Pid, syscall.SIGKILL)
			return "", nil, fmt.Errorf("%s was entered, and errand's agent does not answer from inside ( its words: %s )", layer.Describe(stack[at:at+1]), logPath)
		}
	}
}

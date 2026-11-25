// The command line ( ADR 0035 ): small. It finds this machine's agent -- started when it is not there --, hands it
// what was typed, passes on what it is given to read and the signals it gets, and draws what the agent says
// ( draw/render.go ). Its exit code is the command's.
//
// Typing `errand run` does not start what does the work: the agent is one process per machine, which holds its jobs
// and its locks, and is there already for the command that follows.
//
// One thing is done here and not by the agent: **reading the project** ( `python -m errand.declared`, ADR 0031 ), in
// the shell the command is typed in, whose Python is the one that has what the project's files import.

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/errand-run/errand/internal/draw"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/system"
)

// isTerminal: whether `f` is a terminal -- one that answers for its settings; /dev/null is a character device too
// detachKey: Ctrl-], typed to a run that reads the keys, lets go of it: the command ends, the run goes on
const detachKey = 0x1d

func cliMain(verb string, args []string) int {
	cwd, _ := os.Getwd()
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real // ( a directory reached through a link has two names: a project's root is read by this one )
	}
	ttyOut, ttyIn := draw.IsTerminal(os.Stdout), draw.IsTerminal(os.Stdin)
	r := draw.NewRenderer(os.Stdout, os.Stderr)
	cols, rows := 0, 0
	if ttyOut {
		cols, rows, _ = draw.TermSize(os.Stdout)
		if term := os.Getenv("TERM"); term != "" && term != "dumb" && cols > 0 { // what is transient, when it can show it
			r.OnTerminal(func() int { c, _, _ := draw.TermSize(os.Stdout); return c }, os.Getenv("NO_COLOR") == "")
		}
	}
	restore := func() {}
	defer func() { restore(); r.Close() }() // whatever happens, the terminal is as it was found
	if newer := reach.NewerHere(); newer != "" {
		fmt.Fprintf(os.Stderr, "errand: a newer errand ( release %s ) is in use on this machine, and this one is release %s: update it\n", newer, reach.Release)
		return 1
	}
	var c *reach.Call
	until := time.Now().Add(time.Minute) // ( one that is told to go stops what it holds first )
	for try := 0; ; try++ {              // an agent may be leaving as it is reached ( retired, at its keep ): asked again, another starts
		conn, err := reach.ReachAgent()
		gone := false
		if err == nil {
			if err = reach.AwaitHello(conn); err != nil {
				conn.Close()
				gone = errors.Is(err, io.EOF)
			}
		}
		if err == nil {
			l := reach.NewLink(conn, conn, "this machine's agent")
			c = l.Open(map[string]any{"op": "command", "verb": verb, "args": args, "cwd": cwd, "environ": reach.WithTyped(os.Environ()),
				"tty_out": ttyOut, "tty_in": ttyIn, "cols": cols, "rows": rows})
			if _, err = c.Reply(); err == nil {
				break
			}
			l.Close()
		}
		if (try >= 3 && !gone) || time.Now().After(until) {
			fmt.Fprintln(os.Stderr, "errand:", err)
			return 1
		}
		time.Sleep(100 * time.Millisecond)
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		for sig := range sigs {
			n, _ := sig.(syscall.Signal)
			_ = c.SendJSON(kit.SIGNAL, map[string]any{"sig": int(n)})
		}
	}()
	if ttyOut { // the window changes: the run's terminal with it, and what is transient
		winch := make(chan os.Signal, 4)
		signal.Notify(winch, system.Resized)
		go func() {
			for range winch {
				if cols, rows, ok := draw.TermSize(os.Stdout); ok {
					_ = c.SendJSON(kit.RESIZE, map[string]any{"cols": cols, "rows": rows})
				}
				r.RefreshNow()
			}
		}()
	}
	raw := false                // every key is the run's: a shell
	forward := func(eof bool) { // what is typed, or what this command was given to read, sent as it comes
		buf := make([]byte, 65536)
		for {
			n, err := os.Stdin.Read(buf)
			if i := bytes.IndexByte(buf[:n], detachKey); !eof && i >= 0 && !raw { // typed: Ctrl-] lets go of the run, which goes on
				if i > 0 {
					_ = c.Send(kit.IN, append([]byte{}, buf[:i]...))
				}
				_ = c.SendJSON(kit.SIGNAL, map[string]any{"detach": true})
				return
			}
			if n > 0 {
				_ = c.Send(kit.IN, append([]byte{}, buf[:n]...))
			}
			if err != nil {
				if eof {
					_ = c.Send(kit.EOF, nil)
				}
				return
			}
		}
	}
	if !ttyIn { // what this command was given to read, when nobody types it
		go forward(true)
	}
	typing, forwarding := false, false
	for {
		kind, payload, err := c.Recv()
		if err != nil {
			r.Frame(kit.EXIT, []byte("{}"))
			fmt.Fprintln(os.Stderr, "errand: this machine's agent went away, and what it ran with it: `errand status`")
			return 1
		}
		if kind == kit.NOTE {
			var asked struct {
				Declare *string `json:"declare"`
				Ask     *string `json:"ask"`
				Keys    *bool   `json:"keys"`
				Raw     bool    `json:"raw"`
				Reader  bool    `json:"reader"`
			}
			known := json.Unmarshal(payload, &asked) == nil
			if known && asked.Keys != nil { // a run reads what is typed, or does no more
				if *asked.Keys && ttyIn && !typing {
					if back, err := draw.RawKeys(os.Stdin, asked.Raw); err == nil {
						restore, typing, raw = back, true, asked.Raw
						if !forwarding {
							forwarding = true
							go forward(false)
						}
					}
				} else if !*asked.Keys && typing {
					restore()
					restore, typing = func() {}, false
				}
			}
			if known && asked.Reader { // what relays a stream is over once nobody reads it, whether or not there is something to write
				go func() {
					if system.AwaitNoReader(os.Stdout) {
						os.Exit(1) // ( the agent sees this side go )
					}
				}()
				continue
			}
			if known && asked.Ask != nil { // a line, asked of who is at the terminal
				fmt.Print(*asked.Ask)
				line, err := bufio.NewReader(os.Stdin).ReadString('\n')
				if err != nil && line == "" {
					_ = c.SendJSON(kit.REPLY, map[string]any{"error": "nothing was said"})
				} else {
					_ = c.SendJSON(kit.REPLY, map[string]any{"answer": strings.TrimSpace(line)})
				}
				continue
			}
			if known && asked.Declare != nil { // the project, read here
				if doc, err := project.Load(*asked.Declare, true); err != nil {
					_ = c.SendJSON(kit.REPLY, map[string]any{"error": err.Error()})
				} else {
					_ = c.SendJSON(kit.REPLY, map[string]any{"declared": doc})
				}
				continue
			}
		}
		if code, done := r.Frame(kind, payload); done {
			return code
		}
	}
}

// carried: the verbs an agent carries out for a command line. The others are tools of a place, run by the
// program that is typed.
var carried = map[string]bool{"run": true, "status": true, "attach": true, "stop": true, "setup": true, "envs": true, "watch": true,
	"list": true, "shell": true, "init": true, "forget": true, "machines": true, "agents": true, "caches": true, "check": true, "build-dir": true, "serve": true, "vm": true}

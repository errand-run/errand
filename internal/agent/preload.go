// Warm starts ( the guide's "Preload" ): a run started by a Python of an environment that ends with a `Preload` layer is
// a fork of a process that has already imported, kept by the agent of the place.
//
// That process is errand's Python in the environment ( `python -m errand.preloaded`, started through the layers as any
// run is ). The run's own process is this program ( `errand forked` ): it hands its standard streams, its directory and
// its surroundings over, passes signals on, and ends as the fork ends -- so that the job, its terminal, its record and
// how it is stopped are those of any run. Where that cannot work -- a shell line for a layer, several ranks,
// `ERRAND_WARM=0`, a process that will not start -- the run starts cold, as without the layer.
//
// What is kept warm is memory: it is held in the machine's queue as such, by the agent, and given up as soon as
// somebody needs the room. The process goes with the agent, and after being unused as long.

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/system"
	"github.com/errand-run/errand/lib"
)

func preloadOf(stack []project.Layer) project.Layer {
	for _, l := range stack {
		if l.Type() == "Preload" {
			return l
		}
	}
	return nil
}

// isPython: whether what is run is a Python the place, or its environment, names
func isPython(argv []string) bool {
	return len(argv) > 0 && strings.HasPrefix(filepath.Base(argv[0]), "python")
}

var warming sync.Mutex

// warmed: a run's words, as a fork of the process kept warm for its environment; the words given when it cannot be.
// `first` is where the interpreter is among them.
func (j *job) warmed(argv []string, first int, env []string) []string {
	pre := preloadOf(j.spec.Stack)
	off, _ := kit.EnvOf(env, "ERRAND_WARM")
	if pre == nil || j.spec.Ranks > 1 || off == "0" || first < 0 || first+1 >= len(argv) || len(j.argv) == 0 {
		return argv
	}
	run := argv[first+1:]
	if strings.HasPrefix(run[0], "-") && (len(run) < 2 || run[0] != "-m" && run[0] != "-c") {
		return argv // an option of the interpreter's own: it is started as asked
	}
	learn := pre["learn"] != false
	modules := pre.Strings("modules")
	carried, err := lib.Dir()
	if err != nil {
		return argv
	}
	said, _ := json.Marshal([]any{argv[:first+1], j.rec.Project, j.rec.Tags, modules, learn, carried, j.spec.Env})
	sum := sha256.Sum256(said)
	dir := filepath.Join(j.a.Dir, "warm", hex.EncodeToString(sum[:])[:12])
	warming.Lock()
	defer warming.Unlock()
	if !warmAnswers(dir) {
		if err := j.a.warm(dir, append(append([]string{}, argv[:first+1]...), "-m", "errand.preloaded", dir), learn, modules, env, j.cwd); err != nil {
			return argv
		}
	}
	plan, _ := json.Marshal(map[string]any{"dir": dir, "run": run, "cold": argv})
	self, _ := os.Executable()
	return []string{self, "forked", string(plan)}
}

func warmAnswers(dir string) bool {
	addr, err := kit.SockAddress(filepath.Join(dir, "sock"))
	if err != nil {
		return false
	}
	st, err := os.Stat(addr)
	return err == nil && st.Mode()&os.ModeSocket != 0
}

// warm: the process kept warm for an environment, started through its layers, and its memory held while it is there
func (a *Place) warm(dir string, argv []string, learn bool, modules, env []string, cwd string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	base := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			base[k] = v
		}
	}
	addr, err := kit.SockAddress(filepath.Join(dir, "sock"))
	if err != nil {
		return err
	}
	body, _ := json.Marshal(base)
	if err := os.WriteFile(filepath.Join(dir, "base.json"), body, 0o600); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(dir, "rss"))
	keep := a.keep
	how := "0"
	if learn {
		how = "1"
	}
	argv = append(append(argv, strconv.FormatFloat(keep.Seconds(), 'f', 1, 64), how), modules...)
	path, err := kit.LookIn(argv[0], env, cwd)
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(dir, "log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := &exec.Cmd{Path: path, Args: argv, Dir: cwd, Stdout: log, Stderr: log, SysProcAttr: system.Apart(),
		Env: append(append([]string{}, env...), "ERRAND_AGENT_PID="+strconv.Itoa(os.Getpid()), "ERRAND_WARM_SOCKET="+addr)}
	if err := cmd.Start(); err != nil {
		return err
	}
	gone := make(chan struct{})
	go func() { _ = cmd.Wait(); close(gone) }()
	go func() { // its memory, held as what is kept warm; asked for the room, it goes
		var held held
		var said func() bool
		size := 0.0
		defer func() {
			if held != nil {
				held.Release()
			}
		}()
		for {
			select {
			case <-gone:
				return
			case <-time.After(time.Second):
			}
			data, err := os.ReadFile(filepath.Join(dir, "rss"))
			if err != nil || a.parent != "" { // ( in a container, what it holds is not counted )
				continue
			}
			rss, _ := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
			if held == nil || rss > size*1.1+50 {
				if held != nil {
					held.Release()
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				g, err := queue.Open().Acquire(ctx, queue.Request{Needs: queue.Needs{RAM: rss}, Label: "kept warm: " + filepath.Base(dir)})
				cancel()
				if err != nil { // no room to keep anything warm
					_ = system.Signal(cmd.Process.Pid, syscall.SIGTERM)
					return
				}
				g.SetPreemptible(true)
				held, said, size = g, g.Evicted, rss
			}
			if said != nil && said() {
				_ = system.Signal(cmd.Process.Pid, syscall.SIGTERM)
				return
			}
		}
	}()
	for until := time.Now().Add(20 * time.Second); time.Now().Before(until); time.Sleep(30 * time.Millisecond) {
		if warmAnswers(dir) {
			return nil
		}
		select {
		case <-gone:
			return fmt.Errorf("it ended at once")
		default:
		}
	}
	return fmt.Errorf("it does not answer")
}

// ForkedMain: a run that is a fork of the process kept warm -> its exit code
func ForkedMain(args []string) int {
	var plan struct {
		Dir  string   `json:"dir"`
		Run  []string `json:"run"`
		Cold []string `json:"cold"`
	}
	if len(args) != 1 || json.Unmarshal([]byte(args[0]), &plan) != nil || len(plan.Cold) == 0 {
		fmt.Fprintln(os.Stderr, "errand forked: not a run")
		return 2
	}
	cold := func() int {
		cwd, _ := os.Getwd()
		path, err := kit.LookIn(plan.Cold[0], os.Environ(), cwd)
		if err == nil {
			err = system.Exec(path, plan.Cold, os.Environ())
		}
		fmt.Fprintln(os.Stderr, "errand:", err)
		return 126
	}
	addr, err := kit.SockAddress(filepath.Join(plan.Dir, "sock"))
	if err != nil {
		return cold()
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: addr, Net: "unix"})
	if err != nil {
		return cold()
	}
	defer conn.Close()
	cwd, _ := os.Getwd()
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	asked, _ := json.Marshal(map[string]any{"request": map[string]any{"argv": plan.Run}, "cwd": cwd, "env": env})
	asked = append(asked, '\n')
	n, _, err := conn.WriteMsgUnix(asked[:1], system.Rights(0, 1, 2), nil)
	if err == nil && n == 1 {
		_, err = conn.Write(asked[1:])
	}
	if err != nil {
		return cold()
	}
	dec := json.NewDecoder(io.Reader(conn))
	var first struct {
		Pid int `json:"pid"`
	}
	if dec.Decode(&first) != nil || first.Pid == 0 {
		return cold()
	}
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, system.Resized)
	go func() {
		for s := range sigs {
			_ = system.SignalGroup(first.Pid, s.(syscall.Signal))
		}
	}()
	var last struct {
		Code *int `json:"code"`
	}
	if dec.Decode(&last) != nil || last.Code == nil { // what forked it went: so does the fork
		_ = system.SignalGroup(first.Pid, syscall.SIGKILL)
		return 1
	}
	if c := *last.Code; c < 0 {
		signal.Reset()
		_ = system.Signal(os.Getpid(), syscall.Signal(-c))
		time.Sleep(time.Second)
		return 128 - c
	}
	return *last.Code
}

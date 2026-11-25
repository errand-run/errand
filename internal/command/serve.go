// `errand serve`: the services of a machine, from the command line.

package command

import (
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/agent"
	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/svc"
)

type serveOptions struct {
	spec                       svc.ServiceSpec
	name                       string
	ensure, follow, url, keep  bool
	off, yes, versions, dryRun bool
	urlPort, host, env, root   string
	forKey, into, index        string
	sources, paths             []string
	unused                     *float64
	port                       []string
	holdEnd                    time.Duration // connect: the end of its input, passed on once the server was silent that long after its answer
	words, command             []string
}

func parseServe(args []string) (serveOptions, error) {
	o := serveOptions{spec: svc.ServiceSpec{Env: map[string]string{}, Needs: map[string]any{}, Idle: 600, Grace: 10}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, glued := strings.Cut(a, "=")
		take := func() (string, error) {
			if glued {
				return value, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s: a value is missing", a)
			}
			i++
			return args[i], nil
		}
		var v string
		var err error
		switch {
		case a == "--":
			o.command = args[i+1:]
			return o, nil
		case name == "--gpus" || name == "--gpu-ram" || name == "--ram" || name == "--cpus":
			if v, err = take(); err == nil {
				if _, _, _, err = kit.NeedsFrom([]string{name, v}); err == nil {
					o.spec.Needs[strings.ReplaceAll(name[2:], "-", "_")] = v
				}
			}
		case a == "-x" || a == "--exclusive":
			o.spec.Exclusive = true
		case name == "-n" || name == "--label":
			o.spec.Label, err = take()
		case name == "--name":
			o.name, err = take()
		case name == "--idle" || name == "--grace" || name == "--hold-end":
			if v, err = take(); err == nil {
				var s float64
				if s, err = svc.Duration(v); err == nil {
					switch name {
					case "--idle":
						o.spec.Idle = s
					case "--grace":
						o.spec.Grace = s
					default:
						o.holdEnd = time.Duration(s * float64(time.Second))
					}
				}
			}
		case name == "--busy-if":
			if v, err = take(); err == nil {
				o.spec.Busy = v
			}
		case name == "--port":
			if v, err = take(); err == nil {
				o.port = append(o.port, v)
			}
		case a == "--others":
			o.spec.Others = true
		case a == "--ensure":
			o.ensure = true
		case a == "-f" || a == "--foreground":
			o.follow = true
		case a == "--yes":
			o.yes = true
		case a == "--keep":
			o.keep = true
		case a == "--off":
			o.off = true
		case a == "--versions":
			o.versions = true
		case a == "--dry-run":
			o.dryRun = true
		case name == "--unused":
			if v, err = take(); err == nil {
				d := 0.0
				if _, err = fmt.Sscan(v, &d); err == nil {
					o.unused = &d
				}
			}
		case name == "--url":
			o.url = true
			o.urlPort = value
			if !glued && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && len(o.words) > 0 {
				i++
				o.urlPort = args[i]
			}
		case name == "--host" || name == "--on":
			o.host, err = take()
		case name == "--env":
			o.env, err = take()
		case name == "--root":
			o.root, err = take()
		case name == "--for":
			o.forKey, err = take()
		case name == "--into":
			o.into, err = take()
		case name == "--index":
			o.index, err = take()
		case name == "--source":
			if v, err = take(); err == nil {
				o.sources = append(o.sources, v)
			}
		case name == "--path":
			if v, err = take(); err == nil {
				o.paths = append(o.paths, v)
			}
		case a == "-v" || a == "--verbose":
		case strings.HasPrefix(a, "-"):
			err = fmt.Errorf("%s: not an option of `errand serve`", a)
		default:
			o.words = append(o.words, a)
		}
		if err != nil {
			return o, err
		}
	}
	return o, nil
}

// machineFor: the machine a word names -- one of the ledger, an environment's, an ssh host; nil: this one
func machineFor(t *reach.Term, host, env, root string) (*reach.HostSpec, error) {
	if env != "" {
		doc, err := declared(t, root)
		if err != nil {
			return nil, err
		}
		_, e, err := doc.Env(env)
		if err != nil {
			return nil, err
		}
		if len(e.Stack) > 0 && e.Stack[0].Type() == "Ssh" {
			h := reach.HostOf(e.Stack[0])
			return &h, nil
		}
		return nil, nil
	}
	if host == "" || host == "here" {
		return nil, nil
	}
	for _, m := range reach.Ledger(reach.HereName()) {
		if m.Named(host) {
			return m.Spec(), nil
		}
	}
	if doc, err := declared(t, root); err == nil {
		if e, ok := doc.Envs[host]; ok && len(e.Stack) > 0 && e.Stack[0].Type() == "Ssh" {
			h := reach.HostOf(e.Stack[0])
			return &h, nil
		}
	}
	return &reach.HostSpec{Host: host}, nil
}

func statusLine(st map[string]any) string {
	where := ""
	ports := caches.ListOf(st["ports"])
	for _, x := range ports {
		p, _ := x.(map[string]any)
		one := fmt.Sprintf("127.0.0.1:%.0f", kit.Num(p["port"], 0))
		if len(ports) > 1 {
			one = kit.Text(p["name"]) + "=" + one
		}
		where = strings.TrimSpace(where + " " + one)
	}
	line := fmt.Sprintf("  %-20s %-10s %s %s", kit.Text(st["name"]), kit.Text(st["state"]), where, kit.Text(st["detail"]))
	if n := kit.Num(st["waiting"], 0); n > 0 {
		line += fmt.Sprintf("  %.0f connection%s waiting", n, reach.Plural(int(n)))
	}
	return strings.TrimRight(line, " ")
}

func serveMain(t *reach.Term, args []string) int {
	o, err := parseServe(args)
	if err != nil {
		t.Sayln("errand serve:", err)
		return 2
	}
	if len(o.words) > 1 && len(o.command) > 0 && o.words[0] == "install" || len(o.words) == 1 && len(o.command) > 0 && o.words[0] == "probe" {
		return pageServicesMain(t, o.words[0], o, o.words[1:])
	}
	if o.holdEnd > 0 && (len(o.words) == 0 || o.words[0] != "connect" || len(o.command) > 0) {
		t.Sayln("errand serve: --hold-end is for `errand serve connect`: each connection says it for itself, the server is not concerned")
		return 2
	}
	if len(o.words) > 0 && len(o.command) == 0 {
		switch verb, rest := o.words[0], o.words[1:]; verb {
		case "list":
			if o.versions {
				return versionsMain(t, o)
			}
			return serveList(t, o)
		case "stop":
			return serveStop(t, o, rest)
		case "connect":
			return serveConnect(t, o, rest)
		case "log":
			return serveLog(t, o, rest)
		case "forward":
			return serveForward(t, o, rest)
		case "look", "fetch", "install", "probe":
			return pageServicesMain(t, verb, o, rest)
		case "pin", "unpin", "prune":
			return versionsMain(t, o)
		}
	}
	if len(o.words) == 0 && len(o.command) == 0 {
		return serveList(t, o)
	}
	if len(o.words) > 1 {
		t.Sayf("errand serve: %s: one service at a time; a command goes after `--`", strings.Join(o.words, " "))
		return 2
	}
	req := map[string]any{"op": "service", "cwd": t.Cwd, "environ": t.Environ, "stay": o.follow}
	if len(o.command) > 0 {
		spec := o.spec
		spec.Command = o.command
		for _, p := range o.port {
			one, err := svc.ParsePort(p)
			if err != nil {
				t.Sayln("errand serve:", err)
				return 2
			}
			spec.Ports = append(spec.Ports, one)
		}
		if len(spec.Ports) == 0 {
			spec.Ports = []svc.ServicePort{{Name: "port"}}
		}
		spec.Name = o.name
		if spec.Name == "" && len(o.words) == 1 {
			spec.Name = o.words[0]
		}
		if spec.Name == "" {
			word := spec.Label
			if word == "" {
				for _, w := range o.command { // ( `env A=b ollama serve` is ollama )
					if filepath.Base(w) != "env" && !strings.Contains(w, "=") {
						word = filepath.Base(w)
						break
					}
				}
			}
			spec.Name = strings.Trim(regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(word, "-"), "-.")
		}
		if err := spec.Check(); err != nil {
			t.Sayln("errand serve:", err)
			return 2
		}
		req["spec"], req["name"] = spec, spec.Name
	} else {
		req["name"] = o.words[0]
	}
	h, err := machineFor(t, o.host, o.env, o.root)
	if err != nil {
		t.Sayln("errand serve:", err)
		return 1
	}
	if h != nil {
		req["cwd"], req["environ"] = "", nil
	}
	l, err := reach.LinkTo(t, h)
	if err != nil {
		t.Sayln("errand serve:", err)
		return 1
	}
	st, code := ensureService(t, l, req, o)
	if code != 0 || o.ensure {
		return code
	}
	ports := caches.ListOf(st["ports"])
	if o.url {
		for i, x := range ports {
			p, _ := x.(map[string]any)
			if (o.urlPort == "" && i == 0) || kit.Text(p["name"]) == o.urlPort {
				t.Printf("http://127.0.0.1:%.0f\n", kit.Num(p["port"], 0))
				return 0
			}
		}
		t.Sayf("errand serve: %s: no port %q", kit.Text(st["name"]), o.urlPort)
		return 1
	}
	where := ""
	for _, x := range ports {
		p, _ := x.(map[string]any)
		one := fmt.Sprintf("http://127.0.0.1:%.0f", kit.Num(p["port"], 0))
		if len(ports) > 1 {
			one = kit.Text(p["name"]) + " " + one
		}
		where += "  " + one
	}
	t.Printf("%s%s  ( %s )\n", kit.Text(st["name"]), where, kit.Text(st["state"]))
	return 0
}

// ensureService: a service asked for, and said while it comes -> how it is, once it answers. `-f`: stays until it stops,
// and Ctrl-C stops it.
func ensureService(t *reach.Term, l *reach.Link, req map[string]any, o serveOptions) (map[string]any, int) {
	c := l.Open(req)
	defer c.Close()
	got, err := c.Reply()
	if err != nil {
		t.Sayln("errand serve:", strings.TrimPrefix(err.Error(), "this machine's agent: "))
		return nil, 1
	}
	st, _ := got["status"].(map[string]any)
	msgs := make(chan reach.LinkMsg, 16)
	go func() {
		defer close(msgs)
		for {
			kind, payload, err := c.Recv()
			if err != nil {
				return
			}
			msgs <- reach.LinkMsg{Kind: kind, Payload: payload}
			if kind == kit.EXIT {
				return
			}
		}
	}()
	last := ""
	for {
		select {
		case <-t.Sigs:
			if o.follow {
				_, _ = l.Ask(map[string]any{"op": "service_stop", "name": req["name"]})
			}
			return st, 130
		case <-t.Gone:
			return st, 1
		case m, ok := <-msgs:
			if !ok {
				t.Sayln("errand serve: the connection was lost")
				return st, 1
			}
			var said map[string]any
			_ = json.Unmarshal(m.Payload, &said)
			switch m.Kind {
			case kit.NOTE:
				if s, ok := said["status"].(map[string]any); ok {
					st = s
					line := kit.Text(st["state"]) + " " + kit.Text(st["detail"])
					if line != last && !o.url && !o.ensure && (o.follow || kit.Text(st["state"]) == "waiting") {
						t.Say(statusLine(st))
					}
					last = line
				}
			case kit.EXIT:
				if kit.Num(said["code"], 1) != 0 {
					if o.follow && kit.Text(st["state"]) == "stopped" {
						return st, 0
					}
					t.Sayf("errand serve: %s: %s  ( its words: %s )", kit.Text(st["name"]), kit.Text(st["detail"]), kit.Text(st["log"]))
					return st, 1
				}
				return st, 0
			}
		}
	}
}

func serveList(t *reach.Term, o serveOptions) int {
	h, err := machineFor(t, o.host, o.env, o.root)
	if err != nil {
		t.Sayln("errand serve:", err)
		return 1
	}
	l, err := reach.LinkTo(t, h)
	if err != nil {
		t.Sayln("errand serve:", err)
		return 1
	}
	req := map[string]any{"op": "service_list", "cwd": t.Cwd, "environ": t.Environ}
	if h != nil {
		req["cwd"], req["environ"] = "", nil
	}
	got, err := l.Ask(req)
	if err != nil {
		t.Sayln("errand serve:", err)
		return 1
	}
	for _, s := range kit.Strs(got["said"]) {
		t.Say("  " + s)
	}
	all := caches.ListOf(got["services"])
	if len(all) == 0 {
		t.Println("  no service is declared here ( ~/.config/errand/services.py ), none runs")
	}
	for _, x := range all {
		st, _ := x.(map[string]any)
		t.Println(statusLine(st))
	}
	return 0
}

func serveStop(t *reach.Term, o serveOptions, names []string) int {
	if len(names) == 0 {
		t.Sayln("errand serve stop NAME")
		return 2
	}
	h, err := machineFor(t, o.host, o.env, o.root)
	if err != nil {
		t.Sayln("errand serve:", err)
		return 1
	}
	l, err := reach.LinkTo(t, h)
	if err != nil {
		t.Sayln("errand serve:", err)
		return 1
	}
	code := 0
	for _, n := range names {
		got, err := l.Ask(map[string]any{"op": "service_stop", "name": n})
		switch {
		case err != nil:
			t.Sayln("errand serve:", err)
			code = 1
		case got["stopped"] == true:
			t.Printf("  %s  stopped\n", n)
		default:
			t.Printf("  %s  it does not run\n", n)
		}
	}
	return code
}

func serveLog(t *reach.Term, o serveOptions, names []string) int {
	if len(names) != 1 {
		t.Sayln("errand serve log NAME")
		return 2
	}
	t.Println(filepath.Join(kit.LocalCache(), "serving", names[0], "keeper.log"))
	return 0
}

// exchange is what was said last on a connection: whether the server owes an answer, and since when it is silent.
type exchange struct {
	mu   sync.Mutex
	owed bool
	last time.Time
}

func (e *exchange) asked() {
	e.mu.Lock()
	e.owed = true
	e.mu.Unlock()
}

func (e *exchange) answered() {
	e.mu.Lock()
	e.owed, e.last = false, time.Now()
	e.mu.Unlock()
}

// over: waits until the server, which may think as long as it wants, has answered and been silent for `quiet`; false when
// the connection ended before
func (e *exchange) over(quiet time.Duration, ended <-chan struct{}) bool {
	for {
		e.mu.Lock()
		owed, left := e.owed, quiet-time.Since(e.last)
		e.mu.Unlock()
		switch {
		case owed:
			left = 100 * time.Millisecond
		case left <= 0:
			return true
		}
		select {
		case <-time.After(min(left, 100*time.Millisecond)):
		case <-ended:
			return false
		}
	}
}

// serveConnect: this command's standard streams, relayed to a service
func serveConnect(t *reach.Term, o serveOptions, words []string) int {
	if len(words) == 0 || len(words) > 2 {
		t.Sayln("errand serve connect NAME [PORT]")
		return 2
	}
	h, err := machineFor(t, o.host, o.env, o.root)
	if err != nil {
		t.Sayln("errand serve:", err)
		return 1
	}
	l, err := reach.LinkTo(t, h)
	if err != nil {
		t.Sayln("errand serve:", err)
		return 1
	}
	req := map[string]any{"op": "service_connect", "name": words[0], "cwd": t.Cwd, "environ": t.Environ}
	if len(words) == 2 {
		req["port"] = words[1]
	}
	c := l.Open(req)
	defer c.Close()
	if _, err := c.Reply(); err != nil {
		t.Sayln("errand serve:", err)
		return 1
	}
	t.Event(map[string]any{"reader": true}) // nothing may be written for a long time: that nobody reads any more is looked at
	over := make(chan struct{})
	defer close(over)
	answers := &exchange{}
	go func() {
		for {
			select {
			case data, more := <-t.Stdin:
				if !more { // it sends no more; what the service answers is still relayed, until the service closes
					if o.holdEnd > 0 && !answers.over(o.holdEnd, over) {
						return
					}
					_ = c.Send(kit.EOF, nil)
					return
				}
				answers.asked()
				_ = c.Send(kit.IN, data)
			case <-over:
				return
			}
		}
	}()
	got := make(chan reach.LinkMsg)
	go func() {
		defer close(got)
		for {
			kind, payload, err := c.Recv()
			if err != nil {
				return
			}
			select {
			case got <- reach.LinkMsg{Kind: kind, Payload: payload}:
			case <-over:
				return
			}
			if kind == kit.EXIT {
				return
			}
		}
	}()
	for {
		select {
		case m, more := <-got:
			if !more {
				return 1
			}
			switch m.Kind {
			case kit.OUT:
				answers.answered()
				t.Output(kit.OUT, m.Payload)
			case kit.EXIT:
				var end struct{ Code int }
				_ = json.Unmarshal(m.Payload, &end)
				return end.Code
			}
		case <-t.Gone: // whoever it relayed for went: so does its connection, which counts as a use
			return 1
		case <-t.Sigs:
			return 130
		}
	}
}

// forwardTo: every connection to a listening socket, relayed to a service where `reach` leads -- one channel per
// connection, through the link errand already holds. The first starts the service there.
func forwardTo(lis net.Listener, reach func() (*reach.Link, error), name, port string, said func(string)) (conns func() int) {
	var mu sync.Mutex
	open := 0
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			if !agent.OwnConnection(conn) {
				conn.Close()
				continue
			}
			go func() {
				defer conn.Close()
				mu.Lock()
				open++
				mu.Unlock()
				defer func() {
					mu.Lock()
					open--
					mu.Unlock()
				}()
				l, err := reach()
				if err != nil {
					said(err.Error())
					return
				}
				c := l.Open(map[string]any{"op": "service_connect", "name": name, "port": port, "cwd": ""})
				defer c.Close()
				if _, err := c.Reply(); err != nil {
					said(err.Error())
					return
				}
				go func() {
					buf := make([]byte, 64<<10)
					for {
						n, err := conn.Read(buf)
						if n > 0 && c.Send(kit.IN, append([]byte{}, buf[:n]...)) != nil {
							return
						}
						if err != nil {
							_ = c.Send(kit.EOF, nil)
							return
						}
					}
				}()
				for {
					kind, payload, err := c.Recv()
					if err != nil || kind == kit.EXIT {
						return
					}
					if kind == kit.OUT {
						if _, err := conn.Write(payload); err != nil {
							return
						}
					}
				}
			}()
		}
	}()
	return func() int { mu.Lock(); defer mu.Unlock(); return open }
}

func serveForward(t *reach.Term, o serveOptions, words []string) int {
	if len(words) == 0 || len(words) > 2 || (o.host == "" && o.env == "") {
		t.Sayln("errand serve forward NAME [PORT] --host H | --env E [--port N] [--keep [--off]]")
		return 2
	}
	h, err := machineFor(t, o.host, o.env, o.root)
	if err != nil {
		t.Sayln("errand serve:", err)
		return 1
	}
	if h == nil {
		t.Sayln("errand serve forward: that is this machine: its services are reached on it")
		return 2
	}
	want, port := 0, ""
	if len(o.port) > 0 {
		_, _ = fmt.Sscan(o.port[0], &want)
	}
	if len(words) == 2 {
		port = words[1]
	}
	if o.keep {
		return keepForward(t, o, h, words[0], want)
	}
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", want))
	if err != nil {
		t.Sayln("errand serve forward:", err)
		return 1
	}
	defer l.Close()
	if _, err := reach.LinkTo(t, h); err != nil { // said now, rather than at the first connection
		t.Sayln("errand serve forward:", err)
		return 1
	}
	t.Printf("http://127.0.0.1:%d\n", l.Addr().(*net.TCPAddr).Port)
	forwardTo(l, func() (*reach.Link, error) { return reach.LinkTo(t, h) }, words[0], port, func(s string) { t.Sayln("errand serve forward:", s) })
	select {
	case <-t.Sigs:
	case <-t.Gone:
	}
	return 0
}

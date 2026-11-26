// `errand serve`: the services of a machine, from the command line.

package command

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/svc"
)

type serveOptions struct {
	spec                       svc.ServiceSpec
	name                       string
	ensure, follow, url        bool
	off, yes, versions, dryRun bool
	wait, json, now            bool
	urlPort, host, env, root   string
	request                    string // "-": as JSON, on the standard input
	forKey, into, index, to    string
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
						o.spec.HoldEnd = s
					}
				}
			}
		case name == "--busy-if" || name == "--ready-if" || name == "--busy-http" || name == "--ready-http":
			if v, err = take(); err == nil {
				var probe any = v
				if strings.HasSuffix(name, "-http") {
					probe, err = svc.ParseHTTPProbe(v)
				}
				if strings.HasPrefix(name, "--busy") {
					o.spec.Busy = probe
				} else {
					o.spec.Ready = probe
				}
			}
		case name == "--port":
			if v, err = take(); err == nil {
				o.port = append(o.port, v)
			}
		case a == "--others":
			o.spec.Others = true
		case name == "--listen":
			o.spec.Listen, err = take()
		case a == "--always":
			o.spec.Always = true
		case name == "--keep":
			if v, err = take(); err == nil {
				o.spec.Keep, err = svc.Duration(v)
			}
		case name == "--var":
			if v, err = take(); err == nil {
				k, value, ok := strings.Cut(v, "=")
				if !ok || k == "" {
					err = fmt.Errorf("--var NAME=VALUE ( got %q )", v)
				}
				o.spec.Env[k] = value
			}
		case name == "--cwd":
			o.spec.Cwd, err = take()
		case name == "--server-version":
			o.spec.Version, err = take()
		case name == "--version-of":
			if v, err = take(); err == nil {
				o.spec.VersionOf = append(o.spec.VersionOf, v)
			}
		case a == "--wait":
			o.wait = true
		case name == "--spec":
			o.request, err = take()
			if err == nil && o.request != "-" {
				err = fmt.Errorf("--spec -: the request is read from the standard input")
			}
		case a == "--json":
			o.json = true
		case a == "--now":
			o.now = true
		case a == "--ensure":
			o.ensure = true
		case a == "-f" || a == "--foreground":
			o.follow = true
		case a == "--yes":
			o.yes = true
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
		case name == "--to":
			o.to, err = take()
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
	if h := (reach.HostSpec{Host: host}); !h.IsHere() { // ( a name that leads back here is here )
		return &h, nil
	}
	return nil, nil
}

// onWhat: where a server is to run, as data -- a machine by its name, or an environment of the project, in full
func onWhat(t *reach.Term, o serveOptions) ([]project.Layer, error) {
	if o.env != "" {
		doc, err := declared(t, o.root)
		if err != nil {
			return nil, err
		}
		_, e, err := doc.Env(o.env)
		return e.Stack, err
	}
	h, err := machineFor(t, o.host, "", o.root)
	if err != nil || h == nil {
		return nil, err
	}
	return []project.Layer{h.Layer()}, nil
}

func statusLine(st map[string]any) string {
	where := ""
	ports := caches.ListOf(st["ports"])
	for _, x := range ports {
		p, _ := x.(map[string]any)
		if kit.Num(p["port"], 0) == 0 {
			continue
		}
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
	line = strings.TrimRight(line, " ")
	if until := kit.Num(st["until"], 0); until > 0 {
		line += "  kept " + reach.Span(until-kit.Seconds(time.Now()))
	}
	return line
}

func serveMain(t *reach.Term, args []string) int {
	o, err := parseServe(args)
	if err != nil {
		t.Sayln("errand serve:", err)
		return 2
	}
	if len(o.words) == 1 && len(o.command) > 0 && o.words[0] == "probe" {
		return pageServicesMain(t, o.words[0], o, o.words[1:])
	}
	if len(o.words) > 0 && o.words[0] == "authorize" {
		return serveAuthorize(t, o, o.words[1:])
	}
	if o.holdEnd > 0 && len(o.command) == 0 && o.request == "" && (len(o.words) == 0 || o.words[0] != "connect") {
		t.Sayln("errand serve: --hold-end is of a port ( errand serve --hold-end 2s … -- COMMAND ) or of a connection ( errand serve connect --hold-end 2s HASH )")
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
		case "release":
			return serveRelease(t, o, rest)
		case "connect":
			return serveConnect(t, o, rest)
		case "log":
			return serveLog(t, o, rest)
		case "dev", "source":
			return serveConfig(t, o, verb, rest)
		case "authorized", "withdraw":
			return serveAuthorized(t, o, verb, rest)
		case "install":
			t.Sayln("errand serve install: gone; a page's service is authorized: errand serve authorize --to pages NAME#VERSION [-- WORDS]")
			return 2
		case "look", "fetch", "probe":
			return pageServicesMain(t, verb, o, rest)
		case "pin", "unpin", "prune":
			return versionsMain(t, o)
		}
	}
	if len(o.words) == 0 && len(o.command) == 0 && o.request == "" {
		return serveList(t, o)
	}
	if len(o.words) > 1 {
		t.Sayf("errand serve: %s: one service at a time; a command goes after `--`", strings.Join(o.words, " "))
		return 2
	}
	failed := func(err error, code int) int {
		why := strings.TrimPrefix(err.Error(), "this machine's agent: ")
		if o.json {
			b, _ := json.Marshal(map[string]any{"error": why})
			t.Println(string(b))
		} else {
			t.Sayln("errand serve:", why)
		}
		return code
	}
	req := map[string]any{"op": "lease", "cwd": t.Cwd, "environ": t.Environ}
	var h *reach.HostSpec
	switch {
	case o.request != "" || len(o.command) > 0:
		spec, err := askedInFull(t, o)
		if err == nil {
			err = spec.Check()
		}
		if err != nil {
			return failed(err, 2)
		}
		if len(spec.On) > 0 && spec.On[0].Type() == "Ssh" {
			there := reach.HostOf(spec.On[0])
			h = &there
		}
		req["spec"], req["name"] = spec, spec.Name
	case o.host != "" || o.env != "":
		return failed(errors.New("a server of another machine is asked for in full: errand serve --on HOST [options] -- COMMAND"), 2)
	default:
		req["name"] = o.words[0]
	}
	l, err := reach.LinkTo(t, nil)
	if err != nil {
		return failed(err, 1)
	}
	got, err := l.Ask(req)
	if err != nil {
		return failed(err, 1)
	}
	lease, _ := got["lease"].(map[string]any)
	name := kit.Text(lease["label"])
	urls, ports, first := map[string]any{}, map[string]any{}, ""
	where := ""
	for i, x := range caches.ListOf(lease["ports"]) {
		p, _ := x.(map[string]any)
		url := fmt.Sprintf("http://%s:%.0f", kit.Text(lease["address"]), kit.Num(p["port"], 0))
		urls[kit.Text(p["name"])], ports[kit.Text(p["name"])] = url, p["port"]
		one := url
		if len(caches.ListOf(lease["ports"])) > 1 {
			one = kit.Text(p["name"]) + " " + url
		}
		where += "  " + one
		if i == 0 {
			first = url
		}
	}
	if o.follow && !o.json { // said now: it is where it will answer
		t.Printf("%s%s\n", name, where)
	}
	if o.wait || o.ensure || o.follow {
		req["op"], req["stay"] = "service", o.follow
		ask := func(req map[string]any) map[string]any { // ... the agent of the machine it runs on
			if h == nil {
				return req
			}
			return map[string]any{"op": "at", "host": h, "req": req, "environ": t.Environ}
		}
		if h != nil {
			_, there, _ := req["spec"].(svc.ServiceSpec).Where()
			req["spec"] = there
		}
		st, code := ensureService(t, l, ask, req, o)
		if o.follow {
			_, _ = l.Ask(map[string]any{"op": "lease_release", "id": lease["id"]})
		}
		if code != 0 {
			if o.json {
				b, _ := json.Marshal(map[string]any{"error": kit.Text(st["detail"])})
				t.Println(string(b))
			}
			return code
		}
		if o.ensure || o.follow {
			return 0
		}
		lease["state"] = st["state"]
	}
	switch {
	case o.json:
		delete(lease, "ports")
		lease["url"], lease["urls"], lease["ports"] = first, urls, ports
		b, _ := json.Marshal(lease)
		t.Println(string(b))
	case o.url:
		if o.urlPort != "" {
			first = kit.Text(urls[o.urlPort])
		}
		if first == "" {
			t.Sayf("errand serve: %s: no port %q", name, o.urlPort)
			return 1
		}
		t.Println(first)
	default:
		said := kit.Text(lease["state"])
		if why := kit.Text(lease["why"]); why != "" {
			said += ": " + why
		}
		t.Printf("%s%s  ( %s, kept %s )\n", name, where, said, reach.Span(kit.Num(lease["until"], 0)-kit.Seconds(time.Now())))
	}
	return 0
}

// askedInFull: what is asked for, by the options and the command, or as JSON
func askedInFull(t *reach.Term, o serveOptions) (svc.ServiceSpec, error) {
	spec := o.spec
	if o.request != "" {
		var data []byte
		for more := true; more; {
			var b []byte
			select {
			case b, more = <-t.Stdin:
				data = append(data, b...)
			case <-t.Gone:
				return spec, errors.New("whoever asked went")
			}
		}
		var err error
		if spec, o.host, err = svc.ParseRequest(data); err != nil {
			return spec, err
		}
	} else {
		spec.Command, spec.Name = o.command, o.name
		for _, p := range o.port {
			one, err := svc.ParsePort(p)
			if err != nil {
				return spec, err
			}
			spec.Ports = append(spec.Ports, one)
		}
		if spec.Name == "" && len(o.words) == 1 {
			spec.Name = o.words[0]
		}
	}
	spec.Complete()
	if spec.On == nil {
		var err error
		if spec.On, err = onWhat(t, o); err != nil {
			return spec, err
		}
	}
	return spec, nil
}

// ensureService: a service asked for, and said while it comes -> how it is, once it answers. `-f`: stays until it stops,
// and Ctrl-C stops it.
func ensureService(t *reach.Term, l *reach.Link, ask func(map[string]any) map[string]any, req map[string]any, o serveOptions) (map[string]any, int) {
	c := l.Open(ask(req))
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
				_, _ = l.Ask(ask(map[string]any{"op": "service_stop", "key": st["key"]}))
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
					if line != last && !o.url && !o.ensure && !o.json && (o.follow || kit.Text(st["state"]) == "waiting") {
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
		t.Println("  no server runs here, no port is kept for one")
	}
	named := map[string]int{}
	for _, x := range all {
		st, _ := x.(map[string]any)
		named[kit.Text(st["name"])]++
	}
	for _, x := range all {
		st, _ := x.(map[string]any)
		if hash := kit.Text(st["config"]); named[kit.Text(st["name"])] > 1 && hash != "" {
			st["name"] = kit.Text(st["name"]) + "~" + hash[:min(4, len(hash))]
		}
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
		case kit.Num(got["stopped"], 0) > 1:
			t.Printf("  %s  stopped ( %.0f servers )\n", n, kit.Num(got["stopped"], 0))
		case kit.Num(got["stopped"], 0) > 0:
			t.Printf("  %s  stopped\n", n)
		default:
			t.Printf("  %s  it does not run\n", n)
		}
	}
	return code
}

// serveRelease: leases given back -- their ports are closed, and their servers end when nobody uses them; `--now`: at once
func serveRelease(t *reach.Term, o serveOptions, names []string) int {
	if len(names) == 0 {
		t.Sayln("errand serve release NAME [--now]")
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
		got, err := l.Ask(map[string]any{"op": "lease_release", "name": n})
		if err == nil && o.now {
			_, err = l.Ask(map[string]any{"op": "service_stop", "name": n})
		}
		switch {
		case err != nil:
			t.Sayln("errand serve:", err)
			code = 1
		case kit.Num(got["released"], 0) > 0:
			t.Printf("  %s  released\n", n)
		default:
			t.Printf("  %s  it has no port\n", n)
		}
	}
	return code
}

func serveLog(t *reach.Term, o serveOptions, names []string) int {
	if len(names) != 1 {
		t.Sayln("errand serve log NAME")
		return 2
	}
	for _, d := range svc.ServingDirs() {
		if label, hash, _ := strings.Cut(names[0], "~"); label == d.Label && strings.HasPrefix(d.Key, hash) {
			t.Println(filepath.Join(d.Dir, "keeper.log"))
			return 0
		}
	}
	t.Sayln("errand serve log: no server of that name ran here:", names[0])
	return 1
}

// serveConnect: this command's standard streams, relayed to a service
func serveConnect(t *reach.Term, o serveOptions, words []string) int {
	if len(words) == 0 || len(words) > 2 || !isHash(words[0]) {
		t.Sayln(errNoHash.Error())
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
	req := map[string]any{"op": "service_connect", "hash": words[0], "cwd": t.Cwd, "environ": t.Environ}
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
	answers := &kit.Exchange{}
	go func() {
		for {
			select {
			case data, more := <-t.Stdin:
				if !more { // it sends no more; what the service answers is still relayed, until the service closes
					if o.holdEnd > 0 && !answers.Over(o.holdEnd, over) {
						return
					}
					_ = c.Send(kit.EOF, nil)
					return
				}
				answers.Asked()
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
				answers.Answered()
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

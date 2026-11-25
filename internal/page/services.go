// The services a page served as it is asks for, through its own address ( ADR 0026, 0027 ):
//
//	GET  .errand/services                    per service: version, protocol, `on` ( the machine chosen ), state, why, lines
//	GET  .errand/machines                    the machines offered, in order, each with what is known of it yet
//	GET  .errand/machines/<M>[?refresh=1]    what M has for them
//	POST .errand/services/<name>  {"on": M}  the machine chosen, kept per view
//	POST .errand/copy             {"on": M}  the view's files copied into errand's cache on M, kept up to date
//	POST .errand/machines/<M>/paths  {"tool", "path"}    a place for M's agent to look
//	ANY  .errand/svc/<name>/<p>              relayed to `name#version` on the machine chosen, started there first
//
// A page starts only what both sides name: a service its `services.json` lists, and that the machine declares under that
// exact name and version. Nothing in a page declares a command: the lines are for a person to type.

package page

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/svc"
)

const (
	lookKept     = 5 * time.Minute
	lookKeptHere = 2 * time.Second
)

type looked struct {
	at  time.Time
	got map[string]any
}

type viewCopy struct {
	State string  `json:"state"`
	Error string  `json:"error,omitempty"`
	At    float64 `json:"at,omitempty"`
	asked time.Time
}

var pageMu sync.Mutex

var looks = map[string]looked{}

var viewCopies = map[string]*viewCopy{}

func pageState(name string) string { return filepath.Join(kit.LocalCache(), "page", name) }

func readState(name string) map[string]map[string]string {
	out := map[string]map[string]string{}
	if data, err := os.ReadFile(pageState(name)); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

func keepState(name, vid, what, value string) {
	pageMu.Lock()
	defer pageMu.Unlock()
	got := readState(name)
	if got[vid] == nil {
		got[vid] = map[string]string{}
	}
	got[vid][what] = value
	b, _ := json.MarshalIndent(got, "", " ")
	_ = kit.WriteWhole(pageState(name), string(b))
}

type pageAsk struct {
	w   *webServer
	u   *Ui
	dir string
	vid string
}

func (p pageAsk) machine(name string) *UiMachine {
	for _, m := range p.u.snapshot() {
		if m.Name() == name || (m.Here() && name == "here") {
			m := m
			return p.u.real(m)
		}
	}
	return nil
}

// look: what a machine has for this view's services, kept a while
func (p pageAsk) look(name string, refresh bool) map[string]any {
	k := p.vid + "\x00" + name
	m := p.machine(name)
	pageMu.Lock()
	got, ok := looks[k]
	pageMu.Unlock()
	keep := lookKept
	if m != nil && m.Here() {
		keep = lookKeptHere
	}
	if ok && !refresh && time.Since(got.at) < keep {
		return got.got
	}
	answer := map[string]any{"unreachable": true, "error": name + ": not a machine errand knows", "services": map[string]any{}}
	if m != nil {
		req := svc.LookRequest(p.dir, p.vid, name)
		paths := map[string][]string{}
		for tool, list := range readState("paths.json")[name] {
			paths[tool] = strings.Split(list, "\n")
		}
		req["paths"] = paths
		done := make(chan struct{})
		var a map[string]any
		var err error
		go func() {
			a, err = p.u.Ask(m, map[string]any{"op": "services_do", "req": req})
			close(done)
		}()
		select {
		case <-done:
			if err == nil {
				answer = a
			} else {
				answer["error"] = kit.FirstLine(err.Error())
			}
		case <-time.After(15 * time.Second):
			answer["error"] = name + ": its agent said nothing within 15 s"
		}
	}
	pageMu.Lock()
	if store, ok := answer["store"].(map[string]any); ok && kit.Text(store["seen"]) == "none" && viewCopies[k] != nil {
		store["copying"] = viewCopies[k]
	}
	looks[k] = looked{time.Now(), answer}
	pageMu.Unlock()
	return answer
}

func (p pageAsk) entry(a map[string]any, name string) map[string]any {
	if a["unreachable"] == true {
		return map[string]any{"state": "unreachable", "why": kit.Text(a["error"]), "found": []any{}, "lines": []any{}, "store": map[string]any{}}
	}
	services, _ := a["services"].(map[string]any)
	was, _ := services[name].(map[string]any)
	e := map[string]any{"state": "unknown", "why": "", "found": []any{}, "lines": []any{}}
	for k, v := range was {
		e[k] = v
	}
	store, _ := a["store"].(map[string]any)
	if store == nil {
		store = map[string]any{}
	}
	e["store"] = store
	switch kit.Text(e["state"]) {
	case "ready", "running", "installable":
		if kit.Text(store["seen"]) == "none" {
			e["needs_copy"] = true
			e["missing"] = append([]any{"a copy of this store ( errand copies it into its cache there )"}, anyList(e["missing"])...)
		}
	}
	return e
}

// withDev: the entry as the view runs it -- from the checkout, by default when there is a usable one, or the version
func withDev(e map[string]any, from string) map[string]any {
	d, _ := e["dev"].(map[string]any)
	e["from"] = "version"
	if d == nil || d["off"] == true || d["off"] != nil && d["off"] != false {
		return e
	}
	st := kit.Text(d["state"])
	if from == "" && (st == "ready" || st == "running") {
		from = "dev"
	}
	if from == "dev" {
		e["version_state"], e["version_why"] = e["state"], e["why"]
		e["from"], e["state"], e["why"] = "dev", st, kit.Text(d["why"])
	}
	return e
}

var stateOrder = map[string]int{"running": 0, "ready": 0, "installable": 1, "unverified": 2, "wrong version": 2, "unknown": 3, "unavailable": 4, "unreachable": 5}

// on: the machine chosen for a service, else the first where it is ready, else installable, this one first
func (p pageAsk) on(name string) string {
	if m := readState("choices.json")[p.vid][name]; m != "" {
		return m
	}
	best, rank := "", 9
	for _, m := range p.u.snapshot() {
		if best == "" {
			best = m.Name()
		}
		pageMu.Lock()
		got, ok := looks[p.vid+"\x00"+m.Name()]
		pageMu.Unlock()
		if !ok {
			continue
		}
		r, known := stateOrder[kit.Text(p.entry(got.got, name)["state"])]
		if known && r < rank {
			best, rank = m.Name(), r
		}
	}
	return best
}

func (w *webServer) pageServices(rw http.ResponseWriter, r *http.Request, dir, vid string, rest []string) {
	u, err := w.model()
	if err != nil {
		sendError(rw, err)
		return
	}
	p := pageAsk{w, u, dir, vid}
	pageMu.Lock()
	for k, c := range viewCopies {
		if strings.HasPrefix(k, vid+"\x00") {
			c.asked = time.Now()
		}
	}
	pageMu.Unlock()
	get := r.Method == http.MethodGet || r.Method == http.MethodHead
	want := svc.AskedServices(dir)
	body := func() map[string]any {
		out := map[string]any{}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&out)
		return out
	}
	offered := func(name string) bool { return p.machine(name) != nil }
	for i := range rest {
		if rest[0] != "svc" || i < 2 {
			rest[i], _ = url.PathUnescape(rest[i])
		}
	}
	switch {
	case len(rest) == 1 && rest[0] == "services" && get:
		out := map[string]any{}
		for name, s := range want {
			m := p.on(name)
			e := withDev(p.entry(p.look(m, false), name), readState("from.json")[vid][name])
			e["version"], e["protocol"], e["optional"], e["text"], e["source"], e["on"] = svc.OrNil(s.Version), s.Protocol, s.Optional, s.Text, s.Source, m
			out[name] = e
		}
		sendJSON(rw, 200, out)
	case len(rest) == 1 && rest[0] == "machines" && get:
		out := []map[string]any{}
		for _, m := range u.snapshot() {
			pageMu.Lock()
			got, known := looks[vid+"\x00"+m.Name()]
			pageMu.Unlock()
			row := map[string]any{"name": m.Name(), "here": m.Here(), "known": known, "origin": false}
			if known {
				services := map[string]any{}
				for name := range want {
					e := p.entry(got.got, name)
					services[name] = map[string]any{"state": e["state"], "why": e["why"]}
				}
				row["services"] = services
			}
			out = append(out, row)
		}
		sendJSON(rw, 200, out)
	case len(rest) == 2 && rest[0] == "machines" && get:
		if !offered(rest[1]) {
			http.Error(rw, rest[1]+": not a machine offered", http.StatusNotFound)
			return
		}
		a := p.look(rest[1], r.URL.Query().Get("refresh") == "1")
		services := map[string]any{}
		for name := range want {
			services[name] = p.entry(a, name)
		}
		store := a["store"]
		if store == nil {
			store = map[string]any{}
		}
		sendJSON(rw, 200, map[string]any{"name": rest[1], "store": store, "platform": a["platform"], "cards": anyList(a["cards"]), "unreachable": a["unreachable"] == true,
			"error": a["error"], "services": services})
	case len(rest) == 2 && rest[0] == "services" && r.Method == http.MethodPost:
		b := body()
		name, on, from := rest[1], text2(b["on"]), text2(b["from"])
		switch {
		case !has(want, name):
			http.Error(rw, "this directory's services.json does not name "+name, http.StatusNotFound)
			return
		case on != "" && !offered(on):
			http.Error(rw, on+": not a machine offered", http.StatusForbidden)
			return
		case from != "" && from != "dev" && from != "version":
			http.Error(rw, `"from": "dev" or "version"`, http.StatusBadRequest)
			return
		}
		out := map[string]any{"ok": true}
		if on != "" {
			keepState("choices.json", vid, name, on)
		}
		if from != "" {
			keepState("from.json", vid, name, from)
			out["from"] = from
		}
		out["on"] = p.on(name)
		sendJSON(rw, 200, out)
	case len(rest) == 1 && rest[0] == "copy" && r.Method == http.MethodPost:
		on := text2(body()["on"])
		m := p.machine(on)
		if m == nil {
			http.Error(rw, on+": not a machine offered", http.StatusForbidden)
			return
		}
		if m.Here() {
			sendJSON(rw, 200, map[string]any{"error": "this machine serves the view: nothing to copy"})
			return
		}
		sendJSON(rw, 200, p.copy(m))
	case len(rest) == 3 && rest[0] == "machines" && rest[2] == "paths" && r.Method == http.MethodPost:
		b := body()
		tool, at := text2(b["tool"]), text2(b["path"])
		if tool == "" || at == "" || strings.Contains(at, "\n") {
			http.Error(rw, "{ tool, path }", http.StatusBadRequest)
			return
		}
		list := readState("paths.json")[rest[1]][tool]
		if list != "" {
			list += "\n"
		}
		keepState("paths.json", rest[1], tool, list+at)
		pageMu.Lock()
		delete(looks, vid+"\x00"+rest[1])
		pageMu.Unlock()
		sendJSON(rw, 200, map[string]any{"ok": true})
	case len(rest) >= 2 && rest[0] == "svc":
		if !has(want, rest[1]) {
			http.Error(rw, "this directory's services.json does not name "+rest[1], http.StatusForbidden)
			return
		}
		p.relay(rw, r, rest[1], want[rest[1]], "/"+strings.Join(rest[2:], "/"))
	default:
		http.Error(rw, "not found", http.StatusNotFound)
	}
}

func has(m map[string]svc.AskedService, k string) bool { _, ok := m[k]; return ok }

func text2(v any) string { s, _ := v.(string); return s }

// copy: the view's files in errand's cache on a machine, kept up to date while the page asks
func (p pageAsk) copy(m *UiMachine) *viewCopy {
	k := p.vid + "\x00" + m.Name()
	pageMu.Lock()
	defer pageMu.Unlock()
	if c := viewCopies[k]; c != nil && c.State == "copying" {
		return c
	}
	c := &viewCopy{State: "copying", asked: time.Now()}
	viewCopies[k] = c
	go func() {
		for {
			_, err := p.u.Ask(nil, map[string]any{"op": "copy_view", "host": m.M.Spec(), "dir": p.dir, "vid": p.vid, "machine": m.Name()})
			pageMu.Lock()
			if err != nil {
				c.State, c.Error = "failed", kit.FirstLine(err.Error())
				pageMu.Unlock()
				return
			}
			c.State, c.Error, c.At = "copied", "", kit.Seconds(time.Now())
			delete(looks, k) // ( M sees it now: looked at again )
			asked := c.asked
			pageMu.Unlock()
			time.Sleep(10 * time.Second)
			if time.Since(asked) > time.Minute {
				return
			}
		}
	}()
	return c
}

// chanConn is a connection to a service, on a channel of the agent's link.
type chanConn struct {
	c    *reach.Call
	rest []byte
	done bool
}

func (c *chanConn) Read(b []byte) (int, error) {
	for len(c.rest) == 0 {
		if c.done {
			return 0, io.EOF
		}
		kind, payload, err := c.c.Recv()
		if err != nil || kind == kit.EXIT {
			c.done = true
			return 0, io.EOF
		}
		if kind == kit.OUT {
			c.rest = payload
		}
	}
	n := copy(b, c.rest)
	c.rest = c.rest[n:]
	return n, nil
}

func (c *chanConn) Write(b []byte) (int, error) {
	for at := 0; at < len(b); {
		n := min(len(b)-at, 64<<10)
		if err := c.c.Send(kit.IN, append([]byte{}, b[at:at+n]...)); err != nil {
			return at, err
		}
		at += n
	}
	return len(b), nil
}

func (c *chanConn) Close() error { c.c.Close(); return nil }

func (c *chanConn) LocalAddr() net.Addr { return &net.TCPAddr{} }

func (c *chanConn) RemoteAddr() net.Addr { return &net.TCPAddr{} }

func (c *chanConn) SetDeadline(time.Time) error { return nil }

func (c *chanConn) SetReadDeadline(time.Time) error { return nil }

func (c *chanConn) SetWriteDeadline(time.Time) error { return nil }

// relay: a request of the page, to the service on the machine chosen, its answer back ( a WebSocket included )
func (p pageAsk) relay(rw http.ResponseWriter, r *http.Request, name string, want svc.AskedService, rest string) {
	on := p.on(name)
	m := p.machine(on)
	if m == nil {
		http.Error(rw, on+": not a machine errand knows", http.StatusServiceUnavailable)
		return
	}
	a := p.look(on, false)
	e := withDev(p.entry(a, name), readState("from.json")[p.vid][name])
	key, root := want.Key(name), p.dir
	if e["from"] == "dev" {
		got, err := p.u.Ask(m, map[string]any{"op": "services_do", "req": map[string]any{"do": "dev_ensure", "name": name,
			"want": map[string]any{"version": svc.OrNil(want.Version), "protocol": want.Protocol, "package": want.Package, "source": want.Source, "described": want.Described}}})
		if err != nil {
			http.Error(rw, kit.FirstLine(err.Error()), http.StatusServiceUnavailable)
			return
		}
		key = kit.Text(got["key"])
	}
	if !m.Here() {
		store, _ := a["store"].(map[string]any)
		if text2(store["path"]) == "" {
			http.Error(rw, on+" has no copy of this store: copy it there first", http.StatusServiceUnavailable)
			return
		}
		root = text2(store["path"])
	}
	dial := func(context.Context, string, string) (net.Conn, error) {
		l, err := p.u.link()
		if err != nil {
			return nil, err
		}
		req := map[string]any{"op": "service_connect", "name": key, "cwd": ""}
		if !m.Here() {
			req = map[string]any{"op": "at", "host": m.M.Spec(), "req": req, "environ": os.Environ()}
		}
		c := l.Open(req)
		if _, err := c.Reply(); err != nil {
			c.Close()
			return nil, err
		}
		return &chanConn{c: c}, nil
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", "127.0.0.1"
			pr.Out.URL.Path, pr.Out.URL.RawPath = rest, ""
			if un, err := url.PathUnescape(rest); err == nil {
				pr.Out.URL.Path, pr.Out.URL.RawPath = un, rest
			}
			pr.Out.Host = "127.0.0.1"
			pr.Out.Header.Set("X-Errand-Root", (&url.URL{Path: root}).EscapedPath())
		},
		Transport:     &http.Transport{DialContext: dial, DisableKeepAlives: true},
		FlushInterval: -1,
		ErrorHandler: func(rw http.ResponseWriter, r *http.Request, err error) {
			code := http.StatusServiceUnavailable
			if strings.Contains(err.Error(), "is declared on") {
				code, err = http.StatusForbidden, errors.New("this machine declares no service "+fmt.Sprintf("%q", key))
			}
			http.Error(rw, fmt.Sprintf("service %q: %v", name, err), code)
		},
	}
	proxy.ServeHTTP(rw, r)
}

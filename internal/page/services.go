// The services a page served as it is asks for, through its own address ( ADR 0026, 0041 ):
//
//	GET  .errand/machines                          the machines it may choose, by their ids: [ { id, name, here } ]
//	POST .errand/srv            { …description, on }   { url, state, key } -- or { error, why, html, authorize }
//	ANY  .errand/srv/<MACHINE>/<NAME>~<HASH>/<p>   relayed to that service on that machine, started there first
//
// The page describes its service in full; nothing runs unless a person authorized that description on that machine, in
// errand's own page ( authorize.go ): a page never says a command that runs. A refusal is not written anywhere.

package page

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/svc"
	"github.com/errand-run/errand/internal/system"
)

const rootKept = 30 * time.Second // where another machine sees a view's files

type viewCopy struct {
	State string  `json:"state"`
	Error string  `json:"error,omitempty"`
	At    float64 `json:"at,omitempty"`
	asked time.Time
}

type seenRoot struct {
	path string
	at   time.Time
}

var (
	pageMu     sync.Mutex
	viewCopies = map[string]*viewCopy{}
	roots      = map[string]seenRoot{}
)

type pageAsk struct {
	w   *webServer
	u   *Ui
	dir string
	vid string
}

// machineID: what tells a machine from any other, in a page's addresses -- never its name, which may be ambiguous
func machineID(m *UiMachine) string {
	if m.Here() {
		return svc.Short("machine " + system.MachineID())
	}
	for _, n := range m.M.Notes {
		if n.ID != "" {
			return svc.Short("machine " + n.ID)
		}
	}
	if h := m.M.Spec(); h != nil {
		return svc.Short("host " + h.Key())
	}
	return svc.Short("name " + m.Name())
}

// byID: a machine by its id; "": the one that serves the page
func (p pageAsk) byID(id string) *UiMachine {
	for _, m := range p.u.snapshot() {
		if (id == "" && m.Here()) || (id != "" && machineID(m) == id) {
			return p.u.real(m)
		}
	}
	return nil
}

func (p pageAsk) byName(name string) *UiMachine {
	for _, m := range p.u.snapshot() {
		if m.Name() == name {
			return p.u.real(m)
		}
	}
	return nil
}

func (w *webServer) pageServices(rw http.ResponseWriter, r *http.Request, dir, vid string, rest []string) {
	u, err := w.model()
	if err != nil {
		sendError(rw, err)
		return
	}
	p := pageAsk{w, u, dir, vid}
	pageMu.Lock()
	for k, c := range viewCopies { // ( a copy is kept up to date while its view asks )
		if strings.HasPrefix(k, vid+"\x00") {
			c.asked = time.Now()
		}
	}
	pageMu.Unlock()
	get := r.Method == http.MethodGet || r.Method == http.MethodHead
	switch {
	case len(rest) == 1 && rest[0] == "machines" && get:
		out := []map[string]any{}
		for _, m := range u.snapshot() {
			out = append(out, map[string]any{"id": machineID(m), "name": m.Name(), "here": m.Here()})
		}
		sendJSON(rw, 200, out)
	case len(rest) == 1 && rest[0] == "srv" && r.Method == http.MethodPost:
		p.ask(rw, r)
	case len(rest) >= 3 && rest[0] == "srv":
		p.relay(rw, r, rest[1], rest[2], "/"+strings.Join(rest[3:], "/"))
	default:
		http.Error(rw, "not found", http.StatusNotFound)
	}
}

// look: what machine m has for a description
func (p pageAsk) look(m *UiMachine, d svc.PageDesc, paths map[string][]string) (svc.PageLook, error) {
	var l svc.PageLook
	req := map[string]any{"do": "page_look", "described": d, "paths": paths}
	if !m.Here() {
		req["store"] = svc.StoreOf(p.dir, p.vid)
	}
	got, err := askWithin(p.u, m, map[string]any{"op": "services_do", "req": req}, 15*time.Second)
	if err == nil {
		err = reach.DecodeInto(got["look"], &l)
	}
	return l, err
}

func askWithin(u *Ui, m *UiMachine, req map[string]any, d time.Duration) (map[string]any, error) {
	type answer struct {
		got map[string]any
		err error
	}
	done := make(chan answer, 1)
	go func() {
		got, err := u.Ask(m, req)
		done <- answer{got, err}
	}()
	select {
	case a := <-done:
		return a.got, a.err
	case <-time.After(d):
		return nil, fmt.Errorf("%s: its agent said nothing within %s", m.Name(), d)
	}
}

// ask: a page's description -> its address on the machine it names, or why not and where to set it up
func (p pageAsk) ask(rw http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(io.LimitReader(r.Body, 1<<18))
	d, on, err := svc.ParsePageDesc(data)
	if err != nil {
		sendJSON(rw, http.StatusBadRequest, map[string]any{"error": "not a description", "why": err.Error()})
		return
	}
	m := p.byID(on)
	if m == nil {
		sendJSON(rw, http.StatusNotFound, map[string]any{"error": "no machine", "why": on + ": no such machine here ( GET .errand/machines )"})
		return
	}
	l, err := p.look(m, d, nil)
	if err != nil {
		sendJSON(rw, http.StatusServiceUnavailable, map[string]any{"error": "unreachable", "why": kit.FirstLine(err.Error())})
		return
	}
	mid := machineID(m)
	seen := m.Here() || kit.Text(l.Store["path"]) != ""
	if l.State == "authorized" && seen {
		if !m.Here() {
			pageMu.Lock()
			roots[p.vid+"\x00"+mid] = seenRoot{kit.Text(l.Store["path"]), time.Now()}
			pageMu.Unlock()
		}
		sendJSON(rw, 200, map[string]any{"url": ".errand/srv/" + mid + "/" + l.Key + "/", "state": "authorized", "key": l.Key, "on": mid})
		return
	}
	link := p.w.authorizeURL(r)
	what, why := "not authorized", fmt.Sprintf("not set up on %s yet", m.Name())
	switch {
	case l.State == "authorized":
		what, why = "no store", m.Name()+" does not see this view's files"
	case l.State == "unavailable":
		what, why = "unavailable", l.Why
	}
	described, _ := json.Marshal(d)
	form := map[string]string{"described": string(described), "on": mid, "view": p.vid}
	sendJSON(rw, http.StatusForbidden, map[string]any{"error": what, "why": why, "key": l.Key, "on": mid, "authorize": link, "form": form,
		"html": fragment(l, m.Name(), why, link, form)})
}

// fragment: what a refusal says, as HTML a page inserts as it is -- the idea of it ( the details are the window's ), no
// script; its one button opens errand's window, a form that carries the description there ( nothing is kept in between )
func fragment(l svc.PageLook, machine, why, link string, form map[string]string) string {
	e := html.EscapeString
	var b strings.Builder
	fmt.Fprintf(&b, `<div class="errand-service errand-%s"><style>.errand-service{font:13px/1.4 system-ui,sans-serif}.errand-service ul{margin:.3em 0;padding-left:1.2em}.errand-ok{color:#2a7a2a}.errand-missing{color:#a5520c}.errand-why{margin:.2em 0}.errand-setup{margin:.4em 0 0}</style>`, e(strings.ReplaceAll(l.State, " ", "-")))
	fmt.Fprintf(&b, `<p class="errand-why">%s</p>`, e(why)) // ( what it is, the page says it: its description is its own )
	if len(l.Missing) > 0 {
		b.WriteString(`<ul class="errand-todo">`)
		for _, n := range l.Missing {
			short := n.Short
			if short == "" {
				short = n.What
			}
			fmt.Fprintf(&b, `<li class="errand-missing">%s</li>`, e(short))
		}
		b.WriteString("</ul>")
	}
	fmt.Fprintf(&b, `<form class="errand-setup" method="post" action="%s" target="_blank">`, e(link))
	for _, k := range []string{"described", "on", "view"} {
		fmt.Fprintf(&b, `<input type="hidden" name="%s" value="%s">`, k, e(form[k]))
	}
	fmt.Fprintf(&b, `<button type="submit">Set it up on %s…</button></form></div>`, e(machine))
	return b.String()
}

// root: where machine m sees this view's files; "" when it does not
func (p pageAsk) root(m *UiMachine, mid string) string {
	if m.Here() {
		return p.dir
	}
	k := p.vid + "\x00" + mid
	pageMu.Lock()
	got, ok := roots[k]
	pageMu.Unlock()
	if ok && time.Since(got.at) < rootKept && got.path != "" {
		return got.path
	}
	a, err := askWithin(p.u, m, map[string]any{"op": "services_do", "req": map[string]any{"do": "store_seen", "store": svc.StoreOf(p.dir, p.vid)}}, 15*time.Second)
	path := ""
	if err == nil {
		path = kit.Text(a["path"])
	}
	pageMu.Lock()
	roots[k] = seenRoot{path, time.Now()}
	pageMu.Unlock()
	return path
}

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

// relay: a request of the page, to the service on the machine its address names, its answer back ( a WebSocket included )
func (p pageAsk) relay(rw http.ResponseWriter, r *http.Request, mid, key, rest string) {
	refuse := func(code int, what, why string) { sendJSON(rw, code, map[string]any{"error": what, "why": why}) }
	key, _ = url.PathUnescape(key)
	if _, _, ok := svc.SplitPageKey(key); !ok {
		refuse(http.StatusNotFound, "not an address", key+": NAME~HASH, as POST .errand/srv gives it")
		return
	}
	m := p.byID(mid)
	if m == nil {
		refuse(http.StatusNotFound, "no machine", mid+": no such machine here")
		return
	}
	root := p.root(m, mid)
	if root == "" {
		refuse(http.StatusConflict, "no store", m.Name()+" does not see this view's files: POST .errand/srv says where to set it up")
		return
	}
	dial := func(context.Context, string, string) (net.Conn, error) {
		l, err := p.u.link()
		if err != nil {
			return nil, err
		}
		req := map[string]any{"op": "service_connect", "page": key, "from": p.dir}
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
			if strings.Contains(err.Error(), "is not authorized to pages") {
				refuse(http.StatusForbidden, "not authorized", fmt.Sprintf("%s is not authorized to pages on %s", key, m.Name()))
				return
			}
			refuse(http.StatusServiceUnavailable, "unavailable", fmt.Sprintf("%s: %v", key, errors.Unwrap(err)))
		},
	}
	proxy.ServeHTTP(rw, r)
}

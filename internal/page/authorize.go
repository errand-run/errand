// The window that authorizes what a page asked for ( ADR 0041 ), on errand's own page -- as a card payment is: another
// origin than the view's, errand's cookie, never framed. Nothing is kept between the page and the window: the page's
// fragment POSTs the description here, the window shows it, and Authorize sends it back.
//
//	POST /authorize            a form, from a view: described, on, view      the window, for that description
//	POST /authorize/look       { described, on, view, paths }                what the machine has and lacks
//	POST /authorize/probe      { …, tool, path }                             a program whose version no file says, run once
//	POST /authorize/checkout   { …, repo, path }                             a checkout of a repository in development, there
//	POST /authorize/do         { …, paths, trust }                           fetched, copied, composed and authorized

package page

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/svc"
)

//go:embed page/authorize.html
var authorizeHTML string

// asked is what the window is for: a description, the machine it is to run on, the view that asked.
type asked struct {
	Described json.RawMessage     `json:"described"`
	On        string              `json:"on"`
	View      string              `json:"view"`
	Paths     map[string][]string `json:"paths,omitempty"`
	Trust     []string            `json:"trust,omitempty"`
	Tool      string              `json:"tool,omitempty"`
	Path      string              `json:"path,omitempty"`
	Repo      string              `json:"repo,omitempty"`
}

// fromAView: whether a request comes from a page of errand's views ( their port, on this machine's address )
func (w *webServer) fromAView(r *http.Request) bool {
	u, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		return false
	}
	port, _ := strconv.Atoi(u.Port())
	return port > 0 && (port == w.said.Static || port == w.said.PubSt)
}

// authorizeLanding: a view's form -> the window, the description in it
func (w *webServer) authorizeLanding(rw http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(rw, r.Body, 1<<18)
	page := func(code int, said string) {
		sendHTML(rw, code, "<!DOCTYPE html><meta charset=utf-8><title>errand</title><p style='font:14px sans-serif;margin:2em'>"+said)
	}
	_ = r.ParseForm()
	token := strings.TrimSpace(r.PostForm.Get("token"))
	tokenOK := token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(w.said.Token)) == 1
	origin, _ := url.Parse(r.Header.Get("Origin"))
	switch {
	case !w.fromAView(r) && !(origin != nil && origin.Host == r.Host): // ( a view's form, or the one below )
		page(http.StatusForbidden, "this window is opened from a page errand serves")
		return
	case tokenOK:
		w.setCookie(rw)
	case !w.allowed(r): // errand's page was never opened in this browser: its token, then the window, the description kept in the form
		if token != "" {
			time.Sleep(time.Second) // one guess a second
		}
		sendHTML(rw, http.StatusUnauthorized, w.authorizeLogin(r.PostForm, token != ""))
		return
	}
	a := asked{Described: json.RawMessage(r.PostForm.Get("described")), On: r.PostForm.Get("on"), View: r.PostForm.Get("view")}
	if _, _, err := w.askedOf(a); err != nil {
		page(http.StatusBadRequest, htmlEscape(err.Error()))
		return
	}
	data, _ := json.Marshal(a) // ( < > & escaped: it stays inside its script )
	rw.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	rw.Header().Set("X-Frame-Options", "DENY")
	sendHTML(rw, 200, strings.Replace(authorizeHTML, "{asked}", string(data), 1))
}

// authorizeLogin: errand's login, posted back here with what the page sent
func (w *webServer) authorizeLogin(form url.Values, wrong bool) string {
	e := htmlEscape
	said := "errand's page is not open in this browser yet: its token, once, and the window opens."
	if wrong {
		said = "not the token"
	}
	var hidden strings.Builder
	for _, k := range []string{"described", "on", "view"} {
		fmt.Fprintf(&hidden, `<input type="hidden" name="%s" value="%s">`, k, e(form.Get(k)))
	}
	out := strings.NewReplacer(`action="/login"`, `action="/authorize"`, "{said}", e(said)).Replace(loginHTML)
	return strings.Replace(out, `<input name="next" type="hidden" value="{next}">`, hidden.String(), 1)
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;").Replace(s)
}

// askedOf: what a window is for, read again -> the view's asker, and the machine
func (w *webServer) askedOf(a asked) (pageAsk, *UiMachine, error) {
	dir, ok := svc.RegisteredViews()[a.View]
	if !ok {
		return pageAsk{}, nil, errors.New("no such view here any more")
	}
	u, err := w.model()
	if err != nil {
		return pageAsk{}, nil, err
	}
	p := pageAsk{w: w, u: u, dir: dir, vid: a.View}
	m := p.byID(a.On)
	if m == nil {
		return p, nil, errors.New(a.On + ": no such machine here")
	}
	return p, m, nil
}

// authorizePost: what the window asks, the description sent again each time
func (w *webServer) authorizePost(rw http.ResponseWriter, r *http.Request, verb string) {
	var a asked
	if json.NewDecoder(r.Body).Decode(&a) != nil {
		sendError(rw, errors.New("a JSON body"))
		return
	}
	d, _, err := svc.ParsePageDesc(a.Described)
	if err != nil {
		sendError(rw, err)
		return
	}
	p, m, err := w.askedOf(a)
	if err != nil {
		sendError(rw, err)
		return
	}
	u := p.u
	switch verb {
	case "look":
		l, err := p.look(m, d, a.Paths)
		if err != nil {
			sendError(rw, err)
			return
		}
		described, _ := json.MarshalIndent(d, "", "  ")
		sendJSON(rw, 200, map[string]any{"look": l, "machine": m.Name(), "here": m.Here(), "description": string(described), "command": svc.Typed(l.Command)})
	case "probe":
		l, _ := p.look(m, d, a.Paths)
		known := false
		for _, pr := range l.Probes {
			known = known || (pr.Tool == a.Tool && pr.Path == a.Path)
		}
		if !known {
			sendError(rw, errors.New("nothing to probe there"))
			return
		}
		got, err := u.Ask(m, map[string]any{"op": "services_do", "req": map[string]any{"do": "probe", "argv": []string{a.Path, "--version"}}})
		if err != nil {
			sendError(rw, err)
			return
		}
		sendJSON(rw, 200, got)
	case "checkout": // a checkout of one of its repositories in development, there
		known := false
		for _, r := range d.Repos() {
			known = known || r.Dev && svc.SameRepo(r.Git, a.Repo)
		}
		if !known || a.Path == "" {
			sendError(rw, errors.New("a checkout is said for a repository the description takes in development, by its path"))
			return
		}
		got, err := u.Ask(m, map[string]any{"op": "services_do", "req": map[string]any{"do": "dev_declare", "repo": a.Repo, "path": a.Path}})
		if err != nil {
			sendError(rw, err)
			return
		}
		sendJSON(rw, 200, got)
	case "do":
		l, err := p.look(m, d, a.Paths)
		if err != nil {
			sendError(rw, err)
			return
		}
		if l.State == "unavailable" {
			sendError(rw, errors.New(l.Why))
			return
		}
		for _, n := range l.Missing { // the view's files first: what runs reads them
			if n.Kind != "store" {
				continue
			}
			if _, err := u.Ask(nil, map[string]any{"op": "copy_view", "host": m.M.Spec(), "dir": p.dir, "vid": p.vid, "machine": m.Name()}); err != nil {
				sendError(rw, fmt.Errorf("the view's files, to %s: %v", m.Name(), err))
				return
			}
			p.copy(m) // ( kept up to date from now on, while the view asks )
		}
		got, err := u.Ask(m, map[string]any{"op": "services_do", "req": map[string]any{"do": "authorize", "described": d, "paths": a.Paths, "source_ok": a.Trust}})
		if err != nil {
			sendError(rw, err)
			return
		}
		pageMu.Lock()
		for k := range roots {
			if strings.HasPrefix(k, p.vid+"\x00") {
				delete(roots, k)
			}
		}
		pageMu.Unlock()
		var done svc.PageLook
		_ = reach.DecodeInto(got["look"], &done)
		u.refresh()
		u.Changed(true, nil)
		sendJSON(rw, 200, map[string]any{"ok": true, "key": done.Key, "machine": m.Name(), "said": fmt.Sprintf("%s is authorized to pages on %s", done.Key, m.Name()), "file": kit.Text(got["file"])})
	default:
		http.Error(rw, "nothing here", http.StatusNotFound)
	}
}

// authorizeURL: where a view's form goes -- errand's own page, by the name the view was reached by ( its cookie is that
// name's )
func (w *webServer) authorizeURL(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil || host != "localhost" {
		host = "127.0.0.1"
	}
	port := w.said.Port
	if w.said.Public > 0 {
		port = w.said.Public
	}
	return fmt.Sprintf("http://%s:%d/authorize", host, port)
}

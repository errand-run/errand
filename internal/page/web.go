// `errand web` ( the guide's "Every run, live" ): the runs, the machines and the storage, as a page in the browser --
// what `errand tui` shows, built by the same code ( model.go ).
//
//	address     127.0.0.1 only; port 20000 + uid % 10000, any free one when taken
//	access      the port is open to every account of the machine: a page is served only to who holds the token, kept
//	            for its user alone; `errand web` opens the page with a code used once; then a cookie. The `Host` is checked,
//	            an action from another page refused, a run's files served in a sandbox, apart from the page
//	life        a program of its own, started on demand, one per user; it goes when nobody asked for a while. Machines are
//	            watched only while a page asks

package page

import (
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
	"github.com/errand-run/errand/internal/svc"
	"github.com/errand-run/errand/internal/system"
	"github.com/errand-run/errand/internal/units"
)

//go:embed page/page.html
var pageHTML string

//go:embed page/login.html
var loginHTML string

const (
	webLinger  = 30 * time.Second // the machines are still watched after the last request
	onceFor    = 2 * time.Minute
	cookieDays = 30
	wholeFile  = 64 << 20
)

func stopPid(pid int) error { return system.Signal(pid, syscall.SIGTERM) }

func build() string { return reach.OwnHash() }

// ── the command ──────────────────────────────────────────────────────────────

// WebProgram: `errand web`, and what the system starts of it
func WebProgram(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "serve":
			return webServe(args[1:])
		case "serve-kept-page":
			return keptRelay(false)
		case "serve-kept-stores":
			return keptRelay(true)
		}
	}
	return webMain(args)
}

// ensureWeb: the page served, by this program
func ensureWeb() (runinfo.WebSaid, error) {
	if s, ok := runinfo.WebNow(); ok {
		if s.Build == build() {
			return s, nil
		}
		_ = system.Signal(s.Pid, syscall.SIGTERM) // of another errand: it gives the page up
		for until := time.Now().Add(5 * time.Second); time.Now().Before(until); time.Sleep(50 * time.Millisecond) {
			if _, still := runinfo.WebNow(); !still {
				break
			}
		}
	}
	if err := os.MkdirAll(runinfo.WebDir(), 0o700); err != nil {
		return runinfo.WebSaid{}, err
	}
	log, _ := os.OpenFile(filepath.Join(runinfo.WebDir(), "log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	cmd := exec.Command(reach.OwnCopy(), "web", "serve")
	cmd.Env = reach.WithTyped(os.Environ())
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = system.Apart()
	if err := cmd.Start(); err != nil {
		return runinfo.WebSaid{}, err
	}
	_ = cmd.Process.Release()
	if log != nil {
		log.Close()
	}
	for until := time.Now().Add(20 * time.Second); time.Now().Before(until); time.Sleep(40 * time.Millisecond) {
		if s, ok := runinfo.WebNow(); ok && s.Build == build() {
			return s, nil
		}
	}
	return runinfo.WebSaid{}, fmt.Errorf("the page does not come ( its words: %s )", filepath.Join(runinfo.WebDir(), "log"))
}

func openInBrowser(url string) bool {
	if os.Getenv("ERRAND_NO_BROWSER") != "" || os.Getenv("SSH_CONNECTION") != "" {
		return false
	}
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	} else if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return false
	}
	return exec.Command(name, url).Run() == nil
}

func webMain(args []string) int {
	what := ""
	if len(args) > 0 {
		what = args[0]
	}
	say := func(err error) int { fmt.Fprintln(os.Stderr, "errand web:", err); return 1 }
	switch what {
	case "stop":
		if s, ok := runinfo.WebNow(); ok {
			_ = system.Signal(s.Pid, syscall.SIGTERM)
			fmt.Println("  stopped")
		} else {
			fmt.Println("  it is not served")
		}
		return 0
	case "keep", "unkeep":
		return webKeep(what == "keep")
	case "open": // a directory opened as it is
		if len(args) != 2 {
			return say(errors.New("errand open DIR"))
		}
		return openDir(args[1])
	case "static": // where a view of a run is served: asked by what drives a run
		if len(args) < 3 {
			return 2
		}
		s, err := ensureWeb()
		if err != nil {
			return say(err)
		}
		_ = s
		index := "index.html"
		if len(args) > 3 {
			index = args[3]
		}
		vid := viewID(args[1], args[2])
		svc.RegisterView(vid, filepath.Join(args[1], args[2]))
		fmt.Println(staticURL(vid, index))
		return 0
	}
	s, err := ensureWeb()
	if err != nil {
		return say(err)
	}
	switch what {
	case "token":
		fmt.Println(s.Token)
		return 0
	case "url":
		fmt.Println(s.Url())
		return 0
	case "run": // a run's page, or the page itself: asked by what drives a run ( `errand run -O` )
		link := s.Url()
		if len(args) > 1 {
			link += "r/" + args[1]
		}
		openInBrowser(link + "?o=" + once())
		fmt.Println(link)
		return 0
	case "once":
		fmt.Println(s.Url() + "?o=" + once())
		return 0
	case "":
		link := s.Url() + "?o=" + once()
		if openInBrowser(link) {
			fmt.Println("  " + s.Url())
		} else {
			fmt.Printf("  %s\n  ( from another machine: ssh -L %d:localhost:%d, then this address )\n", link, s.Port, s.Port)
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "errand web [token|url|once|keep|unkeep|stop]")
	return 2
}

// once: a code that opens the page once, within two minutes
func once() string {
	d := filepath.Join(runinfo.WebDir(), "once")
	_ = os.MkdirAll(d, 0o700)
	code := secret(32)
	_ = os.WriteFile(filepath.Join(d, code), nil, 0o600)
	return code
}

func spend(code string) bool {
	if code == "" || strings.ContainsAny(code, "/.") {
		return false
	}
	p := filepath.Join(runinfo.WebDir(), "once", code)
	st, err := os.Stat(p)
	return err == nil && os.Remove(p) == nil && time.Since(st.ModTime()) < onceFor
}

// ── the server ───────────────────────────────────────────────────────────────

type notice struct {
	n       int
	lists   bool
	details []string
}

type webServer struct {
	said    runinfo.WebSaid
	mu      sync.Mutex
	u       *Ui
	last    time.Time
	serving int // requests of the pages it serves, being answered: a service may be long to start, a WebSocket is open for hours
	cond    *sync.Cond
	n       int
	notices []notice
	told    string
	toldAt  int
	plans   map[string]*removal
	asked   UiAsked // the caches tab, as the page last asked it
}

func listenUser(port int) (net.Listener, error) {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		l, err = net.Listen("tcp", "127.0.0.1:0")
	}
	return l, err
}

func webServe(args []string) int {
	if err := os.MkdirAll(runinfo.WebDir(), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	lock, err := os.OpenFile(filepath.Join(runinfo.WebDir(), "lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil || system.TryLock(lock) != nil {
		return 0 // served already
	}
	defer lock.Close()
	port, static := 0, 0
	// the user's ports, the same every time, are the errand in use's: another ( an instance, a test's ) takes any free one
	if system.Usual() { // ( the errand in use, where it usually is: its own ports )
		port, static = runinfo.PortOfUser(), runinfo.PortOfUser()+2
	}
	kept := units.KeptPorts()
	if kept[0] > 0 {
		port = 0
	}
	if kept[1] > 0 {
		static = 0
	}
	l, err := listenUser(port)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	sl, err := listenUser(static)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	w := &webServer{plans: map[string]*removal{}, last: time.Now()}
	w.cond = sync.NewCond(&w.mu)
	w.said = runinfo.WebSaid{Port: l.Addr().(*net.TCPAddr).Port, Static: sl.Addr().(*net.TCPAddr).Port, Token: keptSecret(filepath.Join(runinfo.WebDir(), "token"), 48),
		Pid: os.Getpid(), Build: build(), Public: kept[0], PubSt: kept[1]}
	b, _ := json.Marshal(w.said)
	if err := kit.WriteWhole(filepath.Join(runinfo.WebDir(), "said.json"), string(b)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = os.Chmod(filepath.Join(runinfo.WebDir(), "said.json"), 0o600)
	go func() { _ = (&http.Server{Handler: http.HandlerFunc(w.serve)}).Serve(l) }()
	go func() { _ = (&http.Server{Handler: http.HandlerFunc(w.serveStatic)}).Serve(sl) }()
	go func() { // what woke the port the system keeps: passed on to this server
		for range time.Tick(20 * time.Second) {
			w.mu.Lock()
			w.cond.Broadcast()
			w.mu.Unlock()
		}
	}()
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	keep := time.Duration(reach.KeepDflt) * time.Second
	for {
		select {
		case <-sigs:
			_ = os.Remove(filepath.Join(runinfo.WebDir(), "said.json"))
			return 0
		case <-time.After(time.Second):
		}
		w.mu.Lock()
		idle := w.idle()
		if w.u != nil && idle > webLinger {
			go w.u.Close()
			w.u = nil
		}
		w.mu.Unlock()
		if idle > keep {
			_ = os.Remove(filepath.Join(runinfo.WebDir(), "said.json"))
			return 0
		}
	}
}

// model: what the page shows, watched while a page asks
func (w *webServer) model() (*Ui, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.last = time.Now()
	if w.u != nil {
		return w.u, nil
	}
	u, err := NewUI()
	if err != nil {
		return nil, err
	}
	u.Changed = func(lists bool, details []string) {
		w.mu.Lock()
		w.n++
		w.notices = append(w.notices, notice{w.n, lists, details})
		if len(w.notices) > 500 {
			w.notices = w.notices[len(w.notices)-500:]
		}
		w.cond.Broadcast()
		w.mu.Unlock()
	}
	u.Said = w.tell
	w.u = u
	return u, nil
}

func (w *webServer) tell(said string) {
	w.mu.Lock()
	w.n++
	w.notices = append(w.notices, notice{w.n, true, nil})
	w.told, w.toldAt = said, w.n
	w.cond.Broadcast()
	w.mu.Unlock()
}

// cookie: its name -- by its port too: a browser keeps one cookie per name and host, whatever the port, and two errands on
// one machine ( another HOME, a test ) would each take the other's
func (w *webServer) cookie() string {
	port := w.said.Port
	if w.said.Public > 0 {
		port = w.said.Public
	}
	return fmt.Sprintf("errand-%d-%d", os.Getuid(), port)
}

func hostOK(r *http.Request, ports ...int) bool {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host, port = r.Host, "80"
	}
	if host != "127.0.0.1" && host != "localhost" {
		return false
	}
	_, perr := strconv.Atoi(port) // ( a tunnel may end at another port: the name is what says it is this address )
	return perr == nil
}

func (w *webServer) allowed(r *http.Request) bool {
	c, err := r.Cookie(w.cookie())
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(w.said.Token)) == 1
}

func (w *webServer) logIn(rw http.ResponseWriter, where string) {
	w.setCookie(rw)
	rw.Header().Set("Location", where)
	rw.WriteHeader(http.StatusSeeOther)
}

func (w *webServer) setCookie(rw http.ResponseWriter) {
	http.SetCookie(rw, &http.Cookie{Name: w.cookie(), Value: w.said.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: cookieDays * 86400})
}

func next(where string) string {
	if strings.HasPrefix(where, "/") && !strings.HasPrefix(where, "//") && !strings.ContainsAny(where, "\r\n\\") {
		return where
	}
	return "/"
}

func sendHTML(rw http.ResponseWriter, code int, body string) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "same-origin") // ( nothing to another origin; its own POSTs say their Origin, which no-referrer makes "null" )
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.WriteHeader(code)
	_, _ = rw.Write([]byte(body))
}

func login(said, where string) string {
	return strings.NewReplacer("{said}", html.EscapeString(said), "{next}", html.EscapeString(next(where))).Replace(loginHTML)
}

func sendJSON(rw http.ResponseWriter, code int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	rw.WriteHeader(code)
	_ = json.NewEncoder(rw).Encode(v)
}

func sendError(rw http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	if strings.Contains(err.Error(), "not a path") {
		code = http.StatusForbidden
	} else if strings.Contains(err.Error(), "not there") {
		code = http.StatusNotFound
	}
	sendJSON(rw, code, map[string]any{"error": kit.FirstLine(err.Error())})
}

func (w *webServer) serve(rw http.ResponseWriter, r *http.Request) {
	if !hostOK(r) {
		http.Error(rw, "not for this address", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodPost {
		w.post(rw, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(rw, "read only", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	pageLike := r.URL.Path == "/" || r.URL.Path == "/job" || strings.HasPrefix(r.URL.Path, "/r/") || r.URL.Path == "/authorize"
	if pageLike && q.Get("o") != "" { // a code used once: the cookie, and the same address without it
		code := q.Get("o")
		q.Del("o")
		where := r.URL.Path
		if rest := q.Encode(); rest != "" {
			where += "?" + rest
		}
		switch {
		case spend(code):
			w.logIn(rw, where)
		case w.allowed(r):
			http.Redirect(rw, r, where, http.StatusSeeOther)
		default:
			sendHTML(rw, http.StatusForbidden, login("this link was used already, or is too old", r.URL.Path))
		}
		return
	}
	if !w.allowed(r) {
		if pageLike {
			sendHTML(rw, http.StatusUnauthorized, login("", r.URL.RequestURI()))
		} else {
			http.Error(rw, "no token: `errand web` opens the page", http.StatusForbidden)
		}
		return
	}
	u, err := w.model()
	if err != nil {
		sendError(rw, err)
		return
	}
	switch {
	case r.URL.Path == "/" || r.URL.Path == "/job":
		w.setCookie(rw)
		st := w.said.Static
		if w.said.PubSt > 0 {
			st = w.said.PubSt
		}
		rw.Header().Set("Content-Security-Policy", fmt.Sprintf("default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data:; frame-src 'self' http://127.0.0.1:%d http://localhost:%d; frame-ancestors 'none'", st, st))
		sendHTML(rw, 200, pageOfInstance())
	case r.URL.Path == "/events":
		w.events(rw, r)
	case r.URL.Path == "/authorize": // ( the window comes by a view's form, not by a link )
		sendHTML(rw, http.StatusBadRequest, "<!DOCTYPE html><meta charset=utf-8><title>errand</title><p style='font:14px sans-serif;margin:2em'>this window is opened by a page's <em>Set it up</em> button")
	case strings.HasPrefix(r.URL.Path, "/r/"): // a run by its directory's name: its page
		ident := strings.Trim(r.URL.Path[3:], "/")
		if key := u.runKey(ident); key != "" {
			http.Redirect(rw, r, "/job?key="+url.QueryEscape(key), http.StatusSeeOther)
		} else {
			sendHTML(rw, 200, startingHTML)
		}
	case r.URL.Path == "/state":
		sendJSON(rw, 200, w.state(u, q))
	case r.URL.Path == "/detail":
		d, err := w.detail(u, q.Get("key"))
		if err != nil {
			sendError(rw, err)
			return
		}
		sendJSON(rw, 200, d)
	case r.URL.Path == "/curves": // the points of some curves of a run's steps
		row, err := u.find(q.Get("key"))
		if err != nil {
			sendError(rw, err)
			return
		}
		_, _, got := u.DetailOf(row)
		sendJSON(rw, 200, map[string]any{"curves": curvePoints(got.step, q["name"])})
	case r.URL.Path == "/peek":
		got, err := w.peek(u, q.Get("key"), q.Get("path"), q.Get("size"))
		if err != nil {
			sendError(rw, err)
			return
		}
		sendJSON(rw, 200, got)
	case r.URL.Path == "/ls":
		m, p, err := w.reach(u, q.Get("key"), q.Get("path"))
		var got map[string]any
		if err == nil {
			got, err = u.Ask(m, map[string]any{"op": "ls", "path": p, "dirs": true})
		}
		if err != nil {
			sendError(rw, err)
			return
		}
		now := m.now()
		entries := anyList(got["entries"])
		for _, x := range entries {
			if e, ok := x.(map[string]any); ok {
				e["age"] = now - kit.Num(e["mtime"], now)
			}
		}
		sendJSON(rw, 200, map[string]any{"path": p, "machine": m.Name(), "here": m.Here(), "entries": entries})
	case r.URL.Path == "/file":
		w.file(rw, u, q)
	default:
		http.Error(rw, "nothing here", http.StatusNotFound)
	}
}

// pageOfInstance: the page, its title and header saying the instance when it is not the errand in use where it usually is
func pageOfInstance() string {
	if system.Instance() == "main" {
		return pageHTML
	}
	said := "errand [" + system.Instance() + "]"
	return strings.NewReplacer("<title>errand</title>", "<title>"+said+"</title>",
		`title="all the runs and machines">errand</a>`, `title="`+html.EscapeString(system.Cache())+`">`+said+`</a>`).Replace(pageHTML)
}

// startingHTML: the page of a run its machine has not said yet -- it goes there when the events say something came
const startingHTML = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>errand: a run starts</title></head><body style="font:14px sans-serif;margin:2em;color:#666">the run starts: its page comes as soon as its machine says it …
<script>
let going = false, again = false;
async function look() {
  if (going) { again = true; return; }
  going = true;
  const r = await fetch(location.href).catch(() => null);
  if (r && r.redirected) return location.replace(r.url);
  going = false;
  if (again) { again = false; look(); }
}
const es = new EventSource("/events");
es.onopen = look; es.onmessage = look;
</script></body></html>`

func set(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Split(s, ",") {
		if w != "" {
			out[w] = true
		}
	}
	return out
}

func (w *webServer) state(u *Ui, q url.Values) map[string]any {
	a := UiAsked{Tab: q.Get("tab"), Query: q.Get("q"), Folded: set(q.Get("folded")), Opened: set(q.Get("opened")), Days: 30, All: q.Get("all") == "1" || q.Get("all") == "true"}
	if a.Tab != "machines" && a.Tab != "caches" {
		a.Tab = "runs"
	}
	if d, err := strconv.ParseFloat(q.Get("days"), 64); err == nil && d > 0 {
		a.Days = d
	}
	if a.Tab == "caches" {
		w.mu.Lock()
		w.asked = a
		w.mu.Unlock()
	}
	if strings.HasPrefix(q.Get("sel"), "[") {
		a.Sel = q.Get("sel")
	}
	rows := u.Build(a)
	running, waiting, servers, off, here := 0, 0, 0, 0, ""
	ms := u.snapshot()
	for _, m := range ms {
		if m.Here() {
			here = m.Name()
		}
		if m.M.Dormant() {
			off++
		}
		if m.doc == nil {
			continue
		}
		servers += len(m.doc.Serving)
		for _, r := range m.doc.Rows {
			switch {
			case r.Aside || r.State == "ended":
			case r.State == "waiting":
				waiting++
			default:
				running++
			}
		}
	}
	return map[string]any{"rows": rows, "tab": a.Tab, "here": here, "counts": map[string]any{"running": running, "waiting": waiting, "servers": servers, "machines": len(ms), "off": off}, "kept": w.said.Public > 0}
}

func (w *webServer) detail(u *Ui, key string) (map[string]any, error) {
	row, err := u.find(key)
	if err != nil {
		return nil, err
	}
	rows, m, got := u.DetailOf(row)
	out := map[string]any{"title": row.Label, "kind": row.Kind, "rows": rows, "now": kit.Seconds(time.Now()), "console": nil, "here": false, "copies": []any{}, "can": u.can(row, m),
		"machine": nil, "file": nil, "started": nil, "duration": nil}
	if m != nil {
		out["machine"], out["here"], out["now"] = m.Name(), m.Here(), m.now()
	}
	out["leaf"] = RowLeaf(row)
	if got.step != nil {
		out["saidAt"] = got.step.T
	}
	out["curves"] = curvesOf(got.step)
	if got.Path != "" {
		out["console"] = got.Path
	}
	switch what := row.What.(type) {
	case *reach.TableRow:
		out["started"] = what.Started
		if f := EntryFile(row); f != "" {
			out["file"] = f
		}
	case *runinfo.PastRun:
		out["started"], out["duration"] = what.Started, what.Duration
		if f := EntryFile(row); f != "" {
			out["file"] = f
		}
		if what.Far != nil && what.FarLeaf != "" && what.There == "" {
			out["copies"] = []any{map[string]any{"id": "ran", "machine": what.Host, "what": "its directory there, where it ran"}}
		}
		if what.There == "" {
			for _, c := range filesync.CopiesUnder(what.Leaf) {
				rel, _ := filepath.Rel(what.Leaf, c.Path)
				out["copies"] = append(anyList(out["copies"]), map[string]any{"id": c.ID, "machine": c.Machine, "what": "a copy of " + rel, "state": c.State})
			}
		}
	}
	return out, nil
}

// reach: a path of a row -- one of its facts that is a path, one of its outputs, or under them -- and nothing else of its machine
func (w *webServer) reach(u *Ui, key, p string) (*UiMachine, string, error) {
	row, err := u.find(key)
	if err != nil {
		return nil, "", err
	}
	rows, m, got := u.DetailOf(row)
	if m == nil {
		return nil, "", errors.New("this row is on no machine")
	}
	p = path.Clean(p)
	roots := []string{}
	for _, d := range rows {
		if strings.HasPrefix(d.V, "/") && !strings.Contains(d.V, " ") {
			roots = append(roots, d.V)
		}
		if d.Path != "" {
			roots = append(roots, d.Path)
		}
	}
	roots = append(roots, got.Path, EntryFile(row))
	for _, r := range roots {
		if r != "" && strings.HasPrefix(p, "/") && (p == r || strings.HasPrefix(p, strings.TrimRight(r, "/")+"/")) {
			return m, p, nil
		}
	}
	return nil, "", errors.New("not a path of this row")
}

func (w *webServer) peek(u *Ui, key, p, size string) (map[string]any, error) {
	m, p, err := w.reach(u, key, p)
	if err != nil {
		return nil, err
	}
	n, _ := strconv.Atoi(size)
	if n <= 0 {
		n = 16384
	}
	n = min(n, 4<<20)
	got, err := u.Ask(m, map[string]any{"op": "tail", "path": p, "offset": 0, "size": n})
	if err != nil {
		return nil, err
	}
	if got["missing"] == true {
		return nil, errors.New("not there any more")
	}
	total := kit.Num(got["size"], 0)
	switch strings.ToLower(path.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg":
		return map[string]any{"kind": "image", "size": total}, nil
	case ".html", ".htm", ".pdf":
		return map[string]any{"kind": "page", "size": total}, nil
	}
	text := kit.Text(got["data"])
	if strings.Contains(text, "\x00") || strings.Count(text, "�") > max(8, len(text)/50) {
		return map[string]any{"kind": "binary", "size": total}, nil
	}
	if i := strings.LastIndexByte(text, '\n'); kit.Num(got["end"], 0) < total && i >= 0 {
		text = text[:i] // a line cut in two is not shown
	}
	return map[string]any{"kind": "text", "text": text, "size": total, "more": max(0, total-kit.Num(got["end"], 0))}, nil
}

func (w *webServer) file(rw http.ResponseWriter, u *Ui, q url.Values) {
	m, p, err := w.reach(u, q.Get("key"), q.Get("path"))
	if err != nil {
		sendError(rw, err)
		return
	}
	var whole []byte
	for offset := 0.0; offset < wholeFile; {
		got, err := u.Ask(m, map[string]any{"op": "tail", "path": p, "offset": offset, "size": 1 << 20, "raw": true})
		if err != nil {
			sendError(rw, err)
			return
		}
		if got["missing"] == true {
			sendError(rw, errors.New("not there"))
			return
		}
		b, _ := base64.StdEncoding.DecodeString(kit.Text(got["b64"]))
		whole = append(whole, b...)
		end := kit.Num(got["end"], 0)
		if end == kit.Num(got["offset"], 0) || end >= kit.Num(got["size"], 0) {
			break
		}
		offset = end
	}
	kind := mime.TypeByExtension(path.Ext(p))
	if base, _, _ := strings.Cut(kind, ";"); !strings.HasPrefix(base, "image/") && base != "text/html" && base != "application/pdf" && !strings.HasPrefix(base, "video/") {
		kind = "text/plain; charset=utf-8" // what the browser shows: a script is text to read
	}
	how := "inline"
	if q.Get("download") != "" {
		how = "attachment"
	}
	// A run's page is served apart from this one: its scripts run, never as this page.
	rw.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-popups allow-downloads")
	rw.Header().Set("Content-Disposition", how+"; filename*=UTF-8''"+url.PathEscape(path.Base(p)))
	rw.Header().Set("Content-Type", kind)
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = rw.Write(whole)
}

// events: what changes, pushed
func (w *webServer) events(rw http.ResponseWriter, r *http.Request) {
	fl, ok := rw.(http.Flusher)
	if !ok {
		return
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-store")
	_, _ = rw.Write([]byte(": errand\n\n"))
	fl.Flush()
	w.mu.Lock()
	after := w.n
	w.mu.Unlock()
	for {
		w.mu.Lock()
		w.last = time.Now()
		for w.n <= after && r.Context().Err() == nil {
			w.cond.Wait()
			if w.n <= after {
				break
			}
		}
		lists := false
		details := map[string]bool{}
		for _, n := range w.notices {
			if n.n > after {
				lists = lists || n.lists
				for _, d := range n.details {
					details[d] = true
				}
			}
		}
		told := ""
		if w.toldAt > after {
			told = w.told
		}
		after = w.n
		w.mu.Unlock()
		if r.Context().Err() != nil {
			return
		}
		if !lists && len(details) == 0 && told == "" { // nothing: the stream is kept, and one that is cut is seen to be
			if _, err := rw.Write([]byte(": still here\n\n")); err != nil {
				return
			}
			fl.Flush()
			continue
		}
		names := make([]string, 0, len(details))
		for d := range details {
			names = append(names, d)
		}
		b, _ := json.Marshal(map[string]any{"lists": lists, "details": names, "said": told})
		if _, err := fmt.Fprintf(rw, "data: %s\n\n", b); err != nil {
			return
		}
		fl.Flush()
		if _, err := w.model(); err != nil {
			return
		}
		time.Sleep(150 * time.Millisecond) // ( what changes together is said together )
	}
}

func (w *webServer) post(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/authorize" { // a view's form: its origin is the view's ( authorize.go )
		w.authorizeLanding(rw, r)
		return
	}
	if r.URL.Path != "/login" { // ( the form's own origin is null: the token it carries is the proof there )
		origin := r.Header.Get("Origin")
		if u, err := url.Parse(origin); origin == "" || err != nil || u.Host != r.Host {
			http.Error(rw, "not from this page", http.StatusForbidden)
			return
		}
	}
	r.Body = http.MaxBytesReader(rw, r.Body, 1<<16)
	if r.URL.Path == "/login" {
		_ = r.ParseForm()
		if t := strings.TrimSpace(r.PostForm.Get("token")); t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(w.said.Token)) == 1 {
			w.logIn(rw, next(r.PostForm.Get("next")))
			return
		}
		time.Sleep(time.Second) // one guess a second
		sendHTML(rw, http.StatusForbidden, login("not the token", r.PostForm.Get("next")))
		return
	}
	if !w.allowed(r) {
		http.Error(rw, "no token", http.StatusForbidden)
		return
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, "/authorize/"); ok {
		w.authorizePost(rw, r, rest)
		return
	}
	var body map[string]any
	if json.NewDecoder(r.Body).Decode(&body) != nil || r.URL.Path != "/act" {
		http.Error(rw, "nothing here", http.StatusNotFound)
		return
	}
	u, err := w.model()
	if err == nil {
		var got map[string]any
		if got, err = w.act(u, body); err == nil {
			sendJSON(rw, 200, got)
			return
		}
	}
	sendError(rw, err)
}

// act: what a button or a key asks. Kill, forget and removals say first what they would do.
func (w *webServer) act(u *Ui, body map[string]any) (map[string]any, error) {
	said := func(s string) (map[string]any, error) { return map[string]any{"said": s}, nil }
	confirm := func(title string, lines []string, verb string, danger bool) (map[string]any, error) {
		return map[string]any{"confirm": map[string]any{"title": title, "lines": lines, "verb": verb, "danger": danger}}, nil
	}
	sure := body["sure"] == true
	op := kit.Text(body["op"])
	switch op {
	case "refresh":
		ReadsMu.Lock()
		Reads = map[string]UiRead{}
		ReadsMu.Unlock()
		u.Mu.Lock()
		u.ReadAt, u.PastAt = time.Time{}, time.Time{}
		u.Mu.Unlock()
		u.refresh()
		return said("asking again")
	case "runs_remove":
		if id := kit.Text(body["plan"]); id != "" && sure {
			w.mu.Lock()
			p := w.plans[id]
			delete(w.plans, id)
			w.mu.Unlock()
			if p == nil {
				return nil, errors.New("that was asked too long ago: ask again")
			}
			go u.Remove(p)
			return said(fmt.Sprintf("removing %d run%s…", len(p.runs), reach.Plural(len(p.runs))))
		}
		keys := kit.Strs(body["keys"])
		p, err := u.Plan(keys, kit.Text(body["how"]))
		if err != nil {
			return nil, err
		}
		id := secret(9)
		w.mu.Lock()
		if len(w.plans) > 20 {
			w.plans = map[string]*removal{}
		}
		w.plans[id] = p
		w.mu.Unlock()
		return map[string]any{"plan": id, "confirm": map[string]any{"title": p.Title, "lines": p.Lines, "verb": "remove", "danger": true, "measure": "runs_measure"}}, nil
	case "runs_measure":
		w.mu.Lock()
		p := w.plans[kit.Text(body["plan"])]
		w.mu.Unlock()
		if p == nil {
			return map[string]any{"lines": []string{}}, nil
		}
		return map[string]any{"lines": u.Measure(p)}, nil
	case "runs_check":
		go u.Check()
		return said("checking the other machines…")
	}
	row, err := u.find(kit.Text(body["key"]))
	if err != nil {
		return nil, err
	}
	m := u.machineOf(row)
	switch op {
	case "runs_fetch":
		r, ok := row.What.(*runinfo.PastRun)
		if !ok || r.There == "" {
			return nil, errors.New("b: on a run the check found on another machine only")
		}
		go u.BringBack(r)
		return said("bringing " + path.Base(r.Rel) + " back…")
	case "copy_remove":
		r, ok := row.What.(*runinfo.PastRun)
		if id := kit.Text(body["copy"]); ok && id != "" && id != "ran" {
			if _, err := u.Ask(nil, map[string]any{"op": "copies_drop", "ids": []string{id}}); err != nil {
				return nil, err
			}
			return said("removed there")
		}
		if !ok || r.Far == nil || r.FarLeaf == "" {
			return nil, errors.New("no copy of it elsewhere")
		}
		if !sure {
			return confirm("remove its directory on "+r.Host+"?", []string{r.FarLeaf, "the one here stays"}, "remove", true)
		}
		l, err := u.link()
		if err != nil {
			return nil, err
		}
		at := func(req map[string]any) (map[string]any, error) {
			return l.Ask(map[string]any{"op": "at", "host": r.Far, "req": req, "environ": os.Environ()})
		}
		got, err := at(map[string]any{"op": "runs_remove", "leaves": []string{r.FarLeaf}})
		if err != nil {
			return nil, err
		}
		was := len(kit.Strs(got["removed"])) > 0
		if !was { // not there any more, or not let go of
			there, err := at(map[string]any{"op": "check_there", "paths": []string{r.FarLeaf}})
			if err != nil {
				return nil, err
			}
			if len(kit.Strs(there["missing"])) == 0 {
				return nil, errors.New("not removed on " + r.Host + ": it runs, or it is no run's directory")
			}
		}
		_ = r.LetGo()
		u.Mu.Lock()
		u.PastAt = time.Time{}
		u.Mu.Unlock()
		u.Changed(true, []string{row.Key})
		if !was {
			return said("it was not on " + r.Host + " any more")
		}
		return said("removed on " + r.Host)
	case "cache_remove", "cache_hold":
		w.mu.Lock()
		days := w.asked.Days
		w.mu.Unlock()
		if days == 0 {
			days = 30
		}
		if op == "cache_hold" {
			c, _ := row.What.(map[string]any)
			if c == nil || m == nil {
				return nil, errors.New("on a directory of caches")
			}
			if !sure {
				return confirm("held by "+m.Name()+"?", []string{"its agent purges it, once a day"}, "hold", false)
			}
			if _, err := u.Ask(m, map[string]any{"op": "caches_hold", "id": c["id"]}); err != nil {
				return nil, err
			}
			return said("held by " + m.Name())
		}
		title, lines, do, err := u.CacheRemoval(row, kit.Text(body["how"]), days)
		if err != nil {
			return nil, err
		}
		if !sure {
			return confirm(title, lines, "remove", true)
		}
		return said(do())
	case "withdraw": // an authorization taken back, from all it was given to
		a, ok := row.What.(*reach.AuthRow)
		if !ok || m == nil {
			return nil, errors.New("on an authorization")
		}
		if !sure {
			return confirm("withdraw "+a.Label+"~"+a.Hash+" on "+m.Name()+"?", []string{"authorized to " + strings.Join(a.To, ", ") + ": not any more",
				svc.Typed(a.Command), "a connection already open is not cut"}, "withdraw", true)
		}
		if _, err := u.Ask(m, map[string]any{"op": "services_do", "req": map[string]any{"do": "withdraw", "hash": a.Hash}}); err != nil {
			return nil, err
		}
		u.refresh()
		u.Changed(true, nil)
		return said(a.Label + " is authorized to no one on " + m.Name())
	case "kill":
		list := u.KillList(row)
		if len(list) == 0 {
			return nil, errors.New("put the cursor on something that runs")
		}
		if !sure {
			lines := []string{}
			for _, k := range list {
				lines = append(lines, k.What+" on "+k.M.Name())
			}
			return confirm(fmt.Sprintf("stop %d?", len(list)), lines, "kill", true)
		}
		return said(u.Kill(list))
	}
	if m == nil {
		return nil, errors.New("on a machine, or on something that runs on one")
	}
	switch op {
	case "machine_check":
		return checkedBy(m, body["fix"] == true && sure, body["bring"] == true && sure)
	case "forget":
		if m.Here() {
			return nil, errors.New("this machine is always shown: nothing to forget")
		}
		if !sure {
			return confirm("forget "+m.Name()+"?", []string{"not asked any more, until work is sent there again", "what runs there goes on"}, "forget", false)
		}
		return said(u.Forget(m))
	case "watch":
		return said(u.ToggleWatch(m))
	case "rename":
		if m.Here() { // ( said before a name is asked: what is typed would be lost )
			return nil, errors.New("this machine is shown by its own name")
		}
		label, given := body["label"].(string)
		if !given {
			return map[string]any{"prompt": map[string]any{"title": "a name for " + m.Name(), "text": m.Name()}}, nil
		}
		label = strings.TrimSpace(label)
		if err := m.M.CanBeNamed(label); err != nil {
			return nil, err
		}
		u.EditNotes(m, func(n *reach.MachineNote) { n.Label = label })
		return said("named " + label)
	case "edit", "reveal":
		p := kit.Text(body["path"])
		if p == "" {
			p = EntryFile(row)
		}
		at, p, err := w.reach(u, kit.Text(body["key"]), p)
		if err != nil {
			return nil, err
		}
		if !at.Here() {
			return nil, errors.New("it is on " + at.Name() + ": not a file of this machine")
		}
		return openFile(op, p)
	}
	return nil, fmt.Errorf("%s: not something the page asks", op)
}

// openFile: a file opened in the person's editor, or shown in the files' window
func openFile(op, p string) (map[string]any, error) {
	var argv []string
	switch {
	case op == "reveal" && runtime.GOOS == "darwin":
		argv = []string{"open", "-R", p}
	case op == "reveal":
		argv = []string{"xdg-open", path.Dir(p)}
	default:
		editor := os.Getenv("ERRAND_EDITOR")
		if editor == "" {
			for _, e := range []string{"code", "cursor", "zed", "subl"} {
				if _, err := exec.LookPath(e); err == nil {
					editor = e
					break
				}
			}
		}
		if editor == "" && runtime.GOOS == "darwin" {
			argv = []string{"open", "-t", p}
		} else if editor == "" {
			argv = []string{"xdg-open", p}
		} else {
			argv = append(strings.Fields(editor), p)
		}
	}
	if err := exec.Command(argv[0], argv[1:]...).Start(); err != nil {
		return nil, err
	}
	return map[string]any{"said": "opened " + path.Base(p)}, nil
}

// runKey: the key of the run whose directory is named `ident`
func (u *Ui) runKey(ident string) string {
	u.refresh()
	// one that was launched from here is called by its place here, wherever it runs
	here := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(runinfo.NotesDir(), "*.json"))
	for _, f := range files {
		var n runinfo.RunNote
		if data, err := os.ReadFile(f); err == nil && json.Unmarshal(data, &n) == nil && runinfo.RunIdent(n.Leaf) == ident {
			here[n.ID] = true
		}
	}
	for _, m := range u.snapshot() {
		if m.doc == nil {
			continue
		}
		for _, r := range m.doc.Rows {
			if r.ID == ident || here[r.ID] || runinfo.RunIdent(r.Leaf) == ident {
				return workKey(r.Leaf)
			}
		}
	}
	for _, r := range u.runs() {
		if runinfo.RunIdent(r.Leaf) == ident || runinfo.RunIdent(r.FarLeaf) == ident {
			return workKey(r.Leaf)
		}
	}
	return ""
}

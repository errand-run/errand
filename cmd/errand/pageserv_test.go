package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/units"
)

func TestAPageAsksForItsServices(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	config := filepath.Join(filepath.Dir(p.root), "config")
	p.env = append(p.env, "XDG_CONFIG_HOME="+config, "ERRAND_NO_BROWSER=1")
	srv := p.write("srv.py", `import http.server, socketserver, sys
class H( http.server.BaseHTTPRequestHandler ):
    def do_GET( self ):
        n = int( self.headers.get( "Content-Length" ) or 0 )
        body = f"{self.command} {self.path} root={self.headers.get( 'X-Errand-Root' )} body={self.rfile.read( n ).decode()}".encode()
        self.send_response( 200 ); self.send_header( "Content-Length", str( len( body ) ) ); self.end_headers(); self.wfile.write( body )
    do_POST = do_GET
    def log_message( self, *a ): pass
class S( http.server.ThreadingHTTPServer ):
    server_bind = socketserver.TCPServer.server_bind # ( HTTPServer's looks its address up before it listens: 30 s on some Macs )
S( ( "127.0.0.1", int( sys.argv[ 1 ] ) ), H ).serve_forever()
`)
	described := func(name string) string {
		b, _ := json.Marshal(map[string]any{"name": name, "protocol": 1, "text": "a field", "run": []string{"python3", srv, "{port}"}, "idle": "1h"})
		return string(b)
	}
	store := filepath.Join(p.root, "foam.sviz")
	p.write("foam.sviz/index.html", "<p>foam</p>")
	defer p.errand("", "web", "stop")
	o := p.errand("", "open", store)
	base := strings.TrimSpace(o.out)
	if o.code != 0 || !strings.HasPrefix(base, "http://127.0.0.1:") || !strings.Contains(get(t, base), "foam") {
		t.Fatalf("errand open: %d %q %q", o.code, o.out, o.err)
	}
	var machines []map[string]any
	if json.Unmarshal([]byte(get(t, base+".errand/machines")), &machines) != nil || len(machines) == 0 || machines[0]["here"] != true || len(kit.Text(machines[0]["id"])) != 8 {
		t.Fatalf("the machines, by their ids: %v", machines)
	}
	here := kit.Text(machines[0]["id"])
	ask := func(body string) (int, map[string]any) {
		t.Helper()
		r, err := http.Post(base+".errand/srv", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		return r.StatusCode, got
	}

	// not authorized: why, and a form that opens errand's window with the description -- nothing is kept in between
	code, got := ask(described("field"))
	link := kit.Text(got["authorize"])
	form, _ := got["form"].(map[string]any)
	if code != 403 || got["error"] != "not authorized" || !strings.Contains(kit.Text(got["html"]), `<form class="errand-setup" method="post"`) || strings.Contains(kit.Text(got["html"]), "<script") ||
		!strings.HasSuffix(link, "/authorize") || got["on"] != here || form["view"] == nil || !strings.Contains(kit.Text(form["described"]), srv) {
		t.Fatalf("refused: %d %v", code, got)
	}
	if code, got = ask(`{"name": "x", "run": ["{shell}"]}`); code != 400 {
		t.Errorf("not a description: %d %v", code, got)
	}
	if code, _ = ask(`{"name": "x", "run": ["a"], "on": "00000000"}`); code != 404 {
		t.Errorf("no such machine: %d", code)
	}

	// the window, in errand's page: opened by the view's form ( its origin, errand's cookie ), then asked from itself
	var said struct{ Token string }
	_ = json.Unmarshal([]byte(read(t, filepath.Join(p.cache(), "page", "said.json"))), &said)
	u, _ := url.Parse(link)
	view, _ := url.Parse(base)
	call := func(path, origin, ctype, body string, cookie bool) (int, string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest("POST", "http://"+u.Host+path, strings.NewReader(body))
		if cookie {
			req.AddCookie(&http.Cookie{Name: fmt.Sprintf("errand-%d-%s", os.Getuid(), u.Port()), Value: said.Token})
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", ctype)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b), r.Header
	}
	fields := url.Values{}
	for k, v := range form {
		fields.Set(k, kit.Text(v))
	}
	formType := "application/x-www-form-urlencoded"
	if code, body, _ := call("/authorize", "http://"+view.Host, formType, fields.Encode(), false); code != http.StatusUnauthorized ||
		!strings.Contains(body, `name="token"`) || !strings.Contains(body, `action="/authorize"`) || !strings.Contains(body, `name="described"`) {
		t.Errorf("without errand's cookie: its token asked, the description kept: %d %.400s", code, body)
	}
	errandOrigin := "http://" + u.Host
	withToken := url.Values{}
	for k, v := range fields {
		withToken[k] = v
	}
	withToken.Set("token", "wrong")
	if code, body, _ := call("/authorize", errandOrigin, formType, withToken.Encode(), false); code != http.StatusUnauthorized || !strings.Contains(body, "not the token") {
		t.Errorf("a wrong token: %d", code)
	}
	withToken.Set("token", said.Token)
	if code, body, header := call("/authorize", errandOrigin, formType, withToken.Encode(), false); code != 200 || !strings.Contains(body, "Authorize") || !strings.Contains(header.Get("Set-Cookie"), "errand-") {
		t.Errorf("its token, then the window: %d %v", code, header)
	}
	if code, _, _ := call("/authorize", "http://evil.example", formType, fields.Encode(), true); code != http.StatusForbidden {
		t.Errorf("from another site: %d", code)
	}
	code, body, header := call("/authorize", "http://"+view.Host, formType, fields.Encode(), true)
	if code != 200 || !strings.Contains(body, "Authorize") || header.Get("X-Frame-Options") != "DENY" || strings.Contains(body, "{asked}") {
		t.Fatalf("the window: %d %.300s", code, body)
	}
	asked, _ := json.Marshal(map[string]any{"described": json.RawMessage(kit.Text(form["described"])), "on": form["on"], "view": form["view"]})
	if code, body, _ := call("/authorize/look", "http://"+view.Host, "application/json", string(asked), true); code != http.StatusForbidden {
		t.Errorf("the window's calls are errand's page's alone: %d %s", code, body)
	}
	if code, body, _ := call("/authorize/look", errandOrigin, "application/json", string(asked), true); code != 200 || !strings.Contains(body, `"state":"ready"`) || !strings.Contains(body, srv) {
		t.Errorf("what it would run: %d %s", code, body)
	}
	if code, body, _ := call("/authorize/do", errandOrigin, "application/json", string(asked), true); code != 200 || !strings.Contains(body, "is authorized to pages") {
		t.Fatalf("authorized: %d %s", code, body)
	}
	if data, err := os.ReadFile(filepath.Join(config, "errand", "main", "authorized-services.json")); err != nil || !strings.Contains(string(data), `"page": "field~`) {
		t.Errorf("written: %q %v", data, err)
	} else if st, _ := os.Stat(filepath.Join(config, "errand", "main", "authorized-services.json")); st.Mode().Perm() != 0o600 {
		t.Errorf("for its user alone: %v", st.Mode())
	}

	// asked again: its address
	code, got = ask(described("field"))
	address := kit.Text(got["url"])
	if code != 200 || !strings.HasPrefix(address, ".errand/srv/"+here+"/field~") || got["state"] != "authorized" {
		t.Fatalf("its address: %d %v", code, got)
	}
	if got := get(t, base+address+"at/x?y=1"); !strings.HasPrefix(got, "GET /at/x?y=1 root="+store) {
		t.Errorf("relayed: %q", got)
	}
	r, err := http.Post(base+address+"p", "text/plain", strings.NewReader("sent"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	if !bytes.Contains(b, []byte("POST /p")) || !bytes.Contains(b, []byte("body=sent")) {
		t.Errorf("with a body: %q", b)
	}
	if o = p.errand("", "serve"); !strings.Contains(o.out, "field") {
		t.Errorf("it runs as any service: %q", o.out)
	}
	key := strings.Split(address, "/")[3]
	for path, want := range map[string]int{".errand/srv/" + here + "/nobody~0000aaaa/x": 403, ".errand/srv/" + here + "/nobody/x": 404, ".errand/srv/00000000/" + key + "/x": 404, ".errand/nothing": 404} {
		if r, _ := http.Get(base + path); r.StatusCode != want {
			t.Errorf("%s: %d, not %d", path, r.StatusCode, want)
		}
	}

	// the command line: the same, the description on stdin
	if o = p.errand(described("field2"), "serve", "authorize", "--to", "pages", "--yes", "--spec", "-"); o.code != 0 || !strings.Contains(o.out, "authorized to pages on here") || !strings.Contains(o.out, srv) {
		t.Errorf("serve authorize: %d %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "serve", "authorize", "--to", "pages", "--", "python3", srv); o.code != 2 {
		t.Errorf("a page describes, it does not say a command: %d %q", o.code, o.err)
	}
	if o = p.errand("", "serve", "authorized"); !strings.Contains(o.out, "field") || !strings.Contains(o.out, "to pages") || strings.Contains(o.out, "refused") {
		t.Errorf("authorized: %q %q", o.out, o.err)
	}
	if o = p.errand("", "serve", "probe", "--", "sh", "-c", "echo tool 1.2.3"); o.code != 0 || !strings.Contains(o.out, "1.2.3") {
		t.Errorf("serve probe: %d %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "serve", "list", "--versions"); o.code != 0 || !strings.Contains(o.out, key) {
		t.Errorf("the versions: %d %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "serve", "pin", key); o.code != 0 {
		t.Errorf("pin: %q", o.err)
	}
	if o = p.errand("", "serve", "prune", "--dry-run"); o.code != 0 {
		t.Errorf("prune: %d %q", o.code, o.err)
	}
	// withdrawn: refused again
	var hash string
	for _, line := range strings.Split(p.errand("", "serve", "authorized").out, "\n") {
		if f := strings.Fields(line); len(f) > 1 && f[1] == "field" {
			hash = f[0]
		}
	}
	if o = p.errand("", "serve", "withdraw", hash); o.code != 0 {
		t.Errorf("withdraw %s: %q", hash, o.err)
	}
	time.Sleep(50 * time.Millisecond)
	if r, _ := http.Get(base + address + "x"); r.StatusCode != 403 {
		t.Errorf("withdrawn: %d", r.StatusCode)
	}
	if code, _ = ask(described("field")); code != 403 {
		t.Errorf("asked again, withdrawn: %d", code)
	}
}

func TestPortsKeptByTheSystem(t *testing.T) {
	plist := units.LaunchdPlist("dev.errand.x", []string{"/a b/errand", "kept"}, "127.0.0.1", 12345, map[string]string{"PATH": "/bin&"}, "/log")
	for _, want := range []string{"<string>/a b/errand</string>", "<string>12345</string>", "/bin&amp;", "inetdCompatibility"} {
		if !strings.Contains(plist, want) {
			t.Errorf("the plist does not say %q: %s", want, plist)
		}
	}
	sock, svc := units.SystemdUnits("dev.errand.x", []string{"/a b/errand", "kept"}, "127.0.0.1", 12345, nil)
	if !strings.Contains(sock, "ListenStream=127.0.0.1:12345") || !strings.Contains(svc, "ExecStart='/a b/errand' 'kept'") {
		t.Errorf("the units: %s %s", sock, svc)
	}
}

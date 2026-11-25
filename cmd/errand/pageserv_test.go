package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/errand-run/errand/internal/units"
)

func TestAPageAsksForItsServices(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	config := filepath.Join(filepath.Dir(p.root), "config")
	p.env = append(p.env, "XDG_CONFIG_HOME="+config, "ERRAND_NO_BROWSER=1")
	srv := p.write("srv.py", `import http.server, sys
class H( http.server.BaseHTTPRequestHandler ):
    def do_GET( self ):
        n = int( self.headers.get( "Content-Length" ) or 0 )
        body = f"{self.command} {self.path} root={self.headers.get( 'X-Errand-Root' )} body={self.rfile.read( n ).decode()}".encode()
        self.send_response( 200 ); self.send_header( "Content-Length", str( len( body ) ) ); self.end_headers(); self.wfile.write( body )
    do_POST = do_GET
    def log_message( self, *a ): pass
http.server.ThreadingHTTPServer( ( "127.0.0.1", int( sys.argv[ 1 ] ) ), H ).serve_forever()
`)
	_ = os.MkdirAll(filepath.Join(config, "errand"), 0o755)
	_ = os.WriteFile(filepath.Join(config, "errand", "services.py"), []byte(`import errand
errand.services[ "field#3fa29c1e" ] = errand.Service( [ "python3", "`+srv+`", "{port}" ] )
`), 0o644)
	store := filepath.Join(p.root, "foam.sviz")
	p.write("foam.sviz/index.html", "<p>foam</p>")
	p.write("foam.sviz/services.json", `{"format": 2, "services": {"field": {"version": "3fa29c1e", "protocol": 1, "optional": true, "package": {"name": "mytool"}, "described": {"text": "a field"}},
 "render": {"version": "aaaa1111", "protocol": 1, "package": {"name": "other"}, "described": {"text": "draws"}}}}`)
	defer p.errand("", "web", "stop")
	o := p.errand("", "open", store)
	base := strings.TrimSpace(o.out)
	if o.code != 0 || !strings.HasPrefix(base, "http://127.0.0.1:") || !strings.Contains(get(t, base), "foam") {
		t.Fatalf("errand open: %d %q %q", o.code, o.out, o.err)
	}
	var services map[string]map[string]any
	body := get(t, base+".errand/services")
	if json.Unmarshal([]byte(body), &services) != nil {
		t.Fatalf(".errand/services: %s", body)
	}
	if f := services["field"]; f["version"] != "3fa29c1e" || (f["state"] != "ready" && f["state"] != "unverified") || f["text"] != "a field" {
		t.Errorf("a service this machine declares: %v", services["field"])
	}
	if f := services["render"]; f["state"] == "ready" || f["state"] == nil {
		t.Errorf("one it does not: %v", f)
	}
	if got := get(t, base+".errand/machines"); !strings.Contains(got, `"here":true`) {
		t.Errorf("the machines offered: %s", got)
	}
	if got := get(t, base+".errand/svc/field/at/x?y=1"); !strings.HasPrefix(got, "GET /at/x?y=1 root="+store) {
		t.Errorf("relayed: %q", got)
	}
	r, err := http.Post(base+".errand/svc/field/p", "text/plain", strings.NewReader("sent"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	if !bytes.Contains(b, []byte("POST /p")) || !bytes.Contains(b, []byte("body=sent")) {
		t.Errorf("with a body: %q", b)
	}
	if o = p.errand("", "serve"); !strings.Contains(o.out, "field#3fa29c1e") {
		t.Errorf("it runs as any service: %q", o.out)
	}
	for path, want := range map[string]int{".errand/svc/nobody/x": 403, ".errand/svc/render/x": 403, ".errand/nothing": 404} {
		if r, _ := http.Get(base + path); r.StatusCode != want {
			t.Errorf("%s: %d, not %d", path, r.StatusCode, want)
		}
	}
	// the command line says the same, and what to type
	if o = p.errand("", "serve", "look", store); o.code != 0 || !strings.Contains(o.out, "field#3fa29c1e") || !strings.Contains(o.out, "render#aaaa1111") {
		t.Errorf("serve look: %d %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "serve", "probe", "--", "sh", "-c", "echo tool 1.2.3"); o.code != 0 || !strings.Contains(o.out, "1.2.3") {
		t.Errorf("serve probe: %d %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "serve", "install", "field2#bbbb2222", "--", "python3", srv, "{port}"); o.code != 0 || !strings.Contains(o.out, "declared on here") || !strings.Contains(o.out, "unverified") {
		t.Errorf("serve install: %d %q %q", o.code, o.out, o.err)
	}
	time.Sleep(20 * time.Millisecond)
	if o = p.errand("", "serve"); !strings.Contains(o.out, "field2#bbbb2222") {
		t.Errorf("installed, it is declared: %q %q", o.out, o.err)
	}
	if o = p.errand("", "serve", "list", "--versions"); o.code != 0 || !strings.Contains(o.out, "field2#bbbb2222") {
		t.Errorf("the versions: %d %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "serve", "pin", "field2#bbbb2222"); o.code != 0 {
		t.Errorf("pin: %q", o.err)
	}
	if o = p.errand("", "serve", "prune", "--dry-run"); o.code != 0 {
		t.Errorf("prune: %d %q", o.code, o.err)
	}
}

func TestPortsKeptByTheSystem(t *testing.T) {
	plist := units.LaunchdPlist("dev.errand.x", []string{"/a b/errand", "kept"}, 12345, map[string]string{"PATH": "/bin&"}, "/log")
	for _, want := range []string{"<string>/a b/errand</string>", "<string>12345</string>", "/bin&amp;", "inetdCompatibility"} {
		if !strings.Contains(plist, want) {
			t.Errorf("the plist does not say %q: %s", want, plist)
		}
	}
	sock, svc := units.SystemdUnits("dev.errand.x", []string{"/a b/errand", "kept"}, 12345, nil)
	if !strings.Contains(sock, "ListenStream=127.0.0.1:12345") || !strings.Contains(svc, "ExecStart='/a b/errand' 'kept'") {
		t.Errorf("the units: %s %s", sock, svc)
	}
}

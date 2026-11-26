package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
)

const aServer = `import http.server, json, os, socketserver, sys, time
open( sys.argv[ 2 ], "a" ).write( "started\n" )
class H( http.server.BaseHTTPRequestHandler ):
    def do_GET( self ):
        if self.path == "/slow": time.sleep( 1.2 )
        body = json.dumps( { "path": self.path, "models": [ 1 ] if os.path.exists( sys.argv[ 2 ] + ".busy" ) else [ ], "x": os.environ.get( "X" ) } ).encode()
        self.send_response( 200 ); self.send_header( "Content-Length", str( len( body ) ) ); self.end_headers(); self.wfile.write( body )
    def log_message( self, *a ): pass
class S( http.server.ThreadingHTTPServer ):
    server_bind = socketserver.TCPServer.server_bind # ( HTTPServer's looks its address up before it listens: 30 s on some Macs )
S( ( "127.0.0.1", int( sys.argv[ 1 ] ) ), H ).serve_forever()
`

// leaseHash: the hash of the server a lease leads to -- what `connect` is given
func leaseHash(t *testing.T, p *scene, label string) string {
	t.Helper()
	o := p.errand("", "serve", label, "--json")
	var got struct{ Config string }
	if json.Unmarshal([]byte(o.out), &got) != nil || got.Config == "" {
		t.Fatalf("the lease of %s: %q %q", label, o.out, o.err)
	}
	return got.Config
}

func get(t *testing.T, url string) string {
	t.Helper()
	r, err := http.Get(url)
	if err != nil {
		return "no answer: " + err.Error()
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func TestAServerKeptWhileUsed(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	config := filepath.Join(filepath.Dir(p.root), "config")
	p.env = append(p.env, "XDG_CONFIG_HOME="+config)
	p.write("srv.py", aServer)
	srv, log := filepath.Join(p.root, "srv.py"), filepath.Join(filepath.Dir(p.root), "srv.log")
	_ = os.WriteFile(log, nil, 0o644)
	count := func() int { return strings.Count(read(t, log), "started") }

	// asked for: a port, which starts it, never twice
	one := func(more ...string) outcome {
		return p.errand("", append(append([]string{"serve", "--name", "one", "--idle", "1h"}, more...), "--", "env", "X=1", "python3", srv, "{port}", log)...)
	}
	o := one()
	if o.code != 0 || !strings.Contains(o.out, "( rests, kept 23 h") || count() != 0 {
		t.Errorf("nothing is started by the request: %q %q, %d started", o.out, o.err, count())
	}
	o = p.errand("", "serve", "--name", "one", "--idle", "1h", "--", "env", "X=1", "python3", srv, "{port}", log)
	if o.code != 0 || !strings.HasPrefix(o.out, "one  http://127.0.0.1:") {
		t.Fatalf("a service: %d %q %q", o.code, o.out, o.err)
	}
	url := strings.Fields(o.out)[1]
	if got := get(t, url+"/hello"); !strings.Contains(got, `"path": "/hello"`) || !strings.Contains(got, `"x": "1"`) {
		t.Errorf("it answers, through errand: %q", got)
	}
	if o = p.errand("", "serve", "one", "--url"); o.out != url+"\n" || count() != 1 {
		t.Errorf("asked again, the same one: %q %q, %d started", o.out, o.err, count())
	}
	if o = p.errand("", "serve"); !strings.Contains(o.out, "one") || !strings.Contains(o.out, "warm") {
		t.Errorf("listed: %q", o.out)
	}
	if o = p.errand("", "status"); !strings.Contains(o.out, "service one warm") {
		t.Errorf("and by status: %q", o.out)
	}
	if o = p.errand("GET /x HTTP/1.0\r\n\r\n", "serve", "connect", leaseHash(t, p, "one")); !strings.Contains(o.out, `"path": "/x"`) {
		t.Errorf("connect: %d %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "serve", "stop", "one"); o.code != 0 || !strings.Contains(o.out, "stopped") {
		t.Errorf("stop: %q %q", o.out, o.err)
	}
	if o = one("--url"); o.out != url+"\n" || count() != 1 {
		t.Errorf("stopped, its port stays: %q, not %q; %d started", o.out, url, count())
	}
	if got := get(t, url+"/again"); !strings.Contains(got, `"path": "/again"`) || count() != 2 {
		t.Errorf("a connection starts it again: %q, %d started", got, count())
	}
	// another definition of that label is another server, at another port; how long it is kept, or its label, is not of it
	if o = one("--url", "--var", "Y=2"); o.out == url+"\n" || !strings.HasPrefix(o.out, "http") {
		t.Errorf("another definition: %q %q", o.out, o.err)
	}
	if o = p.errand("", "serve", "--name", "uno", "--idle", "9m", "--keep", "2d", "--grace", "4s", "--url", "--", "env", "X=1", "python3", srv, "{port}", log); o.out != url+"\n" {
		t.Errorf("kept otherwise, the same: %q %q", o.out, o.err)
	}
	var lease struct {
		Url, State, Config, Label string
		Ports                     map[string]int
		Until                     float64
		Again                     float64 `json:"again_in"`
	}
	if o = one("--json"); json.Unmarshal([]byte(o.out), &lease) != nil || lease.Url != url || lease.Label != "one" || lease.State != "warm" || lease.Again < 80000 || lease.Ports["port"] < 20000 || lease.Ports["port"] > 29999 {
		t.Errorf("--json: %q", o.out)
	}
	if o = p.errand("", "serve", "release", "one~"+lease.Config, "--now"); o.code != 0 || !strings.Contains(o.out, "released") {
		t.Errorf("release: %q %q", o.out, o.err)
	}
	if got := get(t, url); !strings.HasPrefix(got, "no answer") {
		t.Errorf("released, nothing listens: %q", got)
	}
	if o = one("--url"); o.out != url+"\n" {
		t.Errorf("its number is kept for it: %q, not %q", o.out, url)
	}
	p.errand("", "serve", "release", "one", "--now")

	// said when it is declared, not when it starts
	for _, c := range [][]string{{"--port", "8000:8000", "--", "x"}, {"--", "x", "{port:nope}"}, {"--port", "A=1", "--", "x"}, {"--name", "a b", "--", "x"}} {
		if o = p.errand("", append([]string{"serve"}, c...)...); o.code != 2 {
			t.Errorf("%q: exit %d %q", c, o.code, o.err)
		}
	}
	if o = p.errand("", "serve", "--wait", "--name", "bad", "--", "sh", "-c", "echo no >&2; exit 3"); o.code != 1 || !strings.Contains(o.err, "it ended before it answered ( exit status 3 )") {
		t.Errorf("a server that ends: %d %q", o.code, o.err)
	}
	if o = p.errand("", "serve", "--json", "--name", "bad", "--", "sh", "-c", "echo no >&2; exit 3"); !strings.Contains(o.out, `"state":"failed"`) || !strings.Contains(o.out, "exit status 3") {
		t.Errorf("why it did not start is said: %q", o.out)
	}
	p.errand("", "serve", "release", "bad")
	for what, args := range map[string][]string{"no such program": {"--", "nosuchprogram"}, "too much": {"--cpus", "99999", "--", "sh"}} {
		if o = p.errand("", append([]string{"serve"}, args...)...); o.code != 1 {
			t.Errorf("%s: %d %q", what, o.code, o.err)
		}
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	if o = p.errand("", "serve", "--name", "fixed", "--port", fmt.Sprint(lis.Addr().(*net.TCPAddr).Port), "--", "sh"); o.code != 1 || !strings.Contains(o.err, "another program") {
		t.Errorf("a number that is taken: %d %q", o.code, o.err)
	}
	if o = p.errand("", "serve", "nobody"); o.code != 1 || !strings.Contains(o.err, `no lease "nobody" on`) {
		t.Errorf("a name nobody asked for: %d %q", o.code, o.err)
	}

	// several ports, a probe that looks at a field of an answer; by its name, what was asked for in full
	two := func(more ...string) outcome {
		return p.errand("", append(append([]string{"serve", "--name", "two", "--port", "a=", "--port", "b=", "--cpus", "1", "--idle", "1500ms", "--busy-http", "/api:models", "--var", "X={port:a}"}, more...), "--",
			"sh", "-c", "python3 "+srv+" {port:a} "+log+" & exec python3 "+srv+" {port:b} "+log)...)
	}
	if o = two("--wait"); o.code != 0 || !strings.Contains(o.out, "a http://127.0.0.1:") || !strings.Contains(o.out, "b http://127.0.0.1:") {
		t.Fatalf("several ports: %d %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "serve", "two", "--url", "b"); !strings.Contains(get(t, strings.TrimSpace(o.out)+"/b"), `"path": "/b"`) {
		t.Errorf("a port by its name: %q", o.out)
	}
	_ = os.WriteFile(log+".busy", nil, 0o644)
	for i := 0; i < 40; i++ {
		if o = p.errand("", "serve"); strings.Contains(o.out, "busy") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(o.out, "busy") {
		t.Errorf("its probe says busy: %q", o.out)
	}
	time.Sleep(2 * time.Second) // ( longer than `idle`: busy is used )
	if o = p.errand("", "status"); !strings.Contains(o.out, "service two busy") {
		t.Errorf("busy, it stays: %q", o.out)
	}
	_ = os.Remove(log + ".busy")
	for i := 0; i < 60; i++ {
		if o = p.errand("", "status"); !strings.Contains(o.out, "service two") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if strings.Contains(o.out, "service two") {
		t.Errorf("unused, it stops: %q", o.out)
	}
	p.errand("", "serve", "release", "two")
	_ = os.MkdirAll(filepath.Join(config, "errand"), 0o755)
	_ = os.WriteFile(filepath.Join(config, "errand", "services.py"), nil, 0o644)
	if o = p.errand("", "serve"); !strings.Contains(o.err, "is not read any more") {
		t.Errorf("a file of before is said: %q %q", o.out, o.err)
	}

	// kept warm, it gives way to what needs its share
	if o = p.errand("", "serve", "--wait", "--name", "warm", "--cpus", "1", "--idle", "1h", "--", "python3", srv, "{port}", log); o.code != 0 {
		t.Fatalf("with a claim: %q", o.err)
	}
	for i := 0; i < 30; i++ {
		if o = p.errand("", "queue"); strings.Contains(o.out, "gives way") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(o.out, "warm ( service )") {
		t.Errorf("its claim: %q", o.out)
	}
	if o = p.errand("", "run", "-q", "-x", "true"); o.code != 0 {
		t.Errorf("a run that needs the machine: %d %q", o.code, o.err)
	}
	if o = p.errand("", "status"); strings.Contains(o.out, "service warm") {
		t.Errorf("it gave way: %q", o.out)
	}

	// what is said wrong is refused before anything is done
	for _, c := range [][]string{{"--busy-http", "api"}, {"--busy-http", "/api:a b"}, {"--var", "X"}, {"--on", "box", "--idle", "soon"}} {
		if o = p.errand("", append(append([]string{"serve"}, c...), "--", "sh")...); o.code == 0 {
			t.Errorf("%q: %d %q", c, o.code, o.err)
		}
	}
}

// A server of another machine is reached by a port of this one; two machines that ask for the same one share it.
func TestAServerOfAnotherMachine(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	py, _ := exec.LookPath("python3")
	srv, log := p.write("srv.py", aServer), filepath.Join(filepath.Dir(p.root), "srv.log")
	_ = os.WriteFile(log, nil, 0o644)
	count := func() int { return strings.Count(read(t, log), "started") }
	ask := func(more ...string) outcome {
		return p.errand("", append(append([]string{"serve", "--on", "box", "--name", "far", "--idle", "1h", "--var", "X=1"}, more...), "--", py, srv, "{port}", log)...)
	}
	o := ask("--url")
	url := strings.TrimSpace(o.out)
	if o.code != 0 || !strings.HasPrefix(url, "http://127.0.0.1:2") || count() != 0 {
		t.Fatalf("a port here: %d %q %q", o.code, o.out, o.err)
	}
	if got := get(t, url+"/far"); !strings.Contains(got, `"path": "/far"`) || !strings.Contains(got, `"x": "1"`) || count() != 1 {
		t.Errorf("which leads there: %q, %d started", got, count())
	}
	if o = p.errand("", "serve"); !strings.Contains(o.out, "far") || !strings.Contains(o.out, "on box") {
		t.Errorf("listed here: %q", o.out)
	}
	if o = p.errand("", "serve", "list", "--host", "box"); !strings.Contains(o.out, "far") || !strings.Contains(o.out, "warm") {
		t.Errorf("and there, where it runs: %q %q", o.out, o.err)
	}
	if o = ask("--json"); !strings.Contains(o.out, `"state":"warm"`) || !strings.Contains(o.out, `"on":"box"`) {
		t.Errorf("how it is, is said from there: %q", o.out)
	}
	// the machine itself, or another, that asks for the same
	there := func(args ...string) outcome {
		was := p.env
		defer func() { p.env = was }()
		p.env = append(append([]string{}, was...), "HOME="+p.far+"/home", "ERRAND_CACHE_DIR="+p.far+"/cache", "XDG_RUNTIME_DIR="+p.far+"/queue", "SHELL=/bin/sh")
		return p.errand("", args...)
	}
	if o = there("serve", "--wait", "--name", "mine", "--idle", "2h", "--var", "X=1", "--", py, srv, "{port}", log); o.code != 0 || count() != 1 {
		t.Errorf("the same one for another machine: %d %q %q, %d started", o.code, o.out, o.err, count())
	}
	if o = p.errand("", "serve", "stop", "far"); !strings.Contains(o.out, "stopped") {
		t.Errorf("stopped from here: %q %q", o.out, o.err)
	}
	if got := get(t, url+"/again"); !strings.Contains(got, `"path": "/again"`) || count() != 2 {
		t.Errorf("its port stays: %q, %d started", got, count())
	}
	for what, c := range map[string][2]string{"no such program there": {"nosuchprogram", "nosuchprogram"}} {
		if o = p.errand("", "serve", "--on", "box", "--", c[0]); o.code != 1 || !strings.Contains(o.err, "box: ") || !strings.Contains(o.err, c[1]) {
			t.Errorf("%s: %d %q", what, o.code, o.err)
		}
	}
	if strings.Contains(o.err, "box: box:") {
		t.Errorf("which machine, said once: %q", o.err)
	}
	if o = p.errand("", "serve", "--on", "nowhere", "--", "sh"); o.code != 1 || !strings.Contains(o.err, "cannot reach") {
		t.Errorf("a machine that is not there: %d %q", o.code, o.err)
	}
	if o = p.errand("", "serve", "--wait", "--on", "box", "--name", "bad", "--", "sh", "-c", "exit 3"); o.code != 1 || !strings.Contains(o.err, "exit status 3") {
		t.Errorf("one that does not start there: %d %q", o.code, o.err)
	}
	p.errand("", "serve", "release", "far", "bad", "--now")
}

// The layers of an environment are the server's, in full; what they do not know how to do yet is said.
func TestAServerInLayers(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	srv, log := p.write("srv.py", aServer), filepath.Join(filepath.Dir(p.root), "srv.log")
	p.write("errand-envs.py", `import errand
errand.envs[ "v" ] = errand.Env( [ errand.Vars( { "X": "of the layer" } ) ] )
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( "box" ), errand.Vars( { "X": "of the layer, there" } ) ] )
errand.envs[ "d" ] = errand.Env( [ errand.Docker( "alpine" ) ] )
`)
	ask := func(env string, more ...string) outcome {
		return p.errand("", append(append([]string{"serve", "--env", env, "--name", "l-" + env, "--url"}, more...), "--", "python3", srv, "{port}", log)...)
	}
	for env, want := range map[string]string{"v": "of the layer", "box": "of the layer, there"} {
		if o := ask(env); !strings.Contains(get(t, strings.TrimSpace(o.out)+"/x"), `"x": "`+want+`"`) {
			t.Errorf("%s: %q %q", env, o.out, o.err)
		}
		if o := ask(env, "--var", "X=written"); !strings.Contains(get(t, strings.TrimSpace(o.out)+"/x"), `"x": "written"`) {
			t.Errorf("%s: what is written comes after: %q %q", env, o.out, o.err)
		}
	}
	if o := ask("d"); o.code != 2 || !strings.Contains(o.err, "a server in a Docker layer: not yet") {
		t.Errorf("a container: %d %q", o.code, o.err)
	}
	p.errand("", "serve", "release", "l-v", "l-box", "--now")
}

// A server starts in the surroundings of a login shell, not in those of whatever started its agent.
func TestAServersSurroundings(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	srv, log := p.write("srv.py", aServer), filepath.Join(filepath.Dir(p.root), "srv.log")
	p.env = append(p.env, "X=of the terminal")
	o := p.errand("", "serve", "--name", "s", "--url", "--", "python3", srv, "{port}", log)
	if got := get(t, strings.TrimSpace(o.out)); !strings.Contains(got, `"x": null`) {
		t.Errorf("%q %q", got, o.err)
	}
	p.errand("", "serve", "release", "s", "--now")
}

// a server that starts and answers when it is told to: `up` starts it, `go` lets it answer. It notes the requests it got.
const aSlowServer = `import os, socket, sys, threading, time
port, at = int( sys.argv[ 1 ] ), sys.argv[ 2 ]
def told( what ):
    while not os.path.exists( at + what ): time.sleep( 0.05 )
def one( c ):
    got = b""
    while b"\n" not in got:
        data = c.recv( 65536 )
        if not data: return
        got += data
    open( at + ".log", "ab" ).write( got )
    told( ".go" )
    c.sendall( b"answer to " + got.strip() + b"\n" )
    c.close()
told( ".up" )
s = socket.socket(); s.setsockopt( socket.SOL_SOCKET, socket.SO_REUSEADDR, 1 ); s.bind( ( "127.0.0.1", port ) ); s.listen( 50 )
while True: threading.Thread( target = one, args = ( s.accept()[ 0 ], ), daemon = True ).start()
`

func TestAClientThatEndsItsInputIsAnswered(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("srv.py", aSlowServer)
	srv, at := filepath.Join(p.root, "srv.py"), filepath.Join(filepath.Dir(p.root), "slow")
	p.errand("", "serve", "--name", "slow", "--idle", "1h", "--", "python3", srv, "{port}", at)
	slow := leaseHash(t, p, "slow")

	status := func() string { return p.errand("", "serve").out }
	until := func(what string, ok func(s string) bool) {
		t.Helper()
		s := ""
		for i := 0; i < 60; i++ {
			if s = status(); ok(s) {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Errorf("%s: %q", what, s)
	}
	conns := func(n string) func(string) bool {
		return func(s string) bool { return strings.Contains(s, n+" connection") }
	}
	none := func(s string) bool { return strings.Contains(s, "slow") && !strings.Contains(s, "connection") }

	// a client that leaves while the server starts: its connection is not counted any more, nor waited for
	for _, how := range []string{"killed", "reader gone", "interrupted"} {
		cmd := p.command("serve", "connect", slow)
		cmd.Stdin = strings.NewReader("lost\n")
		out, _ := cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		until(how+": it waits", conns("1"))
		switch how {
		case "killed":
			_ = cmd.Process.Kill()
		case "reader gone":
			_ = out.Close()
		case "interrupted":
			_ = cmd.Process.Signal(os.Interrupt)
		}
		until(how+": it waits no more", none)
		ended := make(chan struct{})
		go func() { _ = cmd.Wait(); close(ended) }()
		select {
		case <-ended:
		case <-time.After(5 * time.Second):
			t.Errorf("%s: connect stays", how)
			_ = cmd.Process.Kill()
		}
	}

	// the server starts, the input has ended already: what was sent is kept, and answered
	done := make(chan outcome, 1)
	go func() { done <- p.errand("early\n", "serve", "connect", slow) }()
	until("it waits", conns("1"))
	time.Sleep(300 * time.Millisecond)
	_ = os.WriteFile(at+".up", nil, 0o644)
	_ = os.WriteFile(at+".go", nil, 0o644)
	if o := <-done; o.code != 0 || o.out != "answer to early\n" {
		t.Errorf("while it starts: %d %q %q", o.code, o.out, o.err)
	}
	// ( and nothing of those that left was sent to it )
	if got := read(t, at+".log"); got != "early\n" {
		t.Errorf("it was asked %q", got)
	}

	// it runs: answered, however late
	_ = os.Remove(at + ".go")
	go func() { done <- p.errand("late\n", "serve", "connect", slow) }()
	until("it is used", conns("1"))
	time.Sleep(500 * time.Millisecond)
	_ = os.WriteFile(at+".go", nil, 0o644)
	if o := <-done; o.code != 0 || o.out != "answer to late\n" {
		t.Errorf("while it runs: %d %q %q", o.code, o.out, o.err)
	}

	// the same through the announced port
	_ = os.Remove(at + ".go")
	url := strings.TrimSpace(p.errand("", "serve", "slow", "--url").out)
	c, err := net.Dial("tcp", strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("port\n"))
	_ = c.(*net.TCPConn).CloseWrite()
	until("the port is used", conns("1"))
	_ = os.WriteFile(at+".go", nil, 0o644)
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if b, err := io.ReadAll(c); err != nil || string(b) != "answer to port\n" {
		t.Errorf("through its port: %q %v", b, err)
	}
	c.Close()

	// a client that leaves while the server thinks: likewise not counted any more
	_ = os.Remove(at + ".go")
	for _, how := range []string{"killed", "reader gone"} {
		cmd := p.command("serve", "connect", slow)
		cmd.Stdin = strings.NewReader("gone\n")
		out, _ := cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		until(how+": the server thinks", conns("1"))
		time.Sleep(300 * time.Millisecond)
		if how == "killed" {
			_ = cmd.Process.Kill()
		} else {
			_ = out.Close()
		}
		until(how+": it is not waited for", none)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}

	// more than a link holds at once, sent while the server starts: kept, and the link is not held for it
	p.errand("", "serve", "stop", "slow")
	_ = os.Remove(at + ".up")
	_ = os.WriteFile(at+".go", nil, 0o644)
	big := strings.Repeat("x", 12<<20)
	go func() { done <- p.errand(big+"\n", "serve", "connect", slow) }()
	until("it waits with much to send", conns("1"))
	time.Sleep(time.Second)
	_ = os.WriteFile(at+".up", nil, 0o644)
	if o := <-done; o.code != 0 || o.out != "answer to "+big+"\n" {
		t.Errorf("much to send: %d, %d bytes %q", o.code, len(o.out), o.err)
	}
	p.errand("", "serve", "stop", "slow")
}

// a server that does what Go's `net/http` does: the end of a client's input, before the answer, cancels it; after, it closes
const aServerThatCancels = `import os, socket, sys, threading, time
port, at = int( sys.argv[ 1 ] ), sys.argv[ 2 ]
def one( c ):
    got = b""
    while b"\n" not in got:
        data = c.recv( 65536 )
        if not data: return
        got += data
    c.settimeout( 0.05 )
    while True:
        try: data = c.recv( 1 )
        except socket.timeout: data = None
        if data == b"":
            open( at + ".canceled", "a" ).write( "x" )
            return c.sendall( b"canceled\n" ), c.close()
        if os.path.exists( at + ".go" ): break
    for word in b"answer ", b"in ", b"parts\n":
        c.sendall( word ); time.sleep( 0.4 )
        try:
            if c.recv( 1 ) == b"":
                return c.sendall( b"canceled\n" ), c.close()
        except socket.timeout: pass
    c.settimeout( None )
    while c.recv( 65536 ): pass          # kept alive, until its client ends
    c.close()
s = socket.socket(); s.bind( ( "127.0.0.1", port ) ); s.listen( 50 )
while True: threading.Thread( target = one, args = ( s.accept()[ 0 ], ), daemon = True ).start()
`

func TestTheEndOfTheInputHeldUntilTheAnswer(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	srv, at := p.write("srv.py", aServerThatCancels), filepath.Join(filepath.Dir(p.root), "srv")
	p.errand("", "serve", "--name", "held", "--idle", "1h", "--", "python3", srv, "{port}", at)
	held := leaseHash(t, p, "held")
	ask := func(how string, args ...string) (string, time.Duration) {
		t.Helper()
		began := time.Now()
		done := make(chan outcome, 1)
		_ = os.Remove(at + ".go")
		go func() { done <- p.errand("ask\n", append([]string{"serve", "connect"}, args...)...) }()
		time.Sleep(2500 * time.Millisecond) // ( longer than `hold_end`: what has not been answered is not over )
		_ = os.WriteFile(at+".go", nil, 0o644)
		select {
		case o := <-done:
			return o.out, time.Since(began)
		case <-time.After(15 * time.Second):
			t.Errorf("%s: connect stays", how)
			return "", 0
		}
	}
	p.errand("", "serve", "held")
	if got, _ := ask("passed", held); got != "canceled\n" {
		t.Errorf("passed on, the end cancels: %q", got)
	}
	if got, took := ask("held", held, "--hold-end", "1500ms"); got != "answer in parts\n" || took < 4*time.Second {
		t.Errorf("held, it is answered, then closed once quiet: %q in %s", got, took)
	}
	if o := p.errand("", "serve"); strings.Contains(o.out, "connection") {
		t.Errorf("not counted any more: %q", o.out)
	}

	// the server is another version's, whose agent keeps it: nothing is asked of that one
	log := strings.Fields(p.errand("", "serve").out)
	p.program = builtNext
	if got, _ := ask("another version", held, "--hold-end", "1500ms"); got != "answer in parts\n" {
		t.Errorf("from another version: %q", got)
	}
	if o := p.errand("", "serve"); len(strings.Fields(o.out)) < 3 || strings.Fields(o.out)[2] != log[2] || strings.Contains(o.out, "connection") {
		t.Errorf("the same server, not used any more: %q, not %q", o.out, log)
	}
	p.program = ""

	// an input that ends with nothing asked is passed on at once; a client that leaves is no longer waited for
	began := time.Now()
	if o := p.errand("", "serve", "connect", "--hold-end", "1500ms", held); o.code != 0 || time.Since(began) > 1400*time.Millisecond {
		t.Errorf("nothing asked: %d in %s", o.code, time.Since(began))
	}
	_ = os.Remove(at + ".go")
	cmd := p.command("serve", "connect", held, "--hold-end", "1500ms")
	cmd.Stdin = strings.NewReader("ask\n")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	o := p.errand("", "serve")
	for i := 0; i < 40 && strings.Contains(o.out, "connection"); i++ {
		time.Sleep(100 * time.Millisecond)
		o = p.errand("", "serve")
	}
	if strings.Contains(o.out, "connection") {
		t.Errorf("its client left: %q", o.out)
	}

	// an announced port passes it on: there, the end is how a client that left is known
	_ = os.Remove(at + ".canceled")
	url := strings.TrimSpace(p.errand("", "serve", "held", "--url").out)
	c, err := net.Dial("tcp", strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("ask\n"))
	c.Close()
	for i := 0; i < 40; i++ {
		if _, err = os.Stat(at + ".canceled"); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Error("through its port, a client that left cancels what it asked")
	}

	// a port can hold it back too: another port, to the same server
	_ = os.Remove(at + ".go")
	o = p.errand("", "serve", "--name", "held", "--idle", "1h", "--hold-end", "1500ms", "--json", "--", "python3", srv, "{port}", at)
	var lease struct {
		URL    string
		Config string
	}
	if json.Unmarshal([]byte(o.out), &lease) != nil || lease.Config != held || "http://"+strings.TrimPrefix(lease.URL, "http://") == url {
		t.Fatalf("a port of its own, to the same server: %q %q, not %s", o.out, o.err, url)
	}
	c, err = net.Dial("tcp", strings.TrimPrefix(lease.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("ask\n"))
	_ = c.(*net.TCPConn).CloseWrite()
	time.Sleep(2500 * time.Millisecond)
	_ = os.WriteFile(at+".go", nil, 0o644)
	_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
	if got, _ := io.ReadAll(c); string(got) != "answer in parts\n" {
		t.Errorf("held by its port, it is answered: %q", got)
	}
	c.Close()
	p.errand("", "serve", "stop", "held")
	// a connection says it, or a port; not a server
	for _, c := range [][]string{{"connect", held, "--hold-end", "nope"}, {"connect", "held"}, {"held", "--hold-end", "2s"}} {
		if o := p.errand("", append([]string{"serve"}, c...)...); o.code != 2 {
			t.Errorf("%q: exit %d %q", c, o.code, o.err)
		}
	}
}

// A port is taken over by the next agent, of this version or another, and by the one that comes after an agent that was killed.
func TestAPortOutlivesItsAgent(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	srv, log := p.write("srv.py", aServer), filepath.Join(filepath.Dir(p.root), "srv.log")
	args := []string{"serve", "--name", "one", "--idle", "1h", "--", "python3", srv, "{port}", log}
	url := strings.Fields(p.errand("", args...).out)[1]
	if got := get(t, url+"/a"); !strings.Contains(got, `"path": "/a"`) {
		t.Fatalf("it answers: %q", got)
	}
	p.program = builtNext
	if o := p.errand("", args...); strings.Fields(o.out)[1] != url {
		t.Errorf("from another version, the same port: %q", o.out)
	}
	if got := get(t, url+"/b"); !strings.Contains(got, `"path": "/b"`) || strings.Count(read(t, log), "started") != 1 {
		t.Errorf("which leads to the same server: %q, %q", got, read(t, log))
	}
	p.program = ""
	if got := get(t, url+"/c"); !strings.Contains(got, `"path": "/c"`) {
		t.Errorf("and goes on: %q", got)
	}
	_ = exec.Command("pkill", "-9", "-f", filepath.Join(p.cache(), "bin")).Run()
	time.Sleep(300 * time.Millisecond)
	if got := get(t, url); !strings.HasPrefix(got, "no answer") {
		t.Errorf("its agents were killed: %q", got)
	}
	p.errand("", "status") // any request starts an agent, which answers while it opens the ports again
	got := get(t, url+"/d")
	for i := 0; i < 30 && strings.HasPrefix(got, "no answer"); i++ {
		time.Sleep(100 * time.Millisecond)
		got = get(t, url+"/d")
	}
	if !strings.Contains(got, `"path": "/d"`) {
		t.Errorf("the port is there again: %q", got)
	}
	p.errand("", "serve", "release", "one", "--now")

	// a lease that is over
	o := p.errand("", "serve", "--name", "brief", "--keep", "2s", "--idle", "1s", "--url", "--", "python3", srv, "{port}", log, "brief")
	time.Sleep(4 * time.Second)
	if got := get(t, strings.TrimSpace(o.out)); !strings.HasPrefix(got, "no answer") {
		t.Errorf("its lease is over: %q", got)
	}
}

// A program that asks by errand's Python: its server, started by its first request; found again by another, whose agent was killed.
func TestAServerAskedByAProgram(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	srv, log := p.write("srv.py", aServer), filepath.Join(filepath.Dir(p.root), "srv.log")
	_ = os.WriteFile(log, nil, 0o644)
	lib, _ := filepath.Abs("../../lib")
	p.write("ask.py", `import os, subprocess, sys, time, urllib.request
import errand
srv, log = sys.argv[ 1 : ]
agents = os.environ[ "AGENTS" ] # ( not an argument: Linux's pkill -f would find this program too )
def ask( url ): return urllib.request.urlopen( url, timeout = 20 ).read().decode()
s = errand.serve( [ "python3", srv, "{port}", log ], vars = { "X": 1 }, cpus = 1, idle = "1h", busy = errand.http( "/api", field = "models" ), label = "lib" )
print( s.call( ask, "/one" ) )
first = s.url()
subprocess.run( [ "pkill", "-9", "-f", agents ] ); time.sleep( 0.5 )
print( s.call( ask, "/two" ), s.url() == first, s.state() in ( "warm", "in use" ) )
bad = errand.serve( [ "sh", "-c", "exit 3" ], label = "bad" )
try: bad.call( ask )
except errand.ServeError as err: print( err )
try: errand.serve( "nosuchprogram" ).url()
except errand.ServeError as err: print( err )
far = errand.serve( [ "python3", srv, "{port}", log ], on = [ "box", errand.Vars( { "X": "there" } ) ], idle = "1h", label = "far" )
print( far.wait().call( ask, "/far" ) )
for one in s, bad, far: one.release( now = True )
`)
	cmd := exec.Command("python3", "ask.py", srv, log)
	cmd.Dir, cmd.Env = p.root, append(append([]string{}, p.env...), "PYTHONPATH="+lib, "ERRAND="+built, "AGENTS="+filepath.Join(p.cache(), "bin"))
	var errs strings.Builder
	cmd.Stderr = &errs
	out, err := cmd.Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if err != nil || len(lines) != 5 {
		t.Fatalf("%v %q %q", err, out, errs.String())
	}
	for i, want := range []string{`"path": "/one", "models": [], "x": "1"`, `"path": "/two"`, "bad: it ended before it answered ( exit status 3 )", "nosuchprogram", `"path": "/far", "models": [], "x": "there"`} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("%q, not %q", lines[i], want)
		}
	}
	if !strings.HasSuffix(lines[1], "True True") {
		t.Errorf("after its agent was killed, the same port: %q", lines[1])
	}
	// the same, from the command line, is the same server
	a := p.errand(`{"command": ["python3", "x.py", "{port}"], "cpus": 1, "busy": {"http": "/api", "field": "models"}, "ports": [["a", null], ["b", ":18001"]]}`, "serve", "--spec", "-", "--json")
	b := p.errand("", "serve", "--cpus", "1", "--busy-http", "/api:models", "--port", "a=", "--port", "b=:18001", "--json", "--", "python3", "x.py", "{port:a}")
	if a.code != 2 || !strings.Contains(a.out, "{port} with several ports") {
		t.Errorf("a request that is wrong: %d %q", a.code, a.out)
	}
	a = p.errand(`{"command": ["python3", "x.py", "{port:a}"], "cpus": 1, "busy": {"http": "/api", "field": "models"}, "ports": [["a", null], ["b", ":18001"]]}`, "serve", "--spec", "-", "--json")
	config := func(o outcome) string {
		var said map[string]any
		_ = json.Unmarshal([]byte(o.out), &said)
		return fmt.Sprint(said["config"])
	}
	if config(a) != config(b) || config(a) == "<nil>" {
		t.Errorf("one definition, said in two ways: %q %q", a.out, b.out)
	}
	for _, wrong := range []string{`{"command": ["sh"], "gpu": 1}`, `{"command": ["sh"], "idle": "soon"}`, `{"command": ["sh"], "on": 3}`, `{"command": ["sh"], "ports": {"a": 1}}`, `[]`, ``, `{"command": []}`} {
		if o := p.errand(wrong+" ", "serve", "--spec", "-", "--json"); o.code != 2 || !strings.Contains(o.out, `"error"`) {
			t.Errorf("%s: %d %q %q", wrong, o.code, o.out, o.err)
		}
	}
}

// A server authorized to a key ( ADR 0040 ): reached by its hash, through `connect` or `there --only`, which passes on nothing else.
func TestAServerAuthorizedToAKey(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.env = append(p.env, "XDG_CONFIG_HOME="+filepath.Join(filepath.Dir(p.root), "config"))
	srv, log := p.write("srv.py", aServer), filepath.Join(filepath.Dir(p.root), "srv.log")
	authorize := func(to string, more ...string) string {
		t.Helper()
		o := p.errand("", append(append([]string{"serve", "authorize", "--to", to, "--idle", "1h", "--var", "API_TOKEN=s3cret"}, more...), "--", "python3", srv, "{port}", log)...)
		if o.code != 0 || !strings.Contains(o.out, "authorized to "+to+" on here") {
			t.Fatalf("authorize: %d %q %q", o.code, o.out, o.err)
		}
		return strings.Fields(o.out)[0]
	}
	hash := authorize("key:boucles")
	other := authorize("key:other", "--var", "X=2")
	if again := authorize("key:boucles"); again != hash {
		t.Errorf("the same definition, the same hash: %s, %s", again, hash)
	}
	if o := p.errand("", "serve", "authorize", "--", "true"); o.code != 2 {
		t.Errorf("to whom it is given is said: %d %q", o.code, o.err)
	}
	if o := p.errand("", "serve", "authorize", "--to", "everyone", "--", "true"); o.code == 0 {
		t.Errorf("--to pages or key:NAME: %q", o.out)
	}
	if o := p.errand("", "serve", "authorized"); !strings.Contains(o.out, hash) || !strings.Contains(o.out, "to key:boucles") || !strings.Contains(o.out, "API_TOKEN=…") ||
		strings.Contains(o.out, "s3cret") || !strings.Contains(o.out, "never used") {
		t.Errorf("listed, its secret aside: %q %q", o.out, o.err)
	}

	// by its hash, with nothing more
	if o := p.errand("GET /k HTTP/1.0\r\n\r\n", "serve", "connect", hash); !strings.Contains(o.out, `"path": "/k"`) {
		t.Errorf("connect HASH: %d %q %q", o.code, o.out, o.err)
	}
	if o := p.errand("x", "serve", "connect", "0badc0de"); o.code == 0 || !strings.Contains(o.err, "no server 0badc0de is authorized") {
		t.Errorf("an unknown hash: %d %q %q", o.code, o.out, o.err)
	}
	if o := p.errand("x", "serve", "connect", "srv"); o.code != 2 {
		t.Errorf("by a hash only: %d %q", o.code, o.err)
	}

	// through `there --only`: hello, and the servers given to that key
	cmd := p.command("there", "--only", "key:boucles")
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { in.Close(); _ = cmd.Wait() }()
	if err := reach.AwaitHello(out); err != nil {
		t.Fatal(err)
	}
	l := reach.NewLink(out, in, "there --only")
	if got, err := l.Ask(map[string]any{"op": "hello"}); err != nil || got["only"] != "key:boucles" || got["home"] != nil {
		t.Errorf("hello, and not what it holds: %v %v", got, err)
	}
	for _, req := range []map[string]any{{"op": "run", "argv": []string{"true"}}, {"op": "lease", "spec": map[string]any{"command": []string{"true"}}},
		{"op": "service_connect", "name": "srv"}, {"op": "service_connect", "hash": other}, {"op": "ls", "path": "/"}} {
		if _, err := l.Ask(req); err == nil || !strings.Contains(err.Error(), "key:boucles") {
			t.Errorf("%v: refused, not %v", req, err)
		}
	}
	c := l.Open(map[string]any{"op": "service_connect", "hash": hash, "spec": map[string]any{"command": []string{"sh", "-c", "touch " + log + ".evil"}}})
	if _, err := c.Reply(); err != nil {
		t.Fatal(err)
	}
	_ = c.Send(kit.IN, []byte("GET /only HTTP/1.0\r\n\r\n"))
	answer := ""
	for {
		kind, payload, err := c.Recv()
		if err != nil || kind == kit.EXIT {
			break
		}
		answer += string(payload)
	}
	c.Close()
	if !strings.Contains(answer, `"path": "/only"`) || kit.Exists(log+".evil") {
		t.Errorf("its server, and nothing that was added: %q", answer)
	}
	if o := p.errand("", "serve", "authorized"); strings.Contains(o.out, "refused") || strings.Contains(o.out, "asked for") {
		t.Errorf("what was refused is kept nowhere ( ADR 0041 ): %q", o.out)
	}

	// taken back
	if o := p.errand("", "serve", "withdraw", hash); o.code != 0 {
		t.Errorf("withdraw: %d %q", o.code, o.err)
	}
	if o := p.errand("x", "serve", "connect", hash); o.code == 0 {
		t.Errorf("authorized no more: %q", o.out)
	}
	if o := p.errand("", "serve", "withdraw", other, "--to", "key:boucles"); o.code == 0 {
		t.Errorf("not given to that one: %q", o.out)
	}
	p.errand("", "serve", "stop", hash)
}

// A port listened on at another address: other machines may connect, a machine shared with others refuses it.
func TestAPortListenedOnElsewhere(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	srv, log := p.write("srv.py", aServer), filepath.Join(filepath.Dir(p.root), "srv.log")
	command := []string{"--", "python3", srv, "{port}", log}
	var near, wide struct {
		URL, Config, Address string
		Ports                map[string]int
	}
	_ = json.Unmarshal([]byte(p.errand("", append([]string{"serve", "--name", "wide", "--idle", "1h", "--json"}, command...)...).out), &near)
	o := p.errand("", append([]string{"serve", "--name", "wide", "--idle", "1h", "--listen", "0.0.0.0", "--json"}, command...)...)
	if json.Unmarshal([]byte(o.out), &wide) != nil || wide.Config != near.Config || wide.Ports["port"] == near.Ports["port"] || wide.Address != "127.0.0.1" {
		t.Fatalf("another port, to the same server, reached at 127.0.0.1 from here: %q %q, beside %+v", o.out, o.err, near)
	}
	if got := get(t, wide.URL+"/a"); !strings.Contains(got, `"path": "/a"`) {
		t.Errorf("it answers: %q", got)
	}
	if ip := anAddressOfThisMachine(); ip != "" {
		if got := get(t, fmt.Sprintf("http://%s:%d/b", ip, wide.Ports["port"])); !strings.Contains(got, `"path": "/b"`) {
			t.Errorf("from %s: %q", ip, got)
		}
		if got := get(t, fmt.Sprintf("http://%s:%d/c", ip, near.Ports["port"])); !strings.HasPrefix(got, "no answer") {
			t.Errorf("the other port is this machine's alone: %q", got)
		}
	}
	if o := p.errand("", "serve", "list"); !strings.Contains(o.out, "wide") {
		t.Errorf("listed: %q", o.out)
	}
	if o := p.errand("", append([]string{"serve", "--name", "bad", "--listen", "here", "--json"}, command...)...); !strings.Contains(o.out, "listen: an address") {
		t.Errorf("an address as numbers: %q %q", o.out, o.err)
	}
	p.errand("", "serve", "release", "wide", "--now")

	shared := newPlace(t)
	shared.env = append(shared.env, "ERRAND_SHARED=1")
	shared.env = append(shared.env, python(t)...)
	if o := shared.errand("", append([]string{"serve", "--name", "wide", "--listen", "0.0.0.0", "--json"}, command...)...); !strings.Contains(o.out, "shared with others") {
		t.Errorf("refused on a machine shared with others: %q %q", o.out, o.err)
	}
}

func anAddressOfThisMachine() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil && !n.IP.IsLinkLocalUnicast() {
			return n.IP.String()
		}
	}
	return ""
}

// A port the system listens on: a client that has only the number starts errand, even once its agents were killed.
func TestAPortAlwaysListenedOn(t *testing.T) {
	if runtime.GOOS != "darwin" { // ( systemd --user reads its units where it was told to, not in a test's configuration )
		t.Skip("launchd only")
	}
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	srv, log := p.write("srv.py", aServer), filepath.Join(filepath.Dir(p.root), "srv.log")
	o := p.errand("", "serve", "--name", "kept", "--idle", "1h", "--keep", "1h", "--always", "--url", "--", "python3", srv, "{port}", log)
	url := strings.TrimSpace(o.out)
	if o.code != 0 || !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("a lease: %d %q %q", o.code, o.out, o.err)
	}
	t.Cleanup(func() { p.errand("", "serve", "release", "kept", "--now") })
	if got := get(t, url+"/a"); !strings.Contains(got, `"path": "/a"`) {
		t.Fatalf("it answers, through the system: %q", got)
	}
	_ = exec.Command("pkill", "-9", "-f", filepath.Join(p.cache(), "bin")).Run()
	time.Sleep(300 * time.Millisecond)
	if got := get(t, url+"/b"); !strings.Contains(got, `"path": "/b"`) {
		t.Errorf("its agents were killed, and nothing was asked of errand: %q", got)
	}
	if o := p.errand("", "serve", "list"); !strings.Contains(o.out, "kept") {
		t.Errorf("listed: %q", o.out)
	}
	p.errand("", "serve", "release", "kept", "--now")
	time.Sleep(300 * time.Millisecond)
	if got := get(t, url+"/c"); !strings.HasPrefix(got, "no answer") {
		t.Errorf("given back, the system listens no more: %q", got)
	}
}

// The same from Node ( lib/node ): its server, started by its first request; found again once its agent was killed.
func TestAServerAskedFromNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node here")
	}
	if out, err := exec.Command(node, "../../lib/node/test.js").CombinedOutput(); err != nil { // ( against a fake errand )
		t.Errorf("lib/node/test.js: %v %s", err, out)
	}
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	srv, log := p.write("srv.py", aServer), filepath.Join(filepath.Dir(p.root), "srv.log")
	_ = os.WriteFile(log, nil, 0o644)
	lib, _ := filepath.Abs("../../lib/node/index.js")
	p.write("ask.mjs", `import { execFileSync } from "node:child_process";
import { serve, http, ServeError } from "`+lib+`";
const [ srv, log ] = process.argv.slice( 2 );
const agents = process.env.AGENTS; // ( not an argument: Linux's pkill -f would find this program too )
const ask = async url => ( await fetch( url ) ).text();
const s = serve( [ "python3", srv, "{port}", log ], { vars: { X: 1 }, cpus: 1, idle: "1h", busy: http( "/api", { field: "models" } ), label: "node" } );
console.log( await s.call( ask, "/one" ) );
const first = await s.url();
try { execFileSync( "pkill", [ "-9", "-f", agents ] ); } catch {}
await new Promise( r => setTimeout( r, 500 ) );
console.log( await s.call( ask, "/two" ), await s.url() === first, [ "warm", "in use" ].includes( await s.state() ) );
const bad = serve( [ "sh", "-c", "exit 3" ], { label: "bad" } );
try { await bad.call( ask ); } catch ( err ) { console.log( err instanceof ServeError, err.message ); }
try { await serve( [ "nosuchprogram" ] ).url(); } catch ( err ) { console.log( err instanceof ServeError, err.message ); }
try { serve( [ "x" ], { gpu: 1 } ); } catch ( err ) { console.log( err.message ); }
for ( const one of [ s, bad ] ) await one.release( { now: true } );
`)
	cmd := exec.Command(node, "ask.mjs", srv, log)
	cmd.Dir, cmd.Env = p.root, append(append([]string{}, p.env...), "ERRAND="+built, "AGENTS="+filepath.Join(p.cache(), "bin"))
	var errs strings.Builder
	cmd.Stderr = &errs
	out, err := cmd.Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if err != nil || len(lines) != 5 {
		t.Fatalf("%v %q %q", err, out, errs.String())
	}
	for i, want := range []string{`"path": "/one", "models": [], "x": "1"`, `"path": "/two"`, "true bad: it ended before it answered ( exit status 3 )", "true", "gpu is not said of a server"} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("%q, not %q", lines[i], want)
		}
	}
	if !strings.HasSuffix(lines[1], "true true") || !strings.Contains(lines[3], "nosuchprogram") {
		t.Errorf("after its agent was killed, the same port: %q; %q", lines[1], lines[3])
	}
	// the same definition as Python's
	py := p.errand(`{"command": ["python3", "`+srv+`", "{port}", "`+log+`"], "vars": {"X": "1"}, "cpus": 1, "idle": "1h", "busy": {"http": "/api", "field": "models"}, "label": "node"}`, "serve", "--spec", "-", "--json")
	if !strings.Contains(py.out, `"config"`) {
		t.Errorf("%q", py.out)
	}
	p.errand("", "serve", "release", "node", "--now")
}

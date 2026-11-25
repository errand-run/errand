package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const aServer = `import http.server, json, os, sys, time
open( sys.argv[ 2 ], "a" ).write( "started\n" )
class H( http.server.BaseHTTPRequestHandler ):
    def do_GET( self ):
        if self.path == "/slow": time.sleep( 1.2 )
        body = json.dumps( { "path": self.path, "models": [ 1 ] if os.path.exists( sys.argv[ 2 ] + ".busy" ) else [ ], "x": os.environ.get( "X" ) } ).encode()
        self.send_response( 200 ); self.send_header( "Content-Length", str( len( body ) ) ); self.end_headers(); self.wfile.write( body )
    def log_message( self, *a ): pass
http.server.ThreadingHTTPServer( ( "127.0.0.1", int( sys.argv[ 1 ] ) ), H ).serve_forever()
`

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
	count := func() int { return strings.Count(read(t, log), "started") }

	// ad hoc: started on demand, never twice, at an address that is kept
	o := p.errand("", "serve", "--name", "one", "--idle", "1h", "--", "env", "X=1", "python3", srv, "{port}", log)
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
	if o = p.errand("GET /x HTTP/1.0\r\n\r\n", "serve", "connect", "one"); !strings.Contains(o.out, `"path": "/x"`) {
		t.Errorf("connect: %d %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "serve", "stop", "one"); o.code != 0 || !strings.Contains(o.out, "stopped") {
		t.Errorf("stop: %q %q", o.out, o.err)
	}
	if got := get(t, url); !strings.HasPrefix(got, "no answer") {
		t.Errorf("stopped, it does not answer: %q", got)
	}
	if o = p.errand("", "serve", "--name", "one", "--url", "--", "python3", srv, "{port}", log); o.out != url+"\n" {
		t.Errorf("its address is kept: %q, not %q", o.out, url)
	}
	p.errand("", "serve", "stop", "one")

	// said when it is declared, not when it starts
	for _, c := range [][]string{{"--port", "8000:8000", "--", "x"}, {"--", "x", "{port:nope}"}, {"--port", "A=1", "--", "x"}, {"--name", "a b", "--", "x"}} {
		if o = p.errand("", append([]string{"serve"}, c...)...); o.code != 2 {
			t.Errorf("%q: exit %d %q", c, o.code, o.err)
		}
	}
	if o = p.errand("", "serve", "--name", "bad", "--", "sh", "-c", "echo no >&2; exit 3"); o.code != 1 || !strings.Contains(o.err, "it ended before it answered ( exit status 3 )") {
		t.Errorf("a server that ends: %d %q", o.code, o.err)
	}
	if o = p.errand("", "serve", "nobody"); o.code != 1 || !strings.Contains(o.err, `no service "nobody" is declared`) {
		t.Errorf("a service nobody declared: %d %q", o.code, o.err)
	}

	// declared: several ports, a probe that is a function
	_ = os.MkdirAll(filepath.Join(config, "errand"), 0o755)
	_ = os.WriteFile(filepath.Join(config, "errand", "services.py"), []byte(`import errand
errand.services[ "two" ] = errand.Service( [ "sh", "-c", "python3 `+srv+` {port:a} `+log+` & exec python3 `+srv+` {port:b} `+log+`" ],
    ports = { "a": None, "b": None }, cpus = 1, idle = "1500ms", busy = errand.http_json( "/api", lambda r: bool( r[ "models" ] ) ), env = { "X": "{port:a}" } )
`), 0o644)
	if o = p.errand("", "serve", "two"); o.code != 0 || !strings.Contains(o.out, "a http://127.0.0.1:") || !strings.Contains(o.out, "b http://127.0.0.1:") {
		t.Fatalf("declared: %d %q %q", o.code, o.out, o.err)
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
	if o = p.errand("", "serve"); !strings.Contains(o.out, "two") || !strings.Contains(o.out, "declared") {
		t.Errorf("declared, it is listed still: %q", o.out)
	}

	// kept warm, it gives way to what needs its share
	if o = p.errand("", "serve", "--name", "warm", "--cpus", "1", "--idle", "1h", "--", "python3", srv, "{port}", log); o.code != 0 {
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

	// another machine's, from here: a port whose connections go to it, which starts at the first
	cmd := p.command("serve", "forward", "two", "--host", "box")
	out, _ := cmd.StdoutPipe()
	var said strings.Builder
	cmd.Stderr = &said
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(out).ReadString('\n')
	if !strings.HasPrefix(line, "http://127.0.0.1:") {
		t.Fatalf("forward said %q", line)
	}
	if got := get(t, strings.TrimSpace(line)+"/far"); !strings.Contains(got, `"path": "/far"`) {
		t.Errorf("through the forward: %q", got)
	}
	_ = cmd.Process.Signal(os.Interrupt)
	_ = cmd.Wait()
	if t.Failed() {
		t.Log(said.String())
	}
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
	config := filepath.Join(filepath.Dir(p.root), "config", "errand")
	p.write("srv.py", aSlowServer)
	srv, at := filepath.Join(p.root, "srv.py"), filepath.Join(filepath.Dir(p.root), "slow")
	_ = os.MkdirAll(config, 0o755)
	_ = os.WriteFile(filepath.Join(config, "services.py"), []byte("import errand\nerrand.services[ \"slow\" ] = errand.Service( [ \"python3\", \""+srv+"\", \"{port}\", \""+at+"\" ], idle = \"1h\" )\n"), 0o644)

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
		cmd := p.command("serve", "connect", "slow")
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
	go func() { done <- p.errand("early\n", "serve", "connect", "slow") }()
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
	go func() { done <- p.errand("late\n", "serve", "connect", "slow") }()
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
		cmd := p.command("serve", "connect", "slow")
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
	go func() { done <- p.errand(big+"\n", "serve", "connect", "slow") }()
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
	config := filepath.Join(filepath.Dir(p.root), "config", "errand")
	srv, at := p.write("srv.py", aServerThatCancels), filepath.Join(filepath.Dir(p.root), "srv")
	_ = os.MkdirAll(config, 0o755)
	_ = os.WriteFile(filepath.Join(config, "services.py"), []byte(`import errand
errand.services[ "held" ] = errand.Service( [ "python3", "`+srv+`", "{port}", "`+at+`" ], idle = "1h" )
`), 0o644)
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
	if got, _ := ask("passed", "held"); got != "canceled\n" {
		t.Errorf("passed on, the end cancels: %q", got)
	}
	if got, took := ask("held", "held", "--hold-end", "1500ms"); got != "answer in parts\n" || took < 4*time.Second {
		t.Errorf("held, it is answered, then closed once quiet: %q in %s", got, took)
	}
	if o := p.errand("", "serve", "held"); strings.Contains(o.out, "connection") {
		t.Errorf("not counted any more: %q", o.out)
	}

	// the server is another version's, whose agent keeps it: nothing is asked of that one
	log := strings.Fields(p.errand("", "serve").out)
	p.program = builtNext
	if got, _ := ask("another version", "held", "--hold-end", "1500ms"); got != "answer in parts\n" {
		t.Errorf("from another version: %q", got)
	}
	if o := p.errand("", "serve"); len(strings.Fields(o.out)) < 3 || strings.Fields(o.out)[2] != log[2] || strings.Contains(o.out, "connection") {
		t.Errorf("the same server, not used any more: %q, not %q", o.out, log)
	}
	p.program = ""

	// an input that ends with nothing asked is passed on at once; a client that leaves is no longer waited for
	began := time.Now()
	if o := p.errand("", "serve", "connect", "--hold-end", "1500ms", "held"); o.code != 0 || time.Since(began) > 1400*time.Millisecond {
		t.Errorf("nothing asked: %d in %s", o.code, time.Since(began))
	}
	_ = os.Remove(at + ".go")
	cmd := p.command("serve", "connect", "held", "--hold-end", "1500ms")
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
	p.errand("", "serve", "stop", "held")
	// a connection says it, not a server
	for _, c := range [][]string{{"connect", "held", "--hold-end", "nope"}, {"held", "--hold-end", "2s"}, {"--name", "x", "--hold-end", "2s", "--", "true"}} {
		if o := p.errand("", append([]string{"serve"}, c...)...); o.code != 2 {
			t.Errorf("%q: exit %d %q", c, o.code, o.err)
		}
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/errand-run/errand/internal/caches"
)

type browser struct {
	t    *testing.T
	base string
	c    *http.Client
}

func (b browser) get(path string) (int, string) {
	b.t.Helper()
	r, err := b.c.Get(b.base + path)
	if err != nil {
		b.t.Fatal(err)
	}
	defer r.Body.Close()
	data, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(data)
}

func (b browser) json(path string) map[string]any {
	b.t.Helper()
	code, body := b.get(path)
	var out map[string]any
	if code != 200 || json.Unmarshal([]byte(body), &out) != nil {
		b.t.Fatalf("%s: %d %s", path, code, body)
	}
	return out
}

func (b browser) act(body map[string]any, origin string) (int, map[string]any) {
	b.t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", b.base+"/act", bytes.NewReader(data))
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	r, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer r.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(r.Body).Decode(&out)
	return r.StatusCode, out
}

func (b browser) rows(q string) []map[string]any {
	b.t.Helper()
	var out []map[string]any
	for _, x := range caches.ListOf(b.json("/state?" + q)["rows"]) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func rowWith(rows []map[string]any, text string) map[string]any {
	for _, r := range rows {
		if strings.Contains(fmt.Sprint(r["label"]), text) {
			return r
		}
	}
	return nil
}

func TestThePage(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.env = append(p.env, "ERRAND_NO_BROWSER=1")
	p.pytest()
	p.suite()
	p.write("errand-envs.py", "import errand\nerrand.envs[ 'box' ] = errand.Env( [ errand.Ssh( host = 'box', root = 'copy/proj' ) ] )\n")
	p.write("see.sh", `#!/bin/sh
mkdir -p "$ERRAND_OUT_DIR/scene.sviz"; echo "<p>scene</p>" > "$ERRAND_OUT_DIR/scene.sviz/index.html"; echo data > "$ERRAND_OUT_DIR/scene.sviz/d.txt"
echo "<p>hello</p>" > "$ERRAND_OUT_DIR/page.html"
echo '{"t": 1, "text": "half way", "done": 0.5, "residual": 0.25}' >> "$ERRAND_STEPS"
echo '{"t": 2, "view": "page.html", "title": "The page"}' >> "$ERRAND_STEPS"
echo '{"t": 3, "residual": 0.001}' >> "$ERRAND_STEPS"
echo '{"t": 3, "view": "scene.sviz", "open": [{"static": "index.html"}]}' >> "$ERRAND_STEPS"
echo out-said; [ "$1" = wait ] && while [ ! -f gate ]; do sleep 0.1; done; exit 0
`)
	_ = os.Chmod(filepath.Join(p.root, "see.sh"), 0o755)
	p.errand("", "run", "-q", "tests/**::*")
	p.errand("", "run", "-q", "-o", "./see.sh")
	p.errand("", "run", "-q", "--env", "box", "sh", "-c", "echo far-away")
	defer p.errand("", "web", "stop")

	o := p.errand("", "web", "once")
	if o.code != 0 || !strings.Contains(o.out, "/?o=") {
		t.Fatalf("errand web: %d %q %q", o.code, o.out, o.err)
	}
	link, _ := url.Parse(strings.TrimSpace(o.out))
	base := "http://" + link.Host
	plain := browser{t, base, &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	// a run on a terminal says its page; -O opens it
	k := p.onTerminal(100, 30, nil, "run", "-O", "--env", "local", "true")
	if code, out := k.finish(); code != 0 || !strings.Contains(out, "↗ "+base+"/r/") || !strings.Contains(out, "↗ file:///") {
		t.Errorf("a run opened in the browser: %d %q", code, out)
	}
	// nothing without the token; the code serves once
	if code, body := plain.get("/"); code != 401 || !strings.Contains(body, "/login") {
		t.Errorf("without a cookie: %d", code)
	}
	if code, _ := plain.get("/state"); code != 403 {
		t.Errorf("its state without a cookie: %d", code)
	}
	jar, _ := cookiejar.New(nil)
	b := browser{t, base, &http.Client{Jar: jar}}
	if code, body := b.get("/?" + link.RawQuery); code != 200 || !strings.Contains(body, "EventSource") {
		t.Fatalf("with the code: %d", code)
	}
	if code, _ := plain.get("/?" + link.RawQuery); code != 403 {
		t.Errorf("the code a second time: %d", code)
	}
	req, _ := http.NewRequest("GET", base+"/state", nil)
	req.Host = "evil.example"
	if r, err := b.c.Do(req); err != nil || r.StatusCode != 403 {
		t.Errorf("for another host: %v", r.StatusCode)
	}
	if code, _ := b.act(map[string]any{"op": "refresh"}, "http://evil.example"); code != 403 {
		t.Errorf("an action from another page: %d", code)
	}
	tok := p.errand("", "web", "token")
	form, _ := plain.c.PostForm(base+"/login", url.Values{"token": {strings.TrimSpace(tok.out)}, "next": {"//evil.example"}})
	if form.StatusCode != 303 || form.Header.Get("Location") != "/" {
		t.Errorf("the token typed: %d %q", form.StatusCode, form.Header.Get("Location"))
	}

	// the runs, as a tree
	rows := b.rows("tab=runs")
	if rowWith(rows, "tests/") == nil || rowWith(rows, "see.sh") == nil {
		t.Fatalf("the runs: %v", rows)
	}
	opened := ""
	for _, r := range b.rows("tab=runs&q=test") {
		if r["fold"] != nil {
			opened += fmt.Sprint(r["fold"]) + ","
		}
	}
	rows = b.rows("tab=runs&opened=" + url.QueryEscape(opened))
	run := rowWith(rows, "FAIL")
	if run == nil || run["mark"] != "FAIL" || !strings.Contains(fmt.Sprint(run["label"]), "3 passed, 1 failed") {
		t.Fatalf("a run that failed: %v", rows)
	}
	var see map[string]any
	for _, r := range b.rows("tab=runs&q=see.sh") {
		if r["kind"] == "run" {
			see = r
		}
	}
	key := url.QueryEscape(fmt.Sprint(see["key"]))
	d := b.json("/detail?key=" + key)
	said := fmt.Sprint(d["rows"])
	for _, want := range []string{"half way", "out-said", "The page", "page.html", "errand's files", "PASS"} {
		if !strings.Contains(said, want) {
			t.Errorf("its detail does not say %q: %s", want, said)
		}
	}
	// its curves: what the detail says of them, then the points of those asked for
	if got := fmt.Sprint(d["curves"]); got != "[map[last:0.001 n:2 name:residual scale:log varies:true]]" {
		t.Errorf("its curves: %s", got)
	}
	if got := fmt.Sprint(b.json("/curves?key=" + key + "&name=residual")["curves"]); got != "map[residual:map[i:[0 1] n:2 t:[0 2] v:[0.25 0.001]]]" {
		t.Errorf("their points: %s", got)
	}
	var scene, page map[string]any
	for _, x := range caches.ListOf(d["rows"]) {
		r := x.(map[string]any)
		if r["look"] == "static" {
			scene = r
		}
		if r["look"] == "file" {
			page = r
		}
	}
	if scene == nil || page == nil {
		t.Fatalf("what it made to be looked at: %s", said)
	}
	if got := get(t, fmt.Sprint(scene["url"])); !strings.Contains(got, "scene") {
		t.Errorf("served as it is: %q", got)
	}
	if got := get(t, fmt.Sprint(scene["url"])+"d.txt"); got != "data\n" {
		t.Errorf("and what is beside: %q", got)
	}
	if got := get(t, fmt.Sprint(scene["url"])+"../../../../etc/passwd"); strings.Contains(got, "root") {
		t.Errorf("above its directory")
	}
	if code, body := b.get("/file?key=" + key + "&path=" + url.QueryEscape(fmt.Sprint(page["path"]))); code != 200 || !strings.Contains(body, "hello") {
		t.Errorf("a file of it: %d %q", code, body)
	}
	if code, _ := b.get("/file?key=" + key + "&path=/etc/passwd"); code != 403 {
		t.Errorf("a path that is not its: %d", code)
	}
	if got := b.json("/peek?key=" + key + "&path=" + url.QueryEscape(fmt.Sprint(d["console"]))); got["text"] != "out-said" && !strings.HasPrefix(fmt.Sprint(got["text"]), "out-said") {
		t.Errorf("the beginning of a file: %v", got)
	}

	// what runs, pushed as it changes; stopped from the page
	res, err := b.c.Get(base + "/events")
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan string, 100)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := res.Body.Read(buf)
			if err != nil {
				return
			}
			events <- string(buf[:n])
		}
	}()
	p.errand("", "run", "--batch", "-q", "-o", "./see.sh", "wait")
	heard := false
	for until := time.After(10 * time.Second); !heard; {
		select {
		case e := <-events:
			heard = strings.Contains(e, `"lists":true`)
		case <-until:
			t.Fatal("nothing was pushed")
		}
	}
	var proc map[string]any
	for i := 0; i < 50 && (proc == nil || !strings.Contains(fmt.Sprint(proc["detail"]), "half way")); i++ {
		time.Sleep(100 * time.Millisecond)
		for _, r := range b.rows("tab=runs") {
			if r["kind"] == "proc" {
				proc = r
			}
		}
	}
	if proc == nil || !strings.Contains(fmt.Sprint(proc["detail"]), "half way") {
		t.Fatalf("what runs, and where it is: %v", proc)
	}
	_, got := b.act(map[string]any{"op": "kill", "key": proc["key"]}, base)
	if c, ok := got["confirm"].(map[string]any); !ok || c["danger"] != true {
		t.Errorf("kill says first what it would do: %v", got)
	}
	if code, got := b.act(map[string]any{"op": "kill", "key": proc["key"], "sure": true}, base); code != 200 {
		t.Errorf("kill: %v", got)
	}
	gone := false
	for i := 0; i < 80 && !gone; i++ {
		time.Sleep(100 * time.Millisecond)
		gone = true
		for _, r := range b.rows("tab=runs") {
			gone = gone && r["kind"] != "proc"
		}
	}
	if !gone {
		t.Errorf("stopped, it is still there")
	}

	// the machines, their gauges; one that is renamed
	for i := 0; i < 60; i++ {
		rows = b.rows("tab=machines")
		if r := rowWith(rows, "(here)"); r != nil && len(caches.ListOf(r["meters"])) > 0 && rowWith(rows, "box") != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	box := rowWith(rows, "box")
	if box == nil || rowWith(rows, "(here)") == nil {
		t.Fatalf("the machines: %v", rows)
	}
	if _, got := b.act(map[string]any{"op": "rename", "key": box["key"], "label": "lab"}, base); got["said"] != "named lab" {
		t.Errorf("rename: %v", got)
	}
	if rowWith(b.rows("tab=machines"), "lab") == nil {
		t.Errorf("renamed, its row: %v", b.rows("tab=machines"))
	}
	// the storage
	for i := 0; i < 80; i++ {
		rows = b.rows("tab=caches")
		if rowWith(rows, "errand's own") != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if rowWith(rows, "errand's own") == nil {
		t.Errorf("the storage: %v", rows)
	}

	// a run removed, once it is said what goes; here and where it ran
	var far map[string]any
	for _, r := range b.rows("tab=runs&q=far-away") {
		if r["kind"] == "run" {
			far = r
		}
	}
	if far == nil {
		t.Fatalf("the run that was elsewhere: %v", b.rows("tab=runs&q=far-away"))
	}
	_, got = b.act(map[string]any{"op": "runs_remove", "how": "all", "keys": []string{fmt.Sprint(far["key"])}}, base)
	if c, ok := got["confirm"].(map[string]any); !ok || !strings.Contains(fmt.Sprint(c["lines"]), "where 1 ran") {
		t.Fatalf("remove says first what goes: %v", got)
	}
	if _, m := b.act(map[string]any{"op": "runs_measure", "plan": got["plan"]}, base); !strings.Contains(fmt.Sprint(m["lines"]), "here") {
		t.Errorf("how much: %v", m)
	}
	b.act(map[string]any{"op": "runs_remove", "plan": got["plan"], "sure": true}, base)
	left := 1
	for i := 0; i < 60 && left > 0; i++ {
		time.Sleep(100 * time.Millisecond)
		a, _ := filepath.Glob(filepath.Join(p.root, "errand-outputs", "runs", "sh", "*", "launch.json"))
		f, _ := filepath.Glob(filepath.Join(p.far, "home", "copy", "proj", "errand-outputs", "runs", "sh", "*", "launch.json"))
		left = len(a) + len(f)
	}
	if left != 0 {
		t.Errorf("removed, %d directories are left", left)
	}
	res.Body.Close()
	if o = p.errand("", "open", filepath.Join(p.root, "errand-outputs")); o.code != 1 || !strings.Contains(o.err, "no index.html") {
		t.Errorf("open: %d %q", o.code, o.err)
	}
}

func TestTheScreen(t *testing.T) {
	p := newPlace(t)
	p.write("a.sh", "#!/bin/sh\necho hello-from-a; [ \"$1\" = wait ] && sleep 60; exit 0\n")
	_ = os.Chmod(filepath.Join(p.root, "a.sh"), 0o755)
	p.errand("", "run", "-q", "./a.sh")
	p.errand("", "run", "--batch", "-q", "./a.sh", "wait")
	k := p.onTerminal(150, 40, []string{"TERM=xterm-256color"}, "tui")
	k.waitFor("running ( 1 )")
	k.waitFor("./a.sh wait")
	k.waitFor("hello-from-a")
	k.typeIn("2")
	k.waitFor("(here)")
	k.typeIn("1")
	k.waitFor("running ( 1 )")
	k.typeIn("?")
	k.waitFor("stop what is under the cursor")
	k.typeIn(" ")
	time.Sleep(300 * time.Millisecond)
	k.typeIn("k")
	k.waitFor("stop 1?")
	k.typeIn("\r")
	k.waitFor("stopping ./a.sh wait")
	k.typeIn("q")
	if code, out := k.finish(); code != 0 || !strings.Contains(out, "\x1b[?1049l") {
		t.Errorf("left: exit %d", code)
	}
	if o := p.errand("", "tui"); o.code != 2 || !strings.Contains(o.err, "needs a terminal") {
		t.Errorf("off a terminal: %d %q", o.code, o.err)
	}
}

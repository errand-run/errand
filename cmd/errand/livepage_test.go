package main

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/runinfo"
)

// A run that goes on another machine, on its page: found as soon as it starts, its view served while it goes, its folders
// listed when they are opened, and nothing read again while nothing happens
func TestThePageOfARunThatGoesElsewhere(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.env = append(p.env, "ERRAND_NO_BROWSER=1")
	p.write("errand-envs.py", "import errand\nerrand.envs[ 'box' ] = errand.Env( [ errand.Ssh( host = 'box', root = 'copy/proj' ) ] )\n")
	p.write("see.sh", `#!/bin/sh
cd "$ERRAND_OUT_DIR"; mkdir -p scene.sviz/frames deep/er; echo '{"a": 1}' > deep/er/x.json; echo "<p>scene</p>" > scene.sviz/index.html
name() {
  echo '{"t": 3, "view": "'"$PWD"'/scene.sviz", "last": ["index.json"], "open": [{"static": "index.html"}]}' >> "$ERRAND_STEPS"
  echo '{"t": '"$1"', "view": "'"$PWD"'/scene.sviz"}' >> "$ERRAND_STEPS"; }
echo 1 > scene.sviz/frames/1; echo 1 > scene.sviz/index.json; name 4
while [ ! -f "$ERRAND_ROOT/gate1" ]; do sleep 0.1; done
echo 2 > scene.sviz/frames/2; echo 1 2 > scene.sviz/index.json; name 5
while [ ! -f "$ERRAND_ROOT/gate2" ]; do sleep 0.1; done
echo said-at-last
`)
	far := filepath.Join(p.far, "home", "copy", "proj")
	open := func(name string) { _ = os.WriteFile(filepath.Join(far, name), nil, 0o644) }
	o := p.errand("", "web", "once")
	defer p.errand("", "web", "stop")
	link, _ := url.Parse(strings.TrimSpace(o.out))
	base := "http://" + link.Host
	jar, _ := cookiejar.New(nil)
	b := browser{t, base, &http.Client{Jar: jar}}
	b.get("/?" + link.RawQuery)

	over := p.command("run", "-o", "--env", "box", "./see.sh")
	if err := over.Start(); err != nil {
		t.Fatal(err)
	}
	until := func(what string, ok func() bool) {
		t.Helper()
		for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			if ok() {
				return
			}
		}
		t.Fatalf("not seen: %s", what)
	}
	key := ""
	until("the run, in the list", func() bool {
		for _, r := range b.rows("tab=runs") {
			if r["kind"] == "proc" {
				key = fmt.Sprint(r["key"])
			}
		}
		return key != ""
	})
	// its page, by its place here: before it has come back
	notes, _ := filepath.Glob(filepath.Join(p.cache(), "runs", "*.json"))
	if len(notes) != 1 {
		t.Fatalf("its note: %v", notes)
	}
	leaf := strings.SplitN(strings.SplitN(read(t, notes[0]), `"leaf": "`, 2)[1], `"`, 2)[0]
	still := browser{t, base, &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if code, _ := still.get("/r/" + ident(leaf)); code != http.StatusSeeOther {
		t.Errorf("its page, while it goes: %d", code)
	}

	outputs := func() map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, x := range caches.ListOf(b.json("/detail?key=" + url.QueryEscape(key))["rows"]) {
			if r := x.(map[string]any); r["kind"] == "output" {
				out[fmt.Sprint(r["label"])] = r
			}
		}
		return out
	}
	scene := ""
	until("its view, here", func() bool {
		r := outputs()["scene.sviz"]
		if r == nil || r["look"] != "static" || r["ready"] != true {
			return false
		}
		scene = fmt.Sprint(r["url"])
		return true
	})
	if got := get(t, scene) + get(t, scene+"index.json"); !strings.Contains(got, "scene") || !strings.HasSuffix(got, "1\n") {
		t.Errorf("served while it goes: %q", got)
	}
	open("gate1")
	until("what changed in its view", func() bool {
		outputs()
		return get(t, scene+"index.json") == "1 2\n"
	})
	if get(t, scene+"frames/2") != "2\n" {
		t.Errorf("an index that names a file that is not here")
	}

	// a folder is a line, what is in it is asked for
	dir := outputs()["deep"]
	if dir == nil || dir["dir"] != true {
		t.Fatalf("a folder: %v", dir)
	}
	if got := fmt.Sprint(b.json("/ls?key=" + url.QueryEscape(key) + "&path=" + url.QueryEscape(fmt.Sprint(dir["path"])+"/er"))["entries"]); !strings.Contains(got, "x.json") {
		t.Errorf("what is in it: %s", got)
	}

	// nothing happens: nothing is said
	time.Sleep(time.Second)
	asked := func() int { return strings.Count(events(t, b, 2*time.Second), "data:") }
	if n := asked(); n != 0 {
		t.Errorf("while nothing happens, %d notices", n)
	}
	// and what happens is
	got := make(chan string)
	go func() { got <- events(t, b, 4*time.Second) }()
	time.Sleep(500 * time.Millisecond)
	open("gate2")
	if said := <-got; !strings.Contains(said, "data:") {
		t.Errorf("its end is not said: %q", said)
	}
	_ = over.Wait()
	until("its end, in its detail", func() bool {
		return strings.Contains(fmt.Sprint(b.json("/detail?key=" + url.QueryEscape(key))["rows"]), "said-at-last")
	})
	if r := outputs()["scene.sviz"]; r == nil || r["url"] != scene || r["ready"] != true {
		t.Errorf("its view once it came back, at another address: %v, was %s", r, scene)
	}
}

// events: what the page is told in a while
func events(t *testing.T, b browser, during time.Duration) string {
	t.Helper()
	c := &http.Client{Jar: b.c.Jar, Timeout: during}
	r, err := c.Get(b.base + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	out, buf := "", make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		out += string(buf[:n])
		if err != nil {
			return out
		}
	}
}

func ident(leaf string) string { return runinfo.RunIdent(leaf) }

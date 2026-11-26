package svc

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/queue"
)

// servicePlace: a cache, a configuration, a home of our own -> their directory
func servicePlace(t *testing.T) string {
	t.Helper()
	tmp, err := os.MkdirTemp("", "e") // ( short: git for Windows refuses a path of more than 260 characters )
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	tmp, _ = filepath.EvalSymlinks(tmp)
	for _, v := range []string{"MAMBA_ROOT_PREFIX", "CONDA_ROOT", "CONDA_PREFIX"} {
		t.Setenv(v, "")
	}
	t.Setenv("ERRAND_CACHE_DIR", filepath.Join(tmp, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmp, "run"))
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	return tmp
}

// fileURL: a path, as a `file:` URL names it on this system
func fileURL(path string) string {
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func textOf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func put(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// A definition as data is the request that asks for it: read back, the same server.
func TestADefinitionAsData(t *testing.T) {
	s := ServiceSpec{Command: []string{"ollama", "serve"}, Env: map[string]string{"OLLAMA_HOST": "127.0.0.1:{port}"}, Needs: map[string]any{"gpus": "1", "gpu_ram": "10G"},
		Busy: map[string]any{"http": "/api/ps", "field": "models"}, Idle: 120, Keep: 86400, Label: "ollama", Cwd: "~/x",
		Ports: []ServicePort{{Name: "api"}, {Name: "admin", Server: 9001}}, VersionOf: []string{"conf"}, Version: "2"}
	b, _ := json.Marshal(Definition(s))
	back, machine, err := ParseRequest(b)
	if err != nil || machine != "" || back.Text() != s.Text() || back.Idle != 120 || back.Keep != 86400 || back.Label != "ollama" {
		t.Errorf("read back: %s -> %+v %v", b, back, err)
	}
}

func TestAFileFetched(t *testing.T) {
	tmp := servicePlace(t)
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	w := tar.NewWriter(z)
	body := "#!/bin/sh\necho toy\n"
	_ = w.WriteHeader(&tar.Header{Name: "toy-1.0/bin/toy", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = w.Write([]byte(body))
	_ = w.Close()
	_ = z.Close()
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { asked++; _, _ = rw.Write(b.Bytes()) }))
	defer srv.Close()
	sum := sha256.Sum256(b.Bytes())
	sha, from := hex.EncodeToString(sum[:]), srv.URL+"/toy-1.0.tar.gz"
	if _, err := FetchFrom(from+"#sha256="+sha, "", "", "", nil); err == nil || !strings.Contains(err.Error(), "errand.sources") || asked != 0 {
		t.Errorf("an unknown source: %v, asked %d", err, asked)
	}
	if _, err := FetchFrom(from+"#sha256="+strings.Repeat("0", 64), "", "", "", []string{srv.URL}); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Errorf("another file: %v", err)
	}
	if kit.Exists(toolDir(strings.Repeat("0", 64))) {
		t.Errorf("kept all the same")
	}
	got, err := FetchFrom(from+"#sha256="+sha, "", "", "", []string{srv.URL})
	prog := filepath.Join(toolDir(sha), "toy-1.0", "bin", "toy")
	if err != nil || got["path"] != toolDir(sha) || textOf(t, prog) != body {
		t.Fatalf("fetched: %v %v", got, err)
	}
	was := asked
	if _, err := FetchFrom(from+"#sha256="+sha, "", "", "", []string{srv.URL}); err != nil || asked != was {
		t.Errorf("fetched again: %v", err)
	}
	for _, bad := range []string{"http://x/y", "git:noversion", "pip:--upgrade", "git+https://x"} {
		if _, err := FetchFrom(bad, "", "", "k#1", []string{"http://x"}); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
	_ = tmp
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

// aRepository: a repository with a service's files, committed -> its URL, its commit
func aRepository(t *testing.T, tmp string) (string, string) {
	t.Helper()
	repo := filepath.Join(tmp, "repo")
	put(t, filepath.Join(repo, "pkg", "services", "s.py"), "s")
	put(t, filepath.Join(repo, "big", "data.bin"), strings.Repeat("x", 100000))
	git(t, repo, "init", "-q")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-qm", "one")
	git(t, repo, "config", "uploadpack.allowFilter", "true")
	return fileURL(repo), git(t, repo, "rev-parse", "HEAD")
}

func TestADescriptionOfAPage(t *testing.T) {
	text := `{"on": "9c41e2d0", "name": "painted", "protocol": 1, "run": ["{tool:painter}", "-P", "{file:s.py}", "--port", "{port}"],
	 "files": {"git": "https://github.com/me/repo", "rev": "807e184", "dir": "pkg/services"}, "tools": {"painter": {"min": "2.0"}}, "needs": {"gpus": 1}, "idle": "10m"}`
	d, on, err := ParsePageDesc([]byte(text))
	if err != nil || on != "9c41e2d0" || d.Name != "painted" || float64(d.Idle) != 600 {
		t.Fatalf("%+v %q %v", d, on, err)
	}
	again, _, _ := ParsePageDesc([]byte(strings.Replace(text, `"on": "9c41e2d0", `, "", 1)))
	if again.Hash() != d.Hash() || !hashWord.MatchString(d.Hash()) || d.Key() != "painted~"+d.Hash() {
		t.Errorf("on is not of it: %s %s", again.Hash(), d.Hash())
	}
	other, _, _ := ParsePageDesc([]byte(strings.Replace(text, "807e184", "807e185", 1)))
	if other.Hash() == d.Hash() {
		t.Errorf("another commit, another service")
	}
	if name, hash, ok := SplitPageKey(d.Key()); !ok || name != "painted" || hash != d.Hash() {
		t.Errorf("%s", d.Key())
	}
	for _, bad := range []string{
		`{"name": "x", "run": ["a"], "command": ["b"]}`, // a word of no description
		`{"name": "x y", "run": ["a"]}`,                 // a name
		`{"name": "x", "run": []}`,                      // nothing to run
		`{"name": "x", "run": ["{shell}"]}`,             // a placeholder of none
		`{"name": "x", "run": ["{file:s.py}"]}`,         // a file of no repository
		`{"name": "x", "run": ["{python}"]}`,            // an env that installs nothing
		`{"name": "x", "run": ["{python}"], "python": {"install": []}}`,
		`{"name": "x", "run": ["{python}"], "python": {"install": ["--index-url http://x"]}}`, // a requirement, not an option
		`{"name": "x", "run": ["{python}"], "python": {"install": [{"git": "g", "dev": true, "dir": "d"}]}}`,
		`{"name": "x", "run": ["{python}"], "python": {"install": [{"git": "g", "dev": true, "pip": "x"}]}}`,
		`{"name": "x", "run": ["a"], "source": {"git": "g", "rev": "abcdef0"}}`,             // ( gone )
		`{"name": "x", "run": ["{tool:t}"]}`,                                                // a tool not described
		`{"name": "x", "run": ["{file:../s.py}"], "files": {"git": "g", "rev": "abcdef0"}}`, // out of it
		`{"name": "x", "run": ["a"], "files": {"git": "g", "rev": "main"}}`,                 // a commit
		`{"name": "x", "run": ["a"], "files": {"git": "g", "rev": "abcdef0", "dev": true}}`,
		`{"name": "x", "run": ["a"], "files": {"git": "g", "dev": true, "version_of": ["../x"]}}`,
		`{"name": "x", "run": ["a"], "needs": {"disk": 1}}`,
		`{"name": "x", "run": ["a"], "needs": {"gpus": "2-1"}}`,
		`{"name": "x", "run": ["a"], "needs": {"gpus": "some"}}`,
		`{"name": "x", "run": ["a"]} {}`,
	} {
		if _, _, err := ParsePageDesc([]byte(bad)); err == nil {
			t.Errorf("taken: %s", bad)
		}
	}
}

func TestADescriptionOnAMachine(t *testing.T) {
	tmp := servicePlace(t)
	from, rev := aRepository(t, tmp)
	painter := put(t, filepath.Join(tmp, "opt", "painter-2.1.0", "painter"), "#!/bin/sh\n")
	old := put(t, filepath.Join(tmp, "opt", "painter-1.0.0", "painter"), "#!/bin/sh\n")
	tool := func(look ...any) map[string]any {
		return map[string]any{"min": "2.0", "look": map[string]any{"*": look}, "version_from": []any{map[string]any{"dirname": "painter-(\\d+\\.\\d+\\.\\d+)"}}}
	}
	d := PageDesc{Name: "painted", Run: []string{"{tool:painter}", "-P", "{file:s.py}", "--port", "{port}"}, Files: &PageRepo{Git: from, Rev: rev, Dir: "pkg/services"},
		Tools: map[string]any{"painter": tool(old, painter)}, Idle: 7}
	l := LookPage(d, nil, nil)
	where := filepath.Join(repoDir(httpsURL(from), rev), "pkg", "services")
	if l.State != "missing" || len(l.Missing) != 1 || l.Missing[0].Kind != "code" || l.Missing[0].Trusted || strings.Join(l.Command, " ") != painter+" -P "+filepath.Join(where, "s.py")+" --port {port}" {
		t.Fatalf("%+v", l)
	}
	if _, err := AuthorizePage(d, nil, nil, func(string) {}); err == nil || !strings.Contains(err.Error(), "unknown source") || PageAuthorized()[d.Key()] != nil {
		t.Errorf("an unknown source, not accepted: %v", err)
	}
	said := []string{}
	if l, err := AuthorizePage(d, nil, []string{from}, func(s string) { said = append(said, s) }); err != nil || l.State != "authorized" || len(said) != 1 {
		t.Fatalf("%+v %v %q", l, err, said)
	}
	a := PageAuthorized()[d.Key()]
	if a == nil || a.Command()[2] != filepath.Join(where, "s.py") || a.Described["name"] != "painted" || textOf(t, filepath.Join(where, "s.py")) != "s" || kit.Exists(filepath.Join(where, "..", "..", "big")) {
		t.Fatalf("authorized: %+v", a)
	}
	if s, err := a.Spec(); err != nil || s.Idle != 7 || s.Name != "painted" || s.Version != d.Hash() {
		t.Errorf("its server: %+v %v", s, err)
	}
	if l = LookPage(d, nil, nil); l.State != "authorized" || len(l.Missing) != 0 {
		t.Errorf("then: %+v", l)
	}

	// a tool that is not here: fetched, by its URL and its sha256; or none, and the service cannot be
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	w := tar.NewWriter(z)
	_ = w.WriteHeader(&tar.Header{Name: "painter-3.0.0/painter", Mode: 0o755, Size: 10, Typeflag: tar.TypeReg})
	_, _ = w.Write([]byte("#!/bin/sh\n"))
	_ = w.Close()
	_ = z.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { _, _ = rw.Write(b.Bytes()) }))
	defer srv.Close()
	sum := sha256.Sum256(b.Bytes())
	sha := hex.EncodeToString(sum[:])
	fetched := tool(old)
	fetched["fetch"] = map[string]any{platformName(): map[string]any{"url": srv.URL + "/painter.tar.gz", "sha256": sha, "run": "painter-3.0.0/painter", "version": "3.0.0"}}
	e := d
	e.Tools = map[string]any{"painter": fetched}
	l = LookPage(e, nil, nil)
	if l.State != "missing" || len(l.Missing) != 1 || l.Missing[0].Kind != "tool" || l.Missing[0].Sha256 != sha || l.Command[0] != filepath.Join(toolDir(sha), "painter-3.0.0", "painter") {
		t.Fatalf("a tool to fetch: %+v", l)
	}
	if l, err := AuthorizePage(e, nil, []string{srv.URL}, func(string) {}); err != nil || l.State != "authorized" || !kit.IsFile(l.Command[0]) {
		t.Fatalf("fetched: %+v %v", l, err)
	}
	none := d
	none.Tools = map[string]any{"painter": tool(old)}
	if l = LookPage(none, nil, nil); l.State != "unavailable" || !strings.Contains(l.Why, "no painter 2.0 or later") {
		t.Errorf("none: %+v", l)
	}
	if l = LookPage(none, map[string][]string{"painter": {painter}}, nil); l.State != "missing" && l.State != "ready" {
		t.Errorf("a path a person gave: %+v", l)
	}
	unknown := put(t, filepath.Join(tmp, "opt", "bin", "painter"), "#!/bin/sh\n")
	none.Tools = map[string]any{"painter": tool(unknown)}
	if l = LookPage(none, nil, nil); l.State != "unavailable" || len(l.Probes) != 1 || l.Probes[0].Argv[1] != "--version" {
		t.Errorf("a version no file says: %+v", l)
	}

	// the view's files, seen or not
	view := put(t, filepath.Join(tmp, "view", "index.html"), "<p>")
	store := StoreOf(filepath.Dir(view), "v1")
	if l = LookPage(d, nil, store); l.Store["seen"] != "shared" || StoreSeen(store) != filepath.Dir(view) {
		t.Errorf("seen: %+v", l.Store)
	}
	store["paths"] = []string{filepath.Join(tmp, "elsewhere")}
	if l = LookPage(d, nil, store); l.State != "authorized" || l.Store["seen"] != "none" || l.Missing[len(l.Missing)-1].Kind != "store" {
		t.Errorf("not seen: %+v", l)
	}
}

func TestACardIfThereIsOne(t *testing.T) {
	servicePlace(t)
	d, _, err := ParsePageDesc([]byte(`{"name": "g", "run": ["sleep", "{port}"], "needs": {"gpus": "0-1", "gpu_ram": "4G"}}`))
	if err != nil {
		t.Fatal(err)
	}
	l, cards := LookPage(d, nil, nil), queue.Machine().GPUs
	switch {
	case l.State != "ready":
		t.Errorf("%d cards here: %+v", cards, l)
	case cards == 0 && strings.Contains(l.Needs, "gpu"):
		t.Errorf("none here, none asked: %q", l.Needs)
	case cards > 0 && !strings.Contains(l.Needs, "gpus=1"):
		t.Errorf("one here, one asked: %q", l.Needs)
	}
	if two, _, _ := ParsePageDesc([]byte(`{"name": "g", "run": ["sleep", "{port}"], "needs": {"gpus": "2-4"}}`)); cards < 2 && LookPage(two, nil, nil).State != "unavailable" {
		t.Errorf("two at least: %+v", LookPage(two, nil, nil))
	}
}

func TestADescriptionInDevelopment(t *testing.T) {
	tmp := servicePlace(t)
	checkout := filepath.Join(tmp, "proj", "toyrepo")
	put(t, filepath.Join(checkout, "svc", "toy.py"), "print( 'toy' )\n")
	repo := "https://github.com/me/toyrepo.git"
	d := PageDesc{Name: "toy", Run: []string{"{python}", "{file:toy.py}", "--port", "{port}"},
		Files: &PageRepo{Git: repo, Dev: true, Dir: "svc", VersionOf: []string{"svc/**/*.py"}}, Python: &PagePython{Install: []PageRequirement{{Repo: &PageRepo{Git: repo, Dev: true}}}}}
	if l := LookPage(d, nil, nil); l.State != "missing" || len(l.Missing) != 2 || l.Missing[0].Kind != "checkout" || !strings.Contains(l.Missing[0].What, "errand serve dev github.com/me/toyrepo PATH") ||
		len(l.Parts) != 2 || l.Parts[0].Kind != "code" || l.Parts[1].Kind != "env" || l.Parts[1].State != "missing" {
		t.Errorf("no checkout here, one part for the repository: %+v", l)
	}
	if err := DeclareCheckout(repo, filepath.Join(tmp, "nowhere"), ""); err == nil {
		t.Errorf("a checkout that is not there")
	}
	if err := DeclareCheckout(repo, checkout, ""); err != nil {
		t.Fatal(err)
	}
	env := serviceEnv(d.Key())
	if l := LookPage(d, nil, nil); l.State != "missing" || len(l.Missing) != 1 || l.Missing[0].Kind != "env" || !strings.Contains(l.Missing[0].What, "-e "+shortPath(checkout)) ||
		l.Command[0] != filepath.Join(env, "bin", "python") {
		t.Errorf("an env of its own, from the checkout, to build: %+v", l)
	}
	python := put(t, filepath.Join(env, "bin", "python"), "#!/bin/sh\n") // ( as if built: pip is not run in a test )
	installed, _ := json.Marshal([][]string{{"-e", checkout}})
	put(t, filepath.Join(env, envMade), fmt.Sprintf(`{"installed": %q}`, installed))
	l := LookPage(d, nil, nil)
	if l.State != "ready" || l.Checkout != checkout || l.Command[0] != python || l.Command[1] != filepath.Join(checkout, "svc", "toy.py") {
		t.Fatalf("%+v", l)
	}
	if l, err := AuthorizePage(d, nil, nil, func(string) {}); err != nil || l.State != "authorized" {
		t.Fatalf("%+v %v", l, err)
	}
	s, _ := PageAuthorized()[d.Key()].Spec()
	h1, _ := s.Hash()
	time.Sleep(10 * time.Millisecond)
	put(t, filepath.Join(checkout, "svc", "deep", "more.py"), "print( 'another' )\n")
	if h2, _ := s.Hash(); h2 == h1 || PageAuthorized()[d.Key()].Dev != checkout {
		t.Errorf("a file its pattern names: another server behind the same address")
	}

	// two repositories in development: the env waits for both checkouts, and installs both in one go
	loom := filepath.Join(tmp, "proj", "loom")
	put(t, filepath.Join(loom, "python", "pyproject.toml"), "")
	two := d
	two.Python = &PagePython{Install: []PageRequirement{{Repo: &PageRepo{Git: repo, Dev: true}}, {Repo: &PageRepo{Git: "github.com/me/loom", Dev: true, Package: "python"}}, {Pip: "numpy>=2"}}}
	part := func(l PageLook, kind, name string) Part {
		for _, p := range l.Parts {
			if p.Kind == kind && (name == "" || strings.Contains(p.Name, name)) {
				return p
			}
		}
		return Part{}
	}
	if l := LookPage(two, nil, nil); len(l.Parts) != 3 || part(l, "code", "loom").State != "missing" || !strings.Contains(part(l, "env", "").Missing.What, "once errand knows the checkouts") {
		t.Errorf("loom's checkout, not yet: %+v", l.Parts)
	}
	_ = DeclareCheckout("https://github.com/me/loom", loom, "")
	if l := LookPage(two, nil, nil); part(l, "code", "loom").State != "ok" || part(l, "env", "").Missing == nil ||
		!strings.Contains(part(l, "env", "").Missing.What, "-e "+shortPath(filepath.Join(loom, "python"))) || !strings.Contains(part(l, "env", "").Missing.What, "numpy>=2") {
		t.Errorf("both, and numpy, in one go: %+v", l.Parts)
	}
	if !SameRepo("github.com/me/loom", "https://github.com/me/loom.git") || SameRepo("github.com/me/loom", "github.com/me/other") {
		t.Errorf("SameRepo")
	}
	put(t, filepath.Join(tmp, "config", "errand", devOff), fmt.Sprintf("[%q]", checkout))
	if l := LookPage(d, nil, nil); l.State != "authorized" && l.State != "unavailable" {
		t.Errorf("turned off: %+v", l)
	}
	_ = os.Remove(filepath.Join(tmp, "config", "errand", devOff))
	// a copy of a project carries those inside it
	o := originFor(filepath.Join(tmp, "proj"), map[string]DevSource{"github.com/me/toyrepo": {Path: checkout, Env: "far", Envs: map[string]string{machineName(): filepath.Join(tmp, "env")}}, "github.com/x/y": {Path: tmp}})
	if len(caches.ListOf(o["dev"])) != 1 {
		t.Fatalf("%v", o)
	}
	copyOf := filepath.Join(tmp, "copy")
	put(t, filepath.Join(copyOf, "toyrepo", "svc", "toy.py"), "print( 'toy' )\n")
	put(t, filepath.Join(copyOf, filesync.CopyState), "{}")
	b, _ := json.Marshal(o)
	put(t, filepath.Join(copyOf, filesync.CopyOrigin), string(b))
	filesync.NoteCopy(copyOf)
	found := false
	for _, c := range devSources(nil) {
		found = found || c.Path == filepath.Join(copyOf, "toyrepo") && strings.HasPrefix(c.Origin, "copy of ")
	}
	if !found {
		t.Errorf("in a copy: %+v", devSources(nil))
	}
}

func TestTheVersionsHere(t *testing.T) {
	servicePlace(t)
	keys := []string{"toy~aaaa1111", "toy~bbbb2222", "toy~cccc3333"}
	month := time.Now().Add(-30 * 24 * time.Hour)
	for i, k := range keys {
		s := ServiceSpec{Name: "toy", Command: []string{"python3", "-m", "http.server", "{port}"}, Version: fmt.Sprint(i)}
		if _, err := Authorize(s, "pages", Authorization{Page: k}); err != nil {
			t.Fatal(err)
		}
		put(t, filepath.Join(serviceEnv(k), "lib", "big"), strings.Repeat("x", 5000))
		_ = os.Chtimes(serviceEnv(k), month, month)
	}
	_ = changeAuthorized(func(all map[string]*Authorization) error {
		for _, a := range all {
			if a.Page != keys[2] {
				a.Since = month.Format(stamp)
			}
		}
		return nil
	})
	if PinVersion(keys[0], true) != nil || PinVersion("toy~nope", true) == nil {
		t.Errorf("pin")
	}
	by := map[string]*serviceVersionHere{}
	versions, _ := VersionsHere(nil, true, VersionDays)
	for _, v := range versions {
		by[v.Key] = v
	}
	if v := by[keys[0]]; !v.Pinned || v.Unused || v.Name != "toy" || v.Version != "aaaa1111" {
		t.Errorf("pinned: %+v", v)
	}
	if v := by[keys[1]]; !v.Unused || v.Size < 5000 {
		t.Errorf("unused: %+v", v)
	}
	if by[keys[2]].Unused {
		t.Errorf("authorized today: used")
	}
	dry := PruneVersions(nil, VersionDays, true)
	if got := dry["versions"].([]string); len(got) != 1 || got[0] != keys[1] || dry["freed"].(int64) < 5000 || !kit.Exists(serviceEnv(keys[1])) {
		t.Errorf("would go: %v", dry)
	}
	PruneVersions(nil, VersionDays, false)
	if kit.Exists(serviceEnv(keys[1])) || PageAuthorized()[keys[1]] != nil || PageAuthorized()[keys[0]] == nil {
		t.Errorf("what went")
	}
	if got := RemoveViews([]string{"../x", "nobody"})["removed"].([]string); len(got) != 0 {
		t.Errorf("a name that is no copy's: %v", got)
	}
}

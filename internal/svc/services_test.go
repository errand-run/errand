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
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/kit"
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

// aPackage: an env with a package that describes two services -> its module's directory
func aPackage(t *testing.T, prefix, editable string) string {
	t.Helper()
	site := filepath.Join(prefix, "lib", "python3.99", "site-packages")
	dist := filepath.Join(site, "fakepkg-1.2.dist-info")
	put(t, filepath.Join(prefix, "bin", "python3"), "#!/bin/sh\n")
	put(t, filepath.Join(dist, "METADATA"), "Metadata-Version: 2.1\nName: fakepkg\nVersion: 1.2\n\n")
	put(t, filepath.Join(dist, "entry_points.txt"), "[console_scripts]\nx = y:z\n\n[errand.services]\nfakepkg = fakepkg.svc\n")
	dir := filepath.Join(site, "fakepkg", "svc")
	if editable != "" {
		dir = filepath.Join(editable, "src", "fakepkg", "svc")
		put(t, filepath.Join(dist, "direct_url.json"), fmt.Sprintf(`{"url": %q, "dir_info": {"editable": true}}`, fileURL(editable)))
		put(t, filepath.Join(site, "__editable___fakepkg_1_2_finder.py"), "MAPPING: dict[str, str] = {'fakepkg': '"+filepath.Dir(dir)+"'}\n")
	}
	put(t, filepath.Join(dir, "toy.py"), "print( 'toy' )\n")
	put(t, filepath.Join(dir, describedFile), `{"format": 1, "package": {"name": "fakepkg", "pip": "fakepkg"}, "services": {
 "toy": {"protocol": 1, "text": "a toy", "version_of": ["toy.py"], "run": ["{python}", "{file:toy.py}", "--port", "{port}"], "idle": "5s"},
 "drawn": {"protocol": 1, "version_of": ["toy.py"], "run": ["{tool:painter}", "{file:toy.py}", "--port", "{port}"],
  "tools": {"painter": {"min": "2.0", "look": {"*": ["`+filepath.ToSlash(prefix)+`/no-painter"]}, "version_from": [{"dirname": "painter-(\\d+\\.\\d+\\.\\d+)"}]}}}}}`)
	return dir
}

func lookAt(want map[string]any, paths map[string][]string, name string) serviceLook {
	b, _ := json.Marshal(map[string]any{"machine": "m1", "services": map[string]any{name: want}, "paths": paths})
	var req map[string]any
	_ = json.Unmarshal(b, &req)
	return ObjectOf(Look(req, map[string]string{})["services"])[name].(serviceLook)
}

func TestThePackagesThatDescribeServices(t *testing.T) {
	tmp := servicePlace(t)
	dir := aPackage(t, filepath.Join(tmp, "cache", "svc-envs", "fake"), "")
	got := providers()
	if len(got) != 1 || got[0].Package != "fakepkg" || got[0].Version != "1.2" || got[0].Dir != dir || got[0].Python == "" || got[0].Editable != "" {
		t.Fatalf("%+v", got)
	}
	p := got[0]
	v := p.gives("toy")
	// ( what a producer computes: the hash is a contract )
	h := sha256.Sum256([]byte("errand-service/1 toy protocol 1\ntoy.py 15\nprint( 'toy' )\n"))
	if v != hex.EncodeToString(h[:])[:8] {
		t.Errorf("its version: %q", v)
	}
	if len(providing("toy#"+v)) != 1 || len(providing("toy#00000000")) != 0 || len(providing("toy")) != 1 {
		t.Errorf("who provides it")
	}
	words, err := p.compose("toy", nil, true)
	if err != nil || strings.Join(words, " ") != p.Python+" "+filepath.Join(dir, "toy.py")+" --port {port}" {
		t.Errorf("its command: %q %v", words, err)
	}
	put(t, filepath.Join(dir, "toy.py"), "changed")
	if p.gives("toy") == v {
		t.Errorf("another file, another version")
	}
	checkout := filepath.Join(tmp, "checkout")
	inside := aPackage(t, filepath.Join(tmp, "cache", "svc-envs", "editable"), checkout)
	for _, p := range providers() {
		if p.Editable != "" && (p.Dir != inside || p.Editable != checkout) {
			t.Errorf("an editable install: %+v", p)
		}
	}
	if len(providers()) != 2 {
		t.Errorf("%+v", providers())
	}
}

func TestServicesInstalled(t *testing.T) {
	tmp := servicePlace(t)
	dir := aPackage(t, filepath.Join(tmp, "cache", "svc-envs", "fake"), "")
	p := providers()[0]
	key := "toy#" + p.gives("toy")
	words, _ := p.compose("toy", nil, true)
	d, err := InstallService(key, words, map[string]any{"idle": "7s"})
	if err != nil || d["provider"] != p.file() {
		t.Fatalf("as typed: %v %v", d, err)
	}
	decl, err := DeclaredServices("", nil)
	if s := decl.Services[key]; err != nil || s.Idle != 7 || s.Source != "installed" || len(s.Ports) != 1 || strings.Join(s.Command, " ") != strings.Join(words, " ") {
		t.Errorf("declared: %+v %v", s, err)
	}
	if bad := WrongVersion(key, d["provider"]); bad != "" {
		t.Errorf("it is what it says: %q", bad)
	}
	put(t, filepath.Join(dir, "toy.py"), "changed")
	if bad := WrongVersion(key, d["provider"]); !strings.Contains(bad, "wrong version") {
		t.Errorf("changed since: %q", bad)
	}
	if _, err := InstallService("toy", []string{"x"}, nil); err == nil {
		t.Errorf("a bare name")
	}
	if d, err = InstallService("toy#00000000", words, nil); err != nil || d["unverified"] != true || d["provider"] != nil {
		t.Errorf("a version no package provides: %v %v", d, err)
	}
	// composed from the package
	now := "toy#" + p.gives("toy")
	_, needs, said, err := ComposedHere(now)
	if err != nil || needs["idle"] != "5s" || !strings.Contains(said, "fakepkg 1.2") || !strings.Contains(said, "toy.py") {
		t.Errorf("composed: %v %q %v", needs, said, err)
	}
	if d, err = InstallService(now, nil, nil); err != nil || d["idle"] != "5s" {
		t.Errorf("the short form: %v %v", d, err)
	}
	if _, err = InstallService("drawn#"+p.gives("drawn"), nil, nil); err == nil || !strings.Contains(err.Error(), "painter") {
		t.Errorf("a tool that is missing: %v", err)
	}
	// a port of its own, and what is wrong is refused before it is written
	if _, err := DeclareHere("x", []string{"run", "{port:nope}"}, nil, "", nil); err == nil || InstalledHere(nil)["x"] != nil {
		t.Errorf("a port nobody has: %v", err)
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

func TestTheDiagnosis(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the programs it looks at are shell scripts")
	}
	tmp := servicePlace(t)
	dir := aPackage(t, filepath.Join(tmp, "cache", "svc-envs", "fake"), "")
	marker := filepath.Join(tmp, "ran")
	prog := "#!/bin/sh\ntouch " + marker + "\necho painter 2.4.1\n"
	good := put(t, filepath.Join(tmp, "opt", "painter-2.4.1-linux", "bin", "painter"), prog)
	old := put(t, filepath.Join(tmp, "opt", "painter-1.9.0", "painter"), prog)
	odd := put(t, filepath.Join(tmp, "elsewhere", "painter"), prog)
	v := providers()[0].gives("drawn")
	want := map[string]any{"version": v, "package": map[string]any{"name": "fakepkg", "version": "1.2"}}
	e := lookAt(want, map[string][]string{"painter": {old, odd}}, "drawn")
	if all := strings.Join(e.Found, "\n"); e.State != "unknown" || !strings.Contains(all, "1.9.0") || !strings.Contains(all, "needs 2.0") {
		t.Errorf("%+v", e)
	}
	if len(e.Lines) != 1 || e.Lines[0].Command != "errand serve probe --on m1 -- "+odd+" --version" {
		t.Errorf("%+v", e.Lines)
	}
	e = lookAt(want, map[string][]string{"painter": {good}}, "drawn")
	if e.State != "installable" || len(e.Lines) != 1 || e.Lines[0].Command != fmt.Sprintf("errand serve install --on m1 drawn#%s -- %s %s --port {port}", v, good, filepath.Join(dir, "toy.py")) {
		t.Errorf("%+v", e)
	}
	if !strings.HasPrefix(e.Summary, "Not set up on m1 yet.") || kit.Exists(marker) {
		t.Errorf("%q, or something was run", e.Summary)
	}
	if got, err := ProbeProgram([]string{odd, "--version"}); err != nil || got["version"] != "2.4.1" || !kit.Exists(marker) {
		t.Errorf("a probe: %v %v", got, err)
	}
	if got, how := programVersion(odd, nil, nil); got != "2.4.1" || !strings.Contains(how, "probed") {
		t.Errorf("it is remembered: %q %q", got, how)
	}
	if e = lookAt(want, map[string][]string{"painter": {odd}}, "drawn"); e.State != "installable" {
		t.Errorf("probed: %+v", e)
	}
	// installed: ready; changed since: not
	if _, err := InstallService("drawn#"+v, []string{good, filepath.Join(dir, "toy.py"), "{port}"}, nil); err != nil {
		t.Fatal(err)
	}
	if e = lookAt(want, nil, "drawn"); e.State != "unavailable" && e.State != "ready" {
		t.Errorf("declared: %+v", e)
	}
	if e = lookAt(want, map[string][]string{"painter": {good}}, "drawn"); e.State != "ready" {
		t.Errorf("declared: %+v", e)
	}
	// a dishonest store: proposed, said, never run
	e = lookAt(map[string]any{"version": "deadbeef", "package": map[string]any{"name": "evilpkg", "version": "6.6"}, "described_by": "store",
		"described": map[string]any{"run": []string{"{python}", "-c", "import shutil; shutil.rmtree( '/' )"}, "package": map[string]any{"pip": "evilpkg", "index": "https://evil.example/simple"}}}, nil, "evil")
	all, _ := json.Marshal(e)
	if e.State != "installable" || len(e.Lines) != 2 || !e.Lines[0].Unknown || !strings.Contains(e.Lines[0].Command, "--for evil#deadbeef pip:evilpkg==6.6") ||
		e.Lines[1].Command != "errand serve install --on m1 evil#deadbeef" || strings.Contains(string(all), "rmtree") {
		t.Errorf("%s", all)
	}
	// what no line can meet
	e = lookAt(map[string]any{"version": "1", "described": map[string]any{"needs": map[string]any{"platforms": []string{"plan9-*"}}}}, nil, "far")
	if e.State != "unavailable" || !strings.Contains(e.Summary, "Cannot run on m1") {
		t.Errorf("%+v", e)
	}
	for _, c := range []struct {
		v, least  string
		ok, known bool
	}{{"4.5.1", "4.5", true, true}, {"4.4", "4.5", false, true}, {"5", "4.5", true, true}, {"", "1", false, false}, {"10.0", "9.9", true, true}} {
		if ok, known := meets(c.v, c.least); ok != c.ok || known != c.known {
			t.Errorf("%q, at least %q: %v %v", c.v, c.least, ok, known)
		}
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCodeFromARepository(t *testing.T) {
	tmp := servicePlace(t)
	repo := filepath.Join(tmp, "repo")
	svc := filepath.Join(repo, "pkg", "services")
	tool := put(t, filepath.Join(tmp, "opt", "painter"), "#!/bin/sh\n")
	entry := map[string]any{"protocol": 1, "version_of": []any{"s.py", "../store.py"}, "run": []any{"{tool:painter}", "-P", "{file:s.py}", "--port", "{port}"},
		"tools": map[string]any{"painter": map[string]any{"look": map[string]any{"*": []any{tool}}}}}
	put(t, filepath.Join(svc, "s.py"), "s")
	put(t, filepath.Join(repo, "pkg", "store.py"), "store")
	put(t, filepath.Join(repo, "big", "data.bin"), strings.Repeat("x", 100000))
	b, _ := json.Marshal(map[string]any{"format": 1, "package": map[string]any{"name": "pkg"}, "services": map[string]any{"painted": entry}})
	put(t, filepath.Join(svc, describedFile), string(b))
	git(t, repo, "init", "-q")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-qm", "one")
	git(t, repo, "config", "uploadpack.allowFilter", "true")
	rev, from := git(t, repo, "rev-parse", "HEAD"), fileURL(repo)
	v, _ := serviceVersion("painted", entry, svc)
	source := map[string]any{"git": from, "rev": rev, "dir": "pkg/services", "dirty": false}
	want := map[string]any{"version": v, "source": source, "described": entry, "described_by": "store"}
	e := lookAt(want, nil, "painted")
	where := repoDir(from, rev)
	if len(e.Lines) != 2 || e.Lines[0].Command != fmt.Sprintf("errand serve fetch --on m1 git:%s@%s#paths=pkg/services,pkg/store.py", from, rev) || !e.Lines[0].Unknown ||
		!strings.Contains(e.Lines[1].Command, "-P "+typedWords([]string{filepath.Join(where, "pkg", "services", "s.py")})) {
		t.Fatalf("%+v", e)
	}
	if _, err := FetchFrom("git:"+from+"@"+rev, "", "", "", nil); err == nil || !strings.Contains(err.Error(), "errand.sources") {
		t.Errorf("an unknown repository: %v", err)
	}
	got, err := FetchFrom("git:"+from+"@"+rev+"#paths=pkg/services", "", "", "", []string{from})
	if err != nil || got["path"] != where || textOf(t, filepath.Join(where, "pkg", "services", "s.py")) != "s" || kit.Exists(filepath.Join(where, "big")) || kit.Exists(filepath.Join(where, ".git")) || kit.Exists(filepath.Join(where, "pkg", "store.py")) {
		t.Fatalf("its files only: %v %v", got, err)
	}
	if _, err = FetchFrom("git:"+from+"@"+rev+"#paths=pkg/services,pkg/store.py", "", "", "", []string{from}); err != nil || textOf(t, filepath.Join(where, "pkg", "store.py")) != "store" || kit.Exists(filepath.Join(where, "big")) {
		t.Errorf("more of it, later: %v", err)
	}
	if dirs := kit.Strs(caches.ReadObject(filepath.Join(where, sourceFile))["dirs"]); strings.Join(dirs, " ") != "pkg/services pkg/store.py" {
		t.Errorf("what is there: %q", dirs)
	}
	if p := providing("painted#" + v); len(p) != 1 || p[0].Python != "" {
		t.Errorf("found: %+v", p)
	}
	if e = lookAt(want, nil, "painted"); e.State != "installable" || len(e.Lines) != 1 || !strings.HasSuffix(e.Lines[0].Command, fmt.Sprintf("painted#%s -- %s --port {port}", v, typedWords([]string{tool, "-P", filepath.Join(where, "pkg", "services", "s.py")}))) {
		t.Errorf("%+v", e)
	}
	source["dirty"], want["version"] = true, "00000000"
	if e = lookAt(want, nil, "painted"); !strings.Contains(e.Lines[0].Command, "-P CHECKOUT/pkg/services/s.py") || !strings.Contains(e.Lines[0].Comment, "not committed") {
		t.Errorf("not committed: %+v", e)
	}
	if _, err = FetchFrom("git:"+from+"@"+rev, "", "", "", []string{from}); err != nil || !kit.Exists(filepath.Join(where, "big")) {
		t.Errorf("the whole of it: %v", err)
	}
}

func TestDevSources(t *testing.T) {
	tmp := servicePlace(t)
	checkout, env := filepath.Join(tmp, "proj", "toyrepo"), filepath.Join(tmp, "env")
	put(t, filepath.Join(checkout, "svc", "toy.py"), "print( 'toy' )\n")
	put(t, filepath.Join(checkout, "svc", describedFile), `{"format": 1, "package": {"name": "toypkg"}, "services": {"toy": {"version_of": ["toy.py"], "idle": "5s", "run": ["{python}", "{file:toy.py}", "--port", "{port}"]}}}`)
	python := put(t, filepath.Join(env, "bin", "python"), "#!/bin/sh\n")
	dev := map[string]devSource{"github.com/me/toyrepo": {Path: checkout, Env: env}}
	want := AskedService{Version: "deadbeef", Source: map[string]any{"git": "https://github.com/me/toyrepo.git", "rev": "abc", "dir": "svc"}}
	l := &looker{dev: dev}
	got, err := l.devLook("toy", want)
	if err != nil || got == nil || got.State != "ready" || got.Key != "toy#dev" || got.Origin != "declared" || got.Changed == 0 {
		t.Fatalf("%+v %v", got, err)
	}
	other := want
	other.Source = map[string]any{"git": "https://github.com/other/repo", "dir": "svc"}
	if got, _ = l.devLook("toy", other); got != nil {
		t.Errorf("another repository: %+v", got)
	}
	d := DevFor(want, dev)
	p, err := d.Described("toy", want)
	if err != nil {
		t.Fatal(err)
	}
	words, err := d.Command(p, "toy")
	if err != nil || words[0] != python || words[1] != filepath.Join(checkout, "svc", "toy.py") {
		t.Errorf("%q %v", words, err)
	}
	sig, _ := p.Signature("toy")
	put(t, filepath.Join(checkout, "svc", "toy.py"), "print( 'edited' )\n")
	if now, _ := p.Signature("toy"); now == sig {
		t.Errorf("edited, it is the same")
	}
	put(t, filepath.Join(tmp, "config", "errand", devOff), fmt.Sprintf("[%q]", checkout))
	if got, _ = l.devLook("toy", want); got.State != "unavailable" || !got.Off {
		t.Errorf("turned off: %+v", got)
	}
	_ = os.Remove(filepath.Join(tmp, "config", "errand", devOff))
	// a copy of a project carries those inside it
	o := originFor(filepath.Join(tmp, "proj"), map[string]devSource{"github.com/me/toyrepo": {Path: checkout, Env: "far", Envs: map[string]string{machineName(): env}}, "github.com/x/y": {Path: tmp}})
	if len(caches.ListOf(o["dev"])) != 1 {
		t.Fatalf("%v", o)
	}
	copyOf := filepath.Join(tmp, "copy")
	put(t, filepath.Join(copyOf, "toyrepo", "svc", "toy.py"), "print( 'toy' )\n")
	put(t, filepath.Join(copyOf, "toyrepo", "svc", describedFile), textOf(t, filepath.Join(checkout, "svc", describedFile)))
	put(t, filepath.Join(copyOf, filesync.CopyState), "{}")
	b, _ := json.Marshal(o)
	put(t, filepath.Join(copyOf, filesync.CopyOrigin), string(b))
	filesync.NoteCopy(copyOf)
	got, _ = (&looker{}).devLook("toy", want)
	if got == nil || got.State != "ready" || got.Path != filepath.Join(copyOf, "toyrepo") || !strings.HasPrefix(got.Origin, "copy of ") || got.Synced == nil {
		t.Errorf("in a copy: %+v", got)
	}
}

func TestTheVersionsHere(t *testing.T) {
	tmp := servicePlace(t)
	keys := []string{"toy#aaaa1111", "toy#bbbb2222", "toy#cccc3333", "toy#dddd4444"}
	month := time.Now().Add(-30 * 24 * time.Hour)
	for _, k := range keys {
		if _, err := DeclareHere(k, []string{"python3", "-m", "http.server", "{port}"}, nil, "", nil); err != nil {
			t.Fatal(err)
		}
		put(t, filepath.Join(serviceEnv(k), "lib", "big"), strings.Repeat("x", 5000))
		_ = os.Chtimes(serviceEnv(k), month, month)
	}
	_ = changeInstalled(func(all map[string]map[string]any) {
		for _, k := range keys[:3] {
			all[k]["installed"] = month.Format("2006-01-02 15:04:05")
		}
	})
	store := filepath.Join(tmp, "s1.sviz")
	put(t, filepath.Join(store, "services.json"), `{"format": 2, "services": {"toy": {"version": "aaaa1111", "protocol": 1, "described": {}}}}`)
	RegisterView("v1", store)
	if PinVersion(keys[1], true) != nil || PinVersion("toy#nope", true) == nil {
		t.Errorf("pin")
	}
	by := map[string]*serviceVersionHere{}
	versions, _ := VersionsHere(nil, true, VersionDays)
	for _, v := range versions {
		by[v.Key] = v
	}
	if v := by[keys[0]]; len(v.Stores) != 1 || v.Unused {
		t.Errorf("a store asks for it: %+v", v)
	}
	if v := by[keys[1]]; !v.Pinned || v.Unused {
		t.Errorf("pinned: %+v", v)
	}
	if v := by[keys[2]]; !v.Unused || v.Size < 5000 {
		t.Errorf("unused: %+v", v)
	}
	if by[keys[3]].Unused {
		t.Errorf("declared today: used")
	}
	dry := PruneVersions(nil, VersionDays, true)
	if got := dry["versions"].([]string); len(got) != 1 || got[0] != keys[2] || dry["freed"].(int64) < 5000 || !kit.Exists(serviceEnv(keys[2])) {
		t.Errorf("would go: %v", dry)
	}
	if v, _ := VersionsHere(map[string]string{keys[2]: "warm"}, false, VersionDays); v[2].Unused || v[2].State != "warm" {
		t.Errorf("one that runs: %+v", v[2])
	}
	PruneVersions(nil, VersionDays, false)
	if kit.Exists(serviceEnv(keys[2])) || InstalledHere(nil)[keys[2]] != nil || InstalledHere(nil)[keys[1]] == nil {
		t.Errorf("what went")
	}
	_ = PinVersion(keys[1], false)
	if got := PruneVersions(nil, VersionDays, false)["versions"].([]string); len(got) != 1 || got[0] != keys[1] {
		t.Errorf("not pinned any more: %v", got)
	}
	if got := RemoveViews([]string{"../x", "nobody"})["removed"].([]string); len(got) != 0 {
		t.Errorf("a name that is no copy's: %v", got)
	}
}

// What `errand serve install` and `errand serve fetch` do on this machine ( ADR 0027 §4 ): a file checked and unpacked, a
// package in the env of the service it is for, the files of a repository at a commit; and the declarations written here.
//
// A fetch from a source outside `errand.sources` is refused unless the person repeats it ( `--source` ): a store may NAME
// a source, it never vouches for one. Nothing here is reached by a page: the page shows the lines, a person types them.

package svc

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/download"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
)

const pypi = "https://pypi.org/simple"

// where code may be taken from without a second word, when the user's file says nothing
var defaultSources = []string{pypi, "conda-forge", "https://download.blender.org/"}

func UsualSources() []string { return append([]string{}, defaultSources...) }

// platformName: this platform, as descriptions name it: macos-arm64, linux-x86_64, windows-x86_64
func platformName() string {
	system, arch := runtime.GOOS, runtime.GOARCH
	if system == "darwin" {
		system = "macos"
	}
	if arch == "amd64" {
		arch = "x86_64"
	}
	return system + "-" + arch
}

// ── what pages may ask for ───────────────────────────────────────────────────

// PageSpec: what a page's service runs here -- `NAME#VERSION`, its command, and what is said beside it: idle, gpus, gpu_ram,
// ram, cpus, exclusive, grace, keep, others, vars, busy, cwd
func PageSpec(key string, command []string, said map[string]any) (ServiceSpec, error) {
	s := ServiceSpec{Name: key, Label: key, Command: command, Env: map[string]string{}, Needs: map[string]any{}}
	_, s.Version = splitKey(key) // ( two versions are two servers, whatever runs them )
	for k, v := range said {
		var err error
		switch k {
		case "gpus", "gpu_ram", "ram", "cpus":
			s.Needs[k] = fmt.Sprint(v)
		case "idle", "grace", "keep":
			d := 0.0
			if f, ok := v.(float64); ok {
				d = f
			} else {
				d, err = Duration(fmt.Sprint(v))
			}
			*map[string]*float64{"idle": &s.Idle, "grace": &s.Grace, "keep": &s.Keep}[k] = d
		case "exclusive":
			s.Exclusive = v == true
		case "others":
			s.Others = v == true
		case "vars", "env":
			for name, value := range ObjectOf(v) {
				s.Env[name] = fmt.Sprint(value)
			}
		case "busy":
			if v != "" {
				s.Busy = v
			}
		case "cwd":
			s.Cwd = kit.Text(v)
		default:
			err = fmt.Errorf("%s: not something a page's service is given here", k)
		}
		if err != nil {
			return s, err
		}
	}
	s.Complete()
	return s, s.Check()
}

// WrongVersion: why `key` must not start -- the version its provider gives now is another; "" when it may
func WrongVersion(key string, from any) string {
	name, v := splitKey(key)
	file := kit.Text(from)
	if v == "" || file == "" {
		return ""
	}
	data := caches.ReadObject(file)
	p := provider{Dir: filepath.Dir(file), Data: data}
	entry := p.Entry(name)
	if entry == nil {
		return "wrong version: its description " + file + " cannot be read"
	}
	now, err := serviceVersion(name, entry, p.Dir)
	if err != nil {
		return fmt.Sprintf("wrong version: what %s describes cannot be read ( %v )", file, err)
	}
	if now != v {
		return fmt.Sprintf("wrong version: %s now gives %s#%s, declared as #%s", file, name, now, v)
	}
	return ""
}

// ComposedHere: the command of `key` as the package here that provides it describes it, with this machine's tools
func ComposedHere(key string) ([]string, map[string]any, string, error) {
	name, _ := splitKey(key)
	provs := providing(key)
	if len(provs) == 0 {
		return nil, nil, "", fmt.Errorf("no package here provides %s: give the command after --, or fetch the package first", key)
	}
	p := provs[0]
	entry := p.Entry(name)
	tools, missing := map[string]string{}, []string{}
	found := toolsHere(ObjectOf(entry["tools"]), nil)
	for _, t := range sortedKeys(found) {
		if tools[t] = found[t].path; found[t].path == "" {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return nil, nil, "", fmt.Errorf("%s: no %s here that will do ( errand serve look shows what to fetch )", key, strings.Join(missing, ", "))
	}
	words, err := p.compose(name, tools, true)
	if err != nil {
		return nil, nil, "", err
	}
	return words, DescribedNeeds(entry), fmt.Sprintf("service %s, as %s %s in %s describes it:\n  %s", key, p.Package, p.Version, p.Env, quoteAll(words)), nil
}

// Typed: words, as they are typed in a shell
func Typed(words []string) string { return quoteAll(words) }

func quoteAll(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = layer.Quote(w)
	}
	return strings.Join(out, " ")
}

func ObjectOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// AuthorizePage: `key` authorized to pages -- with the words a person typed, else composed from the package here that
// provides it -> the authorization, its hash in it
func AuthorizePage(key string, words []string, needs map[string]any) (Authorization, error) {
	how := Authorization{Page: key, Unverified: true}
	if _, v := splitKey(key); v == "" {
		return how, fmt.Errorf("%s: name#version ( the version is the store's, e.g. blender#3fa29c1e )", key)
	}
	if len(words) == 0 {
		var more map[string]any
		var err error
		if words, more, _, err = ComposedHere(key); err != nil {
			return how, err
		}
		for k, v := range needs {
			more[k] = v
		}
		needs = more
	}
	if provs := providing(key); len(provs) > 0 {
		how.Provider, how.Unverified = provs[0].file(), false
	}
	s, err := PageSpec(key, words, needs)
	if err != nil {
		return how, err
	}
	how.Hash, err = Authorize(s, "pages", how)
	how.Definition, how.Label, how.To = Definition(s), key, []string{"pages"}
	return how, err
}

// ── sources ──────────────────────────────────────────────────────────────────

func startsWithSource(from, source string) bool {
	return from == source || strings.HasPrefix(from, strings.TrimRight(source, "/")+"/")
}

// trusted: whether a source is the user's, or is the repository of a package installed here
func trusted(from string) bool {
	list := defaultSources
	if c := ReadConfig(); c.Sources != nil {
		list = c.Sources
	}
	for _, s := range list {
		if startsWithSource(from, s) {
			return true
		}
	}
	return knownRepos()[httpsURL(from)]
}

func sourceAllowed(from string, said []string) error {
	if trusted(from) {
		return nil
	}
	for _, s := range said {
		if startsWithSource(from, s) {
			return nil
		}
	}
	return fmt.Errorf("%s: not in errand.sources ( an unknown source ): repeat it with --source to fetch it all the same", from)
}

// ── a file ───────────────────────────────────────────────────────────────────

var urlWithHash = regexp.MustCompile(`^([^#]+)#sha256=([0-9a-fA-F]{64})$`)

func toolDir(sha string) string {
	return filepath.Join(kit.LocalCache(), "tools", strings.ToLower(sha), "unpacked")
}

// fetchFile: a file, checked, kept by its contents, unpacked; nothing in it is run
func fetchFile(from, sha string, allowed []string) (string, error) {
	if err := sourceAllowed(from, allowed); err != nil {
		return "", err
	}
	done := toolDir(sha)
	home := filepath.Dir(done)
	if kit.IsDir(done) {
		return done, nil
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return "", err
	}
	name := download.NameFromURL(from)
	digest, err := download.FetchURL(from, home, name, sha, os.Environ(), nil)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(filepath.Join(home, digest))
	tmp, err := os.MkdirTemp(home, "unpacking-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := unpack(filepath.Join(home, digest, name), tmp); err != nil {
		return "", fmt.Errorf("%s: %v", from, err)
	}
	_ = os.Chmod(tmp, 0o755)
	if err := os.Rename(tmp, done); err != nil && !kit.IsDir(done) {
		return "", err
	}
	return done, kit.WriteWholeJSON(filepath.Join(home, "source.json"), map[string]any{"url": from, "sha256": strings.ToLower(sha), "at": time.Now().Format("2006-01-02 15:04:05")})
}

func unpack(archive, into string) error {
	run := func(words ...string) error {
		if said, err := exec.Command(words[0], words[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %v %s", words[0], err, kit.EndOf(string(said), 300))
		}
		return nil
	}
	n := strings.ToLower(filepath.Base(archive))
	switch {
	case strings.HasSuffix(n, ".zip"):
		return unzip(archive, into)
	case strings.Contains(n, ".tar") || strings.HasSuffix(n, ".tgz") || strings.HasSuffix(n, ".txz"):
		return run("tar", "-xf", archive, "-C", into)
	case strings.HasSuffix(n, ".dmg"):
		mount, err := os.MkdirTemp("", "errand-dmg-")
		if err != nil {
			return err
		}
		defer os.Remove(mount)
		if err := run("hdiutil", "attach", "-nobrowse", "-readonly", "-mountpoint", mount, archive); err != nil {
			return err
		}
		defer func() { _ = run("hdiutil", "detach", mount) }()
		items, _ := os.ReadDir(mount)
		for _, item := range items {
			if item.Type()&os.ModeSymlink == 0 {
				if err := run("ditto", filepath.Join(mount, item.Name()), filepath.Join(into, item.Name())); err != nil {
					return err
				}
			}
		}
		return nil
	}
	at := filepath.Join(into, filepath.Base(archive)) // a bare program
	if err := os.Rename(archive, at); err != nil {
		return err
	}
	return os.Chmod(at, 0o755)
}

func unzip(archive, into string) error {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer z.Close()
	for _, f := range z.File {
		if !filepath.IsLocal(filepath.FromSlash(f.Name)) {
			return fmt.Errorf("%q: not a path of an archive", f.Name)
		}
		at := filepath.Join(into, filepath.FromSlash(f.Name))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(at, 0o755); err != nil {
				return err
			}
			continue
		}
		if !f.Mode().IsRegular() {
			continue // ( a link: what it leads to is nobody's to say )
		}
		if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
			return err
		}
		in, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(at, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode().Perm()|0o600)
		if err == nil {
			_, err = io.Copy(out, in)
			if e := out.Close(); err == nil {
				err = e
			}
		}
		in.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// ── a package, a repository ──────────────────────────────────────────────────

var (
	pipSpec  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(\[[A-Za-z0-9,._-]+\])?(==[A-Za-z0-9.+!_-]+)?$`)
	gitSpec  = regexp.MustCompile(`^git\+((?:https?|file)://[^@#\s]+)@([A-Za-z0-9._/-]+)(#subdirectory=[A-Za-z0-9._/-]+)?$`)
	gitPath  = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	notInEnv = regexp.MustCompile(`[^A-Za-z0-9._@-]+`)
)

// serviceEnv: the env of a service ( `sdot.ao#dc9530a9` -> `svc-envs/sdot.ao@dc9530a9` ): one per service
func serviceEnv(key string) string {
	name := strings.NewReplacer("#", "@", "==", "-").Replace(key)
	return filepath.Join(serviceEnvsDir(), strings.Trim(notInEnv.ReplaceAllString(name, "-"), "-"))
}

func uvHere() string {
	if p, err := exec.LookPath("uv"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{filepath.Join(home, ".local", "bin", "uv"), filepath.Join(kit.LocalCache(), "uv", "uv")} {
		if kit.IsFile(p) {
			return p
		}
	}
	return ""
}

func quietly(words ...string) error {
	said, err := exec.Command(words[0], words[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v\n%s", quoteAll(words[:min(len(words), 4)]), err, kit.EndOf(string(said), 1200))
	}
	return nil
}

// fetchPackage: a package ( `sdot==2026.11.3`, or `git+URL@REV` ) into the env of a service, or into one that is there
func fetchPackage(spec, index, into, forKey string, allowed []string) (string, error) {
	if g := gitSpec.FindStringSubmatch(spec); g != nil {
		if err := sourceAllowed(httpsURL(g[1]), allowed); err != nil {
			return "", err
		}
	} else if !pipSpec.MatchString(spec) {
		return "", fmt.Errorf("%q: a package and its version ( sdot==2026.11.3 ), or git+URL@COMMIT", spec)
	}
	if index == "" {
		index = pypi
	}
	if err := sourceAllowed(index, allowed); err != nil {
		return "", err
	}
	if into != "" {
		prefix := into
		if strings.HasPrefix(prefix, "~/") {
			home, _ := os.UserHomeDir()
			prefix = filepath.Join(home, prefix[2:])
		}
		if !kit.IsDir(prefix) {
			prefix = ""
			for _, e := range serviceEnvs() {
				if filepath.Base(e) == into {
					prefix = e
				}
			}
			if prefix == "" {
				return "", fmt.Errorf("%s: no such env here", into)
			}
		}
		python := pythonIn(prefix)
		if python == "" {
			return "", fmt.Errorf("%s: no Python in it", prefix)
		}
		return prefix, quietly(python, "-m", "pip", "install", "--index-url", index, spec)
	}
	if forKey == "" {
		return "", fmt.Errorf("%s: for which service? ( --for NAME#VERSION: one env per service )", spec)
	}
	prefix, uv := serviceEnv(forKey), uvHere()
	if pythonIn(prefix) == "" {
		if err := os.MkdirAll(filepath.Dir(prefix), 0o755); err != nil {
			return "", err
		}
		make := []string{uv, "venv", "--seed", prefix}
		if uv == "" {
			python, err := exec.LookPath("python3")
			if err != nil {
				if python, err = exec.LookPath("python"); err != nil {
					return "", errors.New("neither uv nor a Python here, to make the env of a service with")
				}
			}
			make = []string{python, "-m", "venv", prefix}
		}
		if err := quietly(make...); err != nil {
			return "", err
		}
	}
	python := pythonIn(prefix)
	if uv != "" {
		return prefix, quietly(uv, "pip", "install", "--python", python, "--index-url", index, spec)
	}
	return prefix, quietly(python, "-m", "pip", "install", "--index-url", index, spec)
}

// fetchRepository: the files of a repository at a commit, kept by it; nothing in them is run. `only`: paths of it, the
// rest left out -- a sparse checkout, their blobs only; asked again for other paths, they are added to what is there.
func fetchRepository(from, rev string, only, allowed []string) (string, error) {
	from = httpsURL(from)
	if err := sourceAllowed(from, allowed); err != nil {
		return "", err
	}
	if strings.HasPrefix(from, "-") || !gitPath.MatchString(rev) || strings.HasPrefix(rev, "-") {
		return "", fmt.Errorf("%q: a commit", rev)
	}
	wanted := map[string]bool{}
	for _, o := range only {
		if !gitPath.MatchString(o) || hasDots(o) {
			return "", fmt.Errorf("%q: a path of the repository", o)
		}
		wanted[strings.Trim(o, "/")] = true
	}
	only = sortedKeys(wanted)
	done := repoDir(from, rev)
	said := caches.ReadObject(filepath.Join(done, sourceFile))
	if said != nil {
		if said["dirs"] == nil {
			return done, nil // the whole repository is there
		}
		missing := len(only) == 0
		for _, o := range only {
			covered := false
			for _, d := range kit.Strs(said["dirs"]) {
				covered = covered || o == d || strings.HasPrefix(o, d+"/")
			}
			missing = missing || !covered
		}
		if !missing {
			return done, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(done), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(done), "fetching-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	steps := [][]string{{"init", "-q"}}
	fetch := []string{"fetch", "-q", "--depth", "1"}
	if len(only) > 0 {
		sparse := []string{"sparse-checkout", "set", "--no-cone"}
		for _, o := range only {
			sparse = append(sparse, "/"+o)
		}
		steps, fetch = append(steps, sparse), append(fetch, "--filter=blob:none")
	}
	steps = append(steps, append(fetch, from, rev), []string{"-c", "advice.detachedHead=false", "checkout", "-q", "FETCH_HEAD"})
	for _, s := range steps {
		if out, err := exec.Command("git", append([]string{"-C", tmp}, s...)...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("%s@%s: git %s: %s", from, rev, strings.Join(s, " "), kit.EndOf(strings.TrimSpace(string(out)), 300))
		}
	}
	_ = os.RemoveAll(filepath.Join(tmp, ".git")) // the files only
	_ = os.Chmod(tmp, 0o755)
	now := map[string]any{"git": from, "rev": rev, "at": time.Now().Format("2006-01-02 15:04:05")}
	if said != nil && len(only) > 0 { // other paths of a commit that is here
		dirs := map[string]bool{}
		for _, d := range append(kit.Strs(said["dirs"]), only...) {
			dirs[d] = true
		}
		for _, o := range only {
			src, dst := filepath.Join(tmp, filepath.FromSlash(o)), filepath.Join(done, filepath.FromSlash(o))
			if kit.Exists(src) && !kit.Exists(dst) && os.MkdirAll(filepath.Dir(dst), 0o755) == nil {
				if err := os.Rename(src, dst); err != nil {
					return "", err
				}
			}
		}
		now["dirs"] = sortedKeys(dirs)
		return done, kit.WriteWholeJSON(filepath.Join(done, sourceFile), now)
	}
	if len(only) > 0 {
		now["dirs"] = only
	}
	if err := kit.WriteWholeJSON(filepath.Join(tmp, sourceFile), now); err != nil {
		return "", err
	}
	if said != nil { // ( a part of it was here: the whole takes its place )
		_ = os.RemoveAll(done)
	}
	return done, os.Rename(tmp, done)
}

func hasDots(p string) bool {
	for _, x := range strings.Split(p, "/") {
		if x == ".." {
			return true
		}
	}
	return false
}

// FetchFrom: what `errand serve fetch` is given
func FetchFrom(source, index, into, forKey string, allowed []string) (map[string]any, error) {
	switch {
	case strings.HasPrefix(source, "pip:"), strings.HasPrefix(source, "git+"):
		env, err := fetchPackage(strings.TrimPrefix(source, "pip:"), index, into, forKey, allowed)
		return map[string]any{"env": env}, err
	case strings.HasPrefix(source, "git:"):
		spec, paths, _ := strings.Cut(source[4:], "#paths=")
		i := strings.LastIndex(spec, "@")
		if i <= 0 || i == len(spec)-1 {
			return nil, fmt.Errorf("%q: git:URL@COMMIT[#paths=A,B]", source)
		}
		var only []string
		for _, p := range strings.Split(paths, ",") {
			if p != "" {
				only = append(only, p)
			}
		}
		path, err := fetchRepository(spec[:i], spec[i+1:], only, allowed)
		return map[string]any{"path": path}, err
	}
	m := urlWithHash.FindStringSubmatch(source)
	if m == nil {
		return nil, fmt.Errorf("%q: URL#sha256=<64 hex digits>, git:URL@COMMIT, git+URL@COMMIT or pip:PACKAGE==VERSION", source)
	}
	path, err := fetchFile(m[1], m[2], allowed)
	return map[string]any{"path": path}, err
}

// ── what a person ran, to know a version ─────────────────────────────────────

func probesFile() string { return filepath.Join(kit.LocalCache(), "serving", "probes.json") }

func fileStamp(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", st.Size(), st.ModTime().Unix())
}

var versionWord = regexp.MustCompile(`\b(\d+\.\d+(?:\.\d+)?)\b`)

// ProbeProgram: a program run once with the arguments a person typed; the first version of its output is kept
func ProbeProgram(argv []string) (map[string]any, error) {
	if len(argv) == 0 {
		return nil, errors.New("nothing to run")
	}
	path, err := kit.LookIn(argv[0], os.Environ(), ".")
	if err != nil {
		return nil, fmt.Errorf("%s: %v", argv[0], err)
	}
	type got struct {
		out []byte
		err error
	}
	done := make(chan got, 1)
	cmd := exec.Command(path, argv[1:]...)
	go func() { out, err := cmd.CombinedOutput(); done <- got{out, err} }()
	var g got
	select {
	case g = <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("%s: no answer within 5 s", argv[0])
	}
	m := versionWord.FindStringSubmatch(string(g.out))
	if m == nil {
		return nil, fmt.Errorf("%s: no version in its output: %s", argv[0], kit.EndOf(strings.TrimSpace(string(g.out)), 400))
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	all := caches.ReadObject(probesFile())
	if all == nil {
		all = map[string]any{}
	}
	all[path] = map[string]any{"stamp": fileStamp(path), "version": m[1], "how": "probed ( " + strings.Join(argv, " ") + " )"}
	if err := os.MkdirAll(filepath.Dir(probesFile()), 0o755); err != nil {
		return nil, err
	}
	return map[string]any{"version": m[1], "output": kit.FirstLine(strings.TrimSpace(string(g.out)))}, caches.WriteObject(probesFile(), all, true)
}

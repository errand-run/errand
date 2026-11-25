// What a run finds in its surroundings beside the protocol's own variables ( the guide's "What a run says" ): where
// its libraries' caches go ( ADR 0019, 0022 ), where its environment's builds go, the project's own sources and
// errand's Python on its `PYTHONPATH`, the shell's functions; and what its tool needs, installed when it is missing.

package agent

import (
	"crypto/rand"
	"crypto/sha1"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/runinfo"
	"github.com/errand-run/errand/lib"
)

//go:embed steps.sh
var shellSteps string

// handed: the rest of what a run is handed
func (j *job) handed(env []string, set func(k, v string)) {
	root := j.rec.Project
	where, view := cachesOf(j.outputs, root, env)
	// where what it builds goes ( ADR 0039 ): the directory of its environment, or what the project says
	builds := filepath.Join(where, "build", project.BuildName(j.spec.Env))
	main := filepath.Join(builds, project.MainBuild)
	if dir := filepath.FromSlash(j.spec.BuildDir); dir != "" {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		if !strings.HasSuffix(filepath.ToSlash(dir), "/"+path.Join(project.Builds, project.BuildName(j.spec.Env), project.MainBuild)) {
			main = dir // ( said by the project: `configure( build = … )` )
		}
	}
	set("ERRAND_BUILDS", builds)
	set("ERRAND_BUILD_DIR", main)
	if uses := strings.Join(j.argv, "\x00"); strings.Contains(uses, builds) || (j.spec.BuildDir != "" && strings.Contains(uses, j.spec.BuildDir)) {
		caches.BuildUsed(filepath.Dir(builds), project.BuildName(j.spec.Env), project.MainBuild)
	}
	set("ERRAND_CACHES", where)
	set("ERRAND_CACHES_VIEW", view)
	set("ERRAND_STEPS_SH", shellSteps)
	if _, said := kit.EnvOf(env, "PYTHONFAULTHANDLER"); !said { // a Python that crashes says where, unless told otherwise
		set("PYTHONFAULTHANDLER", "1")
	}
	// the project's own sources first, rather than somebody else's checkout of them; errand's Python last
	var paths []string
	src := j.spec.Src
	if len(src) == 0 {
		src = []string{"src"}
	}
	for _, s := range src {
		p := filepath.Join(root, filepath.FromSlash(s))
		if filepath.IsAbs(s) {
			p = s
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			paths = append(paths, p)
		}
	}
	paths = append(paths, root)
	if was, _ := kit.EnvOf(env, "PYTHONPATH"); was != "" {
		for _, p := range filepath.SplitList(was) { // ( less the Python of the errand whose run this one is started in: another version, maybe )
			if at := filepath.Dir(p); filepath.Base(at) == "binding" && kit.IsFile(filepath.Join(p, "errand", "held.py")) {
				continue
			}
			paths = append(paths, p)
		}
	}
	if dir, err := lib.Dir(); err == nil {
		paths = append(paths, dir)
		include := filepath.Join(dir, "errand", "include")
		set("ERRAND_INCLUDE", include)
		for i := range j.argv {
			j.argv[i] = strings.ReplaceAll(j.argv[i], "{errand_include}", include) // ( a build line's )
		}
	}
	set("PYTHONPATH", strings.Join(paths, string(os.PathListSeparator)))
}

// cachesOf: where a library puts its caches, for a run of this project ( `$ERRAND_CACHES`: the outputs' `caches/`, made
// and marked as a cache ), and what it is seen from -- written down in this machine's registry ( ADR 0022 ), so that
// what its caches no longer need can be found
func cachesOf(outputs, root string, env []string) (string, string) {
	view, said := kit.EnvOf(env, "ERRAND_CACHES_VIEW")
	if !said {
		view = cachesView()
	}
	where := filepath.Join(outputs, "caches")
	tag := filepath.Join(where, "CACHEDIR.TAG")
	if _, err := os.Stat(tag); err != nil {
		_ = os.MkdirAll(where, 0o755)
		_ = os.WriteFile(tag, []byte("Signature: 8a477f597d28d172789f06886806bc55\n# made by errand: the caches of what runs here\n"), 0o644)
	}
	if real, err := filepath.EvalSymlinks(where); err == nil {
		where = real
	}
	registerCaches(where, view, root)
	return where, view
}

func cachesView() string {
	for _, name := range []string{"APPTAINER_CONTAINER", "SINGULARITY_CONTAINER"} {
		if image := os.Getenv(name); image != "" {
			return "apptainer:" + filepath.Base(image)
		}
	}
	for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(p); err == nil {
			return "container:" + queue.Host()
		}
	}
	return ""
}

var cachesSeen sync.Map

func registerCaches(where, view, root string) {
	key := where + "\x00" + view + "\x00" + root
	if _, seen := cachesSeen.LoadOrStore(key, true); seen {
		return
	}
	said := caches.CachesIdentOf(where)
	if said == nil { // written once, by the first run to use it: two paths that say one id are one directory
		id := make([]byte, 8)
		_, _ = rand.Read(id)
		host, _ := os.Hostname()
		body, _ := json.Marshal(map[string]any{"id": hex.EncodeToString(id), "host": host, "made": caches.NowIso(caches.Clock())})
		if f, err := os.OpenFile(filepath.Join(where, caches.CachesIdent), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644); err == nil {
			_, _ = f.Write(body)
			f.Close()
		}
		said = caches.CachesIdentOf(where)
	}
	sum := sha1.Sum([]byte(where))
	path := filepath.Join(kit.LocalCache(), "caches", hex.EncodeToString(sum[:])[:16]+".json")
	old := caches.ReadObject(path)
	views := map[string]bool{view: true}
	for _, v := range caches.StringsOf(old["views"]) {
		views[v] = true
	}
	all := make([]string, 0, len(views))
	for v := range views {
		all = append(all, v)
	}
	sort.Strings(all)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = caches.WriteObject(path, map[string]any{"path": where, "id": said["id"], "host": said["host"], "views": all, "project": root, "seen": caches.NowIso(caches.Clock())}, false)
}

// ── what a runner's tool needs ───────────────────────────────────────────────

var requiredThere sync.Map // what was found there once, by this agent: an environment, an interpreter, a module

// requireTool: the modules a runner says its tool needs, in the interpreter the run is started by -- installed when
// they are missing and the environment is errand's to install into, else a word that says what to do. The system's
// interpreter is shared with everything else on the machine, and is not.
func (j *job) requireTool(stack []project.Layer, lctx layer.Context, env []string) string {
	req := j.spec.Requires
	if req == nil || j.spec.Ranks > 1 {
		return ""
	}
	kept := j.argv
	defer func() { j.argv = kept }()
	start := kept[:1] // the interpreter: the words before its first option
	for i, w := range kept {
		if i > 0 && strings.HasPrefix(w, "-") {
			start = kept[:i]
			break
		}
	}
	ask := func(words ...string) (int, string) {
		j.argv = append(append([]string{}, start...), words...)
		full := append([]string{}, env...)
		got, first, _, err := j.command(stack, lctx, &full)
		if err != nil {
			return 126, err.Error()
		}
		for k, v := range got.Env {
			full = append(full, k+"="+v)
		}
		if first >= 0 && first < len(got.Argv) && got.Argv[first] == "python" {
			for _, python := range []string{j.spec.Python, "python", "python3"} {
				if _, err := kit.LookIn(python, full, j.cwd); python != "" && err == nil {
					got.Argv[first] = python
					break
				}
			}
		}
		path, err := kit.LookIn(got.Argv[0], full, j.cwd)
		if err != nil {
			return 127, err.Error()
		}
		cmd := &exec.Cmd{Path: path, Args: got.Argv, Env: full, Dir: j.cwd}
		out, err := cmd.CombinedOutput()
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), string(out)
		} else if err != nil {
			return 126, err.Error()
		}
		return 0, string(out)
	}
	for _, m := range req.Python {
		key := strings.Join([]string{j.spec.Env, j.rec.Project, fmt.Sprint(j.spec.Tags), strings.Join(start, " "), m.Module}, "\x00")
		if _, known := requiredThere.Load(key); known {
			continue
		}
		if code, _ := ask("-c", "import "+m.Module); code != 0 {
			allowed := req.Install == true
			if req.Install == "auto" || req.Install == nil {
				_, said := ask("-c", "import os, sys; print( sys.prefix != sys.base_prefix or bool( os.environ.get( 'CONDA_PREFIX' ) or os.environ.get( 'VIRTUAL_ENV' ) ) )")
				allowed = strings.TrimSpace(said) == "True"
			}
			if !allowed {
				return fmt.Sprintf("%s has no %s, and it is the system interpreter: installing into it is not errand's to do.\n"+
					"  install it yourself, point the runner at another interpreter ( the runner's python = ..., or the environment's tools = { \"python\": ... } ), "+
					"or say install = True", strings.Join(start, " "), m.Spec)
			}
			// one at a time, for this environment, whoever asks
			release := lockRequired(filepath.Join(j.outputs, "builds", "records"), j.spec.Env)
			if code, _ = ask("-c", "import "+m.Module); code != 0 {
				j.note("  installing " + m.Spec + " into " + strings.Join(start, " ")) // never silent: it is an act
				code, said := ask("-m", "pip", "install", m.Spec)
				if code != 0 {
					release()
					if len(said) > 2000 {
						said = said[len(said)-2000:]
					}
					return fmt.Sprintf("could not install %s:\n%s", m.Spec, said)
				}
			}
			release()
		}
		requiredThere.Store(key, true)
	}
	return ""
}

// lockRequired: a directory made and removed, whose maker says it is alive by its date
func lockRequired(dir, env string) func() {
	_ = os.MkdirAll(dir, 0o755)
	lock := filepath.Join(dir, runinfo.NameOf("requires-"+env)+".lock")
	for {
		if os.Mkdir(lock, 0o755) == nil {
			break
		}
		if st, err := os.Stat(lock); err == nil && time.Since(st.ModTime()) > 2*time.Minute {
			_ = os.Remove(lock)
			continue
		}
		time.Sleep(300 * time.Millisecond)
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(20 * time.Second):
				now := time.Now()
				_ = os.Chtimes(lock, now, now)
			}
		}
	}()
	return func() { close(done); _ = os.Remove(lock) }
}

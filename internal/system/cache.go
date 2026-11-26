package system

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// Instances: errands of one account side by side, to try a build without touching the one in use. `ERRAND_INSTANCE=try`
// gives everything of errand's its own name -- its directory `<errand's>/try`, its configuration, its sockets, its units, and
// the same over there on the machines it reaches. The one in use is `main`. The machine's queue stays one: a card is one.
//
//	<ERRAND_CACHE_DIR, or ~/.cache/errand>/
//	    main/   the errand in use
//	    try/    another, .errand-instance in it
//
// Each has its stable name in it: `main/bin/errand`, what a key forces.

const instanceMark = ".errand-instance"

var instanceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// names a directory of errand had before instances: an instance is never called so
var before = map[string]bool{"bin": true, "binding": true, "caches": true, "copies": true, "hosts": true, "machines": true, "page": true, "place": true,
	"runs": true, "serving": true, "svc-envs": true, "repos": true, "tools": true, "views": true, "leases": true, "kept": true, "uv": true, "once": true}

// Instance: which errand this is; "main" unless ERRAND_INSTANCE says another
func Instance() string {
	n := os.Getenv("ERRAND_INSTANCE")
	if n == "" {
		return "main"
	}
	if !instanceName.MatchString(n) || before[n] {
		fmt.Fprintf(os.Stderr, "errand: ERRAND_INSTANCE=%q: lowercase letters, digits and -, and none of errand's own names; taken as \"main\"\n", n)
		return "main"
	}
	return n
}

// Base: where the instances are, on this machine, for this account
func Base() string {
	if c := os.Getenv("ERRAND_CACHE_DIR"); c != "" {
		return c
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "errand")
}

// Usual: whether this is the errand in use, where it usually is
func Usual() bool { return os.Getenv("ERRAND_CACHE_DIR") == "" && Instance() == "main" }

// Cache: errand's directory on this machine, for this account and this instance
func Cache() string {
	base, inst := Base(), Instance()
	if inst != "main" {
		dir := filepath.Join(base, inst)
		if _, err := os.Stat(filepath.Join(dir, instanceMark)); err != nil && os.MkdirAll(dir, 0o755) == nil {
			_ = os.WriteFile(filepath.Join(dir, instanceMark), []byte(inst+"\n"), 0o644)
		}
		return dir
	}
	main := filepath.Join(base, "main")
	if isDir(main) || !laidOutBefore(base) {
		return main
	}
	if moveToMain(base) {
		return main
	}
	return base // ( as it was, until no agent of it runs: then it moves )
}

func isDir(p string) bool { st, err := os.Stat(p); return err == nil && st.IsDir() }

// laidOutBefore: whether errand's directory holds the errand in use as it was before instances
func laidOutBefore(base string) bool {
	for _, n := range []string{"dir.id", "place", "caches", "page", "copies", "machines"} {
		if _, err := os.Lstat(filepath.Join(base, n)); err == nil {
			return true
		}
	}
	return false
}

func isLink(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode()&os.ModeSymlink != 0
}

var moving sync.Mutex

// moveToMain: what was errand's directory, moved into main/ -- once, when no agent nor page of it runs; a rename, nothing
// copied. Its stable name is main's from then on: `<errand's>/main/bin/errand`.
func moveToMain(base string) bool {
	moving.Lock()
	defer moving.Unlock()
	main := filepath.Join(base, "main")
	if isDir(main) {
		return true
	}
	if running(base) {
		return false
	}
	tmp := filepath.Join(base, ".main-moving")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return false
	}
	entries, _ := os.ReadDir(base)
	for _, e := range entries {
		n := e.Name()
		if n == ".main-moving" || n == "main" {
			continue
		}
		if _, err := os.Stat(filepath.Join(base, n, instanceMark)); err == nil {
			continue // another instance
		}
		if err := os.Rename(filepath.Join(base, n), filepath.Join(tmp, n)); err != nil {
			fmt.Fprintf(os.Stderr, "errand: moving %s into %s: %v\n", n, main, err)
			return false
		}
	}
	_ = os.WriteFile(filepath.Join(tmp, instanceMark), []byte("main\n"), 0o644)
	if err := os.Rename(tmp, main); err != nil {
		return false
	}
	return true
}

// Running: whether an agent or the page of the errand at `dir` runs
func Running(dir string) bool { return running(dir) }

// StopIn: the agents and the page of the errand at `dir`, asked to stop, then made to
func StopIn(dir string) {
	versions, _ := filepath.Glob(filepath.Join(dir, "place", "*", "*", "version"))
	for _, f := range append(versions, filepath.Join(dir, "page", "said.json")) {
		var said struct{ Pid int }
		if data, err := os.ReadFile(f); err == nil && json.Unmarshal(data, &said) == nil && said.Pid > 0 && said.Pid != os.Getpid() && Alive(said.Pid) {
			Terminate(said.Pid)
		}
	}
}

// running: whether an agent or the page of the errand at `dir` runs ( as their files say: pid, alive )
func running(dir string) bool {
	versions, _ := filepath.Glob(filepath.Join(dir, "place", "*", "*", "version"))
	for _, f := range append(versions, filepath.Join(dir, "page", "said.json")) {
		var said struct{ Pid int }
		if data, err := os.ReadFile(f); err == nil && json.Unmarshal(data, &said) == nil && said.Pid > 0 && said.Pid != os.Getpid() && Alive(said.Pid) {
			return true
		}
	}
	return false
}

// ConfigDir: errand's configuration, of this user and instance ( `$XDG_CONFIG_HOME/errand/<instance>` ) -- what was there
// before instances moved into main/ the first time it is asked for
func ConfigDir(environ []string, getenv func([]string, string) (string, bool)) string {
	base, _ := getenv(environ, "XDG_CONFIG_HOME")
	if base == "" {
		home, _ := getenv(environ, "HOME")
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		base = filepath.Join(home, ".config")
	}
	root, inst := filepath.Join(base, "errand"), Instance()
	if v, ok := getenv(environ, "ERRAND_INSTANCE"); ok && instanceName.MatchString(v) && !before[v] {
		inst = v
	}
	dir := filepath.Join(root, inst)
	if inst == "main" && !isDir(dir) {
		entries, _ := os.ReadDir(root)
		var files []string
		for _, e := range entries {
			if !e.IsDir() {
				files = append(files, e.Name())
			}
		}
		if len(files) > 0 && os.MkdirAll(dir, 0o700) == nil {
			for _, n := range files {
				_ = os.Rename(filepath.Join(root, n), filepath.Join(dir, n))
			}
		}
	}
	return dir
}

// CacheShell: a line of shell that leaves in `$c` errand's directory over there, for `base` ( an expression ) and this
// instance -- as Cache says it, the directory of before kept for main until it is moved
func CacheShell(base string) string {
	if inst := Instance(); inst != "main" {
		return `b=` + base + `; c="$b/` + inst + `"; `
	}
	return `b=` + base + `; c="$b/main"; if [ ! -d "$c" ] && { [ -e "$b/dir.id" ] || [ -e "$b/place" ] || [ -e "$b/caches" ] || [ -e "$b/page" ] || [ -e "$b/copies" ] || [ -e "$b/machines" ]; }; then c="$b"; fi; `
}

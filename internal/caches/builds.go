package caches

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/kit"
)

// The build directories of a project ( ADR 0039 ): `<caches>/build/<env>/<name>`, a cache whose writer is errand. Each is an
// entry, its journal's lines those of any library's; one somebody made without saying is found, and dated by what is in it.
// What it holds is whatever a tool puts there: what any tool makes again is told by its name.

const (
	BuildsCache = "build"
	buildsMark  = "errand-builds"  // in the cache: its writer is errand
	thinDays    = 14.0             // an entry not used for that long loses its intermediates, and keeps the rest
	buildBudget = 10 * time.Second // how long the size of one is looked for
)

func isBuilds(cache string) bool { return kit.Exists(filepath.Join(cache, buildsMark)) }

// markBuilds: a cache of builds is one, for whoever tidies caches
func markBuilds(cache string) error {
	if isBuilds(cache) {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(cache, cacheJournals), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cache, buildsMark), []byte("the directories errand gives what is built: <environment>/<name>\n"), 0o644)
}

// BuildUsed: a build directory is used now -- said in the journal, if it is there
func BuildUsed(cache, env, name string) {
	if !kit.IsDir(filepath.Join(cache, env)) || markBuilds(cache) != nil {
		return
	}
	host, _ := os.Hostname()
	journal := filepath.Join(cache, cacheJournals, fmt.Sprintf("%s-%d-errand.jsonl", strings.SplitN(host, ".", 2)[0], os.Getpid()))
	now, key := NowIso(Clock()), env+"/"+name
	_ = kit.AppendLine(journal, map[string]any{"op": "use", "entries": []string{key}, "at": now})
}

// MakeBuild: a build directory, made -> its path
func MakeBuild(cache, env, name string) (string, error) {
	dir := filepath.Join(cache, env, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := markBuilds(cache); err != nil {
		return "", err
	}
	BuildUsed(cache, env, name)
	return dir, nil
}

// NoteBuilds: what a run left in the place of builds, known as a cache
func NoteBuilds(caches string) {
	if at := filepath.Join(caches, BuildsCache); kit.IsDir(at) {
		_ = markBuilds(at)
	}
}

// BuildsSeen is what there is to say of the builds of a project on a machine.
type BuildsSeen struct {
	Path   string `json:"path"`
	Count  int    `json:"count"`
	Size   int64  `json:"size"`
	Unused int    `json:"unused"` // not used for 30 days
	Held   int64  `json:"held"`   // what those hold
}

// SeeBuilds: the builds of the caches this machine knows, as they were last looked at
func SeeBuilds() []BuildsSeen {
	out := []BuildsSeen{}
	for _, rec := range RegisteredCaches() {
		at, _ := rec["path"].(string)
		cache := filepath.Join(at, BuildsCache)
		if !isBuilds(cache) {
			continue
		}
		s := CacheSummary(cache, UnusedDays, false, true, Clock())
		n, _ := s["entries"].(int)
		size, _ := s["size"].(int64)
		unused, _ := s["unused"].(int)
		held, _ := s["unused_size"].(int64)
		if n > 0 {
			out = append(out, BuildsSeen{cache, n, size, unused, held})
		}
	}
	return out
}

// seeBuilds: the entries of a cache of builds, as the disk has them ( under its lock )
func seeBuilds(cache string, m map[string]any) {
	entries := m["entries"].(map[string]any)
	envs, _ := os.ReadDir(cache)
	for _, env := range envs {
		if !env.IsDir() || env.Name() == cacheJournals || strings.HasPrefix(env.Name(), cacheLock) {
			continue
		}
		names, _ := os.ReadDir(filepath.Join(cache, env.Name()))
		for _, name := range names {
			if !name.IsDir() {
				continue
			}
			key := env.Name() + "/" + name.Name()
			at := NowIso(kit.Seconds(lastIn(filepath.Join(cache, env.Name(), name.Name()))))
			e, known := entries[key].(map[string]any)
			if !known {
				e = map[string]any{"product": []any{key}, "intermediate": []any{}, "needs": []any{}, "shared": false, "size": int64(0), "cost_s": 0.0,
					"deps": map[string]any{}, "view": "", "created": at, "used": at}
				entries[key] = e
			}
			if used, _ := e["used"].(string); at > used {
				e["used"] = at
			}
		}
	}
}

// lastIn: when something was last written in a directory, looked for two levels down: a build writes near its top
func lastIn(dir string) time.Time {
	last := time.Time{}
	var walk func(d string, depth int)
	walk = func(d string, depth int) {
		if st, err := os.Stat(d); err == nil && st.ModTime().After(last) {
			last = st.ModTime()
		}
		listed, _ := os.ReadDir(d)
		for _, e := range listed {
			if e.IsDir() && depth > 0 {
				walk(filepath.Join(d, e.Name()), depth-1)
			} else if st, err := e.Info(); err == nil && st.Mode().IsRegular() && st.ModTime().After(last) {
				last = st.ModTime()
			}
		}
	}
	walk(dir, 2)
	return last
}

// sizeBuilds: how big they are, measured again when they were used since
func sizeBuilds(cache string, m map[string]any) {
	entries := m["entries"].(map[string]any)
	for key := range entries {
		e := entryOf(entries, key)
		if sized, _ := e["sized"].(string); sized != "" && sized >= fmt.Sprint(e["used"]) {
			continue
		}
		if n, whole := SizeWithin(filepath.Join(cache, filepath.FromSlash(key)), buildBudget); whole {
			e["size"], e["sized"] = n, NowIso(Clock())
		}
	}
}

var madeAgain = map[string]bool{".gch": true, ".pch": true}

// objects: how a compiled object begins -- ELF, Mach-O, COFF ( x64, arm64, x86, big ), LLVM's bitcode. Its name is not enough: an
// `.obj` may be a mesh.
var objects = [][]byte{{0x7f, 'E', 'L', 'F'}, {0xcf, 0xfa, 0xed, 0xfe}, {0xce, 0xfa, 0xed, 0xfe}, {0x64, 0x86}, {0x64, 0xaa}, {0x4c, 0x01},
	{0, 0, 0xff, 0xff}, {'B', 'C', 0xc0, 0xde}, {0xde, 0xc0, 0x17, 0x0b}}

// isMadeAgain: whether a file of a build is one that whatever built it makes again by itself
func isMadeAgain(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	if madeAgain[ext] {
		return true
	}
	if ext != ".o" && ext != ".obj" {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	for _, o := range objects {
		if bytes.HasPrefix(head, o) {
			return true
		}
	}
	return false
}

// thinBuild: what every tool makes again, removed from a build directory -> the bytes freed
func thinBuild(dir string, dry bool) int64 {
	var freed int64
	_ = filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		switch {
		case e.IsDir() && e.Name() == "incremental" && p != dir: // cargo's
			if !kit.IsDir(filepath.Join(filepath.Dir(p), "deps")) {
				return nil
			}
			freed += kit.TreeSize(p)
			if !dry {
				_ = os.RemoveAll(p)
			}
			return filepath.SkipDir
		case e.Type().IsRegular() && isMadeAgain(p):
			if st, err := e.Info(); err == nil {
				freed += st.Size()
			}
			if !dry {
				_ = os.Remove(p)
			}
		}
		return nil
	})
	return freed
}

// thin: the entries not used for a while lose what can go alone, and keep the rest ( ADR 0020, rule 3 ) -> how many, the bytes
func thin(root string, m map[string]any, now float64, dry bool) (int, int64) {
	entries := m["entries"].(map[string]any)
	n, total := 0, int64(0)
	builds := isBuilds(root)
	for key := range entries {
		e := entryOf(entries, key)
		used, _ := e["used"].(string)
		if was, _ := e["thinned"].(string); now-isoSeconds(used) < thinDays*day || (was != "" && was >= used) {
			continue
		}
		var freed int64
		switch {
		case builds:
			freed = thinBuild(filepath.Join(root, filepath.FromSlash(key)), dry)
		case len(ListOf(e["intermediate"])) > 0 && dry:
			for _, p := range ListOf(e["intermediate"]) {
				if rel, ok := p.(string); ok && filepath.IsLocal(filepath.FromSlash(rel)) {
					freed += kit.TreeSize(filepath.Join(root, rel))
				}
			}
		case len(ListOf(e["intermediate"])) > 0:
			freed = removePaths(root, ListOf(e["intermediate"]))
		}
		if !dry {
			e["thinned"] = NowIso(now)
		}
		if freed > 0 {
			n, total = n+1, total+freed
			if !dry {
				e["size"] = max(0, asInt(e["size"])-freed)
			}
		}
	}
	return n, total
}

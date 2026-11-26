// Where errand keeps things on a machine, and what its caches hold ( ADR 0028 §1; `caches.summary`
// and `caches.report`, ADR 0022 ): the agent's answer to `caches`.
//
// Sharing is decided per directory, by an id file written once in it: two machines that read the same id share that
// directory. The mount that holds a directory only groups the directories for the screens.

package caches

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/system"
)

const (
	dirID      = "errand-dir.id" // in a directory errand made or uses ( a service env… )
	ownID      = "dir.id"        // in errand's own per-user directory
	SizeBudget = 2 * time.Second // spent measuring one directory, at most ( then said as "at least" )
)

// file system types that are another machine's disk, or a share of a host: several machines may see them
var networkFS = map[string]bool{"nfs": true, "nfs4": true, "cifs": true, "smbfs": true, "smb3": true, "afpfs": true,
	"lustre": true, "gpfs": true, "beegfs": true, "cephfs": true, "ceph": true, "glusterfs": true, "fuse.sshfs": true,
	"sshfs": true, "fuse.glusterfs": true, "fuse.cephfs": true, "webdav": true, "davfs": true, "9p": true,
	"virtiofs": true, "fuse.gcsfuse": true, "panfs": true, "wekafs": true, "fuse.juicefs": true, "autofs": true}

// idOf: who the directory `path` is -- { id, made, host }, from the file `name` in it, written once ( the first to
// come wins ). nil when it cannot be read nor written.
func idOf(path, name string) map[string]any {
	f := filepath.Join(path, name)
	if said := ReadObject(f); said != nil {
		return said
	}
	if !kit.IsDir(path) {
		return nil
	}
	host := system.Hostname()
	said := map[string]any{"id": kit.RandomID(), "made": time.Now().Format("2006-01-02T15:04:05"), "host": host}
	out, err := os.OpenFile(f, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return ReadObject(f) // written by another meanwhile: theirs
	}
	defer out.Close()
	if json.NewEncoder(out).Encode(said) != nil {
		return nil
	}
	return said
}

// ── mounts ───────────────────────────────────────────────────────────────────

type mount struct {
	point, source, fstype string
	Network               bool
}

func (m mount) wire(key string) map[string]any {
	return map[string]any{"point": m.point, "source": m.source, "fstype": m.fstype, "network": m.Network, "key": key}
}

var octal = regexp.MustCompile(`\\([0-7]{3})`)

func unescapeMount(s string) string {
	return octal.ReplaceAllStringFunc(s, func(m string) string {
		n, _ := strconv.ParseInt(m[1:], 8, 32)
		return string(rune(n))
	})
}

// parseMountinfo: Linux' `/proc/self/mountinfo` -- `id parent dev root point options ... - type source superoptions`
func parseMountinfo(text string) []mount {
	var out []mount
	for _, line := range strings.Split(text, "\n") {
		left, right, _ := strings.Cut(line, " - ")
		a, b := strings.Fields(left), strings.Fields(right)
		if len(a) < 5 || len(b) < 2 {
			continue
		}
		out = append(out, mount{point: unescapeMount(a[4]), source: unescapeMount(b[1]), fstype: b[0]})
	}
	return out
}

var mountLine = regexp.MustCompile(`^(.*) on (.*) \(([^,)]+)`)

// parseMount: macOS ( and the BSDs ) -- `mount` says `source on point ( type, options )`
func parseMount(text string) []mount {
	var out []mount
	for _, line := range strings.Split(text, "\n") {
		if m := mountLine.FindStringSubmatch(line); m != nil {
			out = append(out, mount{point: m[2], source: m[1], fstype: strings.TrimSpace(m[3])})
		}
	}
	return out
}

var mountsKept struct {
	sync.Mutex
	at   time.Time
	list []mount
}

// mounts: this machine's mounts, read again at most every minute
func mounts() []mount {
	mountsKept.Lock()
	defer mountsKept.Unlock()
	if len(mountsKept.list) > 0 && time.Since(mountsKept.at) < 60*time.Second {
		return mountsKept.list
	}
	var got []mount
	if runtime.GOOS == "linux" {
		if data, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
			got = parseMountinfo(string(data))
		}
	} else if out, err := exec.Command("/sbin/mount").Output(); err == nil {
		got = parseMount(string(out))
	}
	for i := range got {
		if strings.HasPrefix(got[i].fstype, "nfs") {
			got[i].fstype = "nfs"
		}
		got[i].Network = networkFS[got[i].fstype] || strings.HasPrefix(got[i].source, "//")
	}
	mountsKept.list, mountsKept.at = got, time.Now()
	return got
}

var driveLetter = regexp.MustCompile(`^[A-Z]:$`)

// mountKey: what names a mount across machines -- a network one by its source ( the same export seen from two
// machines is one name ), a local one by the machine and its device ( or its type and point, for one with no device )
func mountKey(m mount) string {
	if m.Network {
		return m.fstype + ":" + m.source
	}
	host := system.Hostname()
	short := strings.SplitN(host, ".", 2)[0]
	if strings.HasPrefix(m.source, "/dev/") || driveLetter.MatchString(m.source) {
		return short + ":" + m.source
	}
	return short + ":" + m.fstype + ":" + m.point
}

func deviceOf(path string) (uint64, bool) {
	return system.Device(path)
}

func SameFile(a, b string) bool {
	x, err1 := os.Stat(a)
	y, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(x, y)
}

// MountOf: the mount that holds `path` -- the one whose point is on the same device ( a firmlink, a bind: the path's
// own prefix can lie ), the deepest such; else the deepest whose point prefixes the real path
func MountOf(path string) (mount, string, bool) {
	ms := mounts()
	if len(ms) == 0 {
		return mount{}, "", false
	}
	abs, _ := filepath.Abs(path)
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return mount{}, "", false
	}
	dev, ok := deviceOf(real)
	if !ok {
		return mount{}, "", false
	}
	var same []mount
	for _, m := range ms {
		if d, ok := deviceOf(m.point); ok && d == dev {
			same = append(same, m)
		}
	}
	under := func(m mount) bool {
		return real == m.point || strings.HasPrefix(real, strings.TrimRight(m.point, "/\\")+"/") || m.point == "/"
	}
	inside := func(m mount) bool { // ( macOS' firmlinks: `/Users/h` is `/System/Volumes/Data/Users/h`, one device )
		return m.point != "/" && SameFile(real, strings.TrimRight(m.point, "/")+real)
	}
	filter := func(from []mount, keep func(mount) bool) []mount {
		var out []mount
		for _, m := range from {
			if keep(m) {
				out = append(out, m)
			}
		}
		return out
	}
	pick := filter(same, inside)
	if len(pick) == 0 {
		pick = filter(same, under)
	}
	if len(pick) == 0 {
		pick = same
	}
	if len(pick) == 0 {
		pick = filter(ms, under)
	}
	if len(pick) == 0 {
		return mount{}, "", false
	}
	best := pick[0]
	for _, m := range pick[1:] {
		if len([]rune(m.point)) > len([]rune(best.point)) {
			best = m
		}
	}
	return best, mountKey(best), true
}

func mountKeyOf(path string) any {
	if _, key, ok := MountOf(path); ok {
		return key
	}
	return nil
}

// ── errand's directories on this machine ─────────────────────────────────────

// SizeWithin: what `path` holds, measured for at most `budget` -> its bytes, and whether that is all of it
func SizeWithin(path string, budget time.Duration) (int64, bool) {
	var total int64
	end := time.Now().Add(budget)
	stack := []string{path}
	for len(stack) > 0 {
		if time.Now().After(end) {
			return total, false
		}
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		listed, _ := os.ReadDir(d)
		for _, e := range listed {
			switch {
			case e.IsDir():
				stack = append(stack, filepath.Join(d, e.Name()))
			case e.Type().IsRegular():
				if st, err := e.Info(); err == nil {
					total += st.Size()
				}
			}
		}
	}
	return total, true
}

// ProjectCopies: the copies of projects held here -- { path, id, synced }; a record whose copy is gone is dropped
func ProjectCopies() []map[string]any {
	var out []map[string]any
	files, _ := filepath.Glob(filepath.Join(kit.LocalCache(), "copies", "*.json"))
	sort.Strings(files)
	for _, f := range files {
		rec := ReadObject(f)
		if rec == nil {
			continue
		}
		path, _ := rec["path"].(string)
		if !lexistsFollowed(filepath.Join(path, filesync.CopyState)) {
			_ = os.Remove(f)
			continue
		}
		out = append(out, rec)
	}
	return out
}

func lexistsFollowed(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// errandDirs: errand's directories on this machine, each with its id and its mount's key -- its own per-user
// directory, the copies of projects, the service envs ( one per version ), the fetched code
func errandDirs(sizes bool) []any {
	cache := kit.LocalCache()
	type found struct{ kind, path, name, label string }
	all := []found{{"errand", cache, ownID, "errand's own"}}
	copies := ProjectCopies()
	for _, rec := range copies {
		path, _ := rec["path"].(string)
		all = append(all, found{"copy", path, filesync.CopyID, filepath.Base(path)})
	}
	envs, _ := filepath.Glob(filepath.Join(cache, "svc-envs", "*"))
	sort.Strings(envs)
	for _, env := range envs {
		if kit.IsDir(env) {
			all = append(all, found{"env", env, dirID, strings.ReplaceAll(filepath.Base(env), "@", "#")})
		}
	}
	if repos := filepath.Join(cache, "repos"); kit.IsDir(repos) {
		all = append(all, found{"code", repos, dirID, "fetched code"})
	}
	out := []any{}
	for _, f := range all {
		said := idOf(f.path, f.name)
		d := map[string]any{"kind": f.kind, "label": f.label, "path": f.path, "id": said["id"], "made_on": said["host"],
			"mount": mountKeyOf(f.path)}
		if f.kind == "copy" {
			d["synced"] = nil
			for _, rec := range copies {
				if rec["path"] == f.path {
					d["synced"] = rec["synced"]
					break
				}
			}
		}
		if sizes && f.kind != "errand" {
			d["size"], d["whole"] = SizeWithin(f.path, SizeBudget)
		}
		out = append(out, d)
	}
	return out
}

// storageLook: this machine's mounts that hold something of errand's ( and the one of its home, and those of the
// paths `extra`: the caches ), and errand's directories
func storageLook(sizes bool, extra []string) map[string]any {
	dirs := errandDirs(sizes)
	keys := map[string]bool{}
	for _, d := range dirs {
		if k, ok := d.(map[string]any)["mount"].(string); ok {
			keys[k] = true
		}
	}
	for _, p := range extra {
		if _, k, ok := MountOf(p); ok {
			keys[k] = true
		}
	}
	var home any
	if h, err := os.UserHomeDir(); err == nil {
		if _, k, ok := MountOf(h); ok {
			keys[k], home = true, k
		}
	}
	ms := map[string]any{}
	for _, m := range mounts() {
		if k := mountKey(m); keys[k] {
			if _, ok := ms[k]; !ok {
				ms[k] = m.wire(k)
			}
		}
	}
	host := system.Hostname()
	return map[string]any{"host": host, "mounts": ms, "dirs": dirs, "home": home}
}

// ── what the caches hold ──────────────────────────────────────────────────────

func entryView(e map[string]any, now, days float64) map[string]any {
	needs := e["needs"]
	if needs == nil {
		needs = []any{}
	}
	view, _ := e["view"].(string)
	cost := e["cost_s"]
	if cost == nil {
		cost = 0.0
	}
	return map[string]any{"size": asInt(e["size"]), "used": e["used"], "created": e["created"], "dead": e["dead"],
		"shared": Truthy(e["shared"]), "needs": needs, "view": view, "cost_s": cost,
		"unused": !Truthy(e["shared"]) && now-isoSeconds(e["used"]) >= days*day}
}

// CacheSummary: one cache as the screens show it. Compacted first when its lock is free and `compacted`; otherwise
// read as it is, the journals not merged yet only counted. `entries`: its entries too ( `list` ).
func CacheSummary(cache string, days float64, entries, compacted bool, now float64) map[string]any {
	var m map[string]any
	if compacted {
		if unlock := lockCache(cache, 0); unlock != nil {
			m = compactCache(cache, Clock())
			if isBuilds(cache) {
				sizeBuilds(cache, m)
				_ = saveCache(cache, m)
			}
			unlock()
		}
	}
	if m == nil {
		m = loadCache(cache)
	}
	journals := 0
	state, _ := m["journals"].(map[string]any)
	listed, _ := os.ReadDir(filepath.Join(cache, cacheJournals))
	for _, e := range listed { // those with lines not merged yet
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		st, err := os.Stat(filepath.Join(cache, cacheJournals, e.Name()))
		if err != nil {
			continue
		}
		var read int64
		if was := ListOf(state[e.Name()]); len(was) == 2 && was[0] != nil && asInt(was[0]) == inode(st) {
			read = asInt(was[1])
		}
		if st.Size() > read {
			journals++
		}
	}
	es := map[string]any{}
	var size, deadSize, unusedSize int64
	dead, unused := 0, 0
	views := map[string]bool{}
	all, _ := m["entries"].(map[string]any)
	for k := range all {
		v := entryView(entryOf(all, k), now, days)
		es[k] = v
		n := v["size"].(int64)
		size += n
		if Truthy(v["dead"]) {
			dead++
			deadSize += n
		}
		if v["unused"].(bool) {
			unused++
			unusedSize += n
		}
		views[v["view"].(string)] = true
	}
	seen := make([]string, 0, len(views))
	for v := range views {
		seen = append(seen, v)
	}
	sort.Strings(seen)
	out := map[string]any{"name": filepath.Base(cache), "entries": len(es), "size": size, "dead": dead, "dead_size": deadSize,
		"unused": unused, "unused_size": unusedSize, "journals": journals, "purged": m["purged"], "views": seen}
	if entries {
		out["list"] = es
	}
	return out
}

// servicesMaybe: whether something of this machine is a service's ( a version, its env, fetched code, a dev source ):
// what is worth asking about
func servicesMaybe() bool {
	cache := kit.LocalCache()
	for _, pattern := range []string{"svc-envs/*", "repos/*", "serving/*"} {
		if found, _ := filepath.Glob(filepath.Join(cache, pattern)); len(found) > 0 {
			return true
		}
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		home, _ := os.UserHomeDir()
		config = filepath.Join(home, ".config")
	}
	for _, name := range []string{"config.json", "services-installed.json"} {
		if lexistsFollowed(filepath.Join(config, "errand", name)) {
			return true
		}
	}
	for _, rec := range ProjectCopies() { // a copy that carries dev sources
		path, _ := rec["path"].(string)
		if o := ReadObject(filepath.Join(path, filesync.CopyOrigin)); o != nil && len(ListOf(o["dev"])) > 0 {
			return true
		}
	}
	return false
}

// CachesReport: what this machine holds -- { host, time, dirs: [ { path, id, host, views, project, mount, caches:
// [ summary ] } ], storage }. `entries`: the names of the caches whose entries come too ( `*`: all ). `purge`: ADR
// 0020's rules first ( { dry_run, unused_days } ), in the bare view, each cache's report as `purged_now`.
// `services`: what says the services' part of the storage ( nil: there is none on this machine ).
func CachesReport(req map[string]any, services func() map[string]any) map[string]any {
	days := UnusedDays
	if Truthy(req["days"]) {
		days = kit.Num(req["days"], UnusedDays)
	}
	wanted := map[string]bool{}
	for _, n := range kit.Strs(req["entries"]) {
		wanted[n] = true
	}
	purge, purging := req["purge"].(map[string]any)
	dry := purging && Truthy(purge["dry_run"])
	dirs := []any{}
	var paths []string
	for _, rec := range RegisteredCaches() {
		path, _ := rec["path"].(string)
		caches := []any{}
		for _, cache := range CachesIn(path) {
			caches = append(caches, func() (s map[string]any) {
				defer func() { // one cache that cannot be read is not the report's failure
					if r := recover(); r != nil {
						s = map[string]any{"name": filepath.Base(cache), "error": kit.SaidError(r)}
					}
				}()
				var done map[string]any
				if purging {
					var unused *float64
					if purge["unused_days"] != nil {
						d := kit.Num(purge["unused_days"], 0)
						unused = &d
					}
					done = PurgeCache(cache, Clock(), dry, 10, "", unused)
				}
				s = CacheSummary(cache, days, wanted["*"] || wanted[filepath.Base(cache)], !dry, Clock())
				if done != nil {
					s["purged_now"] = done
				}
				return s
			}())
		}
		rec["caches"], rec["mount"] = caches, mountKeyOf(path)
		dirs = append(dirs, rec)
		paths = append(paths, path)
	}
	host := system.Hostname()
	storage := storageLook(true, paths)
	storage["versions"] = map[string]any{"versions": []any{}, "code": []any{}, "days": 14.0}
	storage["dev"] = []any{}
	if services != nil && servicesMaybe() {
		if said := services(); said == nil {
			storage = map[string]any{"error": "errand's Python did not say what the services hold here"}
		} else if err, _ := said["error"].(string); err != "" {
			storage = map[string]any{"error": err}
		} else {
			storage["versions"], storage["dev"] = said["versions"], said["dev"]
		}
	}
	return map[string]any{"host": host, "time": Clock(), "dirs": dirs, "storage": storage}
}

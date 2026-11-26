// The caches of the libraries a run uses ( ADR 0019, `$ERRAND_CACHES/<name>` ), and what errand
// removes from them ( ADR 0020, 0022 ).
//
//	<cache>/errand-cache-journal/<host>-<pid>-<start>.jsonl     each process its own, one JSON object a line
//	<cache>/errand-cache.json                                   the entries, written only by a compactor
//	<cache>/errand-cache.lock/                                  the compactor's lock ( `mkdir` )
//
// What goes ( `purgeCache` ): an entry not used for a day, one of whose `deps` is gone or holds other contents; a
// `shared` entry not used for a day that no remaining entry `needs`. A path no entry names is never touched.

package caches

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/system"
)

const (
	cacheFormat   = "errand-cache/1"
	cacheManifest = "errand-cache.json"
	cacheJournals = "errand-cache-journal"
	cacheLock     = "errand-cache.lock"
	CachesIdent   = "errand-caches.id" // in `$ERRAND_CACHES`: { id, host, made }
	day           = 86400.0
	staleCompact  = 15 * 60.0 // a lock older than this was left by a compactor that died
	UnusedDays    = 30.0
	isoLayout     = "2006-01-02T15:04:05Z"
)

func NowIso(t float64) string { return time.Unix(int64(t), 0).UTC().Format(isoLayout) }

func Clock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func isoSeconds(v any) float64 {
	s, _ := v.(string)
	t, err := time.Parse(isoLayout, s)
	if err != nil {
		return 0
	}
	return float64(t.Unix())
}

func asInt(v any) int64 {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n
		}
		f, _ := x.Float64()
		return int64(f)
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case bool:
		if x {
			return 1
		}
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	}
	return 0
}

func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, _ := x.Float64()
		return f != 0
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

func ListOf(v any) []any {
	l, _ := v.([]any)
	if l == nil {
		l = []any{}
	}
	return l
}

func decodeNumbers(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // ( a file's date in nanoseconds, an inode: not floats )
	var v any
	err := dec.Decode(&v)
	return v, err
}

func ReadObject(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	v, err := decodeNumbers(data)
	if err != nil {
		return nil
	}
	m, _ := v.(map[string]any)
	return m
}

func WriteObject(path string, v any, indent bool) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", " ")
	}
	if err := enc.Encode(v); err != nil {
		return err
	}
	part := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.%d.part", filepath.Base(path), os.Getpid()))
	if err := os.WriteFile(part, b.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(part, path)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ── the lock ─────────────────────────────────────────────────────────────────

// lockCache: the compactor's lock of the cache at `root`. -> nil when another holds it ( and `wait` ran out ): the
// caller does nothing, the other one is at it.
func lockCache(root string, wait float64) func() {
	lock := filepath.Join(root, cacheLock)
	deadline := time.Now().Add(time.Duration(wait * float64(time.Second)))
	for {
		err := os.Mkdir(lock, 0o777)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return nil
		}
		st, err := os.Stat(lock)
		if err != nil {
			continue
		}
		if time.Since(st.ModTime()).Seconds() > staleCompact {
			_ = os.RemoveAll(lock)
			continue
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	host := system.Hostname()
	_ = os.WriteFile(filepath.Join(lock, "owner"), []byte(fmt.Sprintf("%s %d\n", host, os.Getpid())), 0o644)
	return func() { _ = os.RemoveAll(lock) }
}

// ── the manifest ─────────────────────────────────────────────────────────────

func loadCache(root string) map[string]any {
	if m := ReadObject(filepath.Join(root, cacheManifest)); m != nil && m["format"] == cacheFormat {
		if _, ok := m["entries"].(map[string]any); !ok {
			m["entries"] = map[string]any{}
		}
		return m
	}
	return map[string]any{"format": cacheFormat, "hash": "sha256", "purged": nil, "entries": map[string]any{}}
}

func saveCache(root string, m map[string]any) error {
	return WriteObject(filepath.Join(root, cacheManifest), m, true)
}

// mergeLine: one journal line into the manifest
func mergeLine(m map[string]any, line map[string]any) {
	entries := m["entries"].(map[string]any)
	at, _ := line["at"].(string)
	if at == "" {
		at = NowIso(Clock())
	}
	later := func(e map[string]any) string { // ISO strings of one format sort as times
		if used, _ := e["used"].(string); used > at {
			return used
		}
		return at
	}
	switch line["op"] {
	case "create":
		key, _ := line["entry"].(string)
		if key == "" {
			return
		}
		old, _ := entries[key].(map[string]any)
		if created, _ := old["created"].(string); created > at {
			return // a later build of it was merged already
		}
		deps, _ := line["deps"].(map[string]any)
		if deps == nil {
			deps = map[string]any{}
		}
		cost := line["cost_s"]
		if _, ok := cost.(json.Number); !ok {
			cost = 0.0
		}
		view, _ := line["view"].(string)
		entries[key] = map[string]any{
			"product": ListOf(line["product"]), "intermediate": ListOf(line["intermediate"]), "needs": ListOf(line["needs"]),
			"shared": Truthy(line["shared"]), "size": asInt(line["size"]), "cost_s": cost, "deps": deps, "view": view,
			"created": at, "used": later(old),
		}
	case "use":
		for _, k := range ListOf(line["entries"]) {
			if e, ok := entries[fmt.Sprint(k)].(map[string]any); ok {
				e["used"] = later(e)
			}
		}
	}
}

// readJournal: the whole lines of a journal from byte `start` -> the lines, where the next read starts. A line cut
// short ( a writer at it, a process that died ) is left for the next read.
func readJournal(path string, start int64) ([]map[string]any, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, start, err
	}
	defer f.Close()
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, start, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, start, err
	}
	end := bytes.LastIndexByte(data, '\n') + 1
	var out []map[string]any
	for _, raw := range bytes.FieldsFunc(data[:end], func(r rune) bool { return r == '\n' || r == '\r' }) {
		if v, err := decodeNumbers(raw); err == nil { // garbage: skipped, never read again
			if line, ok := v.(map[string]any); ok {
				out = append(out, line)
			}
		}
	}
	return out, start + int64(end), nil
}

func inode(st os.FileInfo) int64 {
	return int64(system.Inode(st))
}

// compactCache: the journals of the cache at `root` merged into its manifest. To be called with the lock held. A
// journal is read from where the last compaction stopped ( `journals`: name -> [ inode, offset ] ), never renamed; one
// read to its end and untouched for a day is removed.
func compactCache(root string, now float64) map[string]any {
	m := loadCache(root)
	if isBuilds(root) {
		seeBuilds(root, m)
	}
	jd := filepath.Join(root, cacheJournals)
	listed, err := os.ReadDir(jd)
	if err != nil {
		return m
	}
	state, _ := m["journals"].(map[string]any)
	if state == nil {
		state = map[string]any{}
		m["journals"] = state
	}
	var lines []map[string]any
	done := map[string]bool{}
	present := map[string]bool{}
	for _, e := range listed { // ( sorted by name )
		name := e.Name()
		p := filepath.Join(jd, name)
		present[name] = true
		switch {
		case strings.HasSuffix(name, ".jsonl"):
			st, err := os.Stat(p)
			if err != nil {
				continue
			}
			var ino, start int64 = -1, 0
			if was := ListOf(state[name]); len(was) == 2 && was[0] != nil {
				ino, start = asInt(was[0]), asInt(was[1])
			}
			if ino != inode(st) || start > st.Size() {
				start = 0 // another file of that name
			}
			if st.Size() > start {
				got, next, err := readJournal(p, start)
				if err != nil {
					continue
				}
				lines, start = append(lines, got...), next
			}
			state[name] = []any{inode(st), start}
			if start >= st.Size() && now-kit.Seconds(st.ModTime()) > day {
				done[name] = true
			}
		case strings.Contains(name, ".jsonl.taken-"): // an older compactor's, that died after taking it
			got, _, err := readJournal(p, 0)
			if err != nil {
				continue
			}
			lines = append(lines, got...)
			done[name] = true
		}
	}
	sort.SliceStable(lines, func(i, j int) bool {
		a, _ := lines[i]["at"].(string)
		b, _ := lines[j]["at"].(string)
		return a < b
	})
	for _, line := range lines {
		mergeLine(m, line)
	}
	for name := range done {
		_ = os.Remove(filepath.Join(jd, name))
	}
	for name := range state {
		if !present[name] || done[name] {
			delete(state, name)
		}
	}
	_ = saveCache(root, m)
	return m
}

// ── what goes ────────────────────────────────────────────────────────────────

// depsChanged: one of `deps` is gone or holds other contents. `hashes`: path -> its hash now, kept over one purge.
func depsChanged(deps map[string]any, hashes map[string]string) bool {
	paths := make([]string, 0, len(deps))
	for p := range deps {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, path := range paths {
		rec, isList := deps[path].([]any)
		want := deps[path]
		if isList {
			if len(rec) == 0 {
				return true
			}
			want = rec[0]
		}
		st, err := os.Stat(path)
		if err != nil {
			return true
		}
		if isList && len(rec) >= 3 && asInt(rec[1]) == st.Size() && asInt(rec[2]) == st.ModTime().UnixNano() {
			continue
		}
		if _, ok := hashes[path]; !ok {
			h, err := sha256File(path)
			if err != nil {
				return true
			}
			hashes[path] = h
		}
		if w, _ := want.(string); hashes[path] != w {
			return true
		}
		if isList && len(rec) >= 3 { // the same contents, touched: known as it is now
			rec[1], rec[2] = st.Size(), st.ModTime().UnixNano()
		}
	}
	return false
}

// removePaths: what an entry names in its cache, removed -> the bytes freed. An entry names what is in its cache,
// nothing else: a path that leaves it is never touched.
func removePaths(root string, paths []any) int64 {
	var freed int64
	for _, raw := range paths {
		rel, _ := raw.(string)
		outside := rel == "" || filepath.IsAbs(rel)
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			outside = outside || part == ".."
		}
		if outside {
			continue
		}
		p := filepath.Join(root, rel)
		st, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if st.IsDir() {
			freed += kit.TreeSize(p)
			_ = os.RemoveAll(p)
		} else {
			freed += st.Size()
			_ = os.Remove(p)
		}
	}
	return freed
}

func purgedKey(view string) string {
	if view == "" {
		return "purged"
	}
	return "purged:" + view
}

func entryOf(entries map[string]any, k string) map[string]any {
	e, _ := entries[k].(map[string]any)
	if e == nil {
		e = map[string]any{}
	}
	return e
}

// closure: `gone`, with the entries that need a shared one of them, and then the shared entries nothing left needs
// ( not used for a day, unless `eager`: a removal by hand )
func closure(entries map[string]any, gone []string, eager bool, idle func(map[string]any) bool) []string {
	out := map[string]bool{}
	var order []string
	for _, k := range gone {
		if !out[k] {
			out[k] = true
			order = append(order, k)
		}
	}
	alive := map[string]bool{}
	for k := range entries {
		if !out[k] {
			alive[k] = true
		}
	}
	for {
		var more []string
		taken := map[string]bool{}
		if eager {
			for k := range alive {
				for _, n := range ListOf(entryOf(entries, k)["needs"]) {
					if name := fmt.Sprint(n); out[name] && Truthy(entryOf(entries, name)["shared"]) {
						taken[k] = true
					}
				}
			}
		}
		needed := map[string]bool{}
		for k := range alive {
			if !taken[k] {
				for _, n := range ListOf(entryOf(entries, k)["needs"]) {
					needed[fmt.Sprint(n)] = true
				}
			}
		}
		for k := range alive {
			if e := entryOf(entries, k); !taken[k] && Truthy(e["shared"]) && !needed[k] && (eager || idle(e)) {
				taken[k] = true
			}
		}
		for k := range taken {
			more = append(more, k)
		}
		if len(more) == 0 {
			return order
		}
		sort.Strings(more)
		for _, k := range more {
			out[k] = true
			order = append(order, k)
			delete(alive, k)
		}
	}
}

func dropEntries(root string, m map[string]any, gone []string, dry bool) int64 {
	entries := m["entries"].(map[string]any)
	var freed int64
	for _, k := range gone {
		e := entryOf(entries, k)
		if dry {
			freed += asInt(e["size"])
		} else {
			freed += removePaths(root, append(append([]any{}, ListOf(e["product"])...), ListOf(e["intermediate"])...))
			delete(entries, k)
		}
	}
	return freed
}

func cacheReport(m map[string]any, gone []string, freed int64) map[string]any {
	entries := m["entries"].(map[string]any)
	out := map[string]bool{}
	for _, k := range gone {
		out[k] = true
	}
	removed := append([]string{}, gone...)
	sort.Strings(removed)
	n, size := 0, int64(0)
	for k := range entries {
		if !out[k] {
			n++
			size += asInt(entryOf(entries, k)["size"])
		}
	}
	return map[string]any{"removed": removed, "freed": freed, "entries": n, "size": size}
}

// PurgeCache: compact, then remove what the rules say, judging the `deps` of `view`'s entries only, and marking
// `dead` those found dead but used within a day. `unused`: also the entries not shared, not used for that many days,
// and then the shared entries nothing needs, at once. -> nil if another compactor holds the cache.
func PurgeCache(root string, now float64, dry bool, wait float64, view string, unused *float64) map[string]any {
	unlock := lockCache(root, wait)
	if unlock == nil {
		return nil
	}
	defer unlock()
	m := compactCache(root, Clock())
	entries := m["entries"].(map[string]any)
	hashes := map[string]string{}
	var gone []string
	idle := func(e map[string]any) bool { return now-isoSeconds(e["used"]) >= day }
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e := entryOf(entries, k)
		there := false
		for _, p := range ListOf(e["product"]) {
			if rel, ok := p.(string); ok {
				if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
					there = true
				}
			}
		}
		if !there {
			delete(entries, k) // removed by hand, or never published: nothing to do
			continue
		}
		if unused != nil && !Truthy(e["shared"]) && now-isoSeconds(e["used"]) >= *unused*day {
			gone = append(gone, k)
			continue
		}
		if v, _ := e["view"].(string); Truthy(e["shared"]) || v != view {
			continue // judged where its deps are seen ( ADR 0022 )
		}
		deps, _ := e["deps"].(map[string]any)
		if depsChanged(deps, hashes) {
			if !Truthy(e["dead"]) {
				e["dead"] = NowIso(now)
			}
			if idle(e) {
				gone = append(gone, k)
			}
		} else {
			delete(e, "dead") // back as it was: a branch switched away and back
		}
	}
	gone = closure(entries, gone, unused != nil, idle)
	if isBuilds(root) {
		sizeBuilds(root, m)
	}
	freed := dropEntries(root, m, gone, dry)
	thinned, lighter := thin(root, m, now, dry)
	if !dry {
		m[purgedKey(view)] = NowIso(now)
		_ = saveCache(root, m)
	}
	report := cacheReport(m, gone, freed+lighter)
	report["thinned"] = thinned
	return report
}

// removeFromCache: removal by hand ( ADR 0022 ) -- `keys`, or what `rule` names: `dead` ( marked so by a purge in
// their view ), `unused` ( not shared, not used for `days` ). Takes the entries that need a shared one removed, then
// the shared entries nothing needs, at once. -> as `purgeCache`, nil if the lock stayed held.
func removeFromCache(root string, keys []string, rule string, days, now float64, dry bool, wait float64) map[string]any {
	if rule != "" && rule != "dead" && rule != "unused" {
		panic(kit.ValueError(fmt.Sprintf("no rule '%s' ( dead, unused )", rule)))
	}
	unlock := lockCache(root, wait)
	if unlock == nil {
		return nil
	}
	defer unlock()
	m := compactCache(root, Clock())
	entries := m["entries"].(map[string]any)
	var gone []string
	switch rule {
	case "dead", "unused":
		all := make([]string, 0, len(entries))
		for k := range entries {
			all = append(all, k)
		}
		sort.Strings(all)
		for _, k := range all {
			e := entryOf(entries, k)
			if (rule == "dead" && Truthy(e["dead"])) ||
				(rule == "unused" && !Truthy(e["shared"]) && now-isoSeconds(e["used"]) >= days*day) {
				gone = append(gone, k)
			}
		}
	default:
		for _, k := range keys {
			if _, ok := entries[k]; ok {
				gone = append(gone, k)
			}
		}
	}
	if len(gone) > 0 {
		gone = closure(entries, gone, true, func(map[string]any) bool { return true })
	}
	freed := dropEntries(root, m, gone, dry)
	if !dry && len(gone) > 0 {
		_ = saveCache(root, m)
	}
	return cacheReport(m, gone, freed)
}

// ── every cache of a place ───────────────────────────────────────────────────

// CachesIn: the caches under `$ERRAND_CACHES` that follow the format ( a manifest or journals ): the others are left
// alone
func CachesIn(place string) []string {
	listed, err := os.ReadDir(place)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range listed {
		d := filepath.Join(place, e.Name())
		if kit.IsDir(d) && (kit.IsFile(filepath.Join(d, cacheManifest)) || kit.IsDir(filepath.Join(d, cacheJournals))) {
			out = append(out, d)
		}
	}
	return out
}

func Human(n float64) string {
	for _, unit := range []string{"B", "kB", "MB", "GB"} {
		if n < 1000 || unit == "GB" {
			if unit == "B" {
				return fmt.Sprintf("%.0f %s", n, unit)
			}
			return fmt.Sprintf("%.1f %s", n, unit)
		}
		n /= 1000
	}
	return ""
}

// purgeAll: each cache of a place -> [ [ name, report ] ]. `onlyDue`: those not purged for a day in `view`.
func purgeAll(place string, dry, onlyDue bool, say func(string), view string) [][]any {
	out := [][]any{}
	for _, cache := range CachesIn(place) {
		if onlyDue && Clock()-isoSeconds(loadCache(cache)[purgedKey(view)]) < day {
			continue
		}
		r := PurgeCache(cache, Clock(), dry, 0, view, nil)
		if r == nil {
			continue
		}
		name := filepath.Base(cache)
		out = append(out, []any{name, r})
		if n, _ := r["thinned"].(int); say != nil && n > 0 && len(r["removed"].([]string)) == 0 {
			say(fmt.Sprintf("caches/%s: what %d entries not used for %.0f days can make again, %s freed", name, n, thinDays, Human(float64(r["freed"].(int64)))))
		}
		if removed := r["removed"].([]string); say != nil && len(removed) > 0 {
			verb := "freed"
			if dry {
				verb = "would free"
			}
			say(fmt.Sprintf("caches/%s: %d entries, %s %s ( %d left, %s )", name, len(removed),
				Human(float64(r["freed"].(int64))), verb, r["entries"], Human(float64(r["size"].(int64)))))
		}
	}
	return out
}

// ── what a machine holds ( ADR 0022 ) ─────────────────────────────────────────

// CachesIdentOf: who the `$ERRAND_CACHES` at `where` is -- { id, host, made }, written once by the first run to use
// it. Not made here: a run makes it.
func CachesIdentOf(where string) map[string]any {
	return ReadObject(filepath.Join(where, CachesIdent))
}

func StringsOf(v any) []string {
	var out []string
	for _, x := range ListOf(v) {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// RegisteredCaches: the `$ERRAND_CACHES` runs used on this machine, one per directory ( the first path of an id ): a
// record whose directory is gone is dropped
func RegisteredCaches() []map[string]any {
	out := []map[string]any{}
	index := map[string]int{}
	files, _ := filepath.Glob(filepath.Join(kit.LocalCache(), "caches", "*.json"))
	sort.Strings(files)
	for _, f := range files {
		rec := ReadObject(f)
		if rec == nil {
			continue
		}
		path, _ := rec["path"].(string)
		views := StringsOf(rec["views"])
		if path == "" || !kit.IsDir(path) {
			bare := len(views) == 0
			if !bare {
				bare = true
				for _, v := range views {
					bare = bare && v == ""
				}
			}
			if bare { // seen from this machine as it is: gone
				_ = os.Remove(f)
			}
			continue // a container's path for it: its bare path says it
		}
		if said := CachesIdentOf(path); said != nil {
			if Truthy(said["id"]) {
				rec["id"] = said["id"]
			}
			if Truthy(said["host"]) {
				rec["host"] = said["host"]
			}
		}
		if _, ok := rec["id"]; !ok {
			rec["id"] = nil
		}
		if _, ok := rec["host"]; !ok {
			rec["host"] = nil
		}
		key, _ := rec["id"].(string)
		if key == "" {
			key = path
		}
		if at, ok := index[key]; ok {
			first := out[at]
			firstViews := StringsOf(first["views"])
			all := map[string]bool{}
			bareFirst, bareRec := false, false
			for _, v := range firstViews {
				all[v] = true
				bareFirst = bareFirst || v == ""
			}
			for _, v := range views {
				all[v] = true
				bareRec = bareRec || v == ""
			}
			if bareRec && !bareFirst { // the path the machine as it is reaches it by
				for k, v := range rec {
					first[k] = v
				}
			}
			merged := make([]any, 0, len(all))
			names := make([]string, 0, len(all))
			for v := range all {
				names = append(names, v)
			}
			sort.Strings(names)
			for _, v := range names {
				merged = append(merged, v)
			}
			first["views"] = merged
			continue
		}
		index[key] = len(out)
		out = append(out, rec)
	}
	return out
}

// holdCaches: this machine holds the registered directories named by id or path ( all of them when "" ): its host
// agent purges them. -> [ [ path, previous holder ] ]
func holdCaches(which string) [][]any {
	host := system.Hostname()
	out := [][]any{}
	for _, rec := range RegisteredCaches() {
		path, _ := rec["path"].(string)
		if id, _ := rec["id"].(string); which != "" && which != id && which != path {
			continue
		}
		said := CachesIdentOf(path)
		if said == nil { // ( nobody said who it is: said now, as a run would )
			said = map[string]any{"id": kit.RandomID(), "host": host, "made": NowIso(Clock())}
		}
		was := said["host"]
		said["host"], said["held"] = host, NowIso(Clock())
		if err := WriteObject(filepath.Join(path, CachesIdent), said, false); err != nil {
			panic(err)
		}
		out = append(out, []any{path, was})
	}
	if len(out) == 0 {
		if which != "" {
			panic(kit.ValueError(fmt.Sprintf("no cache directory '%s' on %s", which, host)))
		}
		panic(kit.ValueError(fmt.Sprintf("no cache directory on %s: a run under errand writes its $ERRAND_CACHES down", host)))
	}
	return out
}

// removeIn: `removeFromCache` on cache `cache` of the registered directory named by its id ( or its path )
func removeIn(which, cache string, keys []string, rule string, days float64, dry bool) map[string]any {
	host := system.Hostname()
	for _, rec := range RegisteredCaches() {
		path, _ := rec["path"].(string)
		if id, _ := rec["id"].(string); which == "" || (which != id && which != path) {
			continue
		}
		root := filepath.Join(path, cache)
		if cache == "" || cache == "." || cache == ".." || strings.Contains(cache, "/") || !kit.IsDir(root) {
			panic(kit.ValueError(fmt.Sprintf("no cache '%s' in %s", cache, path)))
		}
		got := removeFromCache(root, keys, rule, days, Clock(), dry, 10)
		if got == nil {
			panic(kit.ValueError(fmt.Sprintf("%s: its compactor's lock stayed held; try again", root)))
		}
		return got
	}
	panic(kit.ValueError(fmt.Sprintf("no cache directory '%s' on %s", which, host)))
}

// PurgeHeld: a host agent's daily round ( ADR 0022 ) -- the registered directories this machine holds, in the bare
// view, those not purged for a day. -> [ [ path, name, report ] ]
func PurgeHeld(say func(string)) [][]any {
	host := system.Hostname()
	out := [][]any{}
	for _, rec := range RegisteredCaches() {
		if h, _ := rec["host"].(string); h != "" && strings.SplitN(h, ".", 2)[0] != strings.SplitN(host, ".", 2)[0] {
			continue // another machine holds it
		}
		path, _ := rec["path"].(string)
		for _, r := range purgeAll(path, false, true, say, "") {
			out = append(out, []any{path, r[0], r[1]})
		}
	}
	return out
}

// ── what the agent answers ───────────────────────────────────────────────────

func CachesAnswer(req map[string]any) map[string]any {
	host := system.Hostname()
	if req["op"] == "caches_hold" {
		which, _ := req["id"].(string)
		return map[string]any{"ok": true, "held": holdCaches(which), "host": host}
	}
	rule, _ := req["rule"].(string)
	days := UnusedDays
	if req["days"] != nil {
		days = kit.Num(req["days"], UnusedDays)
	}
	which, _ := req["id"].(string)
	cache, _ := req["cache"].(string)
	got := removeIn(which, cache, kit.Strs(req["keys"]), rule, days, Truthy(req["dry_run"]))
	got["ok"] = true
	return got
}

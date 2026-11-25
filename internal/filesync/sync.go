// Bringing a tree in line with another, by manifest ( ADR 0006 ) -- here, what only reads: a side's
// manifest ( `scan` ), what changed on a side since the manifest it trusts was made ( `compare` ), what a selection
// leaves out.
//
// Paths are posix, relative to the root, NFC: macOS writes NFD, and the two must name the same file.

package filesync

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/text/unicode/norm"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/errand-run/errand/internal/kit"
)

const (
	CopyState    = ".errand-copy" // at a copy's root: its trusted manifest, never part of a sync
	cachedirTag  = "CACHEDIR.TAG"
	cachedirSign = "Signature: 8a477f597d28d172789f06886806bc55"
)

// fnmatch: Python's `fnmatch.fnmatchcase` -- `*` any run of characters ( a `/` too ), `?` one, `[seq]`, `[!seq]`
func fnmatch(name, pattern string) bool {
	n, p := []rune(name), []rune(pattern)
	var match func(ni, pi int) bool
	match = func(ni, pi int) bool {
		for pi < len(p) {
			switch p[pi] {
			case '*':
				for pi < len(p) && p[pi] == '*' {
					pi++
				}
				if pi == len(p) {
					return true
				}
				for k := ni; k <= len(n); k++ {
					if match(k, pi) {
						return true
					}
				}
				return false
			case '?':
				if ni >= len(n) {
					return false
				}
				ni++
				pi++
			case '[':
				// the set ends at the first `]` after its first character; none: a `[` like any other
				j := pi + 1
				if j < len(p) && p[j] == '!' {
					j++
				}
				if j < len(p) && p[j] == ']' {
					j++
				}
				for j < len(p) && p[j] != ']' {
					j++
				}
				if j >= len(p) {
					if ni >= len(n) || n[ni] != '[' {
						return false
					}
					ni++
					pi++
					continue
				}
				if ni >= len(n) {
					return false
				}
				set := p[pi+1 : j]
				negate := len(set) > 0 && set[0] == '!'
				if negate {
					set = set[1:]
				}
				in := false
				for i := 0; i < len(set); i++ {
					if i+2 < len(set) && set[i+1] == '-' {
						if set[i] <= n[ni] && n[ni] <= set[i+2] {
							in = true
						}
						i += 2
					} else if set[i] == n[ni] {
						in = true
					}
				}
				if in == negate {
					return false
				}
				ni++
				pi = j + 1
			default:
				if ni >= len(n) || n[ni] != p[pi] {
					return false
				}
				ni++
				pi++
			}
		}
		return ni == len(n)
	}
	return match(0, 0)
}

// patterns: a name pattern matched against every component ( `__pycache__`, `*.pyc` ), a path anchored at the root
// ( `/torch.sif` ), or an inner path ( `pkg/build` ), which matches at any depth
type patterns struct {
	names, inner, anchored []string
}

func newPatterns(list []string) *patterns {
	p := &patterns{}
	for _, raw := range list {
		if strings.HasPrefix(raw, "/") {
			p.anchored = append(p.anchored, strings.Trim(raw, "/"))
			continue
		}
		free := strings.Trim(raw, "/")
		if strings.Contains(free, "/") {
			p.inner = append(p.inner, free)
		} else {
			p.names = append(p.names, free)
		}
	}
	return p
}

// match: the pattern `key` ( whose last component is `name` ) falls under, "" and false if none
func (p *patterns) match(key, name string) (string, bool) {
	for _, pat := range p.names {
		if fnmatch(name, pat) {
			return pat, true
		}
	}
	for _, pat := range p.anchored {
		if fnmatch(key, pat) {
			return "/" + pat, true
		}
	}
	for _, pat := range p.inner {
		if fnmatch(key, pat) || fnmatch(key, "*/"+pat) {
			return pat, true
		}
	}
	return "", false
}

var zoneKinds = map[string]bool{"build": true, "back": true, "cache": true, "errand": true}

type Zone struct{ Pattern, Kind string }

// Selection: what a sync looks at, on both sides alike ( sync.Selection )
type Selection struct {
	excludes    []string
	followLinks bool
	zones       []Zone
	excl        *patterns
	byKind      []struct {
		kind string
		ps   *patterns
	}
	cache *patterns
}

func NewSelection(excludes []string, follow bool, zones []Zone) (*Selection, error) {
	s := &Selection{excludes: excludes, followLinks: follow, zones: zones, excl: newPatterns(excludes)}
	order := []string{}
	by := map[string][]string{}
	for _, z := range zones {
		if !zoneKinds[z.Kind] {
			return nil, fmt.Errorf("%v: a zone's kind is one of build, back, cache, errand", z)
		}
		if _, ok := by[z.Kind]; !ok {
			order = append(order, z.Kind)
		}
		by[z.Kind] = append(by[z.Kind], z.Pattern)
	}
	for _, kind := range order {
		ps := newPatterns(by[kind])
		s.byKind = append(s.byKind, struct {
			kind string
			ps   *patterns
		}{kind, ps})
		if kind == "cache" {
			s.cache = ps
		}
	}
	return s, nil
}

// selectionFromWire: `{ "excludes", "follow_links", "zones": [ [ pattern, kind ] ] }` ( syncop.selection_from_wire )
func selectionFromWire(d map[string]any) (*Selection, error) {
	var zones []Zone
	raw, _ := d["zones"].([]any)
	for _, z := range raw {
		pair, _ := z.([]any)
		if len(pair) == 2 {
			zones = append(zones, Zone{fmt.Sprint(pair[0]), fmt.Sprint(pair[1])})
		}
	}
	follow, _ := d["follow_links"].(bool)
	return NewSelection(kit.Strs(d["excludes"]), follow, zones)
}

func LastPart(key string) string {
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[i+1:]
	}
	return key
}

func (s *Selection) zoneOf(key, name string) (Zone, bool) {
	for _, k := range s.byKind {
		if p, ok := k.ps.match(key, name); ok {
			return Zone{p, k.kind}, true
		}
	}
	return Zone{}, false
}

func (s *Selection) skips(key, name string) bool {
	if _, ok := s.excl.match(key, name); ok {
		return true
	}
	z, ok := s.zoneOf(key, name)
	return ok && z.Kind != "cache"
}

// cached: the cache zone `key` is in ( itself or one of its parents )
func (s *Selection) cached(key string) (Zone, bool) {
	if s.cache == nil {
		return Zone{}, false
	}
	parts := strings.Split(key, "/")
	for i := 1; i <= len(parts); i++ {
		if p, ok := s.cache.match(strings.Join(parts[:i], "/"), parts[i-1]); ok {
			return Zone{p, "cache"}, true
		}
	}
	return Zone{}, false
}

// ── a manifest ───────────────────────────────────────────────────────────────

// entry: what is kept of a file -- ( kind, size, mtime_ns, exe, digest, target )
type entry struct {
	kind    string // "f" a file, "l" a link, "d" a directory
	size    int64
	mtimeNs int64
	exe     bool
	digest  string
	target  string
}

func (e entry) wire() []any {
	return []any{e.kind, e.size, e.mtimeNs, e.exe, e.digest, e.target}
}

func entryFromWire(v any) (entry, bool) {
	l, ok := v.([]any)
	if !ok || len(l) == 0 {
		return entry{}, false
	}
	e := entry{kind: fmt.Sprint(l[0])}
	get := func(i int) any {
		if i < len(l) {
			return l[i]
		}
		return nil
	}
	if n, ok := get(1).(json.Number); ok {
		e.size, _ = n.Int64()
	}
	if n, ok := get(2).(json.Number); ok {
		e.mtimeNs, _ = n.Int64()
	}
	e.exe, _ = get(3).(bool)
	e.digest, _ = get(4).(string)
	e.target, _ = get(5).(string)
	return e, true
}

// same: equal without reading either -- a quick check, size and mtime ( to 100 ns: what NTFS keeps )
func (e entry) same(o entry) bool {
	if e.kind != o.kind {
		return false
	}
	switch e.kind {
	case "f":
		return e.size == o.size && floorDiv(e.mtimeNs, 100) == floorDiv(o.mtimeNs, 100) && e.exe == o.exe
	case "l":
		return e.target == o.target
	}
	return true
}

// sameContents: the same, but for what not every file system keeps ( Windows': a program's bit )
func (e entry) sameContents(o entry) bool {
	o.exe = e.exe
	return e.same(o)
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

type finding struct {
	key     string
	what    string // "changed", "added", "removed"
	mtimeNs int64
}

type manifest struct {
	entries    map[string]entry
	real       map[string]string // the name on disk, where it is not the path ( NFD on macOS )
	tagged     map[string]bool   // a walk's: the directories it left out, tagged as caches
	generation string
	theirs     map[string]any
	owns       bool            // it says whose its files are; one written before that rule does not, and proves nothing
	posed      map[string]bool // of those, what errand wrote once, and something changed since
	foreign    map[string]bool // what is there and errand did not write: never errand's to remove or replace ( ADR 0038 )
	found      []any           // as written: [ [ finding, entry or null ] ]
}

func newManifest() *manifest {
	return &manifest{entries: map[string]entry{}, real: map[string]string{}, tagged: map[string]bool{}, theirs: map[string]any{}, foreign: map[string]bool{}, posed: map[string]bool{}}
}

func hashFile(path string) (string, error) {
	h, _ := blake2b.New(16, nil)
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// isPart: what errand was writing when it was interrupted: no file of anybody
func isPart(name string) bool {
	return strings.HasPrefix(name, ".") && strings.HasSuffix(name, partSuffix)
}

const partSuffix = ".errand-part"

func cacheTagged(dir string) bool {
	f, err := os.Open(filepath.Join(dir, cachedirTag))
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, len(cachedirSign))
	n, _ := io.ReadFull(f, buf)
	return string(buf[:n]) == cachedirSign
}

// scanTree: what `root` holds, minus what `sel` excludes. A file's hash is taken from `cache` while its size and
// mtime have not changed, computed otherwise -- or left out, without `hashing` ( sync.scan ).
func scanTree(root string, sel *Selection, cache map[string]entry, hashing bool) (*manifest, error) {
	out := newManifest()
	var walk func(dir, keyDir, realDir string) error
	walk = func(dir, keyDir, realDir string) error {
		listed, err := os.ReadDir(dir) // sorted by name
		if err != nil {
			if os.IsNotExist(err) || strings.Contains(err.Error(), "not a directory") {
				return nil
			}
			return nil
		}
		if keyDir != "" {
			for _, e := range listed {
				if e.Name() == cachedirTag && cacheTagged(dir) {
					// a cache's directory ( ADR 0019 ): the side's own, as a build zone
					key := keyDir[:len(keyDir)-1]
					delete(out.entries, key)
					delete(out.real, key)
					out.tagged[key] = true
					return nil
				}
			}
		}
		for _, e := range listed {
			name := norm.NFC.String(e.Name())
			key, real := keyDir+name, realDir+e.Name()
			if sel.skips(key, name) || strings.HasPrefix(name, CopyState) || isPart(name) {
				continue
			}
			if _, dup := out.entries[key]; dup {
				was := key
				if r, ok := out.real[key]; ok {
					was = r
				}
				return fmt.Errorf("%q and %q are one name once normalized: rename one of them", realDir+e.Name(), was)
			}
			path := filepath.Join(dir, e.Name())
			var st os.FileInfo
			if sel.followLinks {
				st, err = os.Stat(path)
			} else {
				st, err = os.Lstat(path)
			}
			if err != nil {
				continue // gone since listed, or a link to nothing that is followed
			}
			if real != key {
				out.real[key] = real
			}
			mode := st.Mode()
			switch {
			case mode&os.ModeSymlink != 0:
				target, _ := os.Readlink(path)
				out.entries[key] = entry{kind: "l", target: target}
			case mode.IsDir():
				out.entries[key] = entry{kind: "d"}
				if err := walk(path, key+"/", real+"/"); err != nil {
					return err
				}
			case mode.IsRegular():
				e2 := entry{kind: "f", size: st.Size(), mtimeNs: st.ModTime().UnixNano(), exe: mode&0o100 != 0}
				if prev, ok := cache[key]; ok && prev.kind == "f" && prev.size == e2.size && prev.mtimeNs == e2.mtimeNs {
					e2.digest = prev.digest
				}
				if hashing && e2.digest == "" {
					e2.digest, _ = hashFile(path)
				}
				out.entries[key] = e2
			}
			// sockets, fifos, devices: not part of a project
		}
		return nil
	}
	if err := walk(root, "", ""); err != nil {
		return nil, err
	}
	return out, nil
}

// loadManifest: a manifest kept from an earlier sync; nil if there is none, or one that cannot be read
func loadManifest(path string) *manifest {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.UseNumber() // ( an mtime in nanoseconds does not hold in a float )
	raw := map[string]any{}
	if dec.Decode(&raw) != nil {
		return nil
	}
	out := newManifest()
	_, out.owns = raw["foreign"]
	if entries, ok := raw["entries"].(map[string]any); ok {
		for k, v := range entries {
			if e, ok := entryFromWire(v); ok {
				out.entries[k] = e
			}
		}
	}
	if real, ok := raw["real"].(map[string]any); ok {
		for k, v := range real {
			out.real[k] = fmt.Sprint(v)
		}
	}
	out.generation, _ = raw["generation"].(string)
	if theirs, ok := raw["theirs"].(map[string]any); ok {
		out.theirs = theirs
	}
	out.found, _ = raw["found"].([]any)
	for _, k := range kit.Strs(raw["foreign"]) {
		out.foreign[k] = true
	}
	for _, k := range kit.Strs(raw["posed"]) {
		out.posed[k] = true
	}
	return out
}

func (m *manifest) wire() map[string]any {
	entries := map[string]any{}
	for k, e := range m.entries {
		entries[k] = e.wire()
	}
	found := m.found
	if found == nil {
		found = []any{}
	}
	for k := range m.foreign {
		if _, there := m.entries[k]; !there {
			delete(m.foreign, k)
		}
	}
	for k := range m.posed {
		if !m.foreign[k] {
			delete(m.posed, k)
		}
	}
	return map[string]any{"entries": entries, "real": m.real, "generation": m.generation, "theirs": m.theirs, "found": found, "foreign": sortedKeys(m.foreign), "posed": sortedKeys(m.posed)}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func saveManifest(path string, m *manifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(m.wire())
	if err != nil {
		return err
	}
	part := path + ".part"
	if err := os.WriteFile(part, b, 0o644); err != nil {
		return err
	}
	return os.Rename(part, path)
}

// compareManifests: what `found` ( a walk of the disk ) says changed since `trusted` was written. What a cache zone
// of `sel` holds is no change: both sides add to it ( sync.compare ).
func compareManifests(trusted, found *manifest, sel *Selection) []finding {
	var out []finding
	for key, e := range found.entries {
		t, ok := trusted.entries[key]
		if !ok {
			out = append(out, finding{key, "added", e.mtimeNs})
		} else if !t.same(e) && !(t.kind == "f" && t.digest != "" && t.digest == e.digest && t.exe == e.exe) {
			out = append(out, finding{key, "changed", e.mtimeNs})
		}
	}
	inTagged := func(k string) bool { // tagged as a cache since: left out of the walk, not removed
		for k != "" {
			if found.tagged[k] {
				return true
			}
			if i := strings.LastIndex(k, "/"); i >= 0 {
				k = k[:i]
			} else {
				k = ""
			}
		}
		return false
	}
	for key := range trusted.entries {
		if _, ok := found.entries[key]; !ok && !(len(found.tagged) > 0 && inTagged(key)) {
			out = append(out, finding{key, "removed", 0})
		}
	}
	if sel != nil && sel.cache != nil {
		kept := out[:0]
		for _, f := range out {
			if _, in := sel.cached(f.key); !in {
				kept = append(kept, f)
			}
		}
		out = kept
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.key != b.key {
			return a.key < b.key
		}
		if a.what != b.what {
			return a.what < b.what
		}
		return a.mtimeNs < b.mtimeNs
	})
	return out
}

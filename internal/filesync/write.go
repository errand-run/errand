// Bringing a tree in line with another ( ADR 0006 ) -- here, what decides and what writes: the plan,
// the destination's writer ( a file written beside its target, then renamed; what a sync would lose is kept ), and
// the two put together for two directories of this machine.

package filesync

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/system"
)

const (
	chunkSize = 1 << 20
	spared    = CopyState + "-kept" // beside the manifest: what a sync would have lost, one directory per sync
	runHeld   = "a run's records"
)

// What says a directory is a run's ( the `.yaml` names: the records errand wrote before this program )
var (
	runMarks     = []string{"result.yaml", "result.json", "output.txt", "stdout.txt", "stderr.txt", "steps.jsonl", "launch.yaml", "launch.json"}
	aboveRunName = []string{"summary.yaml", "summary.json", "latest"}
)

func fail(format string, args ...any) { panic(kit.SyncError(fmt.Sprintf(format, args...))) }

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func (z Zone) String() string { return fmt.Sprintf("%s ( %s zone )", z.Pattern, z.Kind) }

func (m *manifest) onDisk(key string) string {
	if r, ok := m.real[key]; ok {
		return r
	}
	return key
}

// inOrder: the keys as a walk meets them -- a directory, then what it holds, names sorted
func inOrder(entries map[string]entry) []string {
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return strings.ReplaceAll(keys[i], "/", "\x00") < strings.ReplaceAll(keys[j], "/", "\x00")
	})
	return keys
}

func Parent(key string) string {
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[:i]
	}
	return ""
}

// ── what a check found, kept with the manifest ────────────────────────────────

type foundItem struct {
	f finding
	e *entry // as it was on disk; nil: gone
}

func (f finding) wire() []any { return []any{f.key, f.what, f.mtimeNs} }

func findingFromWire(v any) (finding, bool) {
	l, ok := v.([]any)
	if !ok || len(l) < 2 {
		return finding{}, false
	}
	f := finding{key: fmt.Sprint(l[0]), what: fmt.Sprint(l[1])}
	if len(l) > 2 {
		if n, ok := l[2].(json.Number); ok {
			f.mtimeNs, _ = n.Int64()
		}
	}
	return f, true
}

func (m *manifest) foundList() []foundItem {
	var out []foundItem
	for _, raw := range m.found {
		pair, ok := raw.([]any)
		if !ok || len(pair) < 1 {
			continue
		}
		f, ok := findingFromWire(pair[0])
		if !ok {
			continue
		}
		item := foundItem{f: f}
		if len(pair) > 1 {
			if e, ok := entryFromWire(pair[1]); ok {
				item.e = &e
			}
		}
		out = append(out, item)
	}
	return out
}

func (m *manifest) setFound(items []foundItem) {
	m.found = []any{}
	for _, it := range items {
		var e any
		if it.e != nil {
			e = it.e.wire()
		}
		m.found = append(m.found, []any{it.f.wire(), e})
	}
}

// asLeft: the disk as an agent's checks left it -- the trusted manifest, and what they found since
func asLeft(known *manifest) *manifest {
	dst := newManifest()
	for k, e := range known.entries {
		dst.entries[k] = e
	}
	for k, v := range known.real {
		dst.real[k] = v
	}
	for _, it := range known.foundList() {
		if it.e == nil {
			delete(dst.entries, it.f.key)
		} else {
			e := *it.e
			if e.kind == "f" {
				e.digest = ""
			}
			dst.entries[it.f.key] = e
		}
	}
	return dst
}

// checkCopy: the copy at `root` against what errand wrote there ( the manifest at `trusted` ): what the agent holding
// the copy does when it goes ( ADR 0006 ). What it finds is kept with the manifest, for the next sync to say.
func checkCopy(root string, sel *Selection, trusted string) {
	known := withWrites(root, loadManifest(trusted))
	if known == nil {
		return
	}
	disk, err := scanTree(root, sel, known.entries, false)
	if err != nil {
		return
	}
	pending := map[string]foundItem{}
	for _, it := range known.foundList() {
		pending[it.f.key] = it
	}
	for _, f := range compareManifests(known, disk, sel) {
		if _, ok := pending[f.key]; !ok { // the first time it was seen says when
			it := foundItem{f: f}
			if e, ok := disk.entries[f.key]; ok {
				it.e = &e
			}
			pending[f.key] = it
		}
	}
	keys := make([]string, 0, len(pending))
	for k := range pending {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	items := make([]foundItem, 0, len(keys))
	for _, k := range keys {
		items = append(items, pending[k])
	}
	known.setFound(items)
	_ = saveManifest(trusted, known)
}

// ── what to do ───────────────────────────────────────────────────────────────

type plan struct {
	delete    []string // deepest first
	dirs      []string
	links     []string
	send      []string    // files whose contents cross
	touch     []string    // files with the same contents: only mtime and mode set
	keptRuns  []string    // records of runs only the destination has: left there
	conflicts [][2]string // ( key, zone ): in a cache, other contents on each side: left
}

func depthFirst(keys []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.Count(out[i], "/"), strings.Count(out[j], "/")
		if a != b {
			return a > b
		}
		return out[i] < out[j]
	})
	return out
}

// runRecords: the keys of `m` that are records of runs -- everything in a run's directory ( one holding a result, an
// output… ), and the summary and `latest` of the directories above it ( sync.run_records )
func runRecords(m *manifest) map[string]bool {
	marks := map[string]bool{}
	for _, n := range runMarks {
		marks[n] = true
	}
	runs := map[string]bool{}
	for k, e := range m.entries {
		if e.kind == "f" && marks[LastPart(k)] && Parent(k) != "" {
			runs[Parent(k)] = true
		}
	}
	out := map[string]bool{}
	if len(runs) == 0 {
		return out
	}
	above := map[string]bool{}
	for r := range runs {
		parts := strings.Split(r, "/")
		for i := 0; i < len(parts); i++ {
			d := strings.Join(parts[:i], "/")
			for _, n := range aboveRunName {
				if d == "" {
					above[n] = true
				} else {
					above[d+"/"+n] = true
				}
			}
		}
	}
	for k := range m.entries {
		if above[k] {
			out[k] = true
			continue
		}
		for d := k; d != ""; d = Parent(d) {
			if runs[d] {
				out[k] = true
				break
			}
		}
	}
	return out
}

// makePlan: what brings `dst` in line with `src` ( sync.plan ). A file of the same size whose mtime differs is hashed
// on the destination ( `hashDst` ) before its contents are sent. The records of runs only the destination holds are
// never deleted. In a cache zone, only what `dst` lacks is made; other contents there are a conflict.
func makePlan(src, dst *manifest, hashDst func(string) string, sel *Selection) *plan {
	p := &plan{}
	cache := sel != nil && sel.cache != nil
	for _, key := range inOrder(src.entries) {
		s := src.entries[key]
		d, has := dst.entries[key]
		if cache {
			if z, in := sel.cached(key); in {
				if !has && s.kind == "f" && hashDst != nil {
					// not in what the destination says it holds, but maybe there all the same: its hash first
					if theirs := hashDst(key); theirs != "" {
						if theirs != s.digest {
							p.conflicts = append(p.conflicts, [2]string{key, z.String()})
						}
						continue
					}
				}
				switch {
				case !has:
					switch s.kind {
					case "d":
						p.dirs = append(p.dirs, key)
					case "l":
						p.links = append(p.links, key)
					default:
						p.send = append(p.send, key)
					}
				case d.kind != s.kind || (s.kind == "l" && d.target != s.target):
					p.conflicts = append(p.conflicts, [2]string{key, z.String()})
				case s.kind == "f" && !s.same(d):
					theirs := d.digest
					if theirs == "" && hashDst != nil && d.size == s.size {
						theirs = hashDst(key)
					}
					if d.size != s.size || s.digest == "" || theirs != s.digest {
						p.conflicts = append(p.conflicts, [2]string{key, z.String()})
					}
				}
				continue
			}
		}
		if has && (d.kind != s.kind || (s.kind == "l" && d.target != s.target)) {
			p.delete = append(p.delete, key)
			has = false
		}
		switch {
		case s.kind == "d":
			if !has {
				p.dirs = append(p.dirs, key)
			}
		case s.kind == "l":
			if !has {
				p.links = append(p.links, key)
			}
		case !has:
			p.send = append(p.send, key)
		case !s.same(d):
			theirs := d.digest
			if theirs == "" && hashDst != nil && d.size == s.size {
				theirs = hashDst(key)
			}
			if s.digest != "" && theirs == s.digest {
				p.touch = append(p.touch, key)
			} else {
				p.send = append(p.send, key)
			}
		}
	}
	var theirs []string
	for _, key := range inOrder(dst.entries) {
		if _, ok := src.entries[key]; ok {
			continue
		}
		if cache {
			if _, in := sel.cached(key); in {
				continue
			}
		}
		theirs = append(theirs, key)
	}
	if len(theirs) > 0 {
		records := runRecords(dst)
		for _, k := range theirs {
			if records[k] {
				p.keptRuns = append(p.keptRuns, k)
			} else {
				p.delete = append(p.delete, k)
			}
		}
		sort.Strings(p.keptRuns)
	}
	p.delete = depthFirst(p.delete)
	sort.Strings(p.dirs)
	return p
}

// collisions: names that are one on a case-insensitive file system
func collisions(keys []string) [][]string {
	fold := cases.Fold()
	seen := map[string][]string{}
	for _, k := range keys {
		f := fold.String(k)
		seen[f] = append(seen[f], k)
	}
	var out [][]string
	for _, v := range seen {
		if len(v) > 1 {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.Join(out[i], "\x00") < strings.Join(out[j], "\x00") })
	return out
}

func caseInsensitive(root string) bool {
	name := fmt.Sprintf(".errand-case-%d", os.Getpid())
	probe := filepath.Join(root, name)
	f, err := os.OpenFile(probe, os.O_WRONLY|os.O_CREATE, 0o644)
	must(err)
	f.Close()
	defer os.Remove(probe)
	return kit.Lexists(filepath.Join(root, strings.ToUpper(name)))
}

// ── the contents ─────────────────────────────────────────────────────────────

// readInto: the bytes of `key`, given to `write` chunk by chunk. A file that changed while it was read is read once
// more, after `again()`. -> its entry as read, and whether it held still. An error of the file system is returned.
func readInto(root string, src *manifest, key string, write func([]byte), again func()) (entry, bool, error) {
	path := filepath.Join(root, src.onDisk(key))
	var e entry
	buf := make([]byte, chunkSize)
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			again()
		}
		h, _ := blake2b.New(16, nil)
		f, err := os.Open(path)
		if err != nil {
			return e, false, err
		}
		before, err := f.Stat()
		if err != nil {
			f.Close()
			return e, false, err
		}
		var size int64
		for {
			n, err := io.ReadFull(f, buf)
			if n > 0 {
				h.Write(buf[:n])
				size += int64(n)
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				write(chunk)
			}
			if err != nil {
				if err != io.EOF && err != io.ErrUnexpectedEOF {
					f.Close()
					return e, false, err
				}
				break
			}
		}
		after, err := os.Stat(path)
		f.Close()
		if err != nil {
			return e, false, err
		}
		e = entry{kind: "f", size: size, mtimeNs: after.ModTime().UnixNano(), exe: after.Mode()&0o100 != 0,
			digest: hex.EncodeToString(h.Sum(nil))}
		if before.Size() == after.Size() && after.Size() == size && before.ModTime().Equal(after.ModTime()) {
			return e, true, nil
		}
		if again == nil {
			break
		}
	}
	return e, false, nil
}

// scanOne: the entry of one file as it is on disk, its hash left out
func scanOne(path string) entry {
	st, err := os.Lstat(path)
	must(err)
	switch {
	case st.Mode()&os.ModeSymlink != 0:
		target, _ := os.Readlink(path)
		return entry{kind: "l", target: target}
	case st.IsDir():
		return entry{kind: "d"}
	}
	return entry{kind: "f", size: st.Size(), mtimeNs: st.ModTime().UnixNano(), exe: st.Mode()&0o100 != 0}
}

var fileModes = func() [2]os.FileMode { // what a new file and a new program get here: the process's mask, read once
	mask := system.Umask()
	return [2]os.FileMode{os.FileMode(0o666 &^ mask), os.FileMode(0o777 &^ mask)}
}()

// writer: the destination's side -- what a plan does there. A file is written beside its target and renamed into
// place once whole: an interrupted sync never leaves half a file under its name.
//
// `trusted`: what errand wrote there last. A file about to be replaced or removed that is not as it says ( changed
// since, or never written by errand ) is moved under `spareIn`, relative to the root, rather than lost.
type writer struct {
	root    string
	dst     *manifest
	trusted *manifest
	spareIn string
	mark    string               // of this writer's parts, where several may write at once ( a pull )
	spared  [][]any              // [ key, "changed" | "added", its mtime ]
	others  map[string]*manifest // the manifests of the copies that hold this one, or that it holds, by their directory
}

type handle struct {
	part, target string
	f            *os.File
}

func newWriter(root string, dst, trusted *manifest, spareIn string) *writer {
	if dst == nil {
		dst = newManifest()
	}
	return &writer{root: root, dst: dst, trusted: trusted, spareIn: spareIn, spared: [][]any{}, others: map[string]*manifest{}}
}

func (w *writer) path(key string) string {
	return filepath.Join(w.root, filepath.FromSlash(w.dst.onDisk(key)))
}

// asWritten: whether what is at `p` is as a manifest says errand wrote it
func (m *manifest) asWritten(key, p string, st os.FileInfo) bool {
	t, known := m.entries[key]
	if !known || !m.owns || m.foreign[key] {
		return false
	}
	if st.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(p)
		return t.kind == "l" && t.target == target
	}
	return t.kind == "f" && t.size == st.Size() && floorDiv(t.mtimeNs, 100) == floorDiv(st.ModTime().UnixNano(), 100)
}

// notOurs: "" when errand wrote what is at `p` as it is -- this copy's pushes, or those of a copy that holds this one, or
// that it holds --; else "changed" ( errand wrote it, and something wrote it since ) or "added" ( errand never did )
func (w *writer) notOurs(key, p string, st os.FileInfo) string {
	if w.trusted.asWritten(key, p, st) {
		return ""
	}
	for dir := filepath.Dir(p); ; dir = filepath.Dir(dir) {
		if dir != filepath.Clean(w.root) {
			m, looked := w.others[dir]
			if !looked {
				m = withWrites(dir, loadManifest(filepath.Join(dir, CopyState)))
				w.others[dir] = m
			}
			if m != nil {
				if rel, err := filepath.Rel(dir, p); err == nil && m.asWritten(norm.NFC.String(filepath.ToSlash(rel)), p, st) {
					return ""
				}
			}
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	if _, known := w.trusted.entries[key]; known && (!w.trusted.foreign[key] || w.trusted.posed[key]) {
		return "changed"
	}
	return "added"
}

// wouldLose: what is there at `keys`, that errand did not write, and that is not what it is about to write ( `digests`:
// what that is, when it is a file's contents )
func (w *writer) wouldLose(keys []string, digests map[string]any) []finding {
	var out []finding
	for _, key := range keys {
		p := w.path(key)
		st, err := os.Lstat(p)
		if err != nil || st.IsDir() { // ( a directory only goes when it is empty )
			continue
		}
		what := w.notOurs(key, p, st)
		if what == "" {
			continue
		}
		if d := kit.Text(digests[key]); d != "" && st.Mode().IsRegular() {
			if theirs, err := hashFile(p); err == nil && theirs == d {
				continue
			}
		}
		out = append(out, finding{key, what, st.ModTime().UnixNano()})
	}
	return out
}

// spare: out of the way, if errand did not write it as it is, and it is not what comes ( `digest` ) -- kept under `spareIn`,
// said in `spared`
func (w *writer) spare(key, digest string) {
	if w.trusted == nil || w.spareIn == "" {
		return
	}
	p := w.path(key)
	st, err := os.Lstat(p)
	if err != nil || st.IsDir() {
		return
	}
	what := w.notOurs(key, p, st)
	if what == "" {
		return
	}
	if digest != "" && st.Mode().IsRegular() {
		if theirs, err := hashFile(p); err == nil && theirs == digest {
			return
		}
	}
	to := filepath.Join(w.root, filepath.FromSlash(w.spareIn), filepath.FromSlash(w.dst.onDisk(key)))
	must(os.MkdirAll(filepath.Dir(to), 0o755))
	must(os.Rename(p, to))
	w.spared = append(w.spared, []any{key, what, st.ModTime().UnixNano()})
}

// remove: false for a directory that is not empty -- something excluded is left in it
func (w *writer) remove(key string) bool {
	w.spare(key, "")
	p := w.path(key)
	st, err := os.Lstat(p)
	if err == nil {
		if st.IsDir() {
			err = syscall.Rmdir(p)
		} else {
			err = syscall.Unlink(p)
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
			return false
		}
		panic(err)
	}
	delete(w.dst.real, key)
	return true
}

func (w *writer) mkdir(key string) { must(os.MkdirAll(w.path(key), 0o777)) }

func (w *writer) link(key, target string) {
	p := w.path(key)
	must(os.MkdirAll(filepath.Dir(p), 0o777))
	must(os.Symlink(target, p))
}

func (w *writer) begin(key string) *handle {
	p := w.path(key)
	must(os.MkdirAll(filepath.Dir(p), 0o777))
	part := filepath.Join(filepath.Dir(p), "."+filepath.Base(p)+w.mark+partSuffix)
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	must(err)
	return &handle{part: part, target: p, f: f}
}

func (w *writer) restart(h *handle) {
	_, err := h.f.Seek(0, io.SeekStart)
	must(err)
	must(h.f.Truncate(0))
}

func (w *writer) write(h *handle, chunk []byte) {
	if h == nil {
		fail("a chunk of a file that was not begun")
	}
	_, err := h.f.Write(chunk)
	must(err)
}

func setTimes(path string, e entry) {
	mode := fileModes[0]
	if e.exe {
		mode = fileModes[1]
	}
	must(os.Chmod(path, mode))
	t := time.Unix(0, e.mtimeNs)
	must(os.Chtimes(path, t, t))
}

// commit: the file is whole -- its mode and time set, then in place. With `key`, what was there is spared first.
func (w *writer) commit(h *handle, e entry, key string) {
	must(h.f.Close())
	setTimes(h.part, e)
	if key != "" {
		w.spare(key, e.digest)
	}
	must(os.Rename(h.part, h.target))
}

func (w *writer) abort(h *handle) {
	h.f.Close()
	_ = os.Remove(h.part)
}

// keepTheirs: for a file of a cache zone, at its end -- one already at its target is not replaced ( ADR 0012 ).
// -> "" ( nothing there: commit it ), "same" ( the same contents ), "conflict"; the part is dropped unless "".
func (w *writer) keepTheirs(h *handle, e entry) string {
	st, err := os.Lstat(h.target)
	if err != nil {
		return ""
	}
	w.abort(h)
	if st.Mode().IsRegular() {
		if d, err := hashFile(h.target); err == nil && d == e.digest {
			return "same"
		}
	}
	return "conflict"
}

// thereAlready: what comes is what is there, and errand did not write that: the part is dropped
func (w *writer) thereAlready(h *handle, e entry, key string) bool {
	st, err := os.Lstat(h.target)
	if err != nil || !st.Mode().IsRegular() || w.notOurs(key, h.target, st) == "" {
		return false
	}
	if d, err := hashFile(h.target); err != nil || d != e.digest {
		return false
	}
	w.abort(h)
	return true
}

func (w *writer) touch(key string, e entry) { setTimes(w.path(key), e) }

// ── a directory that does not go ──────────────────────────────────────────────

// heldBy: the zone of what keeps `directory` from going, when it is right in it
func heldBy(sel *Selection, key, directory string) (Zone, bool) {
	listed, err := os.ReadDir(directory)
	if err != nil {
		return Zone{}, false
	}
	for _, e := range listed {
		name := norm.NFC.String(e.Name())
		if z, ok := sel.zoneOf(key+"/"+name, name); ok {
			return z, true
		}
	}
	for _, e := range listed {
		if cacheTagged(filepath.Join(directory, e.Name())) {
			return Zone{norm.NFC.String(e.Name()) + "/" + cachedirTag, "build"}, true
		}
	}
	return Zone{}, false
}

// refused: a directory a push could not remove -- what to say of it, if anything, and what the copy's manifest keeps
// of it ( `written` ). One that is a cache ( tagged ) goes out of the manifest. One that holds a run's files is
// theirs from now on: said the first time only.
func refused(sel *Selection, key, directory string, prior map[string]any, written *manifest) string {
	if cacheTagged(directory) {
		delete(written.entries, key)
		delete(written.theirs, key)
		return ""
	}
	held, ok := heldBy(sel, key, directory)
	if ok && held.Kind == "errand" {
		return ""
	}
	if ok {
		written.theirs[key] = held.String()
	} else {
		written.theirs[key] = ""
	}
	if _, was := prior[key]; was || !ok {
		return ""
	}
	return held.String()
}

// runsKept: the records of runs only the destination has, that a push leaves there. One no longer on the disk leaves
// the copy's manifest; the directory of one still there is theirs. -> those not said by an earlier push.
func runsKept(keys []string, path func(string) string, prior map[string]any, written *manifest) []string {
	marks := map[string]bool{}
	for _, n := range runMarks {
		marks[n] = true
	}
	out := []string{}
	for _, key := range keys {
		if !kit.Lexists(path(key)) {
			delete(written.entries, key)
			delete(written.real, key)
			delete(written.theirs, key)
			continue
		}
		if run := Parent(key); marks[LastPart(key)] && run != "" {
			written.theirs[run] = runHeld
			if _, was := prior[run]; !was {
				out = append(out, key)
			}
		}
	}
	return out
}

// ── what is said ─────────────────────────────────────────────────────────────

func when(mtimeNs int64) string { return time.Unix(0, mtimeNs).Format("2006-01-02 15:04:05") }

func sayKept(key, held, where string, say func(string)) {
	say(fmt.Sprintf("  %s: no longer in the project, kept on %s: a run made what it holds, %s. Said once -- "+
		"if the whole directory is a run's, tag it ( %s ) or declare it a zone", key, where, held, cachedirTag))
}

// ForeignError: a push that would have removed or replaced files errand did not write: nothing was done
type ForeignError struct {
	Where, Root string
	Files       []finding
	zones       string
}

const foreignShown = 20

func (e *ForeignError) Error() string {
	one := len(e.Files) == 1
	what := fmt.Sprintf("%d files there are", len(e.Files))
	if one {
		what = "a file there is"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s not errand's, and the push would lose %s: nothing was sent, nothing was touched", what, map[bool]string{true: "it", false: "them"}[one])
	for i, f := range e.Files {
		if i == foreignShown {
			fmt.Fprintf(&b, "\n    ...and %d more", len(e.Files)-i)
			break
		}
		how := "not written by errand"
		if f.what == "changed" {
			how = "changed there since errand wrote it"
		}
		fmt.Fprintf(&b, "\n    %s: %s ( %s )", f.key, how, when(f.mtimeNs))
	}
	fmt.Fprintf(&b, "\n  in %s on %s. Each is yours to keep or to remove there ( `rm` ); `--force` moves them aside, into %s/<when>/, and goes on."+
		"\n  What a run writes there is kept by a zone: `errand.configure( zones = { \"path\": \"back\" } )` ( or \"build\", \"cache\" )%s",
		e.Root, e.Where, spared, e.zones)
	return b.String()
}

// sayGone: what errand wrote there and is there no more
func sayGone(findings []finding, src *manifest, where string, say func(string)) {
	var gone []string
	for _, f := range findings {
		if _, in := src.entries[f.key]; in && f.what == "removed" {
			gone = append(gone, f.key)
		}
	}
	for i, key := range gone {
		if i == foreignShown {
			say(fmt.Sprintf("  ...and %d more", len(gone)-i))
			break
		}
		say(fmt.Sprintf("  %s: removed on %s since errand wrote it -- written again", key, where))
	}
}

// zonesSaid: the zones a project declares, beside what is said of a path that is in none
func zonesSaid(sel *Selection) string {
	var zs []string
	if sel != nil {
		for _, z := range sel.zones {
			if z.Kind != "errand" {
				zs = append(zs, fmt.Sprintf("%s ( %s )", z.Pattern, z.Kind))
			}
		}
	}
	if len(zs) == 0 {
		return ""
	}
	if len(zs) > 12 {
		zs = append(zs[:12], "…")
	}
	return "; declared: " + strings.Join(zs, ", ")
}

func sayConflicts(conflicts [][2]string, where string, say func(string)) {
	for i, c := range conflicts {
		if i == 20 {
			break
		}
		say(fmt.Sprintf("  %s: not the same here and on %s, in %s -- each side keeps its own. A cache "+
			"never replaces a file: give it a name that changes with its contents, or remove the wrong one", c[0], where, c[1]))
	}
	if len(conflicts) > 20 {
		say(fmt.Sprintf("  ...and %d more", len(conflicts)-20))
	}
}

// spareDir: where one sync keeps what it would have lost -- by the time it ran
func spareDir() string { return spared + "/" + time.Now().Format("2006-01-02_15h04m05") }

// ── two directories of this machine ───────────────────────────────────────────

type Report struct {
	Sent, touched, Deleted, kept, unstable, keptRuns []string
	Bytes                                            int64
	findings                                         []finding
	conflicts                                        [][2]string
	seen                                             *PushSeen
}

func list(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func scanOr(root string, sel *Selection, cache map[string]entry, hashing bool) *manifest {
	m, err := scanTree(root, sel, cache, hashing)
	if err != nil {
		panic(kit.SyncError(err.Error()))
	}
	return m
}

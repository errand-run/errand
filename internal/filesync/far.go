// A push by manifest, through the agent that holds the copy ( ADR 0006 ): the agent's side.
//
// The local side opens `{ "op": "sync" | "pull" | "receive", … }`. Then messages cross on the channel, each one frame
// ( IN toward the agent, OUT back ): `J` and JSON, or `C` and a chunk of a file ( packed: its first byte says how ).
//
// The copy's trusted manifest lives IN the copy ( `.errand-copy` ); a lock beside it ( `.errand-copy.lock` ) keeps two
// pushes from writing the copy at once. The agent walks a copy in full the first time it syncs it in its life, and
// once more when it goes ( `closing` ). In between, it trusts the manifest.

package filesync

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
)

const (
	trustedName = CopyState
	lockName    = CopyState + ".lock"
	CopyID      = CopyState + ".id"     // who the copy is
	CopyOrigin  = CopyState + ".origin" // where it comes from
	staleLock   = 1800 * time.Second    // a lock older than this was left by a push that died
	sparedDays  = 30                    // what a push moved aside stays that long
)

var codecs = []string{"d", "-"} // what this side reads: zlib, or as it is

// packChunk: a chunk as it crosses -- zlib where the other side reads it; the first byte says which
func packChunk(data []byte, accepted []string) []byte {
	for _, c := range accepted {
		if c == "d" {
			var b bytes.Buffer
			b.WriteByte('d')
			z, _ := zlib.NewWriterLevel(&b, 1)
			_, _ = z.Write(data)
			_ = z.Close()
			return b.Bytes()
		}
	}
	return append([]byte{'-'}, data...)
}

func unpackChunk(data []byte) []byte {
	if len(data) == 0 {
		fail("an empty chunk")
	}
	switch data[0] {
	case 'd':
		z, err := zlib.NewReader(bytes.NewReader(data[1:]))
		must(err)
		out, err := io.ReadAll(z)
		must(err)
		return out
	case '-':
		return data[1:]
	case 'z':
		fail("a chunk compressed with zstd, which this agent cannot read")
	}
	fail("a chunk of unknown kind %q", data[:1])
	return nil
}

func lockCopy(root string) func() {
	path := filepath.Join(root, lockName)
	waited := time.Now()
	for {
		err := os.Mkdir(path, 0o777) // atomic, NFS and Lustre included
		if err == nil {
			_ = writeJSONWhole(filepath.Join(path, lockOwner), ownLock())
			return func() { _ = os.RemoveAll(path) }
		}
		if !errors.Is(err, os.ErrExist) {
			panic(err)
		}
		if why := StaleLock(root); why != "" {
			breakLock(root)
			continue
		}
		if time.Since(waited) > staleLock {
			fail("%s is held by another push", path)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func writeJSONWhole(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".part")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// NoteCopy: a copy of a project held here -- written down, with its id ( storage.note_copy ). Never fatal.
func NoteCopy(root string) {
	var id any
	f := filepath.Join(root, CopyID)
	if said := kit.ReadJSON(f); said != nil {
		id = said["id"]
	} else if out, err := os.OpenFile(f, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644); err == nil {
		host, _ := os.Hostname()
		made := kit.RandomID()
		_ = json.NewEncoder(out).Encode(map[string]any{"id": made, "made": time.Now().Format("2006-01-02T15:04:05"), "host": host})
		out.Close()
		id = made
	} else if said := kit.ReadJSON(f); said != nil { // the first to come wins
		id = said["id"]
	}
	d := filepath.Join(kit.LocalCache(), "copies")
	if os.MkdirAll(d, 0o755) != nil {
		return
	}
	sum := sha1.Sum([]byte(root))
	_ = writeJSONWhole(filepath.Join(d, hex.EncodeToString(sum[:])[:16]+".json"),
		map[string]any{"path": root, "id": id, "synced": float64(time.Now().UnixNano()) / 1e9})
}

func pruneSpared(root string) {
	listed, err := os.ReadDir(filepath.Join(root, spared))
	if err != nil {
		return
	}
	for _, e := range listed {
		if st, err := e.Info(); err == nil && st.IsDir() && time.Since(st.ModTime()) > sparedDays*24*time.Hour {
			_ = os.RemoveAll(filepath.Join(root, spared, e.Name()))
		}
	}
}

// under: the manifest of what is under each of `paths` in `root`, keyed from `root`; its keys as met ( the paths in
// the order asked, each one's as a walk meets them ); the paths that are not there
func under(root string, paths []string, sel *Selection) (*manifest, []string, []string) {
	out, missing, order := newManifest(), []string{}, []string{}
	add := func(key string, e entry) {
		if _, ok := out.entries[key]; !ok {
			order = append(order, key)
		}
		out.entries[key] = e
	}
	for _, path := range paths {
		path = strings.Trim(path, "/")
		here := filepath.Join(root, filepath.FromSlash(path))
		st, err := os.Lstat(here)
		if err != nil {
			missing = append(missing, path)
			continue
		}
		if st.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(here)
			add(path, entry{kind: "l", target: target})
			continue
		}
		if !st.IsDir() {
			add(path, entry{kind: "f", size: st.Size(), mtimeNs: st.ModTime().UnixNano(), exe: st.Mode()&0o100 != 0})
			continue
		}
		parts := strings.Split(path, "/")
		for i := 1; i <= len(parts); i++ { // the directories that lead there
			add(strings.Join(parts[:i], "/"), entry{kind: "d"})
		}
		sub := scanOr(here, sel, nil, false)
		for _, k := range inOrder(sub.entries) {
			add(path+"/"+k, sub.entries[k])
		}
		for k, v := range sub.real {
			out.real[path+"/"+k] = path + "/" + v
		}
	}
	return out, missing, order
}

// ordered: a JSON object whose keys are written in the order given -- who reads it goes through them in that order
type ordered struct {
	keys []string
	of   map[string]entry
}

func (o ordered) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(k)
		val, _ := json.Marshal(o.of[k].wire())
		b.Write(key)
		b.WriteByte(':')
		b.Write(val)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// seenCopy: what the copy holds -- walked, or as the trusted manifest and its pending findings say -- and what
// changed there behind errand's back
func seenCopy(root string, sel *Selection, known *manifest, check bool) (*manifest, []finding) {
	findings := []finding{}
	if known == nil || check {
		var was map[string]entry
		if known != nil {
			was = known.entries
		}
		dst := scanOr(root, sel, was, false)
		if known == nil {
			return dst, findings
		}
		first := map[string]finding{} // a check saw it first: its time is the one
		for _, it := range known.foundList() {
			first[it.f.key] = it.f
		}
		for _, f := range compareManifests(known, dst, sel) {
			if seen, ok := first[f.key]; ok {
				f = seen
			}
			findings = append(findings, f)
		}
		return dst, findings
	}
	for _, it := range known.foundList() {
		if _, in := sel.cached(it.f.key); !in {
			findings = append(findings, it.f)
		}
	}
	return asLeft(known), findings
}

// ── one transfer, on the channel it was opened on ─────────────────────────────

type syncChan struct {
	out   func(kind byte, payload []byte) // toward who asked, on its channel
	held  *SyncedCopies                   // the copies whoever serves this already walked; nil: none, each is walked
	after func()                          // once through
	req   map[string]any
	inbox chan []byte   // a full one holds the stream back: the disk sets the pace
	gone  chan struct{} // the local side closed, or left
	Done  chan struct{} // this side is through
	once  sync.Once
}

// SyncedCopies: the copies an agent synced in its life, and with which selection
type SyncedCopies struct {
	mu sync.Mutex
	Of map[string]*Selection
}

func (h *SyncedCopies) has(root string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.Of[root]
	return ok
}

func (h *SyncedCopies) set(root string, sel *Selection) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.Of[root] = sel
	h.mu.Unlock()
}

func NewSyncChan(req map[string]any, out func(byte, []byte), held *SyncedCopies, after func()) *syncChan {
	return &syncChan{out: out, held: held, after: after, req: req, inbox: make(chan []byte, 64), gone: make(chan struct{}), Done: make(chan struct{})}
}

// Put: what came on the channel, from the client's reader
func (s *syncChan) Put(payload []byte) {
	select {
	case s.inbox <- payload:
	case <-s.Done:
	case <-s.gone:
	}
}

func (s *syncChan) Leave() { s.once.Do(func() { close(s.gone) }) }

func (s *syncChan) say(v any) {
	b, _ := json.Marshal(v)
	s.out(kit.OUT, append([]byte{'J'}, b...))
}

func (s *syncChan) get() []byte {
	select {
	case got := <-s.inbox: // what was sent before it left is still read
		return got
	default:
	}
	select {
	case got := <-s.inbox:
		return got
	case <-s.gone:
		fail("the local side went away")
	}
	return nil
}

func message(got []byte) map[string]any {
	dec := json.NewDecoder(bytes.NewReader(got[1:]))
	dec.UseNumber() // ( an mtime in nanoseconds does not hold in a float )
	msg := map[string]any{}
	if err := dec.Decode(&msg); err != nil {
		fail("a message that cannot be read: %v", err)
	}
	return msg
}

func (s *syncChan) getJSON() map[string]any {
	got := s.get()
	if len(got) == 0 || got[0] != 'J' {
		fail("expected a message, got a chunk")
	}
	return message(got)
}

func (s *syncChan) Run() {
	code := 0
	func() {
		defer func() {
			if r := recover(); r != nil {
				code = 1
				s.say(map[string]any{"error": kit.SaidError(r)})
			}
		}()
		ok, _ := json.Marshal(map[string]any{"ok": true, "codecs": codecs})
		s.out(kit.REPLY, ok)
		switch s.req["op"] {
		case "pull":
			s.pull()
		case "receive":
			s.receive()
		default:
			s.session()
		}
	}()
	close(s.Done)
	end, _ := json.Marshal(map[string]any{"code": code})
	s.out(kit.EXIT, end)
	if s.after != nil {
		s.after()
	}
}

func (s *syncChan) selection() *Selection {
	wire, _ := s.req["sel"].(map[string]any)
	if wire == nil {
		wire = map[string]any{}
	}
	sel, err := selectionFromWire(wire)
	if err != nil {
		panic(kit.SyncError(err.Error()))
	}
	return sel
}

// session: a push -- the copy here brought in line with the project over there
func (s *syncChan) session() {
	root := reach.CopyRoot(kit.Text(s.req["root"]))
	sel := s.selection()
	dry, _ := s.req["dry"].(bool) // what a push would do, and nothing done
	if !dry {
		must(reach.MakeCopyRoot(root))
		NoteCopy(root)
	}
	if origin, ok := s.req["origin"].(map[string]any); ok && !dry {
		origin["synced"] = float64(time.Now().UnixNano()) / 1e9
		if b, err := json.Marshal(origin); err == nil {
			_ = os.WriteFile(filepath.Join(root, CopyOrigin), b, 0o644)
		}
	}
	if !dry {
		unlock := lockCopy(root)
		defer unlock()
	}
	trusted := filepath.Join(root, trustedName)
	known := withWrites(root, loadManifest(trusted))
	was := s.held.has(root)
	check, _ := s.req["check"].(bool)
	dst, findings := seenCopy(root, sel, known, !was || check)
	errands := known // what errand wrote there; a copy it never wrote, or one made by hand: nothing of it
	if errands == nil {
		errands = newManifest()
	}
	w := newWriter(root, dst, errands, spareDir())
	written := newManifest()
	for k, e := range dst.entries {
		written.entries[k] = e
		// not as errand wrote it, or never written by it: not errand's, until it writes it
		if t, has := errands.entries[k]; e.kind != "d" && (!has || errands.foreign[k] || !t.sameContents(e)) {
			written.foreign[k] = true
			if has && (!errands.foreign[k] || errands.posed[k]) {
				written.posed[k] = true
			}
		}
	}
	for k, v := range dst.real {
		written.real[k] = v
	}
	// What errand writes is said to be errand's before it is in place, whatever interrupts the push: a file that is not as
	// it was said is nobody's word
	log := openWrites(root)
	defer log.close()
	mine := func(key string, e entry) {
		written.entries[key] = e
		delete(written.foreign, key)
	}
	dirty, saved := false, false
	defer func() {
		if dirty && !saved {
			_ = saveManifest(trusted, written)
		}
	}()
	found := [][]any{}
	for _, f := range findings {
		found = append(found, f.wire())
	}
	s.say(map[string]any{"dst": ordered{inOrder(dst.entries), dst.entries}, "findings": found, "ci": !dry && caseInsensitive(root), "foreign": sortedKeys(written.foreign), "root": root})
	prior := map[string]any{} // a run's directories, said already ( ADR 0019 )
	if known != nil {
		for k, v := range known.theirs {
			prior[k] = v
			if _, ok := dst.entries[k]; ok {
				written.theirs[k] = v
			}
		}
	}
	var h *handle
	defer func() {
		if h != nil {
			w.abort(h)
		}
	}()
	raced := [][]string{}
	for {
		got := s.get()
		if len(got) == 0 {
			fail("an empty message")
		}
		if got[0] == 'C' {
			w.write(h, unpackChunk(got[1:]))
			continue
		}
		msg := message(got)
		if keys, ok := msg["hash"]; ok {
			out := map[string]string{}
			for _, key := range kit.Strs(keys) {
				d, err := hashFile(w.path(key))
				if err != nil {
					out[key] = ""
					continue
				}
				out[key] = d
				if e, has := written.entries[key]; has && e.digest == "" { // known from now on, and not read again
					if st, err := os.Lstat(w.path(key)); err == nil && e.size == st.Size() && e.mtimeNs == st.ModTime().UnixNano() {
						e.digest = d
						written.entries[key] = e
					}
				}
				if _, has := written.entries[key]; !has {
					if _, in := sel.cached(key); in {
						// a cache's file the manifest did not know ( a run wrote it ): known from now on
						e := scanOne(w.path(key))
						e.digest = d
						written.entries[key], written.foreign[key] = e, true
					}
				}
			}
			s.say(map[string]any{"digests": out})
		} else if raw, ok := msg["lose"]; ok { // before anything is done: what would be lost, that is not errand's
			l, _ := raw.(map[string]any)
			digests, _ := l["digests"].(map[string]any)
			lost := [][]any{}
			for _, f := range w.wouldLose(kit.Strs(l["keys"]), digests) {
				lost = append(lost, f.wire())
			}
			s.say(map[string]any{"lost": lost, "root": root})
		} else if dry && msg["done"] == nil {
			fail("a push that only looks was asked to write")
		} else if raw, ok := msg["apply"]; ok {
			ap, _ := raw.(map[string]any)
			dirty = true
			deleted, kept := []string{}, [][]string{}
			for _, key := range kit.Strs(ap["delete"]) {
				if w.remove(key) {
					deleted = append(deleted, key)
					delete(written.entries, key)
					delete(written.real, key)
					delete(written.foreign, key)
				} else {
					kept = append(kept, []string{key, refused(sel, key, w.path(key), prior, written)})
				}
			}
			for _, key := range kit.Strs(ap["dirs"]) {
				w.mkdir(key)
				written.entries[key] = entry{kind: "d"}
				delete(written.theirs, key) // the project's again
			}
			links, _ := ap["links"].([]any)
			for _, l := range links {
				if pair := kit.Strs(l); len(pair) == 2 {
					log.say(pair[0], entry{kind: "l", target: pair[1]})
					w.link(pair[0], pair[1])
					mine(pair[0], entry{kind: "l", target: pair[1]})
				}
			}
			runs := runsKept(kit.Strs(ap["runs"]), w.path, prior, written)
			s.say(map[string]any{"deleted": deleted, "kept": kept, "runs": runs})
		} else if key, ok := msg["file"]; ok {
			h = w.begin(kit.Text(key))
		} else if _, ok := msg["restart"]; ok {
			if h == nil {
				fail("a file started over that was not begun")
			}
			w.restart(h)
		} else if raw, ok := msg["end"]; ok {
			e, _ := entryFromWire(raw)
			key := kit.Text(msg["key"])
			if h == nil {
				fail("the end of a file that was not begun")
			}
			theirs := ""
			z, in := sel.cached(key)
			if in {
				theirs = w.keepTheirs(h, e)
			}
			if theirs == "" {
				if w.thereAlready(h, e, key) { // the same, and not errand's: left, and still not errand's
					written.entries[key] = scanOne(w.path(key))
					written.foreign[key] = true
				} else {
					log.say(key, e)
					w.commit(h, e, key)
					mine(key, e)
				}
			} else { // a run wrote it since the copy was walked ( ADR 0012 )
				written.entries[key] = scanOne(w.path(key))
				written.foreign[key] = true
				if theirs == "conflict" {
					raced = append(raced, []string{key, z.String()})
				}
			}
			h = nil
		} else if raw, ok := msg["touch"]; ok {
			list, _ := raw.([]any)
			for _, t := range list {
				pair, _ := t.([]any)
				if len(pair) != 2 {
					continue
				}
				e, _ := entryFromWire(pair[1])
				log.say(kit.Text(pair[0]), e)
				w.touch(kit.Text(pair[0]), e)
				mine(kit.Text(pair[0]), e)
			}
		} else if _, ok := msg["done"]; ok {
			break
		}
	}
	if dry {
		s.say(map[string]any{"saved": false})
		return
	}
	gen := make([]byte, 8)
	_, _ = rand.Read(gen)
	written.generation = hex.EncodeToString(gen)
	must(saveManifest(trusted, written))
	saved = true
	log.done()
	pruneSpared(root)
	s.held.set(root, sel)
	in := ""
	if len(w.spared) > 0 {
		in = w.spareIn
	}
	s.say(map[string]any{"saved": true, "spared": w.spared, "spared_in": in, "conflicts": raced})
}

// pull: what is under some paths of the copy, toward the project over there. It deletes nothing there.
func (s *syncChan) pull() {
	root := reach.CopyRoot(kit.Text(s.req["root"]))
	sel := s.selection()
	src, missing, order := under(root, kit.Strs(s.req["paths"]), sel)
	s.say(map[string]any{"src": ordered{order, src.entries}, "missing": missing})
	accepted := []string{}
	theirs := []string{"-"}
	if _, ok := s.req["codecs"]; ok {
		theirs = kit.Strs(s.req["codecs"])
	}
	for _, c := range theirs {
		if c == "d" || c == "-" {
			accepted = append(accepted, c)
		}
	}
	for {
		msg := s.getJSON()
		if keys, ok := msg["hash"]; ok {
			out := map[string]string{}
			for _, key := range kit.Strs(keys) {
				out[key], _ = hashFile(filepath.Join(root, filepath.FromSlash(src.onDisk(key))))
			}
			s.say(map[string]any{"digests": out})
		} else if keys, ok := msg["want"]; ok {
			for _, key := range kit.Strs(keys) {
				s.say(map[string]any{"file": key})
				e, stable, err := readInto(root, src, key,
					func(b []byte) { s.out(kit.OUT, append([]byte{'C'}, packChunk(b, accepted)...)) },
					func() { s.say(map[string]any{"restart": true}) })
				if errors.Is(err, os.ErrNotExist) {
					s.say(map[string]any{"gone": key}) // removed by the run since it was listed
					continue
				}
				must(err)
				s.say(map[string]any{"end": e.wire(), "key": key, "stable": stable})
			}
			s.say(map[string]any{"done": true})
			if back, _ := s.req["back"].(bool); !back {
				return
			}
		} else if raw, ok := msg["have"]; ok { // what the project holds now, as it was read here
			s.broughtBack(root, raw, sel)
			s.say(map[string]any{"noted": true})
			return
		}
	}
}

// broughtBack: a file errand wrote, changed here since and brought back, is as the project holds it: nothing of it would be
// lost any more. One errand never wrote stays somebody's.
func (s *syncChan) broughtBack(root string, raw any, sel *Selection) {
	have, _ := raw.(map[string]any)
	trusted := filepath.Join(root, trustedName)
	unlock := lockCopy(root)
	defer unlock()
	known := withWrites(root, loadManifest(trusted))
	if known == nil {
		return
	}
	posed := map[string]bool{} // written by errand, whatever was found of it since
	for k := range known.entries {
		posed[k] = !known.foreign[k] || known.posed[k]
	}
	changed := map[string]bool{}
	for key, w := range have {
		e, ok := entryFromWire(w)
		if !ok || e.kind != "f" || !posed[key] || !kit.Lexists(filepath.Join(root, filepath.FromSlash(known.onDisk(key)))) {
			continue
		}
		if t := known.entries[key]; t.sameContents(e) && !known.foreign[key] && !known.hasFound(key) {
			continue
		}
		if e.digest == "" {
			e.digest = known.entries[key].digest
			if !known.entries[key].sameContents(e) {
				e.digest = ""
			}
		}
		known.entries[key] = e
		delete(known.foreign, key)
		changed[key] = true
	}
	if len(changed) == 0 {
		return
	}
	var left []foundItem
	for _, it := range known.foundList() {
		if !changed[it.f.key] {
			left = append(left, it)
		}
	}
	known.setFound(left)
	must(saveManifest(trusted, known))
}

// receive: files sent into a directory, one by one, outside any copy's manifest -- what errand downloaded for a run
// over there ( ADR 0013 ). A file already at its place is left ( a cache's rule ) unless the request says `replace`,
// as for an image built elsewhere. Then, optionally, the answer the run waits for.
func (s *syncChan) receive() {
	root := reach.CopyRoot(kit.Text(s.req["root"]))
	must(os.MkdirAll(root, 0o777))
	w := newWriter(root, nil, nil, "")
	var h *handle
	defer func() {
		if h != nil {
			w.abort(h)
		}
	}()
	left := []string{}
	replace, _ := s.req["replace"].(bool) // the files replace what is there ( the one replaced is kept ), instead of being left
	for {
		got := s.get()
		if len(got) == 0 {
			fail("an empty message")
		}
		if got[0] == 'C' {
			w.write(h, unpackChunk(got[1:]))
			continue
		}
		msg := message(got)
		if key, ok := msg["file"]; ok {
			h = w.begin(kit.Text(key))
		} else if _, ok := msg["restart"]; ok {
			if h == nil {
				fail("a file started over that was not begun")
			}
			w.restart(h)
		} else if raw, ok := msg["end"]; ok {
			if h == nil {
				fail("the end of a file that was not begun")
			}
			e, _ := entryFromWire(raw)
			switch {
			case replace: // what an image built elsewhere comes to replace: the one it replaces is kept, once
				if st, err := os.Lstat(h.target); err == nil && st.Mode().IsRegular() {
					_ = os.Rename(h.target, h.target+".prev")
				}
				w.commit(h, e, "")
			case w.keepTheirs(h, e) == "":
				w.commit(h, e, "")
			default:
				left = append(left, kit.Text(msg["key"]))
			}
			h = nil
		} else if raw, ok := msg["answer"]; ok {
			// what a run waits for: written whole, then renamed ( downloads.write_answer )
			answer, _ := raw.(map[string]any)
			path := kit.Text(answer["path"])
			must(os.MkdirAll(filepath.Dir(path), 0o777))
			must(writeJSONWhole(path, answer["body"]))
		} else if _, ok := msg["done"]; ok {
			break
		}
	}
	s.say(map[string]any{"saved": true, "left": left})
}

// CheckAll: whoever held these copies goes -- each is checked once more, and what changed there since is kept for the
// next push to say
func (h *SyncedCopies) CheckAll() {
	if h == nil {
		return
	}
	h.mu.Lock()
	held := h.Of
	h.Of = map[string]*Selection{}
	h.mu.Unlock()
	for root, sel := range held {
		func() {
			defer func() { _ = recover() }()
			unlock := lockCopy(root)
			defer unlock()
			checkCopy(root, sel, filepath.Join(root, trustedName))
		}()
	}
}

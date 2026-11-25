// Files crossing a link ( ADR 0006 ): the near side of a push and of a pull, whose far side is far.go.
//
// A push brings a copy over there in line with a directory here: only what changed crosses, what a run made over
// there is never deleted, and what the push would lose is kept aside. A pull brings back what is under some paths of
// the copy, and deletes nothing here.

package filesync

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
)

// xfer is a transfer's channel, from the near side: messages ( `J` and JSON ) and chunks ( `C` ) in frames.
type xfer struct{ c *reach.Call }

func (x xfer) say(v any) {
	b, _ := json.Marshal(v)
	must(x.c.Send(kit.IN, append([]byte{'J'}, b...)))
}

// next: the next thing said on the channel -- a message, or a chunk ( msg nil )
func (x xfer) next() (map[string]any, []byte) {
	for {
		kind, payload, err := x.c.Recv()
		if err != nil {
			fail("%s: %v", x.c.L.Where, err)
		}
		switch kind {
		case kit.OUT:
			if len(payload) == 0 {
				continue
			}
			if payload[0] == 'C' {
				return nil, payload[1:]
			}
			msg := message(payload)
			if said, ok := msg["error"]; ok {
				fail("%v", said)
			}
			return msg, nil
		case kit.EXIT:
			fail("%s ended the transfer before it was through", x.c.L.Where)
		}
	}
}

func (x xfer) get() map[string]any {
	msg, _ := x.next()
	if msg == nil {
		fail("expected a message, got a chunk")
	}
	return msg
}

func entriesFromWire(v any) (*manifest, []string) {
	m := newManifest()
	raw, _ := v.(map[string]any)
	keys := make([]string, 0, len(raw))
	for k, w := range raw {
		if e, ok := entryFromWire(w); ok {
			m.entries[k] = e
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return m, keys
}

func (s *Selection) wire() map[string]any {
	zones := [][]string{}
	for _, z := range s.zones {
		zones = append(zones, []string{z.Pattern, z.Kind})
	}
	return map[string]any{"excludes": list(s.excludes), "follow_links": s.followLinks, "zones": zones}
}

func saySpared(spared [][]any, sparedIn, where string, say func(string)) {
	var news []finding
	for _, s := range spared {
		if f, ok := findingFromWire(s); ok {
			news = append(news, f)
		}
	}
	for i, f := range news {
		if i == 20 {
			say(fmt.Sprintf("  ...and %d more, in %s/", len(news)-20, sparedIn))
			break
		}
		how := "not written by errand"
		if f.what == "changed" {
			how = "changed"
		}
		say(fmt.Sprintf("  %s: %s on %s ( %s ) -- moved aside, into %s/", f.key, how, where, when(f.mtimeNs), sparedIn))
	}
}

// guarded: a transfer's panics ( `fail`, `must` ) as an error
func guarded(do func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if foreign, ok := r.(*ForeignError); ok {
				err = foreign
				return
			}
			err = errors.New(kit.SaidError(r))
		}
	}()
	do()
	return nil
}

// SaidOfCopy: what is said of a project to the machine that holds its copy ( package services, which stands on this one )
var SaidOfCopy = func(root string) map[string]any { return nil }

// Push: `remoteRoot`, where `l` leads, in line with `localRoot` here. A file there that errand did not write is neither
// removed nor replaced: the push stops before it does anything ( a ForeignError ) -- unless `force` says to go on, and then
// what it would lose is moved aside.
func Push(l *reach.Link, localRoot, remoteRoot string, sel *Selection, srcCache, where string, force bool, say func(string)) (r *Report, err error) {
	return push(l, localRoot, remoteRoot, sel, srcCache, where, force, false, say)
}

// PushSeen: what a push would do, the copy there looked at whole, and nothing done
type PushSeen struct {
	Root         string // the copy, as its machine names it
	Send, Remove int
	Bytes        int64
	Foreign      []ForeignFile // what would stop it
	RemovedThere int           // what errand wrote there and is there no more
}

type ForeignFile struct {
	Key     string `json:"key"`
	Changed bool   `json:"changed,omitempty"` // errand wrote it, something changed it since
	When    string `json:"when"`
}

func SeePush(l *reach.Link, localRoot, remoteRoot string, sel *Selection, srcCache, where string) (*PushSeen, error) {
	r, err := push(l, localRoot, remoteRoot, sel, srcCache, where, false, true, func(string) {})
	if err != nil {
		return nil, err
	}
	return r.seen, nil
}

func push(l *reach.Link, localRoot, remoteRoot string, sel *Selection, srcCache, where string, force, dry bool, say func(string)) (r *Report, err error) {
	r = &Report{}
	req := map[string]any{"op": "sync", "root": remoteRoot, "sel": sel.wire(), "check": dry, "dry": dry}
	if srcCache != "" && !dry { // a project's: where it comes from, and the checkouts in it that a page's services run from
		req["origin"] = SaidOfCopy(localRoot)
	}
	c := l.Open(req)
	defer c.Close()
	theirs, err := c.Reply()
	if err != nil {
		return r, err
	}
	accepted := []string{}
	for _, t := range kit.Strs(theirs["codecs"]) {
		if t == "d" || t == "-" {
			accepted = append(accepted, t)
		}
	}
	x := xfer{c}
	err = guarded(func() {
		var cache map[string]entry
		if srcCache != "" {
			if m := loadManifest(srcCache); m != nil {
				cache = m.entries
			}
		}
		src := scanOr(localRoot, sel, cache, true)
		if srcCache != "" {
			_ = os.MkdirAll(filepath.Dir(srcCache), 0o755)
			must(saveManifest(srcCache, src))
		}
		got := x.get()
		dst, _ := entriesFromWire(got["dst"])
		records := runRecords(dst)
		raw, _ := got["findings"].([]any)
		for _, w := range raw { // a run's record that appeared over there is a run made there: no finding, it stays
			if f, ok := findingFromWire(w); ok && !(f.what == "added" && records[f.key]) {
				r.findings = append(r.findings, f)
			}
		}
		sayGone(r.findings, src, where, say)
		if ci, _ := got["ci"].(bool); ci {
			if clash := collisions(inOrder(src.entries)); len(clash) > 0 {
				var said []string
				for _, c := range clash {
					said = append(said, strings.Join(c, " / "))
				}
				fail("names that are one on %s ( it does not tell case apart ): %s", where, strings.Join(said, "; "))
			}
		}
		var ask []string
		for _, k := range inOrder(src.entries) {
			s := src.entries[k]
			if s.kind != "f" {
				continue
			}
			if d, has := dst.entries[k]; has {
				if d.kind == "f" && !s.same(d) && d.digest == "" && d.size == s.size {
					ask = append(ask, k)
				}
			} else if _, in := sel.cached(k); in { // a cache's file the manifest does not know may be there all the same
				ask = append(ask, k)
			}
		}
		digests := map[string]any{}
		if len(ask) > 0 {
			x.say(map[string]any{"hash": ask})
			digests, _ = x.get()["digests"].(map[string]any)
		}
		p := makePlan(src, dst, func(k string) string { return kit.Text(digests[k]) }, sel)
		r.conflicts = append(r.conflicts, p.conflicts...)
		// what the push removes, replaces or makes: nothing of it is somebody else's
		at := append(append(append([]string{}, p.delete...), p.send...), p.links...)
		at = append(at, p.dirs...)
		if !force && len(at) > 0 {
			ours := map[string]any{}
			for _, k := range p.send {
				ours[k] = src.entries[k].digest
			}
			x.say(map[string]any{"lose": map[string]any{"keys": at, "digests": ours}})
			said := x.get()
			lost, _ := said["lost"].([]any)
			foreign, seen := &ForeignError{Where: where, Root: kit.Text(said["root"]), zones: zonesSaid(sel)}, map[string]bool{}
			for _, w := range lost {
				if f, ok := findingFromWire(w); ok && !seen[f.key] {
					seen[f.key] = true
					if _, in := sel.cached(f.key); !in { // ( a cache's: never replaced, `keepTheirs` )
						foreign.Files = append(foreign.Files, f)
					}
				}
			}
			if len(foreign.Files) > 0 && !dry {
				panic(foreign)
			}
			if dry {
				r.seen = &PushSeen{}
				for _, f := range foreign.Files {
					r.seen.Foreign = append(r.seen.Foreign, ForeignFile{f.key, f.what == "changed", when(f.mtimeNs)})
				}
			}
		}
		if dry {
			if r.seen == nil {
				r.seen = &PushSeen{}
			}
			r.seen.Root, r.seen.Send = kit.Text(got["root"]), len(p.send)
			for _, k := range p.send {
				r.seen.Bytes += src.entries[k].size
			}
			lost := map[string]bool{}
			for _, f := range r.seen.Foreign {
				lost[f.Key] = true
			}
			for _, k := range p.delete {
				if _, in := src.entries[k]; !in && !lost[k] && dst.entries[k].kind != "d" {
					r.seen.Remove++
				}
			}
			for _, f := range r.findings {
				if _, in := src.entries[f.key]; in && f.what == "removed" && src.entries[f.key].kind != "d" {
					r.seen.RemovedThere++
				}
			}
			x.say(map[string]any{"done": true})
			_ = x.get()
			return
		}
		links := [][]string{}
		for _, k := range p.links {
			links = append(links, []string{k, src.entries[k].target})
		}
		x.say(map[string]any{"apply": map[string]any{"delete": list(p.delete), "dirs": list(p.dirs), "runs": list(p.keptRuns), "links": links}})
		applied := x.get()
		r.keptRuns = kit.Strs(applied["runs"])
		for _, k := range kit.Strs(applied["deleted"]) {
			if _, in := src.entries[k]; !in {
				r.Deleted = append(r.Deleted, k)
			}
		}
		kept, _ := applied["kept"].([]any)
		for _, raw := range kept {
			pair := kit.Strs(raw)
			if len(pair) != 2 {
				continue
			}
			r.kept = append(r.kept, pair[0])
			if _, in := src.entries[pair[0]]; pair[1] != "" && !in {
				sayKept(pair[0], pair[1], where, say)
			}
		}
		for _, key := range p.send {
			x.say(map[string]any{"file": key})
			e, stable, err := readInto(localRoot, src, key,
				func(b []byte) { must(c.Send(kit.IN, append([]byte{'C'}, packChunk(b, accepted)...))) },
				func() { x.say(map[string]any{"restart": true}) })
			must(err)
			x.say(map[string]any{"end": e.wire(), "key": key})
			r.Sent = append(r.Sent, key)
			r.Bytes += e.size
			if !stable {
				r.unstable = append(r.unstable, key)
				say(fmt.Sprintf("  %s: changed while it was read, twice -- what was read is on %s", key, where))
			}
		}
		foreign := map[string]bool{} // there as it is here, and not errand's: left as it is
		for _, k := range kit.Strs(got["foreign"]) {
			foreign[k] = true
		}
		touched := p.touch[:0]
		for _, k := range p.touch {
			if !foreign[k] {
				touched = append(touched, k)
			}
		}
		p.touch = touched
		if len(p.touch) > 0 {
			touch := [][]any{}
			for _, k := range p.touch {
				touch = append(touch, []any{k, src.entries[k].wire()})
			}
			x.say(map[string]any{"touch": touch})
			r.touched = p.touch
		}
		x.say(map[string]any{"done": true})
		saved := x.get()
		var spared [][]any
		rawSpared, _ := saved["spared"].([]any)
		for _, s := range rawSpared {
			if l, ok := s.([]any); ok {
				spared = append(spared, l)
			}
		}
		saySpared(spared, kit.Text(saved["spared_in"]), where, say)
		rawConflicts, _ := saved["conflicts"].([]any)
		for _, raw := range rawConflicts {
			if pair := kit.Strs(raw); len(pair) == 2 {
				r.conflicts = append(r.conflicts, [2]string{pair[0], pair[1]})
			}
		}
		sayConflicts(r.conflicts, where, say)
	})
	return r, err
}

// Pull: what is under `paths` in `remoteRoot` over there, into `localRoot` here. Nothing here is deleted: a file or
// a directory here that is something else over there stays as it is. `keep`: the paths are caches ( ADR 0012 ) --
// only what is missing here comes, and a file here with other contents is a conflict, left as it is.
func Pull(l *reach.Link, remoteRoot, localRoot string, paths []string, sel *Selection, keep bool, where string, say func(string)) (r *Report, err error) {
	return pull(l, remoteRoot, localRoot, paths, sel, keep, false, where, say)
}

// PullBack: a pull from a project's copy into the project. What errand wrote there, changed since, and is here now as it
// is there, is no change behind errand's back any more.
func PullBack(l *reach.Link, remoteRoot, localRoot string, paths []string, where string, say func(string)) (*Report, error) {
	return pull(l, remoteRoot, localRoot, paths, nil, false, true, where, say)
}

func pull(l *reach.Link, remoteRoot, localRoot string, paths []string, sel *Selection, keep, back bool, where string, say func(string)) (r *Report, err error) {
	r = &Report{}
	if sel == nil {
		sel, _ = NewSelection(nil, false, nil)
	}
	c := l.Open(map[string]any{"op": "pull", "root": remoteRoot, "paths": paths, "sel": sel.wire(), "codecs": codecs, "back": back})
	defer c.Close()
	if _, err = c.Reply(); err != nil {
		return r, err
	}
	x := xfer{c}
	err = guarded(func() {
		got := x.get()
		src, _ := entriesFromWire(got["src"])
		missing := map[string]bool{}
		for _, m := range kit.Strs(got["missing"]) {
			missing[m] = true
		}
		var there []string
		for _, p := range paths {
			if !missing[strings.Trim(p, "/")] {
				there = append(there, p)
			}
		}
		here, _, _ := under(localRoot, there, sel)
		var sameSize []string
		for _, k := range inOrder(src.entries) {
			s := src.entries[k]
			if h, has := here.entries[k]; has && s.kind == "f" && h.kind == "f" && !s.same(h) && h.size == s.size {
				sameSize = append(sameSize, k)
			}
		}
		theirs := map[string]any{}
		if len(sameSize) > 0 {
			x.say(map[string]any{"hash": sameSize})
			theirs, _ = x.get()["digests"].(map[string]any)
		}
		w := newWriter(localRoot, here, nil, "")
		w.mark = "." + kit.RandomID() // ( a view is brought as its run goes, and all of the run at its end )
		hashed := newManifest()
		for k, e := range src.entries {
			e.digest = kit.Text(theirs[k])
			hashed.entries[k] = e
		}
		p := makePlan(hashed, here, func(k string) string {
			if _, ok := theirs[k]; !ok {
				return ""
			}
			d, _ := hashFile(w.path(k))
			return d
		}, nil)
		drop := func(gone func(string) bool) {
			filter := func(keys []string) []string {
				out := keys[:0]
				for _, k := range keys {
					if !gone(k) {
						out = append(out, k)
					}
				}
				return out
			}
			p.send, p.dirs, p.links, p.touch = filter(p.send), filter(p.dirs), filter(p.links), filter(p.touch)
		}
		if keep {
			clash := map[string]bool{}
			for _, k := range append(append([]string{}, p.delete...), p.send...) {
				_, inSrc := src.entries[k]
				if _, inHere := here.entries[k]; inSrc && inHere && !clash[k] {
					clash[k] = true
					zone := "a cache zone"
					for _, q := range paths {
						if q = strings.Trim(q, "/"); k == q || strings.HasPrefix(k, q+"/") {
							zone = q + " ( cache zone )"
							break
						}
					}
					r.conflicts = append(r.conflicts, [2]string{k, zone})
				}
			}
			sort.Slice(r.conflicts, func(i, j int) bool { return r.conflicts[i][0] < r.conflicts[j][0] })
			drop(func(k string) bool {
				for c := range clash {
					if k == c || strings.HasPrefix(k, c+"/") {
						return true
					}
				}
				return false
			})
			p.touch, p.delete = nil, nil
			sayConflicts(r.conflicts, where, say)
		}
		for _, key := range p.delete { // only what changed kind: nothing here is removed
			if _, in := src.entries[key]; !in {
				continue
			}
			if here.entries[key].kind == "l" { // a link ( a run's `latest` ): pointed anew
				w.remove(key)
				continue
			}
			what := "file"
			if here.entries[key].kind == "d" {
				what = "directory"
			}
			say(fmt.Sprintf("  %s: a %s here, something else over there -- left as it is", key, what))
			under := func(k string) bool { return k == key || strings.HasPrefix(k, key+"/") }
			for k := range src.entries {
				if under(k) {
					delete(src.entries, k)
				}
			}
			drop(under)
		}
		for _, key := range p.dirs {
			if _, in := src.entries[key]; in {
				w.mkdir(key)
			}
		}
		for _, key := range p.links {
			w.link(key, src.entries[key].target)
		}
		for _, key := range p.touch {
			w.touch(key, src.entries[key])
			r.touched = append(r.touched, key)
		}
		have := map[string]any{} // what is here as it is there
		if back {
			asked := map[string]bool{}
			for _, k := range p.send {
				asked[k] = true
			}
			for k, e := range src.entries {
				if e.kind == "f" && !asked[k] {
					have[k] = e.wire()
				}
			}
		}
		x.say(map[string]any{"want": list(p.send)})
		var h *handle
		defer func() {
			if h != nil {
				w.abort(h)
			}
		}()
		for {
			msg, chunk := x.next()
			if msg == nil {
				if h == nil {
					fail("a chunk of no file")
				}
				w.write(h, unpackChunk(chunk))
				continue
			}
			if key, ok := msg["file"]; ok {
				h = w.begin(kit.Text(key))
			} else if _, ok := msg["restart"]; ok {
				w.restart(h)
			} else if _, ok := msg["gone"]; ok {
				if h != nil {
					w.abort(h)
				}
				h = nil
			} else if raw, ok := msg["end"]; ok {
				e, _ := entryFromWire(raw)
				w.commit(h, e, "")
				h = nil
				key := kit.Text(msg["key"])
				r.Sent = append(r.Sent, key)
				r.Bytes += e.size
				if back {
					have[key] = e.wire()
				}
				if stable, _ := msg["stable"].(bool); !stable {
					r.unstable = append(r.unstable, key)
					say(fmt.Sprintf("  %s: still being written on %s -- what was read is here", key, where))
				}
			} else if _, ok := msg["done"]; ok {
				break
			}
		}
		if back {
			x.say(map[string]any{"have": have})
			_ = x.get()
		}
	})
	return r, err
}

// SendFiles: files of `localDir`, by their keys, into `remoteDir` over there, outside any copy's manifest -- what was
// downloaded here for a run over there ( ADR 0013 ) --, then the answer the run waits for. A file already at its
// place there is left.
func SendFiles(l *reach.Link, localDir, remoteDir string, keys []string, answerPath string, body any) error {
	return SendInto(l, localDir, remoteDir, keys, false, answerPath, body)
}

// SendInto: sendFiles, the files replacing what is there when `replace` says so ( the one replaced is kept as
// `<name>.prev` ), and no answer when `answerPath` is empty
func SendInto(l *reach.Link, localDir, remoteDir string, keys []string, replace bool, answerPath string, body any) error {
	c := l.Open(map[string]any{"op": "receive", "root": remoteDir, "replace": replace})
	defer c.Close()
	theirs, err := c.Reply()
	if err != nil {
		return err
	}
	accepted := []string{}
	for _, t := range kit.Strs(theirs["codecs"]) {
		if t == "d" || t == "-" {
			accepted = append(accepted, t)
		}
	}
	x := xfer{c}
	return guarded(func() {
		src := newManifest()
		for _, key := range keys {
			src.entries[key] = scanOne(filepath.Join(localDir, filepath.FromSlash(key)))
		}
		for _, key := range keys {
			x.say(map[string]any{"file": key})
			e, _, err := readInto(localDir, src, key,
				func(b []byte) { must(c.Send(kit.IN, append([]byte{'C'}, packChunk(b, accepted)...))) },
				func() { x.say(map[string]any{"restart": true}) })
			must(err)
			x.say(map[string]any{"end": e.wire(), "key": key})
		}
		if answerPath != "" {
			x.say(map[string]any{"answer": map[string]any{"path": answerPath, "body": body}})
		}
		x.say(map[string]any{"done": true})
		_ = x.get()
	})
}

package filesync

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// What a push writes, said as it writes it ( `.errand-copy.writes`, a line each ): a push that is cut, its agent killed even,
// leaves a copy that knows what errand wrote there. Put in the manifest, and removed, when a push ends.

const writesName = CopyState + ".writes"

type writesLog struct {
	path string
	f    *os.File
}

func openWrites(root string) *writesLog { return &writesLog{path: filepath.Join(root, writesName)} }

// say: before it is in place
func (l *writesLog) say(key string, e entry) {
	if l.f == nil {
		f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		must(err)
		l.f = f
	}
	b, err := json.Marshal([]any{key, e.wire()})
	must(err)
	_, err = l.f.Write(append(b, '\n'))
	must(err)
}

func (l *writesLog) close() {
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

func (l *writesLog) done() {
	l.close()
	_ = os.Remove(l.path)
}

// withWrites: the manifest a copy can be trusted by -- none, when it does not say whose its files are --, with what a push that was cut wrote since. What was said and is not on the disk as it was said was
// not written: what was known of that file stays.
func withWrites(root string, m *manifest) *manifest {
	if m != nil && !m.owns { // of before: what it lists may have been there before errand
		m = nil
	}
	f, err := os.Open(filepath.Join(root, writesName))
	if err != nil {
		return m
	}
	defer f.Close()
	if m == nil {
		m = newManifest()
		m.owns = true
	}
	lines := bufio.NewReader(f)
	for {
		line, err := lines.ReadBytes('\n')
		if err != nil { // ( a line not whole: its file was not put in place )
			return m
		}
		var pair []json.RawMessage
		var key string
		if json.Unmarshal(line, &pair) != nil || len(pair) != 2 || json.Unmarshal(pair[0], &key) != nil {
			continue
		}
		e, ok := entryFromWire(numbered(pair[1]))
		if !ok {
			continue
		}
		one := newManifest()
		one.owns = true
		one.entries[key] = e
		p := filepath.Join(root, filepath.FromSlash(m.onDisk(key)))
		if st, err := os.Lstat(p); err != nil || !one.asWritten(key, p, st) {
			continue
		}
		m.entries[key] = e
		delete(m.foreign, key)
	}
}

func numbered(raw json.RawMessage) any {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // ( an mtime in nanoseconds does not hold in a float )
	var v any
	_ = dec.Decode(&v)
	return v
}

func (m *manifest) hasFound(key string) bool {
	for _, it := range m.foundList() {
		if it.f.key == key {
			return true
		}
	}
	return false
}

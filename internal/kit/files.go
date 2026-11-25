// What a machine answers about its files: the files of a directory, the end of one, a directory removed.

package kit

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// AsText: bytes read as UTF-8, what is not one replaced as Python's `decode( "utf-8", "replace" )` does: one U+FFFD
// for each longest run of bytes that begins a character and does not end it
func AsText(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			out.WriteByte(c)
			i++
			continue
		}
		need, lo, hi := 0, byte(0x80), byte(0xBF)
		switch {
		case c >= 0xC2 && c <= 0xDF:
			need = 1
		case c == 0xE0:
			need, lo = 2, 0xA0
		case c >= 0xE1 && c <= 0xEC, c == 0xEE, c == 0xEF:
			need = 2
		case c == 0xED:
			need, hi = 2, 0x9F
		case c == 0xF0:
			need, lo = 3, 0x90
		case c >= 0xF1 && c <= 0xF3:
			need = 3
		case c == 0xF4:
			need, hi = 3, 0x8F
		}
		j := i + 1
		for k := 0; k < need && j < len(b); k++ {
			if b[j] < lo || b[j] > hi {
				break
			}
			lo, hi = 0x80, 0xBF
			j++
		}
		if need > 0 && j-i == need+1 {
			out.Write(b[i:j])
		} else {
			out.WriteRune(utf8.RuneError)
		}
		i = j
	}
	return out.String()
}

// LsFiles: files under `d` ( two levels down ), newest first: { name, size, mtime }
func LsFiles(d string) []any {
	type file struct {
		name  string
		size  int64
		mtime time.Time
	}
	var found []file
	add := func(rel string) bool {
		st, err := os.Stat(filepath.Join(d, rel))
		if err != nil {
			return false
		}
		if st.Mode().IsRegular() {
			found = append(found, file{rel, st.Size(), st.ModTime()})
		}
		return st.IsDir()
	}
	top, _ := os.ReadDir(d)
	var dirs []string
	for _, e := range top {
		if add(e.Name()) {
			dirs = append(dirs, e.Name())
		}
	}
	for _, sub := range dirs {
		in, _ := os.ReadDir(filepath.Join(d, sub))
		for _, e := range in {
			add(sub + "/" + e.Name())
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].mtime.After(found[j].mtime) })
	if len(found) > 200 {
		found = found[:200]
	}
	out := []any{}
	for _, f := range found {
		out = append(out, map[string]any{"name": f.name, "size": f.size, "mtime": Seconds(f.mtime)})
	}
	return out
}

// errnoSaid: what the system says of an error, as C's `strerror` writes it
func errnoSaid(err error) string {
	var no syscall.Errno
	said := err.Error()
	if errors.As(err, &no) {
		said = no.Error()
	}
	if said != "" {
		said = strings.ToUpper(said[:1]) + said[1:]
	}
	return said
}

// LsEntries: what is in `d`, one level: { name, dir, size, mtime }, directories first. At most 2000.
func LsEntries(d string) ([]any, error) {
	listed, err := os.ReadDir(d)
	if err != nil {
		return nil, fmt.Errorf("FileNotFoundError: %s: %s", d, errnoSaid(err))
	}
	if len(listed) > 2000 {
		listed = listed[:2000]
	}
	type item struct {
		name  string
		dir   bool
		size  int64
		mtime time.Time
	}
	var found []item
	for _, e := range listed {
		st, err := os.Stat(filepath.Join(d, e.Name()))
		if err != nil {
			continue
		}
		found = append(found, item{e.Name(), st.IsDir(), st.Size(), st.ModTime()})
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].dir != found[j].dir {
			return found[i].dir
		}
		return strings.ToLower(found[i].name) < strings.ToLower(found[j].name)
	})
	out := []any{}
	for _, f := range found {
		out = append(out, map[string]any{"name": f.name, "dir": f.dir, "size": f.size, "mtime": Seconds(f.mtime)})
	}
	return out, nil
}

// TailFile: at most `size` bytes of `path` from `offset`; no offset: its last `size` bytes. `raw`: the bytes as they
// are, in base64, not read as text.
func TailFile(path string, offset any, size int64, raw bool) (map[string]any, error) {
	st, err := os.Stat(path)
	if err != nil {
		return map[string]any{"data": "", "offset": 0, "size": 0, "missing": true}, nil
	}
	total := st.Size()
	start := total - size
	if start < 0 {
		start = 0
	}
	if offset != nil {
		start = int64(Num(offset, 0))
		if start > total {
			start = total
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, size))
	if err != nil {
		return nil, err
	}
	out := map[string]any{"offset": start, "end": start + int64(len(data)), "size": total}
	if raw {
		out["b64"] = base64.StdEncoding.EncodeToString(data)
	} else {
		out["data"] = AsText(data)
	}
	return out, nil
}

// ── the copies of results held here ( the side that holds ) ──

func TreeSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, e os.DirEntry, err error) error {
		if err == nil && e.Type().IsRegular() {
			if st, err := e.Info(); err == nil {
				total += st.Size()
			}
		}
		return nil
	})
	return total
}

// ── runs removed for good ─────────────────────────────────────────────────────

func SizesOf(leaves []string) map[string]int64 {
	out := map[string]int64{}
	du, _ := exec.LookPath("du")
	for at := 0; at < len(leaves); at += 200 {
		end := at + 200
		if end > len(leaves) {
			end = len(leaves)
		}
		chunk := leaves[at:end]
		if du != "" {
			in := map[string]bool{}
			for _, l := range chunk {
				in[l] = true
			}
			if got, err := exec.Command(du, append([]string{"-sk"}, chunk...)...).Output(); err == nil || len(got) > 0 {
				for _, line := range strings.Split(string(got), "\n") {
					kb, path, ok := strings.Cut(line, "\t")
					if n, err := strconv.ParseInt(strings.TrimSpace(kb), 10, 64); ok && err == nil && in[path] {
						out[path] = n * 1024
					}
				}
				continue
			}
		}
		for _, l := range chunk {
			out[l] = TreeSize(l)
		}
	}
	return out
}

// ── a file a run wants and cannot reach ───────────────────────────────────────

func IsFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

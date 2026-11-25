// Package lib is errand's Python, carried by the program ( ADR 0034 ): what reads a project's declarations, what a run
// written in Python imports ( `from errand import step, test`… ), errand's C++ headers. It is put in errand's directory, under
// what it is made of, by whichever program first needs it there: a program and its Python are always of one version, and no
// machine is sent it apart from the program.
package lib

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/system"
)

//go:embed errand/*.py errand/include/errand/*.hpp
var files embed.FS

var made struct {
	sync.Mutex
	key string
}

func paths() []string {
	var out []string
	_ = fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && p != versionFile { // ( what an installation of the library left: the program's own is said )
			out = append(out, p)
		}
		return err
	})
	return append(out, versionFile)
}

const versionFile = "errand/_version.py"

func read(p string) ([]byte, error) {
	if p == versionFile {
		return []byte(fmt.Sprintf("__version__ = %q\n", system.Version)), nil
	}
	return files.ReadFile(p)
}

// Dir: the directory to put on a `PYTHONPATH`; it holds the package, and nothing else
func Dir() (string, error) {
	made.Lock()
	defer made.Unlock()
	if made.key == "" {
		h := sha256.New()
		for _, p := range paths() {
			data, err := read(p)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(h, "%s %d\n", p, len(data))
			h.Write(data)
		}
		made.key = hex.EncodeToString(h.Sum(nil))[:16]
	}
	dir := filepath.Join(system.Cache(), "binding", made.key)
	if st, err := os.Stat(dir); err == nil {
		if now := time.Now(); now.Sub(st.ModTime()) > time.Hour { // it is used: what is not, for a week, goes
			_ = os.Chtimes(dir, now, now)
		}
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	part, err := os.MkdirTemp(filepath.Dir(dir), ".making-") // whole, then renamed: another may be making the same
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(part)
	for _, p := range paths() {
		data, err := read(p)
		if err != nil {
			return "", err
		}
		at := filepath.Join(part, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(at, data, 0o644); err != nil {
			return "", err
		}
	}
	if err := os.Chmod(part, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(part, dir); err != nil {
		if _, there := os.Stat(dir); there != nil {
			return "", err
		}
	}
	return dir, nil
}

// With: surroundings where a Python finds this package before any other that is installed
func With(environ []string) ([]string, error) {
	if environ == nil {
		environ = os.Environ()
	}
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	was := ""
	for _, kv := range environ {
		if len(kv) > 11 && kv[:11] == "PYTHONPATH=" {
			was = kv[11:]
		}
	}
	if was != "" {
		dir += string(os.PathListSeparator) + was
	}
	return append(append([]string{}, environ...), "PYTHONPATH="+dir), nil
}

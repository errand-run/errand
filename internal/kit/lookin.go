package kit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// LookIn: the program a word names, as the job's own surroundings find it ( its PATH, not the agent's )
func LookIn(word string, env []string, cwd string) (string, error) {
	if runtime.GOOS == "windows" {
		return lookInWindows(word, env, cwd)
	}
	if strings.Contains(word, "/") {
		p := word
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		if st, err := os.Stat(p); err != nil {
			return "", err
		} else if st.IsDir() || st.Mode()&0o111 == 0 {
			return "", fmt.Errorf("%s: permission denied", word)
		}
		return p, nil
	}
	path := os.Getenv("PATH")
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v // ( the last one said is the one a process reads )
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, word)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("executable file not found in $PATH")
}

// lookInWindows: the same, where a program is told by how its name ends
func lookInWindows(word string, env []string, cwd string) (string, error) {
	get := func(name string) string {
		out := os.Getenv(name)
		for _, kv := range env {
			if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, name) {
				out = v
			}
		}
		return out
	}
	ends := strings.Split(strings.ToLower(get("PATHEXT")), ";")
	if len(ends) == 1 {
		ends = []string{".com", ".exe", ".bat", ".cmd"}
	}
	try := func(p string) string {
		if filepath.Ext(p) != "" {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
		for _, e := range ends {
			if st, err := os.Stat(p + e); err == nil && !st.IsDir() {
				return p + e
			}
		}
		return ""
	}
	if strings.ContainsAny(word, `/\`) {
		p := filepath.FromSlash(word)
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		if got := try(p); got != "" {
			return got, nil
		}
		return "", &os.PathError{Op: "start", Path: word, Err: os.ErrNotExist}
	}
	for _, dir := range append([]string{cwd}, filepath.SplitList(get("PATH"))...) {
		if got := try(filepath.Join(dir, word)); got != "" {
			return got, nil
		}
	}
	return "", errors.New("executable file not found in %PATH%")
}

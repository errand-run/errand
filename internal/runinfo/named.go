package runinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Target: what the words designate -- the command to start, and the path the run is filed under
func Target(words []string, root, cwd string) (argv []string, path, file string) {
	first := words[0]
	abs := first
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, first)
	}
	if st, err := os.Stat(abs); err == nil && !st.IsDir() {
		rel, err := filepath.Rel(root, abs)
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = filepath.Base(abs) // a file outside its project is filed under its name alone
		}
		if strings.HasSuffix(first, ".py") {
			return append([]string{"python", abs}, words[1:]...), rel, rel
		}
		return append([]string{abs}, words[1:]...), rel, rel
	}
	return words, filepath.Base(first), ""
}

func Took(seconds float64) string {
	switch {
	case seconds < 1:
		return fmt.Sprintf("%.0f ms", seconds*1000)
	case seconds < 120:
		return fmt.Sprintf("%.1f s", seconds)
	}
	return fmt.Sprintf("%.0f min", seconds/60)
}

// Carried: the note of a run made after this one, as this machine keeps it -- over there, on the same machine, with
// its directory brought back where this one's was
func (n RunNote) Carried(next RunNote) RunNote {
	if n.Host == nil || n.There == nil {
		return next
	}
	far := *n.There
	rel, err := filepath.Rel(far.Outputs, next.Leaf)
	if err != nil {
		return next
	}
	far.Leaf, far.LeafRel = next.Leaf, filepath.ToSlash(rel)
	outputs := strings.TrimSuffix(n.Leaf, filepath.FromSlash(n.There.LeafRel))
	out := next
	out.There, out.Host, out.Project, out.Batch = &far, n.Host, n.Project, n.Batch
	out.Leaf = filepath.Join(outputs, filepath.FromSlash(far.LeafRel))
	return out
}

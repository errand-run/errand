package agent

import (
	"strings"

	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/runinfo"
)

// Fill: a runner's words. `several` stand for a whole word each ( `{files}`, `{entries}`, `{args}` ); `one` are put in
// wherever they are written ( `{build_dir}/{stem}` ); a word that is the name of a tool, in braces, is the words that start
// it. What is in braces and none of these is left as it is: a build line is full of braces that are somebody else's.
func Fill(words []string, tools map[string]any, several map[string][]string, one map[string]string) []string {
	known := func(name string) bool {
		_, given := tools[name]
		return given || name == "python" || name == "cargo" || name == "cmake" || name == "ctest"
	}
	var out []string
	for _, w := range words {
		if m := runinfo.OneName.FindStringSubmatch(w); m != nil && m[0] == w {
			if put, ok := several[m[1]]; ok {
				out = append(out, put...)
				continue
			}
			if _, ok := one[m[1]]; !ok && known(m[1]) {
				out = append(out, runinfo.ToolWords(m[1], tools)...)
				continue
			}
		}
		out = append(out, runinfo.OneName.ReplaceAllStringFunc(w, func(put string) string {
			name := put[1 : len(put)-1]
			if v, ok := one[name]; ok {
				return v
			}
			if known(name) {
				return strings.Join(runinfo.ToolWords(name, tools), " ")
			}
			return put
		}))
	}
	return out
}

// Told: a set of entries of `file`, as its tool is told them -> its words, and what is said when the names are those of
// a listing. None: the whole file.
func Told(run project.Running, file string, names []string, listed bool) ([]string, []string) {
	if len(names) == 0 {
		if run.Whole == "" {
			return nil, nil
		}
		return []string{strings.ReplaceAll(run.Whole, "{file}", file)}, nil
	}
	var out []string
	for _, n := range names {
		if listed && run.Escape != "" {
			var b strings.Builder
			for _, c := range n {
				if strings.ContainsRune(run.Escape, c) {
					b.WriteByte('\\')
				}
				b.WriteRune(c)
			}
			n = b.String()
		}
		out = append(out, strings.NewReplacer("{file}", file, "{name}", n).Replace(run.Entry))
	}
	if run.Join != "" {
		out = []string{strings.Join(out, run.Join)}
	}
	if listed {
		return out, run.Exact
	}
	return out, nil
}

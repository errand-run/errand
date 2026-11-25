// What a target with `::` designates ( the guide's "Running a suite"; ADR 0036 ): files of the copy, and for each the
// runner that takes it.
//
//	errand run tests/test_ot.py::basic
//	errand run '**/test_*.py::grad_*'
//	errand run 'tests/{ot,shapes}/test_*.py::{basic,quick}'
//
// A target is a path from the directory the command was typed in, then `::` and what is asked of the file's tool.
// The path is a glob -- `*`, `?`, `[…]` within a name, `{a,b}` alternatives, `**` any depth of directories --, and a
// directory means everything under it. A comma outside braces separates targets; `\,` is a comma that is only one.
//
// This is asked of the agent of the machine that holds the copy, and needs no environment: which files there are,
// and what each runner says it takes ( a name, a line the file holds, a file nearby ), is read from the disk. What a
// target cannot designate is an error, never an empty run: a path without a glob must exist, a glob must match.

package runinfo

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/errand-run/errand/internal/project"
)

// discoverable: the level from which a runner's files are found by a glob, not only when named ( ADR 0001 )
const discoverable = 3

// directories nothing is looked for in: what tools write, never what a project runs
var skipDirs = map[string]bool{".git": true, ".hg": true, ".svn": true, "__pycache__": true, "node_modules": true,
	"build": true, "dist": true, ".venv": true, "venv": true, ".tox": true, ".mypy_cache": true, ".pytest_cache": true,
	".ruff_cache": true, ".cache": true, ".idea": true, ".vscode": true, "target": true, ".claude": true,
	"errand-outputs": true, ".errand": true}

var targetEscapes = strings.NewReplacer(`\,`, "\x01", `\{`, "\x02", `\}`, "\x03")

var targetEscaped = strings.NewReplacer("\x01", ",", "\x02", "{", "\x03", "}")

var targetRestored = strings.NewReplacer("\x01", `\,`, "\x02", `\{`, "\x03", `\}`)

// splitTop: `text` cut at every `sep` that is not inside braces
func splitTop(text string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '{':
			depth++
		case c == '}' && depth > 0:
			depth--
		case c == sep && depth == 0:
			out = append(out, text[start:i])
			start = i + 1
		}
	}
	return append(out, text[start:])
}

// SplitTargets: the targets of a word, its escapes still written
func SplitTargets(word string) []string {
	var out []string
	for _, part := range splitTop(targetEscapes.Replace(word), ',') {
		if part = strings.TrimSpace(targetRestored.Replace(part)); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// expandBraces: `a{b,c}d` -> `abd`, `acd`, as a shell does it; a brace with no comma in it stays as it is
func expandBraces(text string) []string {
	var grow func(s string) []string
	grow = func(s string) []string {
		depth, start := 0, -1
		for i := 0; i < len(s); i++ {
			switch c := s[i]; {
			case c == '{':
				if depth == 0 {
					start = i
				}
				depth++
			case c == '}' && depth > 0:
				if depth--; depth == 0 {
					if parts := splitTop(s[start+1:i], ','); len(parts) > 1 {
						var out []string
						for _, p := range parts {
							out = append(out, grow(s[:start]+p+s[i+1:])...)
						}
						return out
					}
				}
			}
		}
		return []string{s}
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range grow(targetEscapes.Replace(text)) {
		if s = targetEscaped.Replace(s); !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func IsGlob(text string) bool { return strings.ContainsAny(text, "*?[") }

// globRegexp: a glob as a pattern over a whole text. `dirs`: over a path, where `*` and `?` stay inside one
// directory and `**`, as a whole component, is any number of them, none included.
func globRegexp(glob string, dirs bool) *regexp.Regexp {
	var out strings.Builder
	out.WriteString(`(?s)\A`)
	one, any := ".", ".*"
	if dirs {
		one, any = "[^/]", "[^/]*"
	}
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		if dirs && strings.HasPrefix(glob[i:], "**") && (i == 0 || glob[i-1] == '/') {
			if strings.HasPrefix(glob[i:], "**/") {
				out.WriteString("(?:[^/]*/)*")
				i += 2
				continue
			}
			if i+2 == len(glob) {
				out.WriteString(".*")
				i++
				continue
			}
		}
		switch c {
		case '*':
			out.WriteString(any)
		case '?':
			out.WriteString(one)
		case '[':
			j := i + 1
			if j < len(glob) && (glob[j] == '!' || glob[j] == '^') {
				j++
			}
			if j < len(glob) && glob[j] == ']' {
				j++
			}
			for j < len(glob) && glob[j] != ']' {
				j++
			}
			if j >= len(glob) { // it does not close: only a bracket
				out.WriteString(regexp.QuoteMeta("["))
				continue
			}
			body := glob[i+1 : j]
			if strings.HasPrefix(body, "!") || strings.HasPrefix(body, "^") {
				body = "^" + body[1:]
			}
			out.WriteString("[" + strings.NewReplacer(`\`, `\\`, "[", `\[`).Replace(body) + "]")
			i = j
		default:
			out.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	out.WriteString(`\z`)
	re, err := regexp.Compile(out.String())
	if err != nil {
		return regexp.MustCompile(`\A` + regexp.QuoteMeta(glob) + `\z`)
	}
	return re
}

// NameMatches: an entry's name against what was asked after `::` -- the whole of what follows the file, or its last
// part ( `TestA::test_x` is found by `test_x*` )
func NameMatches(glob, name string) bool {
	re := globRegexp(glob, false)
	if re.MatchString(name) {
		return true
	}
	if at := strings.LastIndex(name, "::"); at >= 0 {
		return re.MatchString(name[at+2:])
	}
	return false
}

// ProjectFiles: every file of the copy a runner may take, from the root -- not what tools write, nor what the
// project leaves out, nor another project held in a directory of this one
func ProjectFiles(root string, exclude []string) []string {
	skip := map[string]bool{}
	for _, e := range exclude {
		skip[strings.Trim(e, "/")] = true
	}
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if p == root {
				return nil
			}
			name := d.Name()
			if skipDirs[name] || skip[name] || skip[rel] || strings.HasSuffix(name, ".egg-info") {
				return filepath.SkipDir
			}
			if own, _ := filepath.Glob(filepath.Join(p, "errand-*.py")); len(own) > 0 {
				return filepath.SkipDir // another project: its entries are not ours to run
			}
			return nil
		}
		if d.Type().IsRegular() || d.Type()&os.ModeSymlink != 0 {
			out = append(out, rel)
		}
		return nil
	})
	return out
}

// WholeGlobs: targets without `::` that are globs, as the files they match -- those that can be run, one run each
func WholeGlobs(doc *project.Document, cwd string, given []string) ([]string, error) {
	var out []string
	var files []string
	for _, target := range given {
		if strings.Contains(target, "::") || (!IsGlob(target) && len(expandBraces(target)) == 1) {
			out = append(out, target)
			continue
		}
		if st, err := os.Stat(filepath.Join(cwd, target)); err == nil && !st.IsDir() && !filepath.IsAbs(target) {
			out = append(out, target) // ( a file so named )
			continue
		}
		if files == nil {
			files = ProjectFiles(doc.Root, doc.Settings.Exclude)
		}
		found := 0
		for _, alt := range expandBraces(target) {
			at := alt
			if !filepath.IsAbs(at) {
				at = filepath.Join(cwd, filepath.FromSlash(alt))
			}
			rel, err := filepath.Rel(doc.Root, at)
			if err != nil || strings.HasPrefix(rel, "..") {
				return nil, fmt.Errorf("%s: not in the project", alt)
			}
			re := globRegexp(filepath.ToSlash(rel), true)
			for _, f := range files {
				full := filepath.Join(doc.Root, filepath.FromSlash(f))
				st, err := os.Stat(full)
				if err != nil || !re.MatchString(f) || !strings.HasSuffix(f, ".py") && st.Mode()&0o111 == 0 {
					continue
				}
				if filepath.Dir(full) == doc.Root && strings.HasPrefix(f, "errand-") && strings.HasSuffix(f, ".py") {
					continue // the project's own declarations
				}
				name, err := filepath.Rel(cwd, full)
				if err != nil {
					name = full
				}
				out = append(out, name)
				found++
			}
		}
		if found == 0 {
			return nil, fmt.Errorf("%s: no file that can be run matches it", target)
		}
	}
	return out, nil
}

// claims holds what was read to know who takes what: a file's beginning, a pattern, once.
type claims struct {
	root    string
	runners map[string]project.Runner
	heads   map[string]string
	holds   map[string]*regexp.Regexp
	owned   map[string][]string // a file -> the runners that take it best
}

func (c *claims) head(rel string) string {
	if h, ok := c.heads[rel]; ok {
		return h
	}
	text := ""
	if f, err := os.Open(filepath.Join(c.root, filepath.FromSlash(rel))); err == nil {
		data, _ := io.ReadAll(io.LimitReader(f, 1<<20))
		f.Close()
		text = string(data)
	}
	c.heads[rel] = text
	return text
}

// takes: whether the runner `name` says this file is its own
func (c *claims) takes(name string, rel string) bool {
	t := c.runners[name].Takes
	base := path.Base(rel)
	any := func(patterns []string) bool {
		for _, p := range patterns {
			if ok, _ := path.Match(p, base); ok {
				return true
			}
		}
		return false
	}
	if len(t.Suffixes) > 0 {
		found := false
		for _, s := range t.Suffixes {
			found = found || strings.HasSuffix(base, s)
		}
		if !found {
			return false
		}
	}
	if len(t.Names) > 0 && !any(t.Names) || any(t.NotNames) {
		return false
	}
	if t.Dirs != nil {
		under := false
		for _, d := range t.Dirs {
			d = strings.Trim(filepath.ToSlash(d), "/")
			under = under || d == "." || d == "" || strings.HasPrefix(rel, d+"/")
		}
		if !under {
			return false
		}
	}
	if t.Near != "" {
		if _, ok := c.info(name, rel); !ok {
			return false
		}
	}
	if t.Holds != "" {
		re, ok := c.holds[name]
		if !ok {
			re, _ = regexp.Compile(t.Holds)
			c.holds[name] = re
		}
		if re == nil || !re.MatchString(c.head(rel)) {
			return false
		}
	}
	return true
}

// owners: the runners that take a file best -- one, unless two fit it equally well; `only`: that one or none
func (c *claims) owners(rel, only string) []string {
	key := only + "\x00" + rel
	if got, ok := c.owned[key]; ok {
		return got
	}
	var best []string
	level := 0
	for name, r := range c.runners {
		if !r.Entries || (only != "" && name != only) || (only == "" && r.Level < discoverable) || !c.takes(name, rel) {
			continue
		}
		switch {
		case r.Level > level:
			best, level = []string{name}, r.Level
		case r.Level == level:
			best = append(best, name)
		}
	}
	sort.Strings(best)
	c.owned[key] = best
	return best
}

// Taken is what a target designates for one runner: its files, from the root, and what was asked of them.
type Taken struct {
	Target string              `json:"target"` // as it was typed
	Runner string              `json:"runner"`
	Files  []string            `json:"files"`
	Names  []string            `json:"names"` // what follows `::`, each alternative; none, or `*`: everything
	Info   map[string]FileInfo `json:"info,omitempty"`
}

// FileInfo is what is known of a file from where it is, for its runner's words: the file of its tool nearby, the name
// that one says, what the file is told its tool as.
type FileInfo struct {
	Near     string   `json:"near,omitempty"`
	NearName string   `json:"near_name,omitempty"`
	Variant  []string `json:"variant,omitempty"`
}

// info: what is known of a file, for the runner `name`; false when the runner says where its files are, and this is not one
func (c *claims) info(name, rel string) (FileInfo, bool) {
	t := c.runners[name].Takes
	var out FileInfo
	if t.Near == "" {
		return out, true
	}
	dir := ""
	for d := path.Dir(rel); ; d = path.Dir(d) {
		if st, err := os.Stat(filepath.Join(c.root, filepath.FromSlash(d), t.Near)); err == nil && !st.IsDir() {
			dir, out.Near = d, path.Join(d, t.Near)
			break
		}
		if d == "." || d == "/" {
			return out, false
		}
	}
	if t.NearName != "" {
		if re, err := regexp.Compile(t.NearName); err == nil {
			if m := re.FindStringSubmatch(c.head(out.Near)); len(m) > 1 {
				out.NearName = m[1]
			}
		}
	}
	if len(t.Paths) == 0 {
		return out, true
	}
	from := rel
	if dir != "." {
		from = strings.TrimPrefix(rel, dir+"/")
	}
	for _, v := range t.Paths {
		re, err := regexp.Compile(`\A(?:` + v.Path + `)\z`)
		if err != nil {
			continue
		}
		m := re.FindStringSubmatch(from)
		if m == nil {
			continue
		}
		for _, w := range v.Words {
			w = strings.ReplaceAll(w, "{near_name}", out.NearName)
			for i, g := range re.SubexpNames() {
				if g != "" {
					w = strings.ReplaceAll(w, "{"+g+"}", m[i])
				}
			}
			out.Variant = append(out.Variant, w)
		}
		return out, true
	}
	return out, false
}

// ResolveTargets: what each target designates, in the copy at `root`, for a command typed in `cwd` ( from the root )
func ResolveTargets(root, cwd string, targets []string, runners map[string]project.Runner, only string, exclude []string) ([]Taken, error) {
	if only != "" {
		r, ok := runners[only]
		if !ok {
			names := make([]string, 0, len(runners))
			for n := range runners {
				names = append(names, n)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("-r %s: no runner of that name ( there: %s )", only, strings.Join(names, ", "))
		}
		if !r.Entries {
			return nil, fmt.Errorf("-r %s: it runs a file whole, and `::` asks for what a file holds", only)
		}
	}
	c := &claims{root: root, runners: runners, heads: map[string]string{}, holds: map[string]*regexp.Regexp{}, owned: map[string][]string{}}
	files := ProjectFiles(root, exclude)
	cwd = strings.Trim(filepath.ToSlash(cwd), "/")
	if cwd == "." || strings.HasPrefix(cwd, "..") {
		cwd = ""
	}
	var out []Taken
	for _, text := range targets {
		pathPart, namePart, _ := strings.Cut(text, "::")
		pathPart, namePart = strings.TrimSpace(pathPart), strings.TrimSpace(namePart)
		var names []string
		if namePart != "" && namePart != "*" {
			names = expandBraces(namePart)
		}
		by := map[string][]string{}
		seen := map[string]bool{}
		missing, idle := "", ""
		match := func(rel string) error {
			if seen[rel] {
				return nil
			}
			owners := c.owners(rel, only)
			if len(owners) > 1 {
				return fmt.Errorf("%s: %s take it equally well: say which with -r NAME", rel, strings.Join(owners, " and "))
			}
			if len(owners) == 1 {
				seen[rel] = true
				by[owners[0]] = append(by[owners[0]], rel)
			}
			return nil
		}
		for _, alt := range expandBraces(pathPart) {
			rel := path.Clean(path.Join(cwd, alt))
			if strings.HasPrefix(rel, "..") {
				return nil, fmt.Errorf("%s: outside the project ( %s )", alt, root)
			}
			if rel == "." {
				rel = ""
			}
			if !IsGlob(alt) {
				st, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
				switch {
				case err != nil:
					missing = alt
				case st.IsDir():
					for _, f := range files {
						if rel == "" || strings.HasPrefix(f, rel+"/") {
							if err := match(f); err != nil {
								return nil, err
							}
						}
					}
				default:
					before := len(seen)
					if err := match(rel); err != nil {
						return nil, err
					}
					if len(seen) == before && !seen[rel] {
						idle = alt
					}
				}
				continue
			}
			re := globRegexp(rel, true)
			for _, f := range files {
				for p := f; p != "." && p != "/"; p = path.Dir(p) { // a glob may name a directory: everything in it
					if re.MatchString(p) {
						if err := match(f); err != nil {
							return nil, err
						}
						break
					}
				}
			}
		}
		if len(by) == 0 {
			switch {
			case idle != "" && only != "":
				return nil, fmt.Errorf("%s: the runner %s does not take it", idle, only)
			case idle != "":
				return nil, fmt.Errorf("%s: no entry runner takes it ( see errand.runners, or -r NAME )", idle)
			case missing != "":
				return nil, fmt.Errorf("%s: no such file or directory", missing)
			}
			return nil, fmt.Errorf("'%s' matches no file an entry runner takes", text)
		}
		runnersHere := make([]string, 0, len(by))
		for name := range by {
			runnersHere = append(runnersHere, name)
		}
		sort.Strings(runnersHere)
		for _, name := range runnersHere {
			sort.Strings(by[name])
			info := map[string]FileInfo{}
			if runners[name].Takes.Near != "" {
				for _, f := range by[name] {
					info[f], _ = c.info(name, f)
				}
			}
			out = append(out, Taken{Target: text, Runner: name, Files: by[name], Names: names, Info: info})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("nothing to run")
	}
	return out, nil
}

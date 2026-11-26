// What a machine has for a page's service ( ADR 0027 §5, 0041 ): tools and their versions, read from files, the cards,
// whether a view's files are seen. It runs nothing of what it looks at.

package svc

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
)

// ── versions, from files ─────────────────────────────────────────────────────

var digits = regexp.MustCompile(`\d+`)

// meets: whether a version is `least` or later -> and whether that can be said
func meets(v, least string) (bool, bool) {
	if least == "" {
		return true, true
	}
	if v == "" {
		return false, false
	}
	a, b := digits.FindAllString(v, 4), digits.FindAllString(least, 4)
	for i := 0; i < len(a) || i < len(b); i++ {
		if i >= len(a) {
			return false, true
		}
		if i >= len(b) {
			return true, true
		}
		x, _ := strconv.Atoi(a[i])
		y, _ := strconv.Atoi(b[i])
		if x != y {
			return x > y, true
		}
	}
	return true, true
}

func dpkgVersion(pkg, at string) string {
	if !strings.HasPrefix(at, "/usr/") {
		return ""
	}
	data, _ := os.ReadFile("/var/lib/dpkg/status")
	for _, block := range strings.Split(string(data), "\n\n") {
		version, found := "", false
		for _, line := range strings.Split(block, "\n") {
			found = found || line == "Package: "+pkg
			if v, ok := strings.CutPrefix(line, "Version: "); ok {
				if _, after, epoch := strings.Cut(v, ":"); epoch {
					v = after
				}
				version, _, _ = strings.Cut(v, "-")
			}
		}
		if found && strings.Contains(block, "Status: install ok installed") {
			return version
		}
	}
	return ""
}

func snapVersion(name, at string) string {
	if !strings.HasPrefix(at, "/snap/") {
		return ""
	}
	data, _ := os.ReadFile("/snap/" + name + "/current/meta/snap.yaml")
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "version:"); ok {
			return strings.Trim(strings.TrimSpace(v), `'"`)
		}
	}
	return ""
}

// plistValue: a text of a property list
func plistValue(file, key string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	if strings.HasPrefix(string(data), "bplist") {
		if data, err = exec.Command("plutil", "-convert", "xml1", "-o", "-", file).Output(); err != nil {
			return ""
		}
	}
	m := regexp.MustCompile(`<key>` + regexp.QuoteMeta(key) + `</key>\s*<string>([^<]*)</string>`).FindSubmatch(data)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// programVersion: the version of a program, from files only -> and how it is known
func programVersion(at string, rules []any, fetched map[string]string) (string, string) {
	real, err := filepath.EvalSymlinks(at)
	if err != nil {
		real = at
	}
	if known := ObjectOf(caches.ReadObject(probesFile())[real]); known != nil && kit.Text(known["stamp"]) == fileStamp(real) {
		return kit.Text(known["version"]), kit.Text(known["how"])
	}
	tools, _ := filepath.EvalSymlinks(filepath.Join(kit.LocalCache(), "tools"))
	if rel, err := filepath.Rel(tools, real); err == nil && tools != "" && filepath.IsLocal(rel) {
		sha, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
		if v := fetched[sha]; v != "" {
			return v, "fetched by errand"
		}
	}
	for _, x := range rules {
		r := ObjectOf(x)
		switch {
		case r["plist"] != nil:
			file := filepath.Join(filepath.Dir(real), filepath.FromSlash(kit.Text(r["plist"])))
			key := kit.Text(r["key"])
			if key == "" {
				key = "CFBundleShortVersionString"
			}
			if v := plistValue(file, key); v != "" {
				return v, "Info.plist ( " + file + " )"
			}
		case r["dirname"] != nil:
			re, err := regexp.Compile(kit.Text(r["dirname"]))
			if err != nil {
				continue
			}
			parts := strings.Split(filepath.ToSlash(real), "/")
			for i := len(parts) - 1; i >= 0; i-- {
				if m := re.FindStringSubmatch(parts[i]); len(m) > 1 {
					return m[1], "its directory's name ( " + parts[i] + " )"
				}
			}
		case r["dpkg"] != nil:
			if v := dpkgVersion(kit.Text(r["dpkg"]), at); v != "" {
				return v, "dpkg ( package " + kit.Text(r["dpkg"]) + " )"
			}
		case r["snap"] != nil:
			if v := snapVersion(kit.Text(r["snap"]), real); v != "" {
				return v, "snap ( " + kit.Text(r["snap"]) + " )"
			}
		}
	}
	return "", ""
}

// ── tools ────────────────────────────────────────────────────────────────────

type toolFound struct {
	path, version, how string
	ok, known          bool
}

type toolState struct {
	path       string // the first that will do; "" when none does
	min        string
	candidates []toolFound
	fetch      map[string]any
}

func onPlatform(pattern string) bool {
	ok, _ := path.Match(pattern, platformName())
	return ok
}

func toolCandidates(desc map[string]any, given []string) []toolFound {
	var out []toolFound
	seen := map[string]bool{}
	fetched := map[string]string{}
	fetches := ObjectOf(desc["fetch"])
	for _, k := range sortedKeys(fetches) {
		f := ObjectOf(fetches[k])
		fetched[strings.ToLower(kit.Text(f["sha256"]))] = kit.Text(f["version"])
	}
	add := func(at, version string) {
		real, err := filepath.EvalSymlinks(at)
		if err != nil || seen[real] {
			return
		}
		st, err := os.Stat(real)
		if err != nil || !st.Mode().IsRegular() || runtime.GOOS != "windows" && st.Mode()&0o111 == 0 {
			return
		}
		seen[real] = true
		c := toolFound{path: at, version: version, how: "given"}
		if version == "" {
			c.version, c.how = programVersion(at, caches.ListOf(desc["version_from"]), fetched)
		}
		c.ok, c.known = meets(c.version, kit.Text(desc["min"]))
		out = append(out, c)
	}
	home, _ := os.UserHomeDir()
	for _, g := range given {
		at, v, _ := strings.Cut(g, "@")
		if strings.HasPrefix(at, "~/") {
			at = filepath.Join(home, at[2:])
		}
		add(at, v)
	}
	looks := kit.Strs(desc["look"])
	if by := ObjectOf(desc["look"]); by != nil {
		for _, pattern := range sortedKeys(by) {
			if onPlatform(pattern) {
				looks = append(looks, kit.Strs(by[pattern])...)
			}
		}
	}
	for _, c := range looks {
		if filepath.IsAbs(c) {
			add(c, "")
		} else if p, err := exec.LookPath(c); err == nil {
			add(p, "")
		}
	}
	for _, k := range sortedKeys(fetches) {
		f := ObjectOf(fetches[k])
		if sha, run := kit.Text(f["sha256"]), kit.Text(f["run"]); sha != "" && run != "" {
			add(filepath.Join(toolDir(sha), filepath.FromSlash(run)), "")
		}
	}
	return out
}

// toolsHere: each tool a description asks for, where it may be here, and the first that will do
func toolsHere(tools map[string]any, given map[string][]string) map[string]toolState {
	out := map[string]toolState{}
	for name, x := range tools {
		desc := ObjectOf(x)
		s := toolState{min: kit.Text(desc["min"]), candidates: toolCandidates(desc, given[name]), fetch: ObjectOf(ObjectOf(desc["fetch"])[platformName()])}
		for _, c := range s.candidates {
			if c.ok && s.path == "" {
				s.path = c.path
			}
		}
		out[name] = s
	}
	return out
}

// ── the machine ──────────────────────────────────────────────────────────────

func cardsHere() []any {
	out := []any{}
	for _, x := range kit.GpusNow() {
		g := ObjectOf(x)
		out = append(out, map[string]any{"name": g["name"], "kind": "cuda", "total": g["total"], "used": g["used"]})
	}
	if len(out) == 0 && runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		out = append(out, map[string]any{"name": "Apple GPU", "kind": "metal"})
	}
	return out
}

// storeSeen: where this machine sees a store: one of its paths, holding the files it is checked by; errand's copy; or not
func storeSeen(store map[string]any) map[string]any {
	if len(store) == 0 {
		return map[string]any{"seen": "none"}
	}
	same := func(root string) bool {
		if !kit.IsDir(root) {
			return false
		}
		for _, x := range caches.ListOf(store["check"]) {
			pair := caches.ListOf(x)
			if len(pair) != 2 {
				return false
			}
			st, err := os.Stat(filepath.Join(root, filepath.FromSlash(kit.Text(pair[0]))))
			if err != nil || float64(st.Size()) != kit.Num(pair[1], -1) {
				return false
			}
		}
		return true
	}
	for i, p := range kit.Strs(store["paths"]) {
		if same(p) {
			seen := "shared"
			if i == 0 && store["original"] == true {
				seen = "original"
			}
			return map[string]any{"seen": seen, "path": p}
		}
	}
	if c := kit.Text(store["copy"]); c != "" && filepath.IsLocal(c) {
		if root := filepath.Join(kit.LocalCache(), filepath.FromSlash(c)); same(root) {
			return map[string]any{"seen": "copy", "path": root}
		}
	}
	return map[string]any{"seen": "none", "size": store["size"], "files": store["files"]}
}

// ── a service, here ──────────────────────────────────────────────────────────

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}

var bareField = regexp.MustCompile(`^\{port(:[a-z][a-z0-9_]*)?\}$`)

// typedWords: words as a person types them -- quoted for a shell, `{port}` as it is, paths in full ( a `~` typed here
// would be this machine's home, not the one over there )
func typedWords(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		if out[i] = w; !bareField.MatchString(w) {
			out[i] = quoteAll([]string{w})
		}
	}
	return strings.Join(out, " ")
}

// typedProgram: a program, as the first word of a line typed on its machine. On Windows, bare when it can be ( cmd.exe and
// PowerShell both read it ), else as PowerShell runs a quoted word
func typedProgram(path string) string {
	if layer.OnWindows {
		if strings.ContainsAny(path, " '`$&;(){},") {
			return "& '" + strings.ReplaceAll(path, "'", "''") + "'"
		}
		return path
	}
	return typedWords([]string{path})
}

func short7(rev string) string { return rev[:min(len(rev), 7)] }

func repoName(from string) string {
	return strings.TrimSuffix(path.Base(strings.TrimRight(from, "/")), ".git")
}

// madeWith: what a page was made with, in words

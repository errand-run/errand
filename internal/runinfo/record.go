// A run's record ( the guide's "Results and records" ): one directory per run, plain files, nothing to query but the
// tree.
//
//	<outputs>/runs/{path}/{when}-{env}@{host}[-{params}]/
//	    launch.json    what the run is, written as it starts
//	    result.json    what ran, where, how it ended -- written last
//	    output.txt     everything it printed, both streams as they came
//	    stdout.txt, stderr.txt   each stream, as written to it
//	    steps.jsonl    its journal ( ADR 0032 )
//	<outputs>/runs/{path}/latest         -> the newest run
//	<outputs>/runs/{path}/summary.json   one row per run, and the range of every number across them

package runinfo

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/kit"
)

const (
	ResultName  = "result.json"
	LaunchName  = "launch.json"
	SummaryName = "summary.json"
	stampLayout = "2006-01-02_15h04m05"
	nameWidth   = 60 // of the parameters in a run's name: past it, cut, with a short hash of all of them
)

// Record is what `launch.json` says, and `result.json` once the run ended.
type Record struct {
	V       int            `json:"v"`
	Name    string         `json:"name"`
	File    string         `json:"file,omitempty"` // relative to the root when it is under it
	Argv    []string       `json:"argv"`
	Date    string         `json:"date"`
	Started float64        `json:"started"`
	Env     string         `json:"env"`
	Place   string         `json:"place"` // environment and machine, as in the directory's name
	Host    string         `json:"host"`
	Project string         `json:"project"`
	Commit  *string        `json:"commit"`
	Dirty   bool           `json:"dirty"`
	Errand  string         `json:"errand"`
	Params  map[string]any `json:"params"`
	Tags    map[string]any `json:"tags"`
	Label   string         `json:"label,omitempty"` // what it is called
	// once it ended
	Status   string         `json:"status,omitempty"` // PASS | FAIL | SKIP
	Error    *string        `json:"error"`
	Exit     *int           `json:"exit,omitempty"`
	Duration *float64       `json:"duration_s,omitempty"` // the work, not the waiting
	RAM      *float64       `json:"ram_mb,omitempty"`     // peak memory
	Results  map[string]any `json:"results"`
	Output   *string        `json:"output_file"`
	// what its tool reported of each entry it ran, when it leaves a report ( report.go )
	Entries []entryResult `json:"entries,omitempty"`
	// what its code asked for and it did not have, and the runs made after it for that ( agent/rerun.go )
	Asked []Ask       `json:"asked,omitempty"`
	Again []madeAgain `json:"again,omitempty"`
}

func ReadRecord(path string) (Record, bool) {
	var r Record
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &r) != nil {
		return r, false
	}
	return r, true
}

// NameOf: a word made safe for a directory's name
func NameOf(s string) string {
	var b strings.Builder
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', strings.ContainsRune("-_.=,+@", c), c > 127:
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_.")
}

// paramsName: what was asked for, in words, sorted -- cut past a readable width, with a short hash of all of it
func paramsName(params map[string]any) string {
	if len(params) == 0 {
		return ""
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + fmt.Sprint(params[k])
	}
	whole := strings.Join(parts, ",")
	name := NameOf(whole)
	if r := []rune(name); len(r) > nameWidth || name != whole {
		sum := sha1.Sum([]byte(whole))
		if len(r) > nameWidth {
			name = string(r[:nameWidth])
		}
		if name != whole {
			name += "~" + hex.EncodeToString(sum[:])[:6]
		}
	}
	return name
}

// RunLeaf: a run's directory -- `runs/{path}/{when}-{place}[-{params}]`
func RunLeaf(outputs, path string, when time.Time, place string, params map[string]any) string {
	return runLeaf2(filepath.Join(outputs, "runs"), path, when, place, params)
}

func runLeaf2(tree, path string, when time.Time, place string, params map[string]any) string {
	name := when.Format(stampLayout) + "-" + NameOf(place)
	if p := paramsName(params); p != "" {
		name += "-" + p
	}
	parts := []string{tree}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part = NameOf(part); part != "" && part != ".." {
			parts = append(parts, part)
		}
	}
	return filepath.Join(append(parts, name)...)
}

// OutLeaf: where `--out` puts a run: under another tree, or as a pattern from the root
func OutLeaf(out, root, path string, when time.Time, env, place string, params map[string]any) string {
	under := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(root, p)
	}
	if !strings.Contains(out, "{") {
		return runLeaf2(under(out), path, when, place, params)
	}
	name := path
	if at := strings.LastIndex(path, "/"); at >= 0 {
		name = path[at+1:]
	}
	tag := func(kept map[string]any) string {
		if t := paramsName(kept); t != "" {
			return t
		}
		return "-"
	}
	made := outField.ReplaceAllStringFunc(out, func(f string) string {
		what, spec, _ := strings.Cut(f[1:len(f)-1], ":")
		switch what {
		case "path":
			return path
		case "name":
			return NameOf(name)
		case "when":
			return when.Format(stampLayout)
		case "place":
			return NameOf(place)
		case "env":
			return NameOf(env)
		case "params":
			if spec == "" {
				return tag(params)
			}
			// those that are named, in that order; the others count too: a short hash of all of them goes on the end
			var parts []string
			kept := 0
			for _, k := range strings.Split(spec, ",") {
				if v, ok := params[strings.TrimSpace(k)]; ok {
					parts = append(parts, NameOf(strings.TrimSpace(k))+"="+NameOf(fmt.Sprint(v)))
					kept++
				}
			}
			text := strings.Join(parts, ",")
			if kept < len(params) {
				sum := sha256.Sum256([]byte(fmt.Sprint(params)))
				if text != "" {
					text += "~"
				}
				text += hex.EncodeToString(sum[:])[:6]
			}
			if text == "" {
				return "-"
			}
			return text
		}
		return f
	})
	return under(made)
}

var outField = regexp.MustCompile(`\{(?:path|name|when|place|env|params)(?::[^}]*)?\}`)

// Provenance: the short HEAD of the tree at `root` ( nil outside a git tree ), and whether it had uncommitted changes
func Provenance(root string) (*string, bool) {
	git := func(args ...string) (string, bool) {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err == nil
	}
	head, ok := git("rev-parse", "--short", "HEAD")
	if !ok || head == "" {
		return nil, false
	}
	changes, _ := git("status", "--porcelain")
	return &head, changes != ""
}

// LastLines: the end of what a run wrote on its standard error, for the reason of its failure
func LastLines(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > 8192 {
		data = data[len(data)-8192:]
	}
	lines := strings.Split(strings.TrimRight(kit.AsText(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// Ended: how a run ended, from its exit code -- 0 passes, 77 is a skip ( as in automake and meson ), anything else
// fails, and says the last of its stderr
func (r *Record) Ended(code int, stderr string) {
	r.Exit = &code
	why := ""
	switch {
	case code == 0:
		r.Status = "PASS"
	case code == 77:
		r.Status, why = "SKIP", "exit 77"
	case code < 0:
		r.Status, why = "FAIL", "signal "+signalName(-code)
	default:
		r.Status, why = "FAIL", fmt.Sprintf("exit %d", code)
	}
	if r.Status == "FAIL" {
		if tail := LastLines(stderr, 6); tail != "" {
			why += "\n" + tail
		}
	}
	if why != "" {
		r.Error = &why
	}
}

func signalName(n int) string {
	names := map[int]string{1: "SIGHUP", 2: "SIGINT", 3: "SIGQUIT", 4: "SIGILL", 6: "SIGABRT", 7: "SIGBUS", 8: "SIGFPE", 9: "SIGKILL",
		11: "SIGSEGV", 13: "SIGPIPE", 15: "SIGTERM"}
	if name, ok := names[n]; ok {
		return name
	}
	return fmt.Sprintf("%d", n)
}

// Summarize: `summary.json` and `latest` beside the runs of one thing, from the results that are there. Recomputed
// after every run by reading them, so it is always right: delete it, or half the runs, and the next run writes it
// again.
func Summarize(dir string) {
	listed, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type row map[string]any
	entries := map[string]row{}
	ranges := map[string][2]float64{}
	counts := map[string]int{}
	newest, newestAt := "", 0.0
	note := func(key string, v float64) {
		if r, ok := ranges[key]; ok {
			ranges[key] = [2]float64{min(r[0], v), max(r[1], v)}
		} else {
			ranges[key] = [2]float64{v, v}
		}
	}
	for _, e := range listed {
		if !e.IsDir() {
			continue
		}
		r, ok := ReadRecord(filepath.Join(dir, e.Name(), ResultName))
		if !ok {
			if l, ok := ReadRecord(filepath.Join(dir, e.Name(), LaunchName)); ok && l.Started > newestAt {
				newest, newestAt = e.Name(), l.Started
			}
			continue
		}
		if r.Started > newestAt {
			newest, newestAt = e.Name(), r.Started
		}
		counts[r.Status]++
		one := row{"status": r.Status}
		if r.Duration != nil {
			one["duration_s"] = *r.Duration
			note("duration_s", *r.Duration)
		}
		for k, v := range r.Results { // numbers are summarized; words are kept in the result, and left out here
			if f, ok := v.(float64); ok {
				one[k] = f
				note(k, f)
			}
		}
		entries[e.Name()] = one
	}
	if newest != "" { // ( on a file system without links, it is simply missing )
		link := filepath.Join(dir, "latest")
		if now, _ := os.Readlink(link); now != newest {
			_ = os.Remove(link)
			_ = os.Symlink(newest, link)
		}
	}
	if len(entries) == 0 {
		_ = os.Remove(filepath.Join(dir, SummaryName))
		return
	}
	out := map[string]any{"v": 1, "runs": len(entries), "passed": counts["PASS"], "failed": counts["FAIL"],
		"skipped": counts["SKIP"], "entries": entries}
	for k, r := range ranges {
		if r[0] == r[1] {
			out[k] = r[0] // a single value says a value, a spread a pair
		} else {
			out[k] = []float64{r[0], r[1]}
		}
	}
	_ = kit.WriteRecord(filepath.Join(dir, SummaryName), out)
}

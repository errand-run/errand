// What a tool reports of the entries it ran, read into the run's record ( ADR 0011, ADR 0036 ): a status, a duration
// and a word per entry, their counts, and the names of those that failed. The agent that holds a run reads it when
// the run ends, from the file the runner said its tool leaves in the run's directory. A JUnit XML report is what
// pytest, ctest, cargo-nextest and many others write; a tool with no report is told by its exit code alone.

package runinfo

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/errand-run/errand/internal/project"
)

// entryResult is one entry, as its tool reported it.
type entryResult struct {
	Name    string         `json:"name"`
	Status  string         `json:"status"` // PASS | FAIL | SKIP
	Seconds float64        `json:"seconds"`
	Message string         `json:"message,omitempty"`
	Results map[string]any `json:"results,omitempty"` // the numbers it kept, when its tool says them entry by entry
}

// heldEntries: the lines errand's own entries' tool writes as it goes ( `errand/held.py` ). One entry's numbers are its
// run's; several entries' are named by theirs.
func heldEntries(path string, results map[string]any) []entryResult {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []entryResult
	for _, line := range strings.Split(string(data), "\n") {
		var e entryResult
		if strings.TrimSpace(line) != "" && json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	for _, e := range out {
		for k, v := range e.Results {
			if len(out) > 1 {
				k = e.Name + "." + k
			}
			results[k] = v
		}
	}
	return out
}

var cargoLine = regexp.MustCompile(`^test (\S+)(?: - should panic)? \.\.\. (ok|FAILED|ignored)(?:, (.*))?`)

// cargoEntries: what `cargo test` prints, a line per test
func cargoEntries(path string) []entryResult {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []entryResult
	for _, line := range strings.Split(string(data), "\n") {
		m := cargoLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		e := entryResult{Name: m[1], Status: "PASS", Message: m[3]}
		switch m[2] {
		case "FAILED":
			e.Status = "FAIL"
		case "ignored":
			e.Status = "SKIP"
		}
		out = append(out, e)
	}
	return out
}

// catch2Entries: a Catch2 XML report, and the means of its benchmarks, which a terminal shows and forgets
func catch2Entries(path string, results map[string]any) []entryResult {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	type overall struct {
		Success string  `xml:"success,attr"`
		Skips   int     `xml:"skips,attr"`
		Seconds float64 `xml:"durationInSeconds,attr"`
	}
	type expression struct {
		Success  string `xml:"success,attr"`
		Original string `xml:"Original"`
		Expanded string `xml:"Expanded"`
	}
	var out []entryResult
	var now *entryResult
	assertions := 0
	dec := xml.NewDecoder(f)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch el := tok.(type) {
		case xml.StartElement:
			attr := func(name string) string {
				for _, a := range el.Attr {
					if a.Name.Local == name {
						return a.Value
					}
				}
				return ""
			}
			switch el.Name.Local {
			case "TestCase":
				out = append(out, entryResult{Name: attr("name"), Status: "PASS"})
				now = &out[len(out)-1]
			case "Expression":
				var x expression
				if dec.DecodeElement(&x, &el) == nil && x.Success == "false" && now != nil && now.Message == "" {
					now.Message = strings.TrimSpace(x.Original) + " -- expanded: " + strings.TrimSpace(x.Expanded)
				}
			case "Skip":
				var w string
				if dec.DecodeElement(&w, &el) == nil && now != nil {
					now.Message = strings.TrimSpace(w)
				}
			case "Exception", "FatalErrorCondition", "Failure":
				var w string
				if dec.DecodeElement(&w, &el) == nil && now != nil && now.Message == "" {
					now.Message = strings.TrimSpace(w)
				}
			case "OverallResult":
				var o overall
				if dec.DecodeElement(&o, &el) == nil && now != nil {
					now.Seconds = o.Seconds
					if o.Success == "false" {
						now.Status = "FAIL"
					} else if o.Skips > 0 {
						now.Status = "SKIP"
					}
				}
			case "OverallResults":
				if now == nil || attr("successes") != "" {
					var ok, bad int
					_, _ = fmt.Sscan(attr("successes"), &ok)
					_, _ = fmt.Sscan(attr("failures"), &bad)
					assertions = max(assertions, ok+bad)
				}
			case "BenchmarkResults":
				name := attr("name")
				var b struct {
					Mean struct {
						Value string `xml:"value,attr"`
					} `xml:"mean"`
				}
				if dec.DecodeElement(&b, &el) == nil && b.Mean.Value != "" {
					var ns float64
					if _, err := fmt.Sscan(b.Mean.Value, &ns); err == nil {
						results[name+"_ns"] = ns
					}
				}
			}
		case xml.EndElement:
			if el.Name.Local == "TestCase" {
				now = nil
			}
		}
	}
	if assertions > 0 {
		results["assertions"] = assertions
	}
	return out
}

type junitWord struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

func (w *junitWord) said() string {
	if w == nil {
		return ""
	}
	text := strings.TrimSpace(w.Message)
	if text == "" {
		text = strings.TrimSpace(w.Text)
	}
	if len(text) > 500 {
		text = text[:500]
	}
	return text
}

type junitCase struct {
	Name      string     `xml:"name,attr"`
	Classname string     `xml:"classname,attr"`
	Time      float64    `xml:"time,attr"`
	Failure   *junitWord `xml:"failure"`
	Error     *junitWord `xml:"error"`
	Skipped   *junitWord `xml:"skipped"`
}

// junitEntries: the cases of a JUnit report, named as entries of `file` are -- what follows the file, `Class::test`
func junitEntries(path, file string) []entryResult {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	stem := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	var out []entryResult
	dec := xml.NewDecoder(f)
	for {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "testcase" {
			continue
		}
		var c junitCase
		if dec.DecodeElement(&c, &start) != nil {
			return out
		}
		// `tests.test_a.TestK`: what comes after the file's own name is the class, if any
		parts := strings.Split(c.Classname, ".")
		at := -1
		for i, p := range parts {
			if p == stem {
				at = i
			}
		}
		name := c.Name
		switch {
		case at >= 0 && at+1 < len(parts):
			name = strings.Join(parts[at+1:], "::") + "::" + c.Name
		case at < 0 && c.Classname != "":
			name = c.Classname + "::" + c.Name
		}
		e := entryResult{Name: name, Status: "PASS", Seconds: c.Time}
		switch {
		case c.Failure != nil:
			e.Status, e.Message = "FAIL", c.Failure.said()
		case c.Error != nil:
			e.Status, e.Message = "FAIL", c.Error.said()
		case c.Skipped != nil:
			e.Status, e.Message = "SKIP", c.Skipped.said()
		}
		out = append(out, e)
	}
}

// Reported: the record of a run that ended with `code`, said again from what its tool reported
func (r *Record) Reported(rep *project.Report, leaf string, code int) {
	if rep == nil {
		return
	}
	if rep.Nothing != nil && code == *rep.Nothing { // an empty run looks exactly like a passing one
		why := "the tool ran nothing: no entry was selected"
		r.Status, r.Error = "FAIL", &why
		return
	}
	if r.Results == nil {
		r.Results = map[string]any{}
	}
	var entries []entryResult
	switch rep.Form {
	case "junit":
		entries = junitEntries(filepath.Join(leaf, rep.File), r.File)
	case "entries":
		entries = heldEntries(filepath.Join(leaf, rep.File), r.Results)
	case "cargo":
		entries = cargoEntries(filepath.Join(leaf, StreamNames["out"]))
	case "catch2":
		entries = catch2Entries(filepath.Join(leaf, rep.File), r.Results)
	}
	if len(entries) == 0 {
		return
	}
	r.Entries = entries
	counts := map[string]int{}
	seconds := 0.0
	var failed []string
	first, skipped := "", ""
	for _, e := range entries {
		counts[e.Status]++
		seconds += e.Seconds
		switch e.Status {
		case "FAIL":
			failed = append(failed, e.Name)
			if first == "" {
				first = e.Message
			}
		case "SKIP":
			if skipped == "" {
				skipped = e.Message
			}
		}
	}
	r.Results["passed"], r.Results["failed"], r.Results["skipped"] = counts["PASS"], counts["FAIL"], counts["SKIP"]
	if code < 0 { // ended by a signal: a crash the report does not say
		return
	}
	switch {
	case len(failed) > 0:
		shown := failed
		more := ""
		if len(shown) > 10 {
			shown, more = shown[:10], fmt.Sprintf(" and %d more", len(failed)-10)
		}
		why := fmt.Sprintf("%d failed: %s%s", len(failed), strings.Join(shown, ", "), more)
		if first != "" {
			why += " -- " + first
		}
		r.Status, r.Error = "FAIL", &why
	case code != 0 && code != 77: // the tool ended badly and its report names no failure: its exit code stands
	case counts["SKIP"] > 0 && counts["PASS"] == 0:
		if skipped == "" {
			skipped = "skipped"
		}
		r.Status, r.Error = "SKIP", &skipped
	default:
		r.Status, r.Error = "PASS", nil
	}
}

// What a run says, for whoever would rather not write the lines itself ( ADR 0032 ): from a shell script, Fortran, a
// Makefile.
//
//	"$ERRAND" say step "iteration 41" --done 0.41 residual=3e-7:log
//	"$ERRAND" say view convergence.html --title "Convergence"
//	"$ERRAND" say result seconds=1.52 primes=9592
//
// Each line in one write. Outside a run: nothing written, and nothing failed.

package agent

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/kit"
)

func isName(s string) bool {
	for i, c := range s {
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || (i > 0 && c >= '0' && c <= '9')) {
			return false
		}
	}
	return s != ""
}

func SayMain(args []string) int {
	bad := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "errand say: "+format+"\n", a...)
		return 2
	}
	if len(args) == 0 {
		return bad("step TEXT [--done F] [NAME=NUMBER[:log|:lin]…] | view PATH [--title T] [--last FILE…] | result NAME=VALUE…")
	}
	what, rest := args[0], args[1:]
	now := float64(time.Now().UnixNano()) / 1e9
	option := func(name string) ([]string, error) { // every value given for it, taken out of what is left
		var got, left []string
		for i := 0; i < len(rest); i++ {
			if rest[i] == name {
				if i+1 >= len(rest) {
					return nil, fmt.Errorf("%s: a value is missing", name)
				}
				got = append(got, rest[i+1])
				i++
			} else {
				left = append(left, rest[i])
			}
		}
		rest = left
		return got, nil
	}
	switch what {
	case "step":
		done, err := option("--done")
		if err != nil {
			return bad("%v", err)
		}
		line := map[string]any{"t": now}
		if len(rest) > 0 {
			if name, _, pair := strings.Cut(rest[0], "="); !pair || !isName(name) {
				line["text"], rest = rest[0], rest[1:]
			}
		}
		if len(done) > 0 {
			f, err := strconv.ParseFloat(done[len(done)-1], 64)
			if err != nil {
				return bad("--done %s: from 0 to 1", done[len(done)-1])
			}
			line["done"] = f
		}
		scales := map[string]string{}
		for _, pair := range rest {
			name, value, ok := strings.Cut(pair, "=")
			value, scale, scaled := strings.Cut(value, ":")
			f, err := strconv.ParseFloat(value, 64)
			if !ok || !isName(name) || err != nil || (scaled && scale != "log" && scale != "lin") {
				return bad("%s: a curve is NAME=NUMBER, NAME=NUMBER:log or NAME=NUMBER:lin", pair)
			}
			if !math.IsInf(f, 0) && !math.IsNaN(f) { // ( JSON has neither: the whole line would be lost )
				line[name] = f
			}
			if scaled {
				scales[name] = scale
			}
		}
		if len(scales) > 0 {
			line["scales"] = scales
		}
		if journal := os.Getenv("ERRAND_STEPS"); journal != "" {
			_ = kit.AppendLine(journal, line)
		}
	case "view":
		title, err := option("--title")
		last, err2 := option("--last")
		if err != nil || err2 != nil || len(rest) != 1 {
			return bad("view: one path, from the run's directory")
		}
		line := map[string]any{"t": now, "view": rest[0]}
		if len(title) > 0 {
			line["title"] = title[len(title)-1]
		}
		if len(last) > 0 {
			line["last"] = last
		}
		if journal := os.Getenv("ERRAND_STEPS"); journal != "" {
			_ = kit.AppendLine(journal, line)
		}
	case "result":
		if len(rest) == 0 {
			return bad("result NAME=VALUE …")
		}
		got := map[string]any{}
		for _, pair := range rest {
			name, value, ok := strings.Cut(pair, "=")
			if !ok || !isName(name) {
				return bad("%s: a result is NAME=VALUE", pair)
			}
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				got[name] = f // a number is said as a number, anything else as a word
			} else if value == "true" || value == "false" {
				got[name] = value == "true"
			} else {
				got[name] = value
			}
		}
		if results := os.Getenv("ERRAND_RESULTS"); results != "" {
			_ = kit.AppendLine(results, got)
		}
	default:
		return bad("%s: not something a run says ( step, view, result )", what)
	}
	return 0
}

package system

import (
	"regexp"
	"strconv"
	"strings"
)

// Version is which errand this is, said when it is built ( packaging/backend.py )
var Version = "0+source"

// Built: when it was built, in nanoseconds, said when it is built ( packaging/backend.py ); "": a `go build` by hand
var Built = ""

// BuiltAt: when it was built; 0 when that was not said -- before any build that says it
func BuiltAt() int64 { n, _ := strconv.ParseInt(strings.TrimSpace(Built), 10, 64); return n }

var versionParts = regexp.MustCompile(`^([0-9.]*[0-9])(?:(a|b|rc)([0-9]+))?(?:\+[a-z]*\.?([0-9]*))?`)

// VersionBefore: whether version `a` is an earlier one than `b` ( lib/version.py: 2026.11.25.1, its candidates 2026.11.25.1rc1,
// what comes after it 2026.11.25.1+dev.3.g3e4580c )
func VersionBefore(a, b string) bool {
	x, y := versionKey(a), versionKey(b)
	for i := 0; i < len(x) || i < len(y); i++ {
		var p, q int
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			return p < q
		}
	}
	return false
}

// versionKey: six numbers for the release, then which candidate ( none: after them all ), then how many commits after
func versionKey(v string) []int {
	key := make([]int, 9)
	m := versionParts.FindStringSubmatch(v)
	if m == nil {
		return key
	}
	for i, n := range strings.Split(m[1], ".") {
		if i < 6 {
			key[i], _ = strconv.Atoi(n)
		}
	}
	key[6] = map[string]int{"a": 1, "b": 2, "rc": 3, "": 4}[m[2]]
	key[7], _ = strconv.Atoi(m[3])
	key[8], _ = strconv.Atoi(m[4])
	return key
}

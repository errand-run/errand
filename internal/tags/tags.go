// Package tags chooses among environments ( the guide's "Choosing among environments"; the reference's "Tag
// expressions" ). A tag says what an environment is; an expression, or a tag's flag, selects every environment that
// fits. Saying nothing means every value: an environment that does not mention `fp` covers every precision.
package tags

import (
	"fmt"
	"sort"
	"strings"
)

// said: a value as text, with the obvious courtesies -- `True`, `yes` and `1` are one word, the int and the string
// are the same
func said(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprint(int64(x))
		}
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	switch strings.ToLower(s) {
	case "true", "yes":
		return "true"
	case "false", "no":
		return "false"
	}
	return s
}

func same(declared any, asked string) bool {
	d, a := said(declared), said(asked)
	if _, isBool := declared.(bool); isBool {
		switch a {
		case "1":
			a = "true"
		case "0":
			a = "false"
		}
	}
	return d == a
}

// Alternatives: the values a declared tag covers -- `"32|64"` is either
func Alternatives(declared any) []any {
	s, ok := declared.(string)
	if !ok || !strings.Contains(s, "|") {
		return []any{declared}
	}
	var out []any
	for _, part := range strings.Split(s, "|") {
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

// Covers: whether an environment that declares `declared` for a tag can have the value `asked`
func Covers(declared any, asked string) bool {
	for _, alt := range Alternatives(declared) {
		if same(alt, asked) {
			return true
		}
	}
	return false
}

// Matches: an expression against tags. `cuda`: present; `!cuda`: not; `fp=64`: present with that value; `a & b`;
// `a | b`. No parentheses: `!` binds tighter than `&`, which binds tighter than `|`. Empty: everything matches.
func Matches(expr string, tags map[string]any) (bool, error) {
	for _, either := range strings.Split(expr, "|") {
		all := true
		for _, term := range strings.Split(either, "&") {
			term = strings.TrimSpace(term)
			if term == "" {
				continue // a stray `&` is ignored; an empty alternative matches everything
			}
			not := strings.HasPrefix(term, "!")
			term = strings.TrimSpace(strings.TrimPrefix(term, "!"))
			name, value, valued := strings.Cut(term, "=")
			name, value = strings.TrimSpace(name), strings.TrimSpace(value)
			if name == "" {
				return false, fmt.Errorf("%q in %q names no tag", strings.TrimSpace(term), expr)
			}
			declared, present := tags[name]
			ok := present
			if present && valued {
				ok = Covers(declared, value)
			}
			if ok == not {
				all = false
			}
		}
		if all {
			return true, nil
		}
	}
	return false, nil
}

// Choice is one environment as selected: its name, and its tags with the values chosen.
type Choice struct {
	Env  string
	Tags map[string]any
}

// Asked is what a command says about where it runs.
type Asked struct {
	Envs    []string            // by name ( `--env` )
	Flags   map[string][]string // a tag's flag, and the values asked for it ( several: a grid )
	Expr    string              // `-t`
	Default string              // the environment when nothing is said
}

// Select: the environments that fit, each once per combination of the tag values asked -- in the order they are
// named, else by name. An environment named that cannot have a value asked is an error, not an override.
func Select(declared map[string]map[string]any, a Asked) ([]Choice, error) {
	named := len(a.Envs) > 0
	names := a.Envs
	if !named {
		if len(a.Flags) == 0 && a.Expr == "" {
			names = []string{a.Default}
		} else {
			for n := range declared {
				names = append(names, n)
			}
			sort.Strings(names)
		}
	}
	flags := make([]string, 0, len(a.Flags))
	for f := range a.Flags {
		flags = append(flags, f)
	}
	sort.Strings(flags)
	var out []Choice
	for _, name := range names {
		tags, ok := declared[name]
		if !ok {
			if name != "local" {
				known := make([]string, 0, len(declared))
				for n := range declared {
					known = append(known, n)
				}
				sort.Strings(known)
				return nil, fmt.Errorf("no environment %q: the project declares %s", name, strings.Join(known, ", "))
			}
			tags = map[string]any{} // `local`, declared by nobody: this machine as it is
		}
		if a.Expr != "" {
			fits, err := Matches(a.Expr, tags)
			if err != nil {
				return nil, err
			}
			if !fits {
				if named {
					return nil, fmt.Errorf("the environment %s does not fit %q", name, a.Expr)
				}
				continue
			}
		}
		combos := []map[string]any{{}}
		for _, f := range flags {
			var next []map[string]any
			for _, value := range a.Flags[f] {
				if d, mentioned := tags[f]; mentioned && !Covers(d, value) {
					continue
				}
				for _, c := range combos {
					one := map[string]any{f: value}
					for k, v := range c {
						one[k] = v
					}
					next = append(next, one)
				}
			}
			if len(next) == 0 && named {
				return nil, fmt.Errorf("the environment %s is %s=%v: it cannot be %s=%s", name, f, tags[f], f, strings.Join(a.Flags[f], ", "))
			}
			combos = next
		}
		for _, c := range combos {
			chosen := map[string]any{}
			for k, v := range tags {
				chosen[k] = v
			}
			for k, v := range c {
				chosen[k] = v
			}
			out = append(out, Choice{name, chosen})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no environment fits what was asked")
	}
	return out, nil
}

package runinfo

import (
	"regexp"
	"strings"

	"github.com/errand-run/errand/internal/kit"
)

// ToolWords: the words that start a tool named by its usual name -- the environment's own name for it, or its name
func ToolWords(name string, tools map[string]any) []string {
	if given, ok := tools[name]; ok {
		switch v := given.(type) {
		case string:
			if words := ShellWords(v); len(words) > 0 {
				return words
			}
		case []any:
			if len(v) > 0 {
				return kit.Strs(v)
			}
		}
	}
	return []string{name}
}

// ShellWords: a line cut in words as a shell cuts it, quotes and backslashes read, nothing else of a shell's
func ShellWords(line string) []string {
	var out []string
	var word strings.Builder
	in, quote := false, byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote == 0 && c == '\\' && i+1 < len(line), quote == '"' && c == '\\' && i+1 < len(line) && strings.IndexByte("\"\\$`", line[i+1]) >= 0:
			i++
			word.WriteByte(line[i])
			in = true
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && (c == '"' || c == '\''):
			quote, in = c, true
		case quote == 0 && (c == ' ' || c == '\t' || c == '\n'):
			if in {
				out = append(out, word.String())
				word.Reset()
				in = false
			}
		default:
			word.WriteByte(c)
			in = true
		}
	}
	if in {
		out = append(out, word.String())
	}
	return out
}

var OneName = regexp.MustCompile(`\{([a-z][a-z0-9_+-]*)\}`)

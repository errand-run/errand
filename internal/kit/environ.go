package kit

import (
	"strings"
)

func EnvOf(env []string, name string) (string, bool) {
	out, found := "", false
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			out, found = v, true
		}
	}
	return out, found
}

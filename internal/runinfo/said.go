// What a run is told and says, as its agent reads and writes it ( ADR 0032 ).

package runinfo

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// TagsSaid: the tags of an environment, as a run reads them ( `ERRAND_TAGS`: `name=value,flag` )
func TagsSaid(tags map[string]any) string {
	names := make([]string, 0, len(tags))
	for k := range tags {
		names = append(names, k)
	}
	sort.Strings(names)
	var parts []string
	for _, k := range names {
		switch v := tags[k].(type) {
		case bool:
			if v {
				parts = append(parts, k)
			}
		default:
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return strings.Join(parts, ",")
}

// MergeResults: the lines a run appended to its results' file, merged in order
func MergeResults(path string) map[string]any {
	out := map[string]any{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	lines := bufio.NewScanner(f)
	lines.Buffer(make([]byte, 1<<20), 1<<24)
	for lines.Scan() {
		one := map[string]any{}
		if json.Unmarshal(lines.Bytes(), &one) == nil { // ( a reader ignores a line it cannot read )
			for k, v := range one {
				out[k] = v
			}
		}
	}
	return out
}

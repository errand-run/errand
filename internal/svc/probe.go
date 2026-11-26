// A service's probes, as data ( ADR 0040 ): a line of shell, or a path of its own and what is looked at in the JSON it answers.

package svc

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/errand-run/errand/internal/kit"
)

// HTTPProbe is a question asked of the server: GET `Path`, then `Field` of what it answers ( `a.b`; none: all of it ) -- yes when it
// is there, and neither false, 0 nor empty.
type HTTPProbe struct {
	Path, Field, Port string
}

var probeField = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)

// ParseHTTPProbe: `PATH[:FIELD]`
func ParseHTTPProbe(text string) (map[string]any, error) {
	path, field, _ := strings.Cut(text, ":")
	p := map[string]any{"http": path}
	if field != "" {
		p["field"] = field
	}
	_, _, err := ProbeOf(p)
	return p, err
}

// ProbeOf: a probe, as it is written -> the line of shell, or the question
func ProbeOf(v any) (string, *HTTPProbe, error) {
	switch p := v.(type) {
	case nil:
		return "", nil, nil
	case string:
		return p, nil, nil
	case map[string]any:
		h := &HTTPProbe{kit.Text(p["http"]), kit.Text(p["field"]), kit.Text(p["port"])}
		if !strings.HasPrefix(h.Path, "/") {
			return "", nil, fmt.Errorf("a probe: a line of shell, or a path of the server's ( \"/…\" ), got %v", v)
		}
		if h.Field != "" && !probeField.MatchString(h.Field) {
			return "", nil, fmt.Errorf("a probe's field: names, with dots ( got %q )", h.Field)
		}
		for k := range p {
			if k != "http" && k != "field" && k != "port" {
				return "", nil, fmt.Errorf("a probe: http, field, port ( got %q )", k)
			}
		}
		return "", h, nil
	}
	return "", nil, fmt.Errorf("a probe: a line of shell, or an HTTP path, got %v", v)
}

// In: what it looks at, in an answer
func (h HTTPProbe) In(answer any) any {
	if h.Field == "" {
		return answer
	}
	for _, name := range strings.Split(h.Field, ".") {
		m, ok := answer.(map[string]any)
		if !ok {
			return nil
		}
		answer = m[name]
	}
	return answer
}

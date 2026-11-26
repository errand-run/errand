package reach

import (
	"fmt"
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/system"
)

// RemoveInstance: an instance of errand removed over there, by its own program there ( errand instances remove NAME --here
// --yes ); nothing when it never went there -> what it said
func (h HostSpec) RemoveInstance(name string) (string, error) {
	base := `"${ERRAND_CACHE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/errand}"`
	ready := ""
	if h.Cache != "" {
		at, err := declaredPath(h.Cache)
		if err != nil {
			return "", err
		}
		ready, base = "d="+at+"; ", `"$d"`
	}
	script := ready + system.CacheShell(base) + `if [ -x "$c/bin/errand" ]; then ERRAND_INSTANCE=` + name + ` "$c/bin/errand" instances remove ` + layer.Quote(name) +
		` --here --yes; else echo "nothing of ` + name + ` there"; fi`
	cmd := h.command(append(h.argv(), throughShell(script)))
	out, err := cmd.CombinedOutput()
	said := strings.TrimSpace(kit.EndOf(string(out), 600))
	if err != nil {
		return said, fmt.Errorf("%s: %v", h.Host, err)
	}
	return said, nil
}

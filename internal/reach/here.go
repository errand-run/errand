package reach

import (
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/errand-run/errand/internal/system"
)

var (
	hereMu    sync.Mutex
	hereCache = map[string]bool{}
)

// IsHere: whether ssh, asked for this machine, would come back to the one it is asked on -- `Host lmo` / `HostName localhost`, in
// lmo's own ssh configuration. Such a machine is this one: nothing is reached by ssh.
func (h HostSpec) IsHere() bool {
	if h.VM != nil || h.Host == "" || os.Getenv("ERRAND_SSH") != "" { // ( an ssh of one's own: its names are its own )
		return false
	}
	key := h.Host + "\x00" + strings.Join(h.Options, "\x00")
	hereMu.Lock()
	defer hereMu.Unlock()
	if here, ok := hereCache[key]; ok {
		return here
	}
	out, err := exec.Command("ssh", append(append([]string{"-G"}, h.Options...), h.Host)...).Output()
	here := false
	if err == nil {
		var name, proxy string
		for _, line := range strings.Split(string(out), "\n") {
			k, v, _ := strings.Cut(strings.TrimSpace(line), " ")
			switch k {
			case "hostname":
				name = strings.ToLower(v)
			case "proxyjump", "proxycommand":
				proxy = v
			}
		}
		full := strings.ToLower(system.Hostname())
		here = proxy == "" && (name == "localhost" || name == "127.0.0.1" || name == "::1" || name == full ||
			name == strings.ToLower(system.ShortHost()) || (full != "" && strings.HasPrefix(full, name+".")))
	}
	hereCache[key] = here
	return here
}

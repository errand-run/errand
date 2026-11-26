package system

import (
	"os"
	"strings"
	"sync"
)

// Hostname: the name of this machine, the same from one network to the next: what errand keeps is under it -- its agents, its
// queue, its leases. Said once: a process that lasts keeps the one it began with.
var Hostname = sync.OnceValue(func() string {
	if name := ownName(); name != "" {
		return name
	}
	name, _ := os.Hostname()
	return name
})

// ShortHost: its first word
func ShortHost() string {
	if h := strings.SplitN(Hostname(), ".", 2)[0]; h != "" {
		return h
	}
	return "localhost"
}

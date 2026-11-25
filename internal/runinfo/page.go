// Where the page of this machine is ( `errand web` ), for whoever says its address: the page's program, and the agent that
// follows a run.

package runinfo

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/system"
	"github.com/errand-run/errand/internal/units"
)

func WebDir() string { return filepath.Join(kit.LocalCache(), "page") }

func PortOfUser() int { return 20000 + os.Getuid()%10000 }

type WebSaid struct {
	Port   int    `json:"port"`
	Static int    `json:"static"`
	Token  string `json:"token"`
	Pid    int    `json:"pid"`
	Build  string `json:"build"`
	Public int    `json:"public,omitempty"`        // the port the system keeps for the page
	PubSt  int    `json:"public_static,omitempty"` // and for the stores
}

func (s WebSaid) Url() string {
	p := s.Port
	if s.Public > 0 {
		p = s.Public
	}
	return fmt.Sprintf("http://127.0.0.1:%d/", p)
}

// WebNow: where the page of this machine is, if it is served
func WebNow() (WebSaid, bool) {
	var s WebSaid
	data, err := os.ReadFile(filepath.Join(WebDir(), "said.json"))
	if err != nil || json.Unmarshal(data, &s) != nil || s.Port == 0 {
		return s, false
	}
	f, err := os.OpenFile(filepath.Join(WebDir(), "lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return s, false
	}
	defer f.Close()
	if system.TryLock(f) == nil {
		_ = system.Unlock(f)
		return s, false // nobody holds it: what the file says is a leftover
	}
	return s, true
}

func RunIdent(leaf string) string {
	if leaf == "" {
		return ""
	}
	sum := sha1.Sum([]byte(strings.TrimRight(filepath.ToSlash(leaf), "/")))
	return hex.EncodeToString(sum[:])[:16]
}

// PageOf: the address of a run's page, when the page is served here or the system keeps its port; ""
func PageOf(leaf string) string {
	if s, ok := WebNow(); ok {
		return s.Url() + "r/" + RunIdent(leaf)
	}
	if p := units.KeptPort(units.WebLabel); p > 0 {
		return fmt.Sprintf("http://127.0.0.1:%d/r/%s", p, RunIdent(leaf))
	}
	return ""
}

package system

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// MachineID: what tells this machine from any other -- the identifier its system was given when it was installed
// ( Linux's `/etc/machine-id`, a Mac's IOPlatformUUID, Windows' MachineGuid ), with its name: nodes booted from one
// image may share the first, machines of two sites the second, seldom both. Not one errand would keep in its own
// directory: a cluster's nodes share a home, and so that directory. "": the system has none ( a container ).
var MachineID = sync.OnceValue(func() string {
	given := systemID()
	if given == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.ToLower(given) + "\x00" + strings.ToLower(Hostname())))
	return hex.EncodeToString(sum[:])[:16]
})

var (
	platformUUID = regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([^"]+)"`)
	machineGuid  = regexp.MustCompile(`MachineGuid\s+REG_SZ\s+(\S+)`)
)

func systemID() string {
	switch runtime.GOOS {
	case "darwin":
		out, _ := exec.Command("/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
		if m := platformUUID.FindSubmatch(out); m != nil {
			return string(m[1])
		}
	case "windows":
		out, _ := exec.Command("reg", "query", `HKLM\SOFTWARE\Microsoft\Cryptography`, "/v", "MachineGuid").Output()
		if m := machineGuid.FindSubmatch(out); m != nil {
			return string(m[1])
		}
	default:
		for _, f := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id", "/etc/hostid"} {
			if data, err := os.ReadFile(f); err == nil {
				if id := strings.TrimSpace(string(data)); id != "" && strings.Trim(id, "0") != "" { // ( all zeros: not set yet )
					return id
				}
			}
		}
	}
	return ""
}

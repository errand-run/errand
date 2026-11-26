package system

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// On a Mac `hostname` is what the network calls it ( DHCP ), unless one was set: `dock-25014` at a desk, another at home. The
// name of its own is the one in the Sharing settings, `<name>.local`.
func ownName() string {
	if data, err := os.ReadFile("/Library/Preferences/SystemConfiguration/preferences.plist"); err == nil {
		if name := localHostName(string(data)); name != "" {
			return name
		}
	}
	out, _ := exec.Command("/usr/sbin/scutil", "--get", "LocalHostName").Output() // ( that file may not be text )
	return strings.TrimSpace(string(out))
}

var localHostKey = regexp.MustCompile(`<key>LocalHostName</key>\s*<string>([^<]+)</string>`)

func localHostName(plist string) string {
	if m := localHostKey.FindStringSubmatch(plist); m != nil {
		return m[1]
	}
	return ""
}

package system

import (
	"os/exec"
	"strings"
	"testing"
)

func TestTheNameOfAMacIsItsOwn(t *testing.T) {
	plist := "<dict>\n<key>HostNames</key>\n<dict>\n\t<key>LocalHostName</key>\n\t<string>Some-Mac</string>\n</dict></dict>"
	if got := localHostName(plist); got != "Some-Mac" {
		t.Errorf("%q", got)
	}
	if got := localHostName("bplist00"); got != "" {
		t.Errorf("%q", got)
	}
	if out, err := exec.Command("/usr/sbin/scutil", "--get", "LocalHostName").Output(); err == nil {
		if want := strings.TrimSpace(string(out)); Hostname() != want || ShortHost() != strings.SplitN(want, ".", 2)[0] {
			t.Errorf("%q, not %q", Hostname(), want)
		}
	}
}

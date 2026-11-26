package system

import (
	"regexp"
	"runtime"
	"testing"
)

// A machine's id: 16 hex digits, the same each time; on a Mac or a Linux outside a container, there is one.
func TestMachineID(t *testing.T) {
	id := MachineID()
	if id == "" {
		if runtime.GOOS == "darwin" {
			t.Fatal("a Mac without IOPlatformUUID")
		}
		t.Skip("this system has no id ( a container )")
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(id) {
		t.Errorf("%q", id)
	}
	if systemID() == "" || MachineID() != id {
		t.Error("not the same twice")
	}
}

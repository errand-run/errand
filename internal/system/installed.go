package system

import (
	"os"
	"path/filepath"
)

// Typed says to a daemon, which runs from a copy of its own, which program was typed
const Typed = "ERRAND_TYPED"

// Installed: the directories where what errand is distributed with may be -- that of the program that was typed, and of
// this one
func Installed(getenv func(string) string) []string {
	var out []string
	if p := getenv(Typed); p != "" {
		out = append(out, filepath.Dir(p))
	}
	self, _ := os.Executable()
	if real, err := filepath.EvalSymlinks(self); err == nil {
		self = real
	}
	return append(out, filepath.Dir(self))
}

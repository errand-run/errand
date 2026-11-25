package draw

import (
	"os"
)

func IsTerminal(f *os.File) bool {
	_, err := getTermios(f)
	return err == nil
}

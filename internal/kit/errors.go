package kit

import (
	"fmt"
	"os"
)

type ValueError string // what Python raises as `ValueError`

func (e ValueError) Error() string { return string(e) }

// SyncError: why a sync is stopped. The functions here panic with one; who calls them recovers.
type SyncError string

func (e SyncError) Error() string { return string(e) }

// SaidError: what was recovered, in words
func SaidError(r any) string {
	switch e := r.(type) {
	case SyncError:
		return "SyncError: " + string(e)
	case ValueError:
		return "ValueError: " + string(e)
	case error:
		return "OSError: " + e.Error()
	}
	return fmt.Sprint(r)
}

func Lexists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

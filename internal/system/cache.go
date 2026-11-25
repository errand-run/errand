package system

import (
	"os"
	"path/filepath"
)

// Cache: errand's directory on this machine, for this account
func Cache() string {
	if c := os.Getenv("ERRAND_CACHE_DIR"); c != "" {
		return c
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "errand")
}

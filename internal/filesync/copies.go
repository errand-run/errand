// The copies errand makes of a result on other machines ( ADR 0028 ): a view's files, in errand's cache of the machine
// that runs its services. Each is written down, on this side, before the other machine is told anything; it is removed
// on demand -- by its row in the detail of a result, or with the result --, and one whose machine does not answer is
// asked again, every hour.

package filesync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
)

type CopyNote struct {
	ID      string         `json:"id"`
	Path    string         `json:"path"` // here
	Host    reach.HostSpec `json:"host"`
	Machine string         `json:"machine"`
	Vid     string         `json:"vid"`
	There   string         `json:"there,omitempty"`
	State   string         `json:"state"` // intended | there | removing
	At      float64        `json:"at"`
}

func copiesDir() string { return filepath.Join(kit.LocalCache(), "copied") }

func (c CopyNote) File() string { return filepath.Join(copiesDir(), c.ID+".json") }

func (c CopyNote) Write() {
	if os.MkdirAll(copiesDir(), 0o755) == nil {
		_ = kit.WriteRecord(c.File(), c)
	}
}

func CopyNoteID(path string, h reach.HostSpec) string {
	sum := sha256.Sum256([]byte(path + "\x00" + h.Key()))
	return hex.EncodeToString(sum[:])[:16]
}

// CopiesKnown: every copy this machine asked for
func CopiesKnown() []CopyNote {
	files, _ := filepath.Glob(filepath.Join(copiesDir(), "*.json"))
	sort.Strings(files)
	var out []CopyNote
	for _, f := range files {
		var c CopyNote
		if data, err := os.ReadFile(f); err == nil && json.Unmarshal(data, &c) == nil && c.ID != "" {
			out = append(out, c)
		}
	}
	return out
}

// CopiesUnder: those of a directory, and of what is under it
func CopiesUnder(roots ...string) []CopyNote {
	var out []CopyNote
	for _, c := range CopiesKnown() {
		for _, r := range roots {
			r = strings.TrimRight(r, "/")
			if c.Path == r || strings.HasPrefix(c.Path, r+"/") {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

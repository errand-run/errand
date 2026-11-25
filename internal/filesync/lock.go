package filesync

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/system"
)

// Who holds a copy's lock: said in it, so that one its process died over is nobody's at once. One that does not say, or
// whose machine is another that shares the disk, is left by a push that died when it is old.

const lockOwner = "owner.json"

func ownLock() map[string]any {
	pid := os.Getpid()
	return map[string]any{"host": queue.Host(), "pid": pid, "since": fmt.Sprint(kit.Started(pid)), "at": kit.Seconds(time.Now())}
}

// StaleLock: why the lock of a copy is nobody's any more; "" when there is none, or when it may be held
func StaleLock(root string) string {
	path := filepath.Join(root, lockName)
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if o := kit.ReadJSON(filepath.Join(path, lockOwner)); o != nil && kit.Text(o["host"]) == queue.Host() {
		pid := int(kit.Num(o["pid"], 0))
		switch {
		case pid <= 0:
		case system.Signal(pid, 0) != nil:
			return fmt.Sprintf("its process ( %d ) is gone", pid)
		case kit.Text(o["since"]) != fmt.Sprint(kit.Started(pid)):
			return fmt.Sprintf("its process ( %d ) is gone, another has its number", pid)
		default:
			return ""
		}
	}
	if age := time.Since(st.ModTime()); age > staleLock {
		return fmt.Sprintf("held for %s", age.Round(time.Minute))
	}
	return ""
}

// Locked: whether a copy is locked
func Locked(root string) bool { return kit.IsDir(filepath.Join(root, lockName)) }

// breakLock: a lock that is nobody's, removed -- moved first: of two that see it, one does it; and one that was taken anew
// meanwhile is given back
func breakLock(root string) {
	path := filepath.Join(root, lockName)
	was := kit.ReadJSON(filepath.Join(path, lockOwner))
	aside := path + "." + kit.RandomID()
	if os.Rename(path, aside) != nil {
		return
	}
	now := kit.ReadJSON(filepath.Join(aside, lockOwner))
	if fmt.Sprint(was) != fmt.Sprint(now) && os.Rename(aside, path) == nil {
		return
	}
	_ = os.RemoveAll(aside)
}

// Unlock: the lock of a copy removed, when it is nobody's
func Unlock(root string) bool {
	if StaleLock(root) == "" {
		return false
	}
	breakLock(root)
	return !Locked(root)
}

// CopySeen: what is there to say of a copy, read from what errand keeps in it
type CopySeen struct {
	Path    string         `json:"path"`
	Gone    bool           `json:"gone,omitempty"`    // a record, and nothing there
	Bare    bool           `json:"bare,omitempty"`    // a directory without a manifest
	Old     bool           `json:"old,omitempty"`     // a manifest that does not say whose its files are
	Lock    string         `json:"lock,omitempty"`    // why its lock is nobody's
	Cut     bool           `json:"cut,omitempty"`     // a push was cut
	Aside   int            `json:"aside,omitempty"`   // how many pushes moved files aside
	Foreign int            `json:"foreign,omitempty"` // files errand did not write, that it knows of
	Origin  map[string]any `json:"origin,omitempty"`
}

func SeeCopy(root string) CopySeen {
	out := CopySeen{Path: root}
	if !kit.IsDir(root) {
		out.Gone = true
		return out
	}
	m := loadManifest(filepath.Join(root, CopyState))
	out.Bare = m == nil
	if m != nil {
		out.Old, out.Foreign = !m.owns, len(m.foreign)
	}
	out.Lock = StaleLock(root)
	out.Cut = kit.Exists(filepath.Join(root, writesName)) && !Locked(root)
	if kept, err := os.ReadDir(filepath.Join(root, spared)); err == nil {
		out.Aside = len(kept)
	}
	out.Origin = kit.ReadJSON(filepath.Join(root, CopyOrigin))
	return out
}

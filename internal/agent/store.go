// What errand keeps on a machine for every project ( ADR 0044 ): the environments of its store, the images of its cache, the
// installed Windows. Listed for `errand caches`, and tidied by the agent's daily round and by `errand caches` itself: what a
// project still declares never goes by itself; the rest goes once nothing used it for a while.

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/system"
)

const (
	storeUnusedDays       = 30.0 // what no project declares any more goes after this unused
	storeUnusedDaysShared = 7.0  // on a machine shared with others ( ADR 0028 )
	storeReplacedDays     = 1.0  // an environment built again beside it ( changed by hand, `--force` )
)

// StoreEntry: one thing of the store
type StoreEntry struct {
	Kind     string  `json:"kind"` // conda, uv, image, windows
	Path     string  `json:"path"`
	Name     string  `json:"name"` // as a person reads it: the layer that made it, when it is known
	Size     int64   `json:"size"`
	Used     string  `json:"used"` // when it was last used
	Days     float64 `json:"days"` // since
	Declared bool    `json:"declared"`
	Replaced bool    `json:"replaced,omitempty"`
	Due      bool    `json:"due"` // the tidying takes it
	busy     bool
}

// storeEntries: what the store holds, and what the tidying would take with this many days of no use -- refs of projects that
// are gone are dropped on the way when `drop`
func storeEntries(now time.Time, unused float64, drop bool) []StoreEntry {
	store := layer.MambaStore()
	declared, names := map[string]bool{}, map[string]string{}
	refs, _ := filepath.Glob(filepath.Join(store, "refs", "*.json"))
	for _, path := range refs {
		var ref layer.MambaRef
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, &ref) != nil || !kit.IsDir(ref.Root) {
			if drop {
				_ = os.Remove(path) // its project is gone
			}
			continue
		}
		prefix := filepath.Clean(ref.Prefix)
		label := filepath.Base(ref.Root)
		if ref.Name != "" && ref.Name != "image" {
			label += " " + ref.Name
		}
		names[prefix] = label
		if ref.Layer != nil { // an image: still the one the project's recipe makes
			if layer.ImageOf(ref.Layer, ref.Root) == ref.Prefix {
				declared[prefix] = true
			} else if drop {
				_ = os.Remove(path)
			}
			continue
		}
		lock, err := layer.ReadLock(ref.Root)
		if current, ok := layer.UvLockHash(ref.Root, ref.Python); ref.UvLock && ok && current == ref.ID {
			declared[prefix] = true // the project's uv.lock is still that one
		} else if err == nil && lock.Has(ref.Declaration, ref.ID) {
			declared[prefix] = true
		} else if seen, err := time.ParseInLocation("2006-01-02 15:04:05", ref.Seen, time.Local); err == nil && now.Sub(seen) > 30*24*time.Hour && drop {
			_ = os.Remove(path) // a declaration that is no longer the project's
		}
	}
	var out []StoreEntry
	add := func(e StoreEntry, used time.Time, days float64) {
		e.Used, e.Days = used.Format("2006-01-02"), now.Sub(used).Hours()/24
		e.Name = names[filepath.Clean(e.Path)]
		e.Declared = declared[filepath.Clean(e.Path)] && !e.Replaced
		e.Due = !e.Declared && !e.busy && e.Days >= days
		out = append(out, e)
	}
	for _, kind := range []string{"mamba", "uv"} {
		records, _ := filepath.Glob(filepath.Join(storeDir(kind), "*.json"))
		for _, path := range records {
			var rec storeRecord
			data, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(data, &rec) != nil || rec.Dir == "" {
				continue
			}
			prefix := filepath.Join(storeDir(rec.Kind), rec.Dir)
			used, err := time.ParseInLocation("2006-01-02 15:04:05", rec.Used, time.Local)
			if err != nil {
				used = now
			}
			days := unused
			if rec.Replaced {
				days = storeReplacedDays
			}
			add(StoreEntry{Kind: map[string]string{"mamba": "conda", "uv": "uv"}[kind], Path: prefix, Size: kit.TreeSize(prefix), Replaced: rec.Replaced,
				busy: kit.Exists(filepath.Join(storeDir(rec.Kind), rec.Dir+".lock"))}, used, days)
		}
	}
	images, _ := filepath.Glob(filepath.Join(layer.ImagesStore(), "*.sif"))
	for _, img := range images {
		st, err := os.Stat(img + ".used")
		if err != nil {
			st, err = os.Stat(img)
		}
		if err != nil {
			continue
		}
		size := int64(0)
		if f, err := os.Stat(img); err == nil {
			size = f.Size()
		}
		add(StoreEntry{Kind: "image", Path: img, Size: size}, st.ModTime(), unused)
	}
	bases, _ := filepath.Glob(filepath.Join(system.Cache(), "windows", "go-*"))
	for _, b := range bases {
		st, err := os.Stat(filepath.Join(b, "used"))
		if err != nil {
			st, err = os.Stat(filepath.Join(b, "ready"))
		}
		if err != nil {
			continue
		}
		busy := false
		if f, err := os.OpenFile(filepath.Join(b, "installing"), os.O_RDWR|os.O_CREATE, 0o644); err == nil {
			busy = system.TryLock(f) != nil
			if !busy {
				_ = system.Unlock(f)
			}
			f.Close()
		}
		add(StoreEntry{Kind: "windows", Path: b, Size: kit.TreeSize(b), busy: busy}, st.ModTime(), unused)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind+out[i].Path < out[j].Kind+out[j].Path })
	return out
}

// tidyStore: what is due, removed -> what went, said
func tidyStore(entries []StoreEntry, dry bool) []string {
	var gone []string
	condaGone := false
	for _, e := range entries {
		if !e.Due {
			continue
		}
		gone = append(gone, fmt.Sprintf("%s ( %s, %s, unused since %s )", e.Path, e.Kind, human(e.Size), e.Used))
		if dry {
			continue
		}
		switch e.Kind {
		case "conda", "uv":
			_ = os.RemoveAll(e.Path)
			_ = os.Remove(e.Path + ".json")
			condaGone = condaGone || e.Kind == "conda"
		case "image":
			for _, p := range []string{e.Path, e.Path + ".used", e.Path + ".prev", e.Path + ".new"} {
				_ = os.RemoveAll(p)
			}
		case "windows":
			_ = os.RemoveAll(e.Path)
		}
	}
	if !dry { // what a failed resolution or build left
		scratch, _ := filepath.Glob(filepath.Join(layer.MambaStore(), "tmp", "*"))
		for _, s := range scratch {
			if st, err := os.Stat(s); err == nil && time.Since(st.ModTime()) > 24*time.Hour {
				_ = os.RemoveAll(s)
			}
		}
	}
	if condaGone {
		if exe := layer.MambaExe(nil); kit.Exists(exe) { // the packages no environment links any more
			setting{say: func(string) {}}.line(mambaLine(exe, "clean", "-y", "--no-rc", "-r", mambaRoot(), "--packages", "--tarballs")+" >/dev/null 2>&1", nil)
		}
	}
	return gone
}

func human(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.0f MB", float64(n)/1e6)
	}
	return fmt.Sprintf("%.0f kB", float64(n)/1e3)
}

// PurgeMambaStore: the agent's daily round over the store -> what went
func PurgeMambaStore(now time.Time, shared, dry bool, say func(string)) []string {
	days := storeUnusedDays
	if shared {
		days = storeUnusedDaysShared
	}
	gone := tidyStore(storeEntries(now, days, !dry), dry)
	if say != nil {
		for _, g := range gone {
			say("removed from errand's store: " + g)
		}
	}
	return gone
}

// storeAnswer: the store, as `errand caches` shows it -- tidied first when `purge`, with `unused` days in place of the
// usual ones when they are said
func storeAnswer(purge, dry bool, unused *float64) map[string]any {
	days := storeUnusedDays
	if sharedHere() {
		days = storeUnusedDaysShared
	}
	if unused != nil {
		days = *unused
	}
	entries := storeEntries(time.Now(), days, purge && !dry)
	out := map[string]any{"path": layer.MambaStore(), "days": days}
	if purge {
		out["removed"] = tidyStore(entries, dry)
		if !dry {
			entries = storeEntries(time.Now(), days, false)
		}
	}
	out["entries"] = entries
	return out
}

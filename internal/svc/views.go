package svc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/errand-run/errand/internal/kit"
)

// ── the copies of views this machine holds ───────────────────────────────────

func viewsHeld() string { return filepath.Join(kit.LocalCache(), "views") }

var viewName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ViewInto: where the copy of a view goes here. Whose it is is written beside it: in it, the next push of the view would
// remove it as a file the view has not.
func ViewInto(vid string, origin map[string]any) (map[string]any, error) {
	if !viewName.MatchString(vid) {
		return nil, fmt.Errorf("%q: not a view", vid)
	}
	root := filepath.Join(viewsHeld(), vid)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	if len(origin) > 0 {
		origin["told"] = kit.Seconds(time.Now())
		_ = kit.WriteWholeJSON(root+".origin.json", origin)
	}
	return map[string]any{"root": root, "rel": "views/" + vid}, nil
}

// RemoveViews: the copies this machine holds, removed. One that is not there is none to remove.
func RemoveViews(vids []string) map[string]any {
	removed, freed := []string{}, int64(0)
	for _, vid := range vids {
		if !viewName.MatchString(vid) {
			continue
		}
		root := filepath.Join(viewsHeld(), vid)
		if st, err := os.Lstat(root); err == nil && st.IsDir() {
			if freed += removeTree(root); !kit.Exists(root) {
				removed = append(removed, vid)
			}
		}
		_ = os.Remove(root + ".origin.json")
	}
	_ = os.Remove(viewsHeld())
	return map[string]any{"removed": removed, "freed": freed}
}

func viewsFile() string { return filepath.Join(kit.LocalCache(), "page", "views.json") }

func RegisteredViews() map[string]string {
	out := map[string]string{}
	if data, err := os.ReadFile(viewsFile()); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

func RegisterView(vid, dir string) {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	views := RegisteredViews()
	if views[vid] == dir {
		return
	}
	views[vid] = dir
	_ = os.MkdirAll(filepath.Dir(viewsFile()), 0o700)
	b, _ := json.MarshalIndent(views, "", " ")
	_ = kit.WriteWhole(viewsFile(), string(b))
}
